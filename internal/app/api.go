// Package app wires dependencies for each sluice subcommand. It is the only
// place that constructs pools, clients and servers; packages below it receive
// their dependencies explicitly (no global mutable state).
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"

	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/events"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/obs"
)

// requestTimeout bounds every API request context.
const requestTimeout = 10 * time.Second

// Deps are the long-lived dependencies of the API process.
type Deps struct {
	Config   *config.Config
	Log      *slog.Logger
	Registry *prometheus.Registry
	PG       *pgxpool.Pool
	Redis    *redis.Client
	// Migrations reports whether goose and River migrations are applied
	// (nil error) for /readyz. Usually (*db.Migrator).Check.
	Migrations func(ctx context.Context) error
}

// NewRedis builds a go-redis client with bounded timeouts.
func NewRedis(url string) (*redis.Client, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, errors.New("parse REDIS_URL: invalid URL")
	}
	opt.DialTimeout = 2 * time.Second
	opt.ReadTimeout = 2 * time.Second
	opt.WriteTimeout = 2 * time.Second
	return redis.NewClient(opt), nil
}

// APIHandler builds the full API handler: routes plus the middleware stack.
// Order (outermost first): request id → observe (metrics + access log) →
// recover → CORS → timeout → router.
func APIHandler(d Deps) http.Handler {
	rt := httpx.NewRouter()
	checks := []httpx.Check{
		{Name: "postgres", Ping: d.PG.Ping},
		{Name: "redis", Ping: func(ctx context.Context) error { return d.Redis.Ping(ctx).Err() }},
	}
	if d.Migrations != nil {
		checks = append(checks, httpx.Check{Name: "migrations", Ping: d.Migrations})
	}
	httpx.RegisterHealth(rt, d.Log, checks...)
	rt.Handle("GET /metrics", obs.MetricsHandler(d.Registry))
	events.RegisterRoutes(rt, events.NewStore(gen.New(d.PG)), d.Log)

	m := httpx.NewHTTPMetrics(d.Registry, "api")
	return httpx.Chain(rt,
		httpx.RequestID(d.Config.TrustedProxies),
		httpx.Observe(d.Log, m),
		httpx.Recover(d.Log),
		httpx.CORS(d.Config.CORSOrigins),
		httpx.Timeout(requestTimeout),
	)
}

// RunAPI runs `sluice api` until ctx is cancelled (SIGINT/SIGTERM).
func RunAPI(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	log = log.With(slog.String("svc", "api"))

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

	d := Deps{Config: cfg, Log: log, Registry: obs.NewRegistry(), PG: pg, Redis: rdb, Migrations: mig.Check}
	srv := httpx.NewServer(cfg.HTTPAddr, APIHandler(d), log)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpx.Serve(gctx, srv, log, nil) })
	if ps := obs.NewPprofServer(cfg.PprofAddr); ps != nil {
		g.Go(func() error { return httpx.Serve(gctx, ps, log.With(slog.String("server", "pprof")), nil) })
	}
	if err := g.Wait(); err != nil {
		return fmt.Errorf("api: %w", err)
	}
	log.InfoContext(context.WithoutCancel(ctx), "api stopped")
	return nil
}
