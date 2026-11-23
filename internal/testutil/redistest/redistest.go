// Package redistest runs integration tests against a real Redis 8 container
// (testcontainers-go), one container per test package. NewClient flushes the
// server, so tests that use it must not run in parallel with each other
// (none call t.Parallel). The container runs with the same durability flags
// as deploy/compose.yml: AOF on, noeviction.
//
// Import it from _test.go files only.
package redistest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
)

// Image must match deploy/compose.yml.
const Image = "redis:8.10.3"

// Server is one Redis container shared by a test package.
type Server struct {
	container *tcredis.RedisContainer
	url       string
}

// Start launches the container.
func Start(ctx context.Context) (*Server, error) {
	c, err := tcredis.Run(ctx, Image,
		testcontainers.WithCmd("redis-server", "--appendonly", "yes", "--appendfsync", "everysec",
			"--maxmemory-policy", "noeviction"))
	if err != nil {
		if c != nil {
			_ = testcontainers.TerminateContainer(c)
		}
		return nil, fmt.Errorf("start redis: %w", err)
	}
	url, err := c.ConnectionString(ctx)
	if err != nil {
		_ = testcontainers.TerminateContainer(c)
		return nil, fmt.Errorf("redis connection string: %w", err)
	}
	return &Server{container: c, url: url}, nil
}

// Close stops the container.
func (s *Server) Close() { _ = testcontainers.TerminateContainer(s.container) }

// URL is the redis:// URL of the server.
func (s *Server) URL() string { return s.url }

// Require skips t when no server is running (-short).
func (s *Server) Require(t testing.TB) {
	t.Helper()
	if s == nil {
		t.Skip("integration test (testcontainers); skipped with -short")
	}
}

// NewClient flushes the server and returns a client on it.
func (s *Server) NewClient(t testing.TB) *redis.Client {
	t.Helper()
	s.Require(t)
	opt, err := redis.ParseURL(s.url)
	if err != nil {
		t.Fatal(err)
	}
	opt.PoolSize = 256
	opt.PoolTimeout = 30 * time.Second // race tests queue thousands of goroutines
	rdb := redis.NewClient(opt)
	t.Cleanup(func() { _ = rdb.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := rdb.FlushAll(ctx).Err(); err != nil {
		t.Fatalf("flushall: %v", err)
	}
	return rdb
}
