package events

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sheshakanthra/sluice/internal/httpx"
)

// Reader is what the handlers need (Store implements it).
type Reader interface {
	List(ctx context.Context) ([]Event, error)
	Get(ctx context.Context, idOrSlug string) (EventDetail, error)
}

// ListResponse is the body of GET /api/v1/events.
type ListResponse struct {
	Events []Event `json:"events"`
}

// RegisterRoutes mounts the catalogue endpoints.
func RegisterRoutes(rt *httpx.Router, r Reader, log *slog.Logger) {
	rt.HandleFunc("GET /api/v1/events", func(w http.ResponseWriter, req *http.Request) {
		evs, err := r.List(req.Context())
		if err != nil {
			writeStoreError(w, req, log, err)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		httpx.WriteJSON(w, http.StatusOK, ListResponse{Events: evs})
	})
	rt.HandleFunc("GET /api/v1/events/{eventId}", func(w http.ResponseWriter, req *http.Request) {
		ev, err := r.Get(req.Context(), req.PathValue("eventId"))
		if err != nil {
			writeStoreError(w, req, log, err)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		httpx.WriteJSON(w, http.StatusOK, ev)
	})
}

// writeStoreError maps store errors to problems: not found is 404; a Postgres
// error with a SQLSTATE is a bug (500 INTERNAL); anything else (connection
// refused, timeout) means Postgres is unreachable (503, retry in 1 s).
func writeStoreError(w http.ResponseWriter, r *http.Request, log *slog.Logger, err error) {
	if errors.Is(err, ErrEventNotFound) {
		httpx.WriteError(w, r, httpx.CodeNotFound, "no event with that id or slug")
		return
	}
	log.ErrorContext(r.Context(), "events store", slog.String("error", err.Error()))
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		httpx.WriteError(w, r, httpx.CodeInternal, "")
		return
	}
	httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeDependencyUnavailable, "the event catalogue is temporarily unavailable").
		WithRetryAfter(time.Second))
}
