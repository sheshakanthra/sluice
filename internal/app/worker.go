package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/obs"
)

// idempotencyGCInterval: keys live 24 h, so an hourly sweep is plenty.
const idempotencyGCInterval = time.Hour

// RunWorker runs `sluice worker`: the hold sweeper (which also loads any
// event whose Redis inventory is missing, so start-up loads every open
// event), the idempotency-key GC, and a health/metrics server on
// WORKER_HTTP_ADDR. Admission ticks and River jobs arrive in P3/P4.
func RunWorker(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	log = log.With(slog.String("svc", "worker"))

	pg, err := db.NewPool(ctx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer pg.Close()
	mig, err := db.NewMigrator(pg, log)
	if err != nil {
		return err
	}
	defer func() { _ = mig.Close() }()
	rdb, err := NewRedis(cfg.RedisURL)
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	reg := obs.NewRegistry()
	inv, err := newInventory(ctx, cfg, log, reg, pg, rdb)
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}

	rt := httpx.NewRouter()
	httpx.RegisterHealth(rt, log,
		httpx.Check{Name: "postgres", Ping: pg.Ping},
		httpx.Check{Name: "redis", Ping: func(ctx context.Context) error { return rdb.Ping(ctx).Err() }},
		httpx.Check{Name: "migrations", Ping: mig.Check},
	)
	rt.Handle("GET /metrics", obs.MetricsHandler(reg))
	srv := httpx.NewServer(cfg.WorkerHTTPAddr, httpx.Chain(rt, httpx.Recover(log)), log)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpx.Serve(gctx, srv, log, nil) })
	g.Go(func() error { return inv.RunSweeper(gctx, cfg.SweeperInterval) })
	g.Go(func() error { return runIdempotencyGC(gctx, gen.New(pg), log) })
	if ps := obs.NewPprofServer(cfg.PprofAddr); ps != nil {
		g.Go(func() error { return httpx.Serve(gctx, ps, log.With(slog.String("server", "pprof")), nil) })
	}
	log.InfoContext(ctx, "worker started", slog.Duration("sweeper_interval", cfg.SweeperInterval),
		slog.String("hold_path", string(cfg.HoldPath)))
	if err := g.Wait(); err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	log.InfoContext(context.WithoutCancel(ctx), "worker stopped")
	return nil
}

func runIdempotencyGC(ctx context.Context, q *gen.Queries, log *slog.Logger) error {
	t := time.NewTicker(idempotencyGCInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			n, err := httpx.GCIdempotencyKeys(ctx, q, time.Now())
			if err != nil {
				log.ErrorContext(ctx, "idempotency gc", slog.String("error", err.Error()))
				continue
			}
			log.InfoContext(ctx, "idempotency gc", slog.Int64("deleted", n))
		}
	}
}
