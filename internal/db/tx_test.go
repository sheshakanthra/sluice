package db_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sheshakanthra/sluice/internal/db"
)

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{&pgconn.PgError{Code: "40001"}, true},
		{&pgconn.PgError{Code: "40P01"}, true},
		{fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "40001"}), true},
		{&pgconn.PgError{Code: "23505"}, false},
		{errors.New("plain"), false},
		{nil, false},
	}
	for _, tt := range tests {
		if got := db.IsRetryable(tt.err); got != tt.want {
			t.Errorf("IsRetryable(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}

func TestConstraintHelpers(t *testing.T) {
	uv := &pgconn.PgError{Code: "23505", ConstraintName: "x_key"}
	cv := &pgconn.PgError{Code: "23514", ConstraintName: "quota_cap"}
	tests := []struct {
		name string
		got  bool
		want bool
	}{
		{"unique any", db.IsUniqueViolation(uv, ""), true},
		{"unique named", db.IsUniqueViolation(fmt.Errorf("w: %w", uv), "x_key"), true},
		{"unique other name", db.IsUniqueViolation(uv, "y_key"), false},
		{"unique vs check", db.IsUniqueViolation(cv, ""), false},
		{"check named", db.IsCheckViolation(cv, "quota_cap"), true},
		{"check vs unique", db.IsCheckViolation(uv, ""), false},
		{"nil", db.IsCheckViolation(nil, ""), false},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
		}
	}
}

func TestWithTx(t *testing.T) {
	pool, _ := pg.NewMigratedDB(t)
	ctx := context.Background()
	// Hand-written SQL: a scratch table for exercising the tx helper itself.
	if _, err := pool.Exec(ctx, `CREATE TABLE counter (id int PRIMARY KEY, n int NOT NULL); INSERT INTO counter VALUES (1, 0)`); err != nil {
		t.Fatal(err)
	}
	read := func() int {
		var n int
		if err := pool.QueryRow(ctx, `SELECT n FROM counter WHERE id = 1`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	reset := func() {
		if _, err := pool.Exec(ctx, `UPDATE counter SET n = 0`); err != nil {
			t.Fatal(err)
		}
	}
	serialization := &pgconn.PgError{Code: "40001", Message: "could not serialize access"}

	t.Run("commits", func(t *testing.T) {
		reset()
		if err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE counter SET n = n + 1`)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if read() != 1 {
			t.Fatal("not committed")
		}
	})

	t.Run("rolls_back_on_error_without_retry", func(t *testing.T) {
		reset()
		calls := 0
		boom := errors.New("boom")
		err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
			calls++
			if _, err := tx.Exec(ctx, `UPDATE counter SET n = n + 1`); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) || calls != 1 || read() != 0 {
			t.Fatalf("err=%v calls=%d n=%d; want boom, 1 call, rolled back", err, calls, read())
		}
	})

	t.Run("retries_serialization_failure_then_commits", func(t *testing.T) {
		reset()
		calls := 0
		err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
			calls++
			if _, err := tx.Exec(ctx, `UPDATE counter SET n = n + 1`); err != nil {
				return err
			}
			if calls < 3 {
				return serialization
			}
			return nil
		})
		if err != nil || calls != 3 || read() != 1 {
			t.Fatalf("err=%v calls=%d n=%d; want nil, 3 calls, exactly one committed increment", err, calls, read())
		}
	})

	t.Run("gives_up_after_max_attempts", func(t *testing.T) {
		calls := 0
		err := db.WithTx(ctx, pool, func(pgx.Tx) error { calls++; return serialization }, db.MaxAttempts(4))
		if !errors.Is(err, db.ErrRetriesExhausted) || !db.IsRetryable(err) || calls != 4 {
			t.Fatalf("err=%v calls=%d; want ErrRetriesExhausted wrapping 40001 after 4 calls", err, calls)
		}
	})

	t.Run("no_retry_runs_once", func(t *testing.T) {
		calls := 0
		err := db.WithTx(ctx, pool, func(pgx.Tx) error { calls++; return serialization }, db.NoRetry())
		if calls != 1 || !db.IsRetryable(err) {
			t.Fatalf("err=%v calls=%d; want one call", err, calls)
		}
	})

	t.Run("context_cancel_stops_retrying", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		calls := 0
		err := db.WithTx(cctx, pool, func(pgx.Tx) error {
			calls++
			cancel()
			return serialization
		}, db.MaxAttempts(50))
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("err=%v calls=%d; want context.Canceled after 1 call", err, calls)
		}
	})

	// Real contention: serializable read-modify-write increments from many
	// goroutines. Postgres aborts the losers with 40001; WithTx re-runs them,
	// so every increment lands exactly once.
	t.Run("serializable_contention_all_increments_land", func(t *testing.T) {
		reset()
		const n = 32
		var attempts atomic.Int64
		var wg sync.WaitGroup
		start := make(chan struct{})
		for range n {
			wg.Go(func() {
				<-start
				err := db.WithTx(ctx, pool, func(tx pgx.Tx) error {
					attempts.Add(1)
					var cur int
					if err := tx.QueryRow(ctx, `SELECT n FROM counter WHERE id = 1`).Scan(&cur); err != nil {
						return err
					}
					time.Sleep(time.Millisecond) // widen the race window
					_, err := tx.Exec(ctx, `UPDATE counter SET n = $1 WHERE id = 1`, cur+1)
					return err
				}, db.Isolation(pgx.Serializable), db.MaxAttempts(200))
				if err != nil {
					t.Errorf("increment: %v", err)
				}
			})
		}
		close(start)
		wg.Wait()
		if got := read(); got != n {
			t.Fatalf("counter = %d, want %d", got, n)
		}
		if attempts.Load() <= n {
			t.Logf("note: no serialization failures occurred (%d attempts)", attempts.Load())
		} else {
			t.Logf("%d attempts for %d increments (%d retries)", attempts.Load(), n, attempts.Load()-n)
		}
	})
}
