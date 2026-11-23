// Package db owns Postgres access: the pgx pool, transaction helpers with
// serialization-failure-aware retry, and migrations. Generated queries live
// in internal/db/gen (sqlc; run `task gen`).
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// connectTimeout bounds each new connection attempt.
const connectTimeout = 3 * time.Second

// NewPool builds a pgx pool capped at maxConns (DB_MAX_CONNS). Connections
// open lazily, so a process starts (and /readyz reports 503) even while
// Postgres is down.
func NewPool(ctx context.Context, url string, maxConns int32) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, errors.New("parse DATABASE_URL: invalid connection string") // never echo the URL (password)
	}
	if maxConns > 0 {
		pc.MaxConns = maxConns
	}
	pc.ConnConfig.ConnectTimeout = connectTimeout
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("create pg pool: %w", err)
	}
	return pool, nil
}
