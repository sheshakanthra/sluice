package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/obs"
)

// Images must match deploy/compose.yml.
const (
	postgresImage = "postgres:18.7"
	redisImage    = "redis:8.10.3"
)

// TestReadyzAgainstRealDependencies runs the real API handler against real
// Postgres 18 and Redis 8 containers: /readyz is 503 until migrations are
// applied, 200 once they are, and 503 again once Redis is stopped.
func TestReadyzAgainstRealDependencies(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test (testcontainers); skipped with -short")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	pgC, err := tcpostgres.Run(ctx, postgresImage,
		tcpostgres.WithDatabase("sluice"),
		tcpostgres.WithUsername("sluice"),
		tcpostgres.WithPassword("sluice"),
		tcpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, pgC)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	redisC, err := tcredis.Run(ctx, redisImage)
	testcontainers.CleanupContainer(t, redisC)
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}

	pgURL, err := pgC.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	redisURL, err := redisC.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(func(k string) (string, bool) {
		switch k {
		case "DATABASE_URL":
			return pgURL, true
		case "REDIS_URL":
			return redisURL, true
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}

	pool, err := db.NewPool(ctx, cfg.DatabaseURL, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	rdb, err := NewRedis(cfg.RedisURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rdb.Close() }()

	mig, err := db.NewMigrator(pool, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mig.Close() }()

	h := APIHandler(Deps{
		Config: cfg, Log: slog.New(slog.DiscardHandler), Registry: obs.NewRegistry(), PG: pool, Redis: rdb,
		Migrations: mig.Check,
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	readyz := func() (int, map[string]any) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/readyz", http.NoBody)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatalf("GET /readyz: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, body
	}

	// Not migrated yet: reachable but not ready.
	code, body := readyz()
	checks, _ := body["checks"].(map[string]any)
	if code != http.StatusServiceUnavailable || checks["migrations"] != "unavailable" || checks["postgres"] != "ok" || checks["redis"] != "ok" {
		t.Fatalf("before migrate: /readyz = %d %v", code, body)
	}
	if _, err := mig.Up(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if code, body := readyz(); code != http.StatusOK {
		t.Fatalf("migrated, both deps up: /readyz = %d %v", code, body)
	}

	// The events catalogue is mounted on the same handler.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", http.NoBody)
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Events []any `json:"events"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&list)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || list.Events == nil || len(list.Events) != 0 {
		t.Fatalf("/api/v1/events on an empty catalogue = %d %+v", resp.StatusCode, list)
	}

	stopTimeout := 10 * time.Second
	if err := redisC.Stop(ctx, &stopTimeout); err != nil {
		t.Fatalf("stop redis: %v", err)
	}
	code, body = readyz()
	checks, _ = body["checks"].(map[string]any)
	if code != http.StatusServiceUnavailable || checks["redis"] != "unavailable" || checks["postgres"] != "ok" {
		t.Fatalf("redis down: /readyz = %d %v", code, body)
	}

	// /metrics is served from the same handler.
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/metrics", http.NoBody)
	resp, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/metrics = %d", resp.StatusCode)
	}
}
