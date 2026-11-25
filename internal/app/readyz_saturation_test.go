package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/obs"
)

// testReadyzUnderSaturation reproduces the P5b 20k cascade (ADR 0013): the
// request pool is fully checked out, so every DB-backed request queues.
//
//   - Before (readiness on the request pool): /readyz cannot get a
//     connection and answers 503 after the 2 s check timeout, which is what
//     made Caddy eject every replica.
//   - After (dedicated health pool, cached state): /readyz answers 200 fast
//     while every request connection is busy and requests queue.
//
// Called from TestReadyzAgainstRealDependencies with a migrated database.
func testReadyzUnderSaturation(ctx context.Context, t *testing.T, base *config.Config, migCheck func(context.Context) error, rdb *redis.Client) {
	const reqConns = 2
	reqPool, err := db.NewPool(ctx, base.DatabaseURL, reqConns)
	if err != nil {
		t.Fatal(err)
	}
	defer reqPool.Close()
	healthPool, err := db.NewPool(ctx, base.DatabaseURL, healthPoolConns)
	if err != nil {
		t.Fatal(err)
	}
	defer healthPool.Close()

	// Check out every request-pool connection: the replica is saturated.
	held := make([]*pgxpool.Conn, 0, reqConns)
	for range reqConns {
		c, err := reqPool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	released := false
	release := func() {
		if !released {
			for _, c := range held {
				c.Release()
			}
			released = true
		}
	}
	defer release()

	cfg := *base
	cfg.MaxInFlight = 4
	cfg.ShedWait = 50 * time.Millisecond

	build := func(health *pgxpool.Pool) *httptest.Server {
		h, err := APIHandler(ctx, Deps{
			Config: &cfg, Log: slog.New(slog.DiscardHandler), Registry: obs.NewRegistry(),
			PG: reqPool, Redis: rdb, HealthPG: health, Migrations: migCheck,
		})
		if err != nil {
			t.Fatal(err)
		}
		return httptest.NewServer(h)
	}
	get := func(srv *httptest.Server, path string, timeout time.Duration) (int, http.Header, map[string]any, time.Duration) {
		rctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		req, _ := http.NewRequestWithContext(rctx, http.MethodGet, srv.URL+path, http.NoBody)
		start := time.Now()
		resp, err := srv.Client().Do(req)
		if err != nil {
			return 0, nil, nil, time.Since(start)
		}
		defer func() { _ = resp.Body.Close() }()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, resp.Header, body, time.Since(start)
	}

	// Before: readiness shares the saturated pool (HealthPG nil → PG).
	before := build(nil)
	defer before.Close()
	code, _, body, took := get(before, "/readyz", 5*time.Second)
	t.Logf("before (shared pool): /readyz = %d in %v %v", code, took.Round(time.Millisecond), body["checks"])
	if code != http.StatusServiceUnavailable {
		t.Fatalf("shared pool: expected /readyz 503 while saturated, got %d", code)
	}

	// After: dedicated health pool.
	after := build(healthPool)
	defer after.Close()

	// DB-backed requests that block on the saturated pool.
	var wg sync.WaitGroup
	blocked := make([]int, cfg.MaxInFlight)
	for i := range cfg.MaxInFlight {
		wg.Go(func() { blocked[i], _, _, _ = get(after, "/api/v1/events", 15*time.Second) })
	}
	time.Sleep(200 * time.Millisecond) // let them take their slots

	// More DB-backed reads queue on the saturated pool too (reads are never
	// shed; write shedding is covered by httpx.TestShed).
	extra := make([]int, 4)
	for i := range extra {
		wg.Go(func() { extra[i], _, _, _ = get(after, "/api/v1/events", 15*time.Second) })
	}
	time.Sleep(200 * time.Millisecond)

	// /readyz still answers 200, fast, while the replica is saturated.
	for i := range 5 {
		code, _, body, took = get(after, "/readyz", 3*time.Second)
		t.Logf("after: /readyz #%d = %d in %v", i, code, took.Round(time.Millisecond))
		if code != http.StatusOK {
			t.Fatalf("dedicated health pool: /readyz = %d %v while saturated", code, body)
		}
		if took > 500*time.Millisecond {
			t.Errorf("/readyz took %v while saturated (Caddy health_timeout is 2 s)", took)
		}
	}

	// Free the pool: the queued requests complete normally.
	release()
	wg.Wait()
	for i, c := range append(blocked, extra...) {
		if c != http.StatusOK {
			t.Errorf("blocked request %d = %d after release, want 200", i, c)
		}
	}
}
