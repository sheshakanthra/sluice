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

// newScripts builds the shared Lua script set with its latency histogram on
// reg. Scripts are preloaded when Redis is reachable; if it is not, EVALSHA
// falls back to EVAL later.
func newScripts(ctx context.Context, log *slog.Logger, reg prometheus.Registerer, rdb *redis.Client) (*redisx.Scripts, error) {
	sc, err := redisx.NewScripts(rdb, reg)
	if err != nil {
		return nil, err
	}
	if err := sc.Load(ctx); err != nil {
		log.WarnContext(ctx, "preload redis scripts", slog.String("error", err.Error()))
	}
	return sc, nil
}

// newInventory builds the inventory service with its metrics on reg.
func newInventory(cfg *config.Config, log *slog.Logger, reg prometheus.Registerer,
	pg *pgxpool.Pool, rdb *redis.Client, sc *redisx.Scripts, onReclaim inventory.ReclaimHook,
) (*inventory.Service, error) {
	m, err := inventory.NewMetrics(reg)
	if err != nil {
		return nil, err
	}
	return inventory.NewService(pg, rdb, sc, inventory.Options{
		HoldTTL: cfg.HoldTTL, MaxSeats: cfg.MaxSeatsPerOrder, PGOnly: cfg.HoldPath == config.HoldPathPGOnly,
		Log: log, Metrics: m, OnReclaim: onReclaim,
	})
}
