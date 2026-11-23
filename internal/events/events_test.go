package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sheshakanthra/sluice/internal/httpx"
)

func dur(d time.Duration) *time.Duration { return &d }

func TestSeedOptionsSpecs(t *testing.T) {
	tests := []struct {
		name      string
		opts      SeedOptions
		wantErr   string
		wantSlugs []string
		wantCap   int
		wantOpens []time.Duration
	}{
		{name: "defaults", opts: SeedOptions{}, wantSlugs: []string{SlugTatkal, SlugQuiet}, wantCap: 500, wantOpens: []time.Duration{10 * time.Minute, 0}},
		{name: "one demo event", opts: SeedOptions{Event: SlugQuiet}, wantSlugs: []string{SlugQuiet}, wantCap: 500, wantOpens: []time.Duration{0}},
		{name: "opens-in overrides all", opts: SeedOptions{OpensIn: dur(time.Minute)}, wantSlugs: []string{SlugTatkal, SlugQuiet}, wantCap: 500, wantOpens: []time.Duration{time.Minute, time.Minute}},
		{name: "custom event with capacity in rush", opts: SeedOptions{Event: "loadtest-big", Capacity: 50000, AllowCapacity: true}, wantSlugs: []string{"loadtest-big"}, wantCap: 50000, wantOpens: []time.Duration{10 * time.Minute}},
		{name: "capacity refused outside rush/test", opts: SeedOptions{Event: "loadtest-big", Capacity: 50000}, wantErr: "only allowed when APP_ENV"},
		{name: "capacity needs event", opts: SeedOptions{Capacity: 10, AllowCapacity: true}, wantErr: "needs --event"},
		{name: "capacity too large", opts: SeedOptions{Event: "x", Capacity: 2_000_000, AllowCapacity: true}, wantErr: "must be 1.."},
		{name: "negative capacity", opts: SeedOptions{Event: "x", Capacity: -1, AllowCapacity: true}, wantErr: "must be 1.."},
		{name: "negative opens-in", opts: SeedOptions{OpensIn: dur(-time.Second)}, wantErr: "must be >= 0"},
		{name: "bad slug", opts: SeedOptions{Event: "Bad Slug!"}, wantErr: "not a valid slug"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			specs, err := tt.opts.Specs()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(specs) != len(tt.wantSlugs) {
				t.Fatalf("%d specs, want %d", len(specs), len(tt.wantSlugs))
			}
			for i, s := range specs {
				if s.Slug != tt.wantSlugs[i] || s.Capacity != tt.wantCap || s.OpensIn != tt.wantOpens[i] || s.MaxPerUser != 4 || s.PricePaise != 125000 {
					t.Errorf("spec %d = %+v", i, s)
				}
			}
		})
	}
}

func TestValidSlug(t *testing.T) {
	for s, want := range map[string]bool{
		"tatkal-chn-sbc": true, "quiet-tuesday": true, "a": true, "loadtest-big": true,
		"": false, "-a": false, "a-": false, "a--b": false, "A": false, "a_b": false, "a b": false,
		"0193a3a0-0000-7000-8000-000000000000": true, // uuid-shaped; parsed as a uuid first
		string(make([]byte, 65)):               false,
	} {
		if got := ValidSlug(s); got != want {
			t.Errorf("ValidSlug(%q) = %v, want %v", s, got, want)
		}
	}
}

type fakeReader struct {
	list []Event
	get  EventDetail
	err  error
}

func (f fakeReader) List(context.Context) ([]Event, error)            { return f.list, f.err }
func (f fakeReader) Get(context.Context, string) (EventDetail, error) { return f.get, f.err }

func TestHandlersErrorMapping(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   httpx.Code
	}{
		{"not found", ErrEventNotFound, http.StatusNotFound, httpx.CodeNotFound},
		{"postgres down", fmt.Errorf("get event: %w", errors.New("dial tcp: connection refused")), http.StatusServiceUnavailable, httpx.CodeDependencyUnavailable},
		{"sql bug", fmt.Errorf("get event: %w", &pgconn.PgError{Code: "42703"}), http.StatusInternalServerError, httpx.CodeInternal},
	}
	for _, tt := range tests {
		for _, path := range []string{"/api/v1/events", "/api/v1/events/nope"} {
			t.Run(tt.name+" "+path, func(t *testing.T) {
				rt := httpx.NewRouter()
				RegisterRoutes(rt, fakeReader{err: tt.err}, slog.New(slog.DiscardHandler))
				rec := httptest.NewRecorder()
				rt.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
				if path == "/api/v1/events" && errors.Is(tt.err, ErrEventNotFound) {
					return // List never returns not-found in practice
				}
				var body map[string]any
				_ = json.Unmarshal(rec.Body.Bytes(), &body)
				if rec.Code != tt.wantStatus || body["code"] != string(tt.wantCode) {
					t.Fatalf("status %d body %v, want %d %s", rec.Code, body, tt.wantStatus, tt.wantCode)
				}
				if rec.Header().Get("Content-Type") != httpx.ProblemContentType {
					t.Fatalf("content-type %q", rec.Header().Get("Content-Type"))
				}
				if tt.wantStatus == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") != "1" {
					t.Fatalf("Retry-After %q", rec.Header().Get("Retry-After"))
				}
			})
		}
	}
}
