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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/events"
	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/jobs"
	"github.com/sheshakanthra/sluice/internal/obs"
	"github.com/sheshakanthra/sluice/internal/orders"
	"github.com/sheshakanthra/sluice/internal/queue"
	"github.com/sheshakanthra/sluice/internal/redisx"
)

// requestTimeout bounds every API request context.
const requestTimeout = 10 * time.Second

// availabilityMinAge is how often each replica re-checks an event's seat map.
const availabilityMinAge = 100 * time.Millisecond

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
func NewRedis(url string) (*redis.Client, error) { return redisx.NewClient(url) }

// APIHandler builds the full API handler: routes plus the middleware stack.
// Order (outermost first): request id → observe (metrics + access log) →
// recover → CORS → timeout → router.
func APIHandler(ctx context.Context, d Deps) (http.Handler, error) {
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

	keyring, err := auth.NewKeyring(d.Config.TokenKeys, nil)
	if err != nil {
		return nil, err
	}
	sc, err := newScripts(ctx, d.Log, d.Registry, d.Redis)
	if err != nil {
		return nil, err
	}
	var ord *orders.Service // the reclaim hook needs the orders service, which needs inventory
	inv, err := newInventory(d.Config, d.Log, d.Registry, d.PG, d.Redis, sc,
		func(ctx context.Context, tx pgx.Tx, hid uuid.UUID) error { return ord.OnReclaim(ctx, tx, hid) })
	if err != nil {
		return nil, err
	}
	ins, err := jobs.NewInsertClient(d.PG, d.Log)
	if err != nil {
		return nil, err
	}
	if ord, err = newOrders(d.Config, d.Log, d.Registry, d.PG, d.Redis, inv, ins); err != nil {
		return nil, err
	}
	replays, err := httpx.NewReplayCounter(d.Registry)
	if err != nil {
		return nil, err
	}
	idem := httpx.NewIdempotency(gen.New(d.PG), auth.UserID, d.Log, nil).WithReplays(replays)
	avail := inv.NewAvailabilityCache(availabilityMinAge)
	q := gen.New(d.PG)

	store := events.NewStore(q).WithLiveCounts(func(ctx context.Context, id uuid.UUID) (events.Counts, error) {
		c, err := avail.LiveCounts(ctx, id)
		//nolint:gosec // G115: seat counts are bounded by events.capacity (int32)
		return events.Counts{Available: int32(c.Available), Held: int32(c.Held), Sold: int32(c.Sold), Capacity: int32(c.Capacity)}, err
	})
	events.RegisterRoutes(rt, store, d.Log)

	rt.HandleFunc("POST /api/v1/sessions", keyring.SessionsHandler(d.Log))
	if d.Config.TestEndpointsEnabled && d.Config.AppEnv.AllowsUnsafeModes() {
		resolve := func(ctx context.Context, idOrSlug string) (uuid.UUID, bool, error) {
			ev, err := inv.Event(ctx, idOrSlug)
			if err != nil {
				if errors.Is(err, inventory.ErrEventNotFound) {
					return uuid.Nil, false, nil
				}
				return uuid.Nil, false, err
			}
			return ev.ID, true, nil
		}
		rt.HandleFunc("POST /api/v1/test/admit", keyring.TestAdmitHandler(resolve, d.Config.AdmissionTTL, d.Log))
	}
	inventory.RegisterRoutes(rt, inventory.HandlerDeps{
		Svc: inv, Avail: avail, Keys: keyring,
		Idem:          idem,
		QueueEnforced: d.Config.QueueEnforced, AdminToken: d.Config.AdminToken, Log: d.Log,
	})
	qsvc, err := newQueueService(d.Config, d.Log, d.Registry, sc, keyring)
	if err != nil {
		return nil, err
	}
	// The snapshot refresher lives as long as the process context.
	go func() { _ = qsvc.Cache().Run(ctx, queueSnapshotInterval) }()
	queue.RegisterRoutes(rt, queue.HandlerDeps{Svc: qsvc, Events: inv, Keys: keyring, Log: d.Log})
	orders.RegisterRoutes(rt, orders.HandlerDeps{
		Svc: ord, Keys: keyring, Idem: idem, QueueEnforced: d.Config.QueueEnforced,
		AdminToken: d.Config.AdminToken, WebhookSecret: d.Config.MockpayWebhookSecret, Log: d.Log,
	})

	m := httpx.NewHTTPMetrics(d.Registry, "api")
	return httpx.Chain(rt,
		httpx.RequestID(d.Config.TrustedProxies),
		httpx.Observe(d.Log, m),
		httpx.Recover(d.Log),
		httpx.CORS(d.Config.CORSOrigins),
		httpx.Timeout(requestTimeout),
	), nil
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
	h, err := APIHandler(ctx, d)
	if err != nil {
		return fmt.Errorf("api: %w", err)
	}
	srv := httpx.NewServer(cfg.HTTPAddr, h, log)

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
