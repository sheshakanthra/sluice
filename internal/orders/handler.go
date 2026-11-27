package orders

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/payments"
)

// Route patterns (also the idempotency route scope).
const (
	RouteCreate     = "POST /api/v1/orders"
	RoutePay        = "POST /api/v1/orders/{orderId}/pay"
	RouteNewAttempt = "POST /api/v1/orders/{orderId}/payments"
	RouteConfirm    = "POST /api/v1/orders/{orderId}/confirm"
	RouteGet        = "GET /api/v1/orders/{orderId}"
	RouteWebhook    = "POST /api/v1/webhooks/mockpay"
	RouteReconcile  = "POST /api/v1/admin/reconcile"
)

const maxWebhookBody = 64 << 10

// HandlerDeps are what the order routes need.
type HandlerDeps struct {
	Svc  *Service
	Keys *auth.Keyring
	Idem *httpx.Idempotency
	// QueueEnforced requires an admission token for POST /orders; when false
	// (rush/test only) a session token is accepted as well.
	QueueEnforced bool
	AdminToken    string
	WebhookSecret string
	Now           func() time.Time
	Log           *slog.Logger
	// Overrides replace the handlers of these routes (BASELINE naive or
	// holds_only only, rush/test; ADR 0011). They run behind the same auth
	// but without the idempotency middleware: the baselines have none.
	Overrides map[string]http.Handler
}

// RegisterRoutes mounts the order, webhook and reconcile routes.
func RegisterRoutes(rt *httpx.Router, d HandlerDeps) {
	if d.Now == nil {
		d.Now = time.Now
	}
	createAuds := []auth.Audience{auth.AudAdmission}
	missing := httpx.CodeNotAdmitted
	if !d.QueueEnforced {
		createAuds = append(createAuds, auth.AudSession)
		missing = httpx.CodeTokenInvalid
	}
	// handler is the route's handler: its override as is, or h behind the
	// idempotency middleware when idem is set.
	handler := func(route string, h http.HandlerFunc, idem bool) http.Handler {
		if o, ok := d.Overrides[route]; ok {
			return o
		}
		if idem {
			return d.Idem.Wrap(route, h)
		}
		return h
	}
	session := func(route string, h http.HandlerFunc, idem bool) {
		rt.Handle(route, d.Keys.Require(httpx.CodeTokenInvalid, handler(route, h, idem), auth.AudSession))
	}
	rt.Handle(RouteCreate, d.Keys.RequireHoldBacked(missing, handler(RouteCreate, d.create, true), createAuds...))
	session(RoutePay, d.pay, true)
	session(RouteNewAttempt, d.newAttempt, true)
	session(RouteConfirm, d.confirm, true)
	session(RouteGet, d.get, false)
	rt.HandleFunc(RouteWebhook, d.webhook)
	rt.Handle(RouteReconcile, auth.RequireAdmin(d.AdminToken, http.HandlerFunc(d.reconcile)))
}

func decode(r *http.Request, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	dec.DisallowUnknownFields()
	return dec.Decode(v) == nil && !dec.More()
}

func orderID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("orderId"))
	if err != nil {
		httpx.WriteError(w, r, httpx.CodeNotFound, "no such order")
		return uuid.Nil, false
	}
	return id, true
}

func writeView(w http.ResponseWriter, status int, v View) {
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, status, v)
}

func (d HandlerDeps) create(w http.ResponseWriter, r *http.Request) {
	var b struct {
		HoldID uuid.UUID `json:"hold_id"`
	}
	if !decode(r, &b) || b.HoldID == uuid.Nil {
		httpx.WriteError(w, r, httpx.CodeValidation, `body must be {"hold_id": "<uuid>"}`)
		return
	}
	c, _ := auth.ClaimsFrom(r.Context())
	if c.Audience == auth.AudAdmission {
		h, err := d.Svc.q.GetHold(r.Context(), b.HoldID)
		if err == nil && h.EventID != c.EventID {
			httpx.WriteError(w, r, httpx.CodeTokenInvalid, "the admission token is for another event")
			return
		}
		// Past the admission's cap (ADR 0018): the token may only turn the
		// user's own live hold into an order, the hold it already paid for.
		if auth.AdmissionPastCap(r.Context()) &&
			(err != nil || h.UserID != c.Subject || h.Status != gen.HoldStatusACTIVE || !h.ExpiresAt.After(d.Now())) {
			httpx.WriteError(w, r, httpx.CodeAdmissionExpired,
				"your admission reached its time limit and this hold is not live; rejoin the queue (POST /queue?rejoin=true)")
			return
		}
	}
	v, err := d.Svc.Create(r.Context(), c.Subject, b.HoldID)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	writeView(w, http.StatusCreated, v)
}

func (d HandlerDeps) pay(w http.ResponseWriter, r *http.Request) {
	id, ok := orderID(w, r)
	if !ok {
		return
	}
	var b struct {
		PaymentMethod string `json:"payment_method"`
	}
	if !decode(r, &b) || !payments.ValidMethod(b.PaymentMethod) {
		httpx.WriteError(w, r, httpx.CodeValidation, "payment_method must be pm_ok, pm_decline, pm_timeout or pm_slow")
		return
	}
	c, _ := auth.ClaimsFrom(r.Context())
	v, err := d.Svc.Pay(r.Context(), id, c.Subject, b.PaymentMethod)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	writeView(w, http.StatusOK, v)
}

