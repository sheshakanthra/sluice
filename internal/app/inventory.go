package app

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/redisx"
)

// newInventory builds the inventory service with its scripts and metrics
// registered on reg. Scripts are preloaded when Redis is reachable; if it
// is not, EVALSHA falls back to EVAL later.
func newInventory(ctx context.Context, cfg *config.Config, log *slog.Logger, reg prometheus.Registerer,
	pg *pgxpool.Pool, rdb *redis.Client,
) (*inventory.Service, error) {
	sc, err := redisx.NewScripts(rdb, reg)
	if err != nil {
		return nil, err
	}
	if err := sc.Load(ctx); err != nil {
		log.WarnContext(ctx, "preload redis scripts", slog.String("error", err.Error()))
	}
	m, err := inventory.NewMetrics(reg)
	if err != nil {
		return nil, err
	}
	return inventory.NewService(pg, rdb, sc, inventory.Options{
		HoldTTL: cfg.HoldTTL, MaxSeats: cfg.MaxSeatsPerOrder, PGOnly: cfg.HoldPath == config.HoldPathPGOnly,
		Log: log, Metrics: m,
	})
}
