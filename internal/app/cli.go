package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/events"
)

// oneShotTimeout bounds `sluice migrate` and `sluice seed`.
const oneShotTimeout = 5 * time.Minute

// RunMigrate runs `sluice migrate`: goose up, then River's migrator. Running
// it on a migrated database is a no-op.
func RunMigrate(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	ctx, cancel := context.WithTimeout(ctx, oneShotTimeout)
	defer cancel()
	log = log.With(slog.String("svc", "migrate"))

	pool, err := db.NewPool(ctx, cfg.DatabaseURL, 2)
	if err != nil {
		return err
	}
	defer pool.Close()
	m, err := db.NewMigrator(pool, log)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()

	res, err := m.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	log.InfoContext(ctx, "migrations applied",
		slog.Any("goose_applied", res.GooseApplied), slog.Int64("goose_version", res.GooseVersion),
		slog.Any("river_applied", res.RiverApplied))
	return nil
}

// SeedFunc returns the `sluice seed` service for the parsed flags. The
// --capacity permission depends on APP_ENV, which is only known once the
// config has loaded.
func SeedFunc(opts events.SeedOptions) func(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	return func(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
		ctx, cancel := context.WithTimeout(ctx, oneShotTimeout)
		defer cancel()
		log = log.With(slog.String("svc", "seed"))

		opts.AllowCapacity = cfg.AppEnv.AllowsUnsafeModes()
		specs, err := opts.Specs()
		if err != nil {
			return fmt.Errorf("seed: %w", err)
		}
		pool, err := db.NewPool(ctx, cfg.DatabaseURL, 2)
		if err != nil {
			return err
		}
		defer pool.Close()

		res, err := events.Seed(ctx, pool, specs, time.Now())
		if err != nil {
			return err
		}
		for _, r := range res {
			log.InfoContext(ctx, "event seeded",
				slog.String("slug", r.Slug), slog.Bool("created", r.Created),
				slog.Int64("seats_inserted", r.SeatsInserted), slog.Int("seats", r.Seats),
				slog.String("status", string(r.Status)), slog.Time("sale_opens_at", r.SaleOpensAt))
		}
		return nil
	}
}
