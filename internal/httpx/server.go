package httpx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Server timeouts. ReadHeaderTimeout guards against slowloris; IdleTimeout
// keeps keep-alive connections (the swarm holds one per user) reasonable.
const (
	ReadHeaderTimeout = 5 * time.Second
	ReadTimeout       = 15 * time.Second
	WriteTimeout      = 30 * time.Second
	IdleTimeout       = 120 * time.Second
	ShutdownTimeout   = 15 * time.Second
)

// NewServer returns an http.Server with SLUICE's standard timeouts.
func NewServer(addr string, h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       ReadTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       IdleTimeout,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// Serve listens on srv.Addr and serves until ctx is cancelled, then shuts
// down gracefully, waiting up to ShutdownTimeout for in-flight requests.
// started, if non-nil, receives the bound address once listening (useful
// with ":0" in tests).
func Serve(ctx context.Context, srv *http.Server, log *slog.Logger, started func(addr net.Addr)) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", srv.Addr, err)
	}
	log.InfoContext(ctx, "http server listening", slog.String("addr", ln.Addr().String()))
	if started != nil {
		started(ln.Addr())
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve %s: %w", srv.Addr, err)
	case <-ctx.Done():
	}

	log.InfoContext(ctx, "http server shutting down", slog.String("addr", ln.Addr().String()))
	// The parent ctx is already cancelled; shutdown gets its own deadline.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("shutdown %s: %w", srv.Addr, err)
	}
	if err := <-errc; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve %s: %w", srv.Addr, err)
	}
	return nil
}
