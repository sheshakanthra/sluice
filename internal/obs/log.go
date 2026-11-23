// Package obs holds observability plumbing: structured logging, the
// Prometheus registry and the optional pprof server.
package obs

import (
	"context"
	"io"
	"log/slog"
)

type ctxKey int

const (
	keyRequestID ctxKey = iota
	keyUserID
	keyEventID
)

// WithRequestID returns a context carrying the request id for logging.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyRequestID, id)
}

// RequestID returns the request id stored in ctx, or "".
func RequestID(ctx context.Context) string {
	s, _ := ctx.Value(keyRequestID).(string)
	return s
}

// WithUserID returns a context carrying the user id for logging.
func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyUserID, id)
}

// WithEventID returns a context carrying the event id for logging.
func WithEventID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyEventID, id)
}

// NewLogger returns a JSON slog logger that adds request_id, user_id and
// event_id from the record's context when present.
func NewLogger(w io.Writer, level slog.Leveler) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(contextHandler{h})
}

// contextHandler decorates records with ids carried in the context, so call
// sites only need slog.InfoContext(ctx, ...).
type contextHandler struct{ slog.Handler }

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if v, ok := ctx.Value(keyRequestID).(string); ok && v != "" {
		r.AddAttrs(slog.String("request_id", v))
	}
	if v, ok := ctx.Value(keyUserID).(string); ok && v != "" {
		r.AddAttrs(slog.String("user_id", v))
	}
	if v, ok := ctx.Value(keyEventID).(string); ok && v != "" {
		r.AddAttrs(slog.String("event_id", v))
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
