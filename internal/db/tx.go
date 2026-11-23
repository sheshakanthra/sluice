package db

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// SQLSTATEs that mean "the transaction lost a race; running it again may
// succeed". Postgres rolls the whole transaction back in both cases.
const (
	sqlstateSerializationFailure = "40001"
	sqlstateDeadlockDetected     = "40P01"
)

// Defaults for WithTx.
const (
	defaultMaxAttempts = 5
	baseBackoff        = 5 * time.Millisecond
	maxBackoff         = 200 * time.Millisecond
)

// ErrRetriesExhausted wraps the last retryable error once every attempt lost.
var ErrRetriesExhausted = errors.New("transaction retries exhausted")

// Beginner is satisfied by *pgxpool.Pool, *pgx.Conn and pgx.Tx.
type Beginner interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// TxFunc is the body of a transaction. When WithTx may retry, the body can run
// more than once, so it must be idempotent: no side effects outside the
// transaction (HTTP calls, Redis writes, channel sends) and no state carried
// between attempts.
type TxFunc func(tx pgx.Tx) error

// TxOption configures WithTx.
type TxOption func(*txConfig)

type txConfig struct {
	opts        pgx.TxOptions
	maxAttempts int
}

// Isolation sets the isolation level (default READ COMMITTED). Never lower it
// to make a test pass (CLAUDE.md rule 4).
func Isolation(level pgx.TxIsoLevel) TxOption {
	return func(c *txConfig) { c.opts.IsoLevel = level }
}

// ReadOnly marks the transaction READ ONLY.
func ReadOnly() TxOption {
	return func(c *txConfig) { c.opts.AccessMode = pgx.ReadOnly }
}

// MaxAttempts caps how many times a retryable failure is re-run (>= 1).
func MaxAttempts(n int) TxOption {
	return func(c *txConfig) {
		if n >= 1 {
			c.maxAttempts = n
		}
	}
}

// NoRetry runs fn exactly once. Use it for closures that are not idempotent.
func NoRetry() TxOption { return MaxAttempts(1) }

// WithTx runs fn inside a transaction and commits it. If fn or the commit fails
// with a serialization failure (40001) or a deadlock (40P01), the transaction
// is rolled back and fn runs again with jittered exponential backoff, up to
// MaxAttempts (default 5). Every other error rolls back and returns at once.
//
// Because fn may run several times, it must be idempotent (see TxFunc), or the
// caller must pass NoRetry.
func WithTx(ctx context.Context, b Beginner, fn TxFunc, opts ...TxOption) error {
	cfg := txConfig{maxAttempts: defaultMaxAttempts}
	for _, o := range opts {
		o(&cfg)
	}
	var lastErr error
	for attempt := 1; attempt <= cfg.maxAttempts; attempt++ {
		if attempt > 1 {
			if err := sleepCtx(ctx, backoff(attempt-1)); err != nil {
				return fmt.Errorf("tx retry: %w (last error: %w)", err, lastErr)
			}
		}
		err := runOnce(ctx, b, cfg.opts, fn)
		if err == nil {
			return nil
		}
		if !IsRetryable(err) {
			return err
		}
		lastErr = err
	}
	return fmt.Errorf("%w after %d attempts: %w", ErrRetriesExhausted, cfg.maxAttempts, lastErr)
}

func runOnce(ctx context.Context, b Beginner, opts pgx.TxOptions, fn TxFunc) (err error) {
	tx, err := b.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if err != nil {
			// Rollback after a failed statement or commit is expected to be a
			// no-op or to fail with "tx closed"; the original error wins.
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
	}()
	if err = fn(tx); err != nil { //nolint:gocritic // sloppyReassign: the named result drives the deferred rollback
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// IsRetryable reports whether err is a serialization failure or a deadlock.
func IsRetryable(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	return pgErr.Code == sqlstateSerializationFailure || pgErr.Code == sqlstateDeadlockDetected
}

// IsUniqueViolation reports whether err violates the named unique constraint
// or index (any unique constraint when name is empty).
func IsUniqueViolation(err error, name string) bool {
	return isConstraint(err, "23505", name)
}

// IsCheckViolation reports whether err violates the named CHECK constraint
// (any CHECK when name is empty).
func IsCheckViolation(err error, name string) bool {
	return isConstraint(err, "23514", name)
}

func isConstraint(err error, code, name string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		return false
	}
	return name == "" || pgErr.ConstraintName == name
}

// backoff returns a full-jitter delay for retry n (1-based).
func backoff(n int) time.Duration {
	d := baseBackoff << min(n-1, 6)
	d = min(d, maxBackoff)
	return time.Duration(rand.Int64N(int64(d)) + 1) //nolint:gosec // G404: jitter, not security
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
