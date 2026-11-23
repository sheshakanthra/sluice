package queue

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/obs"
)

// Routes (API.md §2).
const (
	RouteJoin   = "POST /api/v1/events/{eventId}/queue"
	RouteStatus = "GET /api/v1/events/{eventId}/queue/status"
	RouteLeave  = "DELETE /api/v1/events/{eventId}/queue"
)

// EventSource resolves an event UUID or slug (inventory.Service; its event
// cache is in memory, so the status path stays off Redis).
type EventSource interface {
	Event(ctx context.Context, idOrSlug string) (*inventory.EventInfo, error)
}

// HandlerDeps are the HTTP handlers' dependencies.
type HandlerDeps struct {
	Svc    *Service
	Events EventSource
	Keys   *auth.Keyring
	Log    *slog.Logger
}

// RegisterRoutes mounts join (session token), status (queue token) and
// leave (queue or admission token).
func RegisterRoutes(rt *httpx.Router, d HandlerDeps) {
	rt.Handle(RouteJoin, d.Keys.Require(httpx.CodeTokenInvalid, http.HandlerFunc(d.join), auth.AudSession))
	rt.Handle(RouteStatus, d.Keys.Require(httpx.CodeTokenInvalid, http.HandlerFunc(d.status), auth.AudQueue))
	rt.Handle(RouteLeave, d.Keys.Require(httpx.CodeTokenInvalid, http.HandlerFunc(d.leave), auth.AudQueue, auth.AudAdmission))
}

// event resolves the path event; for queue and admission tokens it also
// checks the token was issued for that event. false means a response was
// written.
func (d HandlerDeps) event(ctx context.Context, w http.ResponseWriter, r *http.Request) (Event, auth.Claims, bool) {
	c, _ := auth.ClaimsFrom(ctx)
	info, err := d.Events.Event(ctx, r.PathValue("eventId"))
	if errors.Is(err, inventory.ErrEventNotFound) {
		httpx.WriteError(w, r, httpx.CodeNotFound, "no event with that id or slug")
		return Event{}, c, false
	}
	if err != nil {
		d.unavailable(ctx, w, r, "resolve event", err)
		return Event{}, c, false
	}
	if c.Audience != auth.AudSession && c.EventID != info.ID {
		httpx.WriteError(w, r, httpx.CodeTokenInvalid, "the token is for another event")
		return Event{}, c, false
	}
	return Event{ID: info.ID, Capacity: info.Capacity, SaleOpensAt: info.SaleOpensAt,
		StartsAt: info.StartsAt, Status: info.Status}, c, true
}

func (d HandlerDeps) join(w http.ResponseWriter, r *http.Request) {
	rejoin := false
	switch r.URL.Query().Get("rejoin") {
	case "", "false":
	case "true":
		rejoin = true
	default:
		httpx.WriteError(w, r, httpx.CodeValidation, "rejoin must be true or false")
		return
	}
	ctx := r.Context()
	ev, c, ok := d.event(ctx, w, r)
	if !ok {
		return
	}
	ctx = obs.WithEventID(ctx, ev.ID.String())
	resp, err := d.Svc.Join(ctx, ev, c.Subject, rejoin)
	if errors.Is(err, ErrRejoinLimit) {
		httpx.WriteError(w, r, httpx.CodeRejoinLimit, "a ticket can be re-issued at most twice")
		return
	}
	if err != nil {
		d.unavailable(ctx, w, r, "queue join", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (d HandlerDeps) status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ev, c, ok := d.event(ctx, w, r)
	if !ok {
		return
	}
	ctx = obs.WithEventID(ctx, ev.ID.String())
	resp, err := d.Svc.Status(ctx, ev, c)
	if err != nil {
		d.unavailable(ctx, w, r, "queue status", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, resp)
}

func (d HandlerDeps) leave(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ev, c, ok := d.event(ctx, w, r)
	if !ok {
		return
	}
	ctx = obs.WithEventID(ctx, ev.ID.String())
	if err := d.Svc.Leave(ctx, ev, c); err != nil {
		d.unavailable(ctx, w, r, "queue leave", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// unavailable answers 503 DEPENDENCY_UNAVAILABLE (never a 5xx INTERNAL for
// a dependency outage; I8).
func (d HandlerDeps) unavailable(ctx context.Context, w http.ResponseWriter, r *http.Request, what string, err error) {
	if d.Log != nil {
		d.Log.ErrorContext(ctx, what, slog.String("error", err.Error()))
	}
	httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeDependencyUnavailable, "").WithRetryAfter(time.Second))
}
