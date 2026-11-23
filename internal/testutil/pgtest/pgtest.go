// Package pgtest runs integration tests against a real Postgres 18 container
// (testcontainers-go). One container serves a whole test package (TestMain);
// every test gets its own database, either empty or cloned from a template
// that `sluice migrate` (goose + River) has already run against.
//
// Import it from _test.go files only.
package pgtest

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/sheshakanthra/sluice/internal/db"
)

// Image must match deploy/compose.yml.
const Image = "postgres:18.7"

const templateDB = "sluice_template"

// Server is one Postgres container shared by a test package.
type Server struct {
	container *tcpostgres.PostgresContainer
	baseURL   *url.URL
	admin     *pgxpool.Pool

	mu       sync.Mutex
	next     int
	template bool
}

// Main is used from TestMain:
//
//	var pg *pgtest.Server
//	func TestMain(m *testing.M) { os.Exit(pgtest.Main(m, &pg)) }
//
// With -short no container starts and *srv stays nil; tests call srv.Require.
func Main(m *testing.M, srv **Server) int {
	flag.Parse()
	if testing.Short() {
		return m.Run()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	s, err := Start(ctx)
	cancel()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "pgtest: %v\n", err)
		return 1
	}
	*srv = s
	code := m.Run()
	s.Close()
	return code
}

// Start launches the container.
func Start(ctx context.Context) (*Server, error) {
	c, err := tcpostgres.Run(ctx, Image,
		tcpostgres.WithDatabase("postgres"),
		tcpostgres.WithUsername("sluice"),
		tcpostgres.WithPassword("sluice"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		if c != nil {
			_ = testcontainers.TerminateContainer(c)
		}
		return nil, fmt.Errorf("start postgres: %w", err)
	}
	raw, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = testcontainers.TerminateContainer(c)
		return nil, fmt.Errorf("connection string: %w", err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		_ = testcontainers.TerminateContainer(c)
		return nil, fmt.Errorf("parse connection string: %w", err)
	}
	admin, err := db.NewPool(ctx, raw, 4)
	if err != nil {
		_ = testcontainers.TerminateContainer(c)
		return nil, err
	}
	return &Server{container: c, baseURL: u, admin: admin}, nil
}

// Close stops the container.
func (s *Server) Close() {
	s.admin.Close()
	_ = testcontainers.TerminateContainer(s.container)
}

// Require skips t when no server is running (-short).
func (s *Server) Require(t testing.TB) {
	t.Helper()
	if s == nil {
		t.Skip("integration test (testcontainers); skipped with -short")
	}
}

// URL returns the connection URL of database name.
func (s *Server) URL(name string) string {
	u := *s.baseURL
	u.Path = "/" + name
	return u.String()
}

// Container exposes the container (for stop/start chaos in tests).
func (s *Server) Container() *tcpostgres.PostgresContainer { return s.container }

// NewDB creates an empty database and returns a pool on it plus its URL.
func (s *Server) NewDB(t testing.TB) (*pgxpool.Pool, string) {
	t.Helper()
	s.Require(t)
	return s.create(t, "")
}

// NewMigratedDB returns a pool on a fresh, fully migrated database.
func (s *Server) NewMigratedDB(t testing.TB) (*pgxpool.Pool, string) {
	t.Helper()
	s.Require(t)
	if err := s.ensureTemplate(); err != nil {
		t.Fatalf("pgtest template: %v", err)
	}
	return s.create(t, templateDB)
}

func (s *Server) create(t testing.TB, template string) (*pgxpool.Pool, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	s.mu.Lock()
	s.next++
	name := fmt.Sprintf("t%04d", s.next)
	// CREATE DATABASE cannot take parameters; name and template are
	// generated here, never user input.
	stmt := "CREATE DATABASE " + pgx.Identifier{name}.Sanitize()
	if template != "" {
		stmt += " TEMPLATE " + pgx.Identifier{template}.Sanitize()
	}
	_, err := s.admin.Exec(ctx, stmt)
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}

	dsn := s.URL(name)
	pool, err := db.NewPool(ctx, dsn, 32)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, dsn
}

// ensureTemplate migrates the template database once per package.
func (s *Server) ensureTemplate() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.template {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := s.admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{templateDB}.Sanitize()); err != nil {
		return fmt.Errorf("create template: %w", err)
	}
	pool, err := db.NewPool(ctx, s.URL(templateDB), 4)
	if err != nil {
		return err
	}
	// The template must have no open connections when it is cloned.
	defer pool.Close()
	m, err := db.NewMigrator(pool, slog.New(slog.DiscardHandler))
	if err != nil {
		return err
	}
	_, upErr := m.Up(ctx)
	closeErr := m.Close()
	if err := errors.Join(upErr, closeErr); err != nil {
		return fmt.Errorf("migrate template: %w", err)
	}
	s.template = true
	return nil
}