func (d HandlerDeps) newAttempt(w http.ResponseWriter, r *http.Request) {
	id, ok := orderID(w, r)
	if !ok {
		return
	}
	c, _ := auth.ClaimsFrom(r.Context())
	v, err := d.Svc.NewAttempt(r.Context(), id, c.Subject)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	writeView(w, http.StatusCreated, v)
}

func (d HandlerDeps) confirm(w http.ResponseWriter, r *http.Request) {
	id, ok := orderID(w, r)
	if !ok {
		return
	}
	c, _ := auth.ClaimsFrom(r.Context())
	v, err := d.Svc.Confirm(r.Context(), id, c.Subject)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	writeView(w, http.StatusOK, v)
}

func (d HandlerDeps) get(w http.ResponseWriter, r *http.Request) {
	id, ok := orderID(w, r)
	if !ok {
		return
	}
	c, _ := auth.ClaimsFrom(r.Context())
	v, err := d.Svc.Get(r.Context(), id, c.Subject)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	writeView(w, http.StatusOK, v)
}

// webhook is the MockPay inbox (API.md §4): verify the signature (constant
// time, 300 s tolerance), then store and enqueue in one transaction, and
// answer 200 fast. A duplicate is a 200 no-op.
func (d HandlerDeps) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
	if err != nil || len(body) > maxWebhookBody {
		httpx.WriteError(w, r, httpx.CodeValidation, "body unreadable or too large")
		return
	}
	if err := payments.VerifySignature(d.WebhookSecret, r.Header.Get(payments.SignatureHeader), body, d.Now(),
		payments.SignatureTolerance); err != nil {
		d.Svc.CountWebhook("unknown", WebhookBadSignature)
		d.Log.WarnContext(r.Context(), "webhook rejected", slog.String("reason", err.Error()))
		httpx.WriteError(w, r, httpx.CodeValidation, "invalid webhook signature")
		return
	}
	var ev payments.WebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil || ev.ID == "" || ev.Type == "" || ev.Data.Intent.ID == "" {
		httpx.WriteError(w, r, httpx.CodeValidation, "webhook body is not a MockPay event")
		return
	}
	if h := r.Header.Get(payments.EventIDHeader); h != "" && h != ev.ID {
		httpx.WriteError(w, r, httpx.CodeValidation, "MockPay-Event-Id does not match the body")
		return
	}
	if _, err := d.Svc.ReceiveWebhook(r.Context(), ev, body); err != nil {
		d.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]bool{"received": true})
}

func (d HandlerDeps) reconcile(w http.ResponseWriter, r *http.Request) {
	res, err := d.Svc.Reconcile(r.Context())
	if err != nil {
		d.Log.ErrorContext(r.Context(), "reconcile", slog.String("error", err.Error()))
		httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeDependencyUnavailable, "reconcile did not finish: "+err.Error()).
			WithRetryAfter(time.Second))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// writeError maps domain errors to API.md §1.3. Business outcomes are 4xx;
// a PSP that is down is 503 (the Idempotency-Key is released, so the retry
// runs again with the same PSP keys); a Postgres error with a SQLSTATE is a
// bug (500); anything else is a dependency problem (503).
func (d HandlerDeps) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var pgErr *pgconn.PgError
	switch {
	case errors.Is(err, ErrOrderNotFound), errors.Is(err, inventory.ErrHoldNotFound):
		httpx.WriteError(w, r, httpx.CodeNotFound, "no such order or hold")
	case errors.Is(err, inventory.ErrHoldExpired):
		httpx.WriteError(w, r, httpx.CodeHoldExpired, "")
	case errors.Is(err, inventory.ErrHoldState), errors.Is(err, ErrOrderState):
		httpx.WriteError(w, r, httpx.CodeOrderState, err.Error())
	case errors.Is(err, ErrAttemptsExhausted):
		httpx.WriteError(w, r, httpx.CodePaymentAttemptsExhausted, "")
	case errors.Is(err, ErrInvalidMethod):
		httpx.WriteError(w, r, httpx.CodeValidation, err.Error())
	case errors.Is(err, inventory.ErrEventNotFound):
		httpx.WriteError(w, r, httpx.CodeNotFound, "no such event")
	case errors.As(err, &pgErr):
		// A database error we did not map is a bug: log the whole wrapped
		// chain plus SQLSTATE, table and constraint, then answer 500.
		d.Log.ErrorContext(r.Context(), "orders: unexpected database error", db.ErrorAttrs(err)...)
		httpx.WriteError(w, r, httpx.CodeInternal, "")
	default:
		d.Log.ErrorContext(r.Context(), "orders dependency", slog.String("error", err.Error()))
		httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeDependencyUnavailable, "").WithRetryAfter(time.Second))
	}
}
