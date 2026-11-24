// Package naive is the payment code of the B0 and B1 baselines.
//
// ##########################################################################
// ##  BASELINE ONLY. NEVER USE IN THE REAL SYSTEM.                        ##
// ##  This is the payment code a hurried team writes (LOADTEST §4; ADR    ##
// ##  0011): charge on every click with a random PSP key, no idempotency, ##
// ##  no lock, no reconciliation. It exists so the rush can show the      ##
// ##  duplicate charges (and, in B0, the oversells) the real system       ##
// ##  prevents, and so the auditor can prove it catches them. The API     ##
// ##  mounts it only when BASELINE is naive or holds_only, which the      ##
// ##  config refuses outside APP_ENV rush/test.                           ##
// ##########################################################################
package naive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/orders"
	"github.com/sheshakanthra/sluice/internal/payments"
)

// Naive order states (naive_orders.status / pay_status).
const (
	statusPending   = "PENDING_PAYMENT"
	statusConfirmed = "CONFIRMED"
	statusFailed    = "FAILED"
	payCreated      = "CREATED"
	payCaptured     = "CAPTURED"
	payFailed       = "FAILED"
)

// charge is the naive charge: a new intent under a random key, authorized
// on creation (confirm=true), then captured at once. Every call is a new
// charge, which is the whole bug.
func charge(ctx context.Context, psp *payments.Client, ref, eventID string, amount int64, method string) (payments.Intent, error) {
	in, err := psp.CreateIntent(ctx, uuid.NewString(), payments.CreateIntentRequest{
		AmountPaise: amount, Currency: "INR", MerchantRef: ref,
		Metadata: map[string]string{"event_id": eventID, "baseline": "naive"},
		Confirm:  true, PaymentMethod: method,
	})
	if err != nil {
		return in, fmt.Errorf("naive charge: create: %w", err)
	}
	if in.Status != payments.StatusAuthorized {
		return in, nil
	}
	if in, err = psp.Capture(ctx, in.ID); err != nil {
		return in, fmt.Errorf("naive charge: capture: %w", err)
	}
	return in, nil
}

func decodeMethod(w http.ResponseWriter, r *http.Request) (string, bool) {
	var b struct {
		PaymentMethod string `json:"payment_method"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	if err := dec.Decode(&b); err != nil || !payments.ValidMethod(b.PaymentMethod) {
		httpx.WriteError(w, r, httpx.CodeValidation, "payment_method must be pm_ok, pm_decline, pm_timeout or pm_slow")
		return "", false
	}
	return b.PaymentMethod, true
}

func unavailable(log *slog.Logger, w http.ResponseWriter, r *http.Request, what string, err error) {
	log.WarnContext(r.Context(), "naive baseline: "+what, slog.String("error", err.Error()))
	httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeDependencyUnavailable, "").WithRetryAfter(time.Second))
}

func writeView(w http.ResponseWriter, status int, v orders.View) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, status, v)
}

// Orders is the B0 (BASELINE=naive) order and payment routes over
// naive_orders and naive_sales.
type Orders struct {
	Inv *inventory.Service
	Q   *gen.Queries
	PSP *payments.Client
	Log *slog.Logger
}

func (o *Orders) owned(w http.ResponseWriter, r *http.Request, id string) (gen.NaiveOrder, bool) {
	ref, err := uuid.Parse(id)
	if err != nil {
		httpx.WriteError(w, r, httpx.CodeNotFound, "no such order")
		return gen.NaiveOrder{}, false
	}
	no, err := o.Q.NaiveGetOrder(r.Context(), ref)
	c, _ := auth.ClaimsFrom(r.Context())
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && no.UserID != c.Subject) {
		httpx.WriteError(w, r, httpx.CodeNotFound, "no such order")
		return gen.NaiveOrder{}, false
	}
	if err != nil {
		unavailable(o.Log, w, r, "get order", err)
		return gen.NaiveOrder{}, false
	}
	return no, true
}

func (o *Orders) view(r *http.Request, no gen.NaiveOrder, failure string) orders.View {
	v := orders.View{OrderID: no.OrderRef, EventID: no.EventID, Status: no.Status, AmountPaise: no.AmountPaise,
		Currency: "INR", SeatNos: no.SeatNos, Tickets: []orders.TicketView{},
		Payment: &orders.PaymentView{Attempt: no.Attempts, Status: no.PayStatus}, Charged: no.PayStatus == payCaptured}
	if failure != "" {
		v.FailureCode = &failure
	}
	if no.Status == statusConfirmed {
		ev, err := o.Inv.Event(r.Context(), no.EventID.String())
		for _, s := range no.SeatNos {
			label := ""
			if err == nil {
				label = ev.Label(s)
			}
			v.Tickets = append(v.Tickets, orders.TicketView{SeatNo: s, Label: label, Code: "NAIVE-" + no.OrderRef.String()[:8]})
		}
	}
	return v
}

// Create is POST /orders: the pseudo-hold becomes the order (same ref).
func (o *Orders) Create(w http.ResponseWriter, r *http.Request) {
	var b struct {
		HoldID string `json:"hold_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&b); err != nil {
		httpx.WriteError(w, r, httpx.CodeValidation, `body must be {"hold_id": "<uuid>"}`)
		return
	}
	no, ok := o.owned(w, r, b.HoldID)
	if !ok {
		return
	}
	writeView(w, http.StatusCreated, o.view(r, no, ""))
}

