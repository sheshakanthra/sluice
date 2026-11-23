// Package httpx is SLUICE's small HTTP layer on top of net/http: a router on
// the stdlib ServeMux, middleware, RFC 9457 problem responses and health checks.
package httpx

import (
	"context"
	"net/http"
)

// RouteUnmatched is the metrics/log route label for requests no pattern matched.
const RouteUnmatched = "unmatched"

// Router wraps http.ServeMux (Go 1.22+ "METHOD /path/{param}" patterns) and
// records the matched pattern so metrics use a bounded route label instead of
// raw paths. Unmatched requests get a problem+json 404.
type Router struct {
	mux *http.ServeMux
}

// NewRouter returns an empty router whose fallback is a NOT_FOUND problem.
func NewRouter() *Router {
	rt := &Router{mux: http.NewServeMux()}
	rt.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, CodeNotFound, "no route for "+r.Method+" "+r.URL.Path)
	})
	return rt
}

// Handle registers h for pattern.
func (rt *Router) Handle(pattern string, h http.Handler) {
	rt.mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setRoute(r.Context(), pattern)
		h.ServeHTTP(w, r)
	}))
}

// HandleFunc registers f for pattern.
func (rt *Router) HandleFunc(pattern string, f func(http.ResponseWriter, *http.Request)) {
	rt.Handle(pattern, http.HandlerFunc(f))
}

// ServeHTTP dispatches to the matching handler.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) { rt.mux.ServeHTTP(w, r) }

// routeHolder is placed in the context by the outermost observing middleware
// and filled in by the router. A pointer survives r.WithContext copies made by
// middleware in between.
type routeHolder struct{ pattern string }

type routeKey struct{}

func withRouteHolder(ctx context.Context) (context.Context, *routeHolder) {
	h := &routeHolder{pattern: RouteUnmatched}
	return context.WithValue(ctx, routeKey{}, h), h
}

func setRoute(ctx context.Context, pattern string) {
	if h, ok := ctx.Value(routeKey{}).(*routeHolder); ok {
		h.pattern = pattern
	}
}

// Middleware decorates a handler.
type Middleware func(http.Handler) http.Handler

// Chain applies mws so the first one is the outermost.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}
