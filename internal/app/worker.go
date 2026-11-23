package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/errgroup"

	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/jobs"
	"github.com/sheshakanthra/sluice/internal/obs"
	"github.com/sheshakanthra/sluice/internal/orders"
)

// idempotencyGCInterval: keys live 24 h, so an hourly sweep is plenty.
const idempotencyGCInterval = time.Hour

// RunWorker runs `sluice worker`: the hold sweeper (which also loads any
// event whose Redis inventory is missing, so start-up loads every open
// event, and settles the orders of reclaimed holds), the River jobs
// (process_webhook, capture/void/refund, the periodic reconciler and live
// audit cache), the idempotency-key GC, the queue admission controller
// (one queue_tick loop per open event every QUEUE_TICK), and a
// health/metrics server on WORKER_HTTP_ADDR.
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
	sc, err := newScripts(ctx, log, reg, rdb)
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	var ord *orders.Service // the reclaim hook needs the orders service, which needs inventory
	inv, err := newInventory(cfg, log, reg, pg, rdb, sc,
		func(ctx context.Context, tx pgx.Tx, hid uuid.UUID) error { return ord.OnReclaim(ctx, tx, hid) })
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	ins, err := jobs.NewInsertClient(pg, log)
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	if ord, err = newOrders(cfg, log, reg, pg, rdb, inv, ins); err != nil {
		return fmt.Errorf("worker: %w", err)
	}
	rc, err := jobs.NewWorkerClient(pg, ord, jobs.Config{ReconcileInterval: cfg.ReconcileInterval, Log: log})
	if err != nil {
		return fmt.Errorf("worker: %w", err)
	}

	ctrl, err := newQueueController(cfg, log, gen.New(pg), sc)
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
	g.Go(func() error { return ctrl.Run(gctx) })
	g.Go(func() error {
		if err := rc.Start(gctx); err != nil {
			return fmt.Errorf("river start: %w", err)
		}
		<-gctx.Done()
		sctx, cancel := context.WithTimeout(context.WithoutCancel(gctx), 10*time.Second)
		defer cancel()
		if err := rc.Stop(sctx); err != nil {
			log.WarnContext(sctx, "river stop", slog.String("error", err.Error()))
		}
		return nil
	})
	if ps := obs.NewPprofServer(cfg.PprofAddr); ps != nil {
		g.Go(func() error { return httpx.Serve(gctx, ps, log.With(slog.String("server", "pprof")), nil) })
	}
	log.InfoContext(ctx, "worker started", slog.Duration("sweeper_interval", cfg.SweeperInterval),
		slog.Duration("reconcile_interval", cfg.ReconcileInterval), slog.Duration("queue_tick", cfg.QueueTick),
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
