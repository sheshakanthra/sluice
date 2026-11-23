package obs

import (
	"net/http"
	"net/http/pprof"
	"time"
)

// NewPprofServer returns an HTTP server exposing pprof on addr, or nil when
// addr is empty. It uses its own mux, so pprof never leaks onto the public
// API mux. Callers own its lifecycle (start + graceful shutdown).
func NewPprofServer(addr string) *http.Server {
	if addr == "" {
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("POST /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		// CPU profiles and traces stream for up to their ?seconds= value.
		WriteTimeout: 2 * time.Minute,
		IdleTimeout:  120 * time.Second,
	}
}