// Pay is POST /orders/{orderId}/pay: check, insert, charge. No lock, no
// constraint, no idempotency, a random PSP key on every call.
func (o *Orders) Pay(w http.ResponseWriter, r *http.Request) {
	method, ok := decodeMethod(w, r)
	if !ok {
		return
	}
	no, ok := o.owned(w, r, r.PathValue("orderId"))
	if !ok {
		return
	}
	ctx := r.Context()
	// Check ...
	taken, err := o.Q.NaiveTakenSeats(ctx, gen.NaiveTakenSeatsParams{EventID: no.EventID, OrderRef: no.OrderRef, SeatNos: no.SeatNos})
	if err != nil {
		unavailable(o.Log, w, r, "check seats", err)
		return
	}
	if len(taken) > 0 {
		if err := o.Q.NaiveSetOrderStatus(ctx, gen.NaiveSetOrderStatusParams{OrderRef: no.OrderRef, Status: statusFailed, PayStatus: no.PayStatus}); err != nil {
			unavailable(o.Log, w, r, "fail order", err)
			return
		}
		no.Status = statusFailed
		writeView(w, http.StatusOK, o.view(r, no, "SEATS_TAKEN"))
		return
	}
	// ... then insert, as a separate statement.
	if err := o.Q.NaiveInsertSales(ctx, gen.NaiveInsertSalesParams{EventID: no.EventID, OrderRef: no.OrderRef, UserID: no.UserID, SeatNos: no.SeatNos}); err != nil {
		unavailable(o.Log, w, r, "insert sales", err)
		return
	}
	in, err := charge(ctx, o.PSP, no.OrderRef.String(), no.EventID.String(), no.AmountPaise, method)
	if err != nil {
		unavailable(o.Log, w, r, "charge", err) // the sales rows stay; the client retries and charges again
		return
	}
	switch in.Status {
	case payments.StatusCaptured:
		no.Status, no.PayStatus = statusConfirmed, payCaptured
	default: // declined: give the seats back and let the user try again
		if err := o.Q.NaiveDeleteSales(ctx, no.OrderRef); err != nil {
			unavailable(o.Log, w, r, "undo sales", err)
			return
		}
		no.PayStatus = payFailed
	}
	if err := o.Q.NaiveSetOrderStatus(ctx, gen.NaiveSetOrderStatusParams{OrderRef: no.OrderRef, Status: no.Status, PayStatus: no.PayStatus}); err != nil {
		unavailable(o.Log, w, r, "set status", err)
		return
	}
	writeView(w, http.StatusOK, o.view(r, no, ""))
}

// NewAttempt is POST /orders/{orderId}/payments (at most 3 attempts).
func (o *Orders) NewAttempt(w http.ResponseWriter, r *http.Request) {
	no, ok := o.owned(w, r, r.PathValue("orderId"))
	if !ok {
		return
	}
	next, err := o.Q.NaiveNewAttempt(r.Context(), no.OrderRef)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, r, httpx.CodePaymentAttemptsExhausted, "")
		return
	}
	if err != nil {
		unavailable(o.Log, w, r, "new attempt", err)
		return
	}
	writeView(w, http.StatusCreated, o.view(r, next, ""))
}

// Get serves GET /orders/{orderId} and POST /orders/{orderId}/confirm
// (there is nothing to sync: the naive pay is synchronous).
func (o *Orders) Get(w http.ResponseWriter, r *http.Request) {
	no, ok := o.owned(w, r, r.PathValue("orderId"))
	if !ok {
		return
	}
	writeView(w, http.StatusOK, o.view(r, no, ""))
}

// HoldsOnly is the B1 (BASELINE=holds_only) /pay: the holds, orders and
// fenced ConfirmOrder are the real ones, but every /pay call charges a new
// intent with a random key and captures it, without looking at the
// order's live payment. ConfirmOrder can then sell the seats only once (no
// oversell), but each extra click is an extra charge.
type HoldsOnly struct {
	Orders *orders.Service
	Q      *gen.Queries
	PSP    *payments.Client
	Log    *slog.Logger
}

// Pay is the naive POST /orders/{orderId}/pay.
func (h *HoldsOnly) Pay(w http.ResponseWriter, r *http.Request) {
	method, ok := decodeMethod(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("orderId"))
	if err != nil {
		httpx.WriteError(w, r, httpx.CodeNotFound, "no such order")
		return
	}
	ctx := r.Context()
	c, _ := auth.ClaimsFrom(ctx)
	v, err := h.Orders.Get(ctx, id, c.Subject)
	if errors.Is(err, orders.ErrOrderNotFound) {
		httpx.WriteError(w, r, httpx.CodeNotFound, "no such order")
		return
	}
	if err != nil {
		unavailable(h.Log, w, r, "get order", err)
		return
	}
	in, err := charge(ctx, h.PSP, id.String(), v.EventID.String(), v.AmountPaise, method)
	if err != nil {
		unavailable(h.Log, w, r, "charge", err)
		return
	}
	// Point the open attempt at this charge (whatever it pointed at), then
	// let the real, fenced ConfirmOrder sell the seats.
	if _, err := h.Q.NaiveRelinkPayment(ctx, gen.NaiveRelinkPaymentParams{OrderID: id, ProviderIntentID: &in.ID}); err != nil {
		unavailable(h.Log, w, r, "relink payment", err)
		return
	}
	if err := h.Orders.ConfirmOrder(ctx, id); err != nil {
		unavailable(h.Log, w, r, "confirm order", err)
		return
	}
	if v, err = h.Orders.Get(ctx, id, c.Subject); err != nil {
		unavailable(h.Log, w, r, "get order", err)
		return
	}
	writeView(w, http.StatusOK, v)
}
