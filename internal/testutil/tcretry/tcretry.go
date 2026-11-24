// Package tcretry retries testcontainers start-up. On Windows with Docker
// Desktop, starting containers from several test binaries at once
// occasionally fails before any test runs (the Docker provider is not found,
// or the named pipe times out). Those are infrastructure failures, not test
// failures, so the start is retried with backoff and, if it still fails, the
// error says so plainly.
package tcretry

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Attempts is how many times a container start is tried.
const Attempts = 3

// ErrInfra marks a container that never started.
var ErrInfra = errors.New("infra: container failed to start")

// Backoff is the wait before attempt n+1 (n = 1, 2, ...): 2 s, 4 s, ...
func Backoff(n int) time.Duration { return time.Duration(1<<n) * time.Second }

// Start runs start up to Attempts times. cleanup, if not nil, is called on
// the partial result of a failed attempt (testcontainers may return a
// container alongside an error). The final error wraps ErrInfra and the last
// attempt's error.
func Start[T any](ctx context.Context, what string, start func(context.Context) (T, error), cleanup func(T)) (T, error) {
	return startWith(ctx, what, start, cleanup, Backoff)
}

func startWith[T any](ctx context.Context, what string, start func(context.Context) (T, error), cleanup func(T),
	backoff func(int) time.Duration,
) (T, error) {
	var (
		zero    T
		lastErr error
	)
	for n := 1; n <= Attempts; n++ {
		v, err := start(ctx)
		if err == nil {
			return v, nil
		}
		lastErr = err
		if cleanup != nil {
			cleanup(v)
		}
		if n == Attempts {
			break
		}
		select {
		case <-ctx.Done():
			return zero, fmt.Errorf("%w: %s: %w (gave up after %d attempt(s): %w)", ErrInfra, what, lastErr, n, ctx.Err())
		case <-time.After(backoff(n)):
		}
	}
	return zero, fmt.Errorf("%w: %s after %d attempts: %w", ErrInfra, what, Attempts, lastErr)
}
