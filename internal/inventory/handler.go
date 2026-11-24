package inventory

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/obs"
)

// Route patterns (also the idempotency route scope).
const (
	RouteHold         = "POST /api/v1/events/{eventId}/holds"
	RouteRelease      = "DELETE /api/v1/holds/{holdId}"
	RouteAvailability = "GET /api/v1/events/{eventId}/availability"
	RouteRebuild      = "POST /api/v1/admin/events/{eventId}/rebuild-cache"
)

// HandlerDeps are what the inventory routes need.
type HandlerDeps struct {
	Svc   *Service
	Avail *AvailabilityCache
	Keys  *auth.Keyring
	Idem  *httpx.Idempotency
	// QueueEnforced requires an admission token; when false (rush/test
	// only) a session token is accepted as well.
	QueueEnforced bool
	AdminToken    string
	Log           *slog.Logger
}

// HoldResponse is the 201 body of POST /events/{id}/holds (API.md §3.4).
type HoldResponse struct {
	HoldID    uuid.UUID `json:"hold_id"`
	Status    string    `json:"status"`
	SeatNos   []int32   `json:"seat_nos"`
	Labels    []string  `json:"labels"`
	Fence     int64     `json:"fence"`
	ExpiresAt time.Time `json:"expires_at"`
}

type holdBody struct {
	SeatNos  []int32 `json:"seat_nos"`
	Quantity *int    `json:"quantity"`
}

// RegisterRoutes mounts holds, release, availability and rebuild-cache.
func RegisterRoutes(rt *httpx.Router, d HandlerDeps) {
	auds := []auth.Audience{auth.AudAdmission}
	missing := httpx.CodeNotAdmitted
	if !d.QueueEnforced {
		auds = append(auds, auth.AudSession)
		missing = httpx.CodeTokenInvalid
	}
	rt.Handle(RouteHold, d.Keys.Require(missing, d.Idem.Wrap(RouteHold, http.HandlerFunc(d.hold)), auds...))
	rt.Handle(RouteRelease, d.Keys.Require(missing, http.HandlerFunc(d.release), auds...))
	rt.HandleFunc(RouteAvailability, d.availability)
	rt.Handle(RouteRebuild, auth.RequireAdmin(d.AdminToken, http.HandlerFunc(d.rebuild)))
}

func (d HandlerDeps) hold(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ev, err := d.Svc.Event(ctx, r.PathValue("eventId"))
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	ctx = obs.WithEventID(ctx, ev.ID.String())
	r = r.WithContext(ctx)
	c, _ := auth.ClaimsFrom(ctx)
	if c.Audience == auth.AudAdmission && c.EventID != ev.ID {
		httpx.WriteError(w, r, httpx.CodeTokenInvalid, "the admission token is for another event")
		return
	}
	var b holdBody
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&b); err != nil || dec.More() {
		httpx.WriteError(w, r, httpx.CodeValidation, `body must be {"seat_nos": [..]} or {"quantity": n}`)
		return
	}
	req := Request{SeatNos: b.SeatNos}
	if b.Quantity != nil {
		if *b.Quantity <= 0 {
			httpx.WriteError(w, r, httpx.CodeValidation, "quantity must be positive")
			return
		}
		req.Quantity = *b.Quantity
	}
	h, err := d.Svc.Hold(ctx, ev.ID, c.Subject, r.Header.Get(httpx.IdempotencyHeader), req)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusCreated, HoldResponse{
		HoldID: h.ID, Status: string(h.Status), SeatNos: h.SeatNos, Labels: ev.Labels(h.SeatNos),
		Fence: h.Fence, ExpiresAt: h.ExpiresAt,
	})
}

func (d HandlerDeps) release(w http.ResponseWriter, r *http.Request) {
	hid, err := uuid.Parse(r.PathValue("holdId"))
	if err != nil {
		httpx.WriteError(w, r, httpx.CodeNotFound, "no such hold")
		return
	}
	c, _ := auth.ClaimsFrom(r.Context())
	if _, err := d.Svc.Release(r.Context(), hid, c.Subject); err != nil {
		d.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (d HandlerDeps) availability(w http.ResponseWriter, r *http.Request) {
	ev, err := d.Svc.Event(r.Context(), r.PathValue("eventId"))
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	a, err := d.Avail.Get(r.Context(), ev)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	h := w.Header()
	h.Set("ETag", a.ETag)
	h.Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == a.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, a)
}

func (d HandlerDeps) rebuild(w http.ResponseWriter, r *http.Request) {
	ev, err := d.Svc.Event(r.Context(), r.PathValue("eventId"))
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	res, err := d.Svc.Rebuild(r.Context(), ev.ID)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, res)
}

// writeError maps domain errors to API.md §1.3. Business outcomes are 4xx;
// a Postgres error with a SQLSTATE is a bug (500); anything else means a
// dependency is unreachable (503, retry in 1 s).
func (d HandlerDeps) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		taken  *SeatsTakenError
		insuff *InsufficientError
		pgErr  *pgconn.PgError
	)
	switch {
	case errors.As(err, &taken):
		httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeSeatsTaken,
			fmt.Sprintf("%d of the requested seats are no longer available", len(taken.Taken))).With("taken", taken.Taken))
	case errors.As(err, &insuff):
		httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeInsufficientSeats,
			fmt.Sprintf("only %d seats are left", insuff.Available)).With("available", insuff.Available))
	case errors.Is(err, ErrSoldOut):
		httpx.WriteError(w, r, httpx.CodeSoldOut, "")
	case errors.Is(err, ErrHoldLimit):
		httpx.WriteError(w, r, httpx.CodeHoldLimit, "release your current hold first")
	case errors.Is(err, ErrHoldExpired):
		httpx.WriteError(w, r, httpx.CodeHoldExpired, "")
	case errors.Is(err, ErrHoldState):
		httpx.WriteError(w, r, httpx.CodeOrderState, err.Error())
	case errors.Is(err, ErrQuotaExceeded):
		httpx.WriteError(w, r, httpx.CodeQuotaExceeded, err.Error())
	case errors.Is(err, ErrInvalidRequest):
		httpx.WriteError(w, r, httpx.CodeValidation, err.Error())
	case errors.Is(err, ErrEventNotFound):
		httpx.WriteError(w, r, httpx.CodeNotFound, "no event with that id or slug")
	case errors.Is(err, ErrHoldNotFound):
		httpx.WriteError(w, r, httpx.CodeNotFound, "no such hold")
	case errors.As(err, &pgErr):
		// A database error we did not map is a bug: log the whole wrapped
		// chain plus SQLSTATE, table and constraint, then answer 500.
		d.Log.ErrorContext(r.Context(), "inventory: unexpected database error", db.ErrorAttrs(err)...)
		httpx.WriteError(w, r, httpx.CodeInternal, "")
	default:
		d.Log.ErrorContext(r.Context(), "inventory dependency", slog.String("error", err.Error()))
		httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeDependencyUnavailable, "").WithRetryAfter(time.Second))
	}
}
