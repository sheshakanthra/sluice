package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	dbfs "github.com/sheshakanthra/sluice/db"
)

// ErrMigrationsPending means the database is reachable but not fully migrated.
var ErrMigrationsPending = errors.New("migrations pending")

// Migrator applies SLUICE's goose migrations, then River's own schema.
type Migrator struct {
	sqlDB *sql.DB
	goose *goose.Provider
	river *rivermigrate.Migrator[pgx.Tx]
}

// MigrateResult summarises one Up run.
type MigrateResult struct {
	GooseApplied []int64 // goose versions applied by this run
	GooseVersion int64   // goose version after the run
	RiverApplied []int   // River versions applied by this run
}

// NewMigrator wires goose (with a Postgres advisory session lock, so two
// concurrent `sluice migrate` runs serialise) and River's migrator onto pool.
// Close releases the database/sql wrapper; it never closes pool.
func NewMigrator(pool *pgxpool.Pool, log *slog.Logger) (*Migrator, error) {
	sub, err := fs.Sub(dbfs.Migrations, "migrations")
	if err != nil {
		return nil, fmt.Errorf("migrations fs: %w", err)
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, fmt.Errorf("goose locker: %w", err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	gp, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub, goose.WithSessionLocker(locker))
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("goose provider: %w", err)
	}
	rm, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Logger: log})
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("river migrator: %w", err)
	}
	return &Migrator{sqlDB: sqlDB, goose: gp, river: rm}, nil
}

// Close releases the database/sql wrapper (the pgx pool stays open).
func (m *Migrator) Close() error { return m.sqlDB.Close() }

// Up applies pending goose migrations, then River's. Running it again on a
// migrated database is a no-op.
func (m *Migrator) Up(ctx context.Context) (MigrateResult, error) {
	var res MigrateResult
	applied, err := m.goose.Up(ctx)
	if err != nil {
		return res, fmt.Errorf("goose up: %w", err)
	}
	for _, r := range applied {
		res.GooseApplied = append(res.GooseApplied, r.Source.Version)
	}
	if res.GooseVersion, err = m.goose.GetDBVersion(ctx); err != nil {
		return res, fmt.Errorf("goose version: %w", err)
	}
	rr, err := m.river.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return res, fmt.Errorf("river migrate: %w", err)
	}
	for _, v := range rr.Versions {
		res.RiverApplied = append(res.RiverApplied, v.Version)
	}
	return res, nil
}

// DownAll rolls every goose migration back (River's tables are left alone).
// Tests use it to prove the Down section is complete.
func (m *Migrator) DownAll(ctx context.Context) error {
	if _, err := m.goose.DownTo(ctx, 0); err != nil {
		return fmt.Errorf("goose down: %w", err)
	}
	return nil
}

// Check returns nil when both goose and River are fully migrated, and an error
// wrapping ErrMigrationsPending when either has work left. It takes no lock,
// so /readyz can call it while a migration runs.
func (m *Migrator) Check(ctx context.Context) error {
	pending, err := m.goose.HasPending(ctx)
	if err != nil {
		return fmt.Errorf("goose status: %w", err)
	}
	if pending {
		return fmt.Errorf("%w: goose", ErrMigrationsPending)
	}
	vr, err := m.river.Validate(ctx, nil)
	if err != nil {
		return fmt.Errorf("river status: %w", err)
	}
	if !vr.OK {
		return fmt.Errorf("%w: river: %s", ErrMigrationsPending, strings.Join(vr.Messages, "; "))
	}
	return nil
}
