package ratelimit_test

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/ratelimit"
	"github.com/sheshakanthra/sluice/internal/testutil/redistest"
)

var rs *redistest.Server

func TestMain(m *testing.M) {
	flag.Parse()
	if !testing.Short() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		var err error
		rs, err = redistest.Start(ctx)
		cancel()
		if err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "redis container:", err)
			os.Exit(1)
		}
	}
	code := m.Run()
	if rs != nil {
		rs.Close()
	}
	os.Exit(code)
}

var trusted = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("172.16.0.0/12")}

func TestClientIP(t *testing.T) {
	tests := []struct {
		name, remote string
		xff          []string
		want         string
	}{
		{"direct client, no xff", "203.0.113.7:5000", nil, "203.0.113.7"},
		{"untrusted peer: xff ignored", "203.0.113.7:5000", []string{"198.51.100.1"}, "203.0.113.7"},
		{"trusted proxy: last hop", "172.18.0.5:5000", []string{"198.51.100.1"}, "198.51.100.1"},
		{"spoofed left entries ignored", "172.18.0.5:5000", []string{"6.6.6.6, 198.51.100.1"}, "198.51.100.1"},
		{"chain of trusted proxies", "127.0.0.1:5000", []string{"198.51.100.1, 172.18.0.9"}, "198.51.100.1"},
		{"multiple header lines", "127.0.0.1:5000", []string{"6.6.6.6", "198.51.100.2"}, "198.51.100.2"},
		{"trusted proxy, no xff", "172.18.0.5:5000", nil, "172.18.0.5"},
		{"all hops trusted", "127.0.0.1:5000", []string{"172.18.0.9"}, "172.18.0.9"},
		{"garbage hop stops the walk", "127.0.0.1:5000", []string{"198.51.100.1, nonsense"}, "127.0.0.1"},
		{"ipv4-mapped ipv6 peer", "[::ffff:203.0.113.7]:5000", nil, "203.0.113.7"},
		{"bad remote addr", "nonsense", nil, "invalid IP"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			r.RemoteAddr = tc.remote
			for _, v := range tc.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := ratelimit.ClientIP(r, trusted).String(); got != tc.want {
				t.Errorf("ClientIP = %s, want %s", got, tc.want)
			}
		})
	}
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }

// send makes one request from ip (through a trusted proxy) as user uid.
func send(h http.Handler, ip string, uid uuid.UUID) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/x", http.NoBody)
	r.RemoteAddr = "172.18.0.2:4000"
	r.Header.Set("X-Forwarded-For", ip)
	if uid != uuid.Nil {
		r.Header.Set("X-Test-User", uid.String())
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// testUser reads the user id from a test header (the app uses the verified
// bearer token's subject).
func testUser(r *http.Request) (uuid.UUID, bool) {
	u, err := uuid.Parse(r.Header.Get("X-Test-User"))
	return u, err == nil
}

// TestGCRA checks the Redis limiter against real Redis 8: per-IP and
// per-user budgets, independent subjects, the 429 problem with Retry-After
// and the RateLimit fields, and that concurrent replicas share one budget.
func TestGCRA(t *testing.T) {
	if rs == nil {
		t.Skip("needs Redis")
	}
	rdb := rs.NewClient(t)
	g := ratelimit.NewGCRA(rdb, trusted, testUser, nil, nil)

	t.Run("per ip", func(t *testing.T) {
		h := g.Middleware(ratelimit.PerMinute("t-sessions", ratelimit.ByIP, 20))(http.HandlerFunc(ok))
		for i := range 20 {
			if rec := send(h, "198.51.100.10", uuid.Nil); rec.Code != http.StatusNoContent {
				t.Fatalf("request %d: %d", i+1, rec.Code)
			}
		}
		rec := send(h, "198.51.100.10", uuid.Nil)
		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("21st request: %d", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "RATE_LIMITED") || rec.Header().Get("Retry-After") != "3" {
			t.Errorf("429 body %s Retry-After %q (one token per 3 s)", rec.Body, rec.Header().Get("Retry-After"))
		}
		if p := rec.Header().Get("RateLimit-Policy"); p != `"t-sessions-ip";q=20;w=60` {
			t.Errorf("RateLimit-Policy = %q", p)
		}
		if rl := rec.Header().Get("RateLimit"); !strings.HasPrefix(rl, `"t-sessions-ip";r=0;t=`) {
			t.Errorf("RateLimit = %q", rl)
		}
		if rec := send(h, "198.51.100.11", uuid.Nil); rec.Code != http.StatusNoContent {
			t.Errorf("another IP is limited too: %d", rec.Code)
		}
	})

	t.Run("join: user and ip", func(t *testing.T) {
		h := g.Middleware(
			ratelimit.PerMinute("t-join", ratelimit.ByUser, 6),
			ratelimit.PerMinute("t-join", ratelimit.ByIP, 30),
		)(http.HandlerFunc(ok))
		u := uuid.New()
		for i := range 6 {
			rec := send(h, "198.51.100.20", u)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("join %d: %d", i+1, rec.Code)
			}
			if i == 0 && rec.Header().Get("RateLimit") != `"t-join-user";r=5;t=10` {
				t.Errorf("RateLimit after 1 of 6 = %q (the tighter rule is reported)", rec.Header().Get("RateLimit"))
			}
		}
		if rec := send(h, "198.51.100.20", u); rec.Code != http.StatusTooManyRequests {
			t.Fatalf("7th join by one user: %d", rec.Code)
		}
		// Same IP, other users: the IP budget (30) has room for 24 more (the
		// user's 7th request was refused by the user rule, before the IP rule
		// took a token).
		for i := range 24 {
			if rec := send(h, "198.51.100.20", uuid.New()); rec.Code != http.StatusNoContent {
				t.Fatalf("join %d from the shared IP: %d", i+7, rec.Code)
			}
		}
		if rec := send(h, "198.51.100.20", uuid.New()); rec.Code != http.StatusTooManyRequests {
			t.Errorf("31st join from one IP: %d", rec.Code)
		}
		// No token: user rule skipped, IP rule still applies.
		if rec := send(h, "198.51.100.21", uuid.Nil); rec.Code != http.StatusNoContent {
			t.Errorf("anonymous request: %d", rec.Code)
		}
	})

	t.Run("shared across replicas", func(t *testing.T) {
		// Three middleware instances (three API replicas) share Redis.
		var hs []http.Handler
		for range 3 {
			hs = append(hs, ratelimit.NewGCRA(rdb, trusted, testUser, nil, nil).
				Middleware(ratelimit.PerMinute("t-holds", ratelimit.ByUser, 20))(http.HandlerFunc(ok)))
		}
		u := uuid.New()
		var allowed, limited atomic.Int64
		var wg sync.WaitGroup
		for i := range 60 {
			wg.Go(func() {
				switch send(hs[i%3], "198.51.100.30", u).Code {
				case http.StatusNoContent:
					allowed.Add(1)
				case http.StatusTooManyRequests:
					limited.Add(1)
				}
			})
		}
		wg.Wait()
		if allowed.Load() != 20 || limited.Load() != 40 {
			t.Errorf("60 concurrent requests over 3 replicas: %d allowed, %d limited; want 20/40",
				allowed.Load(), limited.Load())
		}
	})

	t.Run("redis down fails open", func(t *testing.T) {
		dead := rs.NewClient(t)
		_ = dead.Close()
		h := ratelimit.NewGCRA(dead, trusted, testUser, nil, nil).
			Middleware(ratelimit.PerMinute("t-down", ratelimit.ByIP, 1))(http.HandlerFunc(ok))
		for range 3 {
			if rec := send(h, "198.51.100.40", uuid.Nil); rec.Code != http.StatusNoContent {
				t.Fatalf("with Redis unavailable: %d", rec.Code)
			}
		}
	})
}

// TestLocal checks the in-process status limiter: burst 6, then 3/s; users
// are independent; the LRU stays bounded.
func TestLocal(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := ratelimit.NewLocal(ratelimit.LocalConfig{Rate: 3, Burst: 6, Capacity: 64, Now: func() time.Time { return now }})
	u := uuid.New()
	for i := range 6 {
		if okay, _ := l.Allow(u); !okay {
			t.Fatalf("burst request %d refused", i+1)
		}
	}
	okay, retry := l.Allow(u)
	if okay || retry <= 0 || retry > time.Second/3+time.Millisecond {
		t.Fatalf("7th request: allowed=%v retry=%v, want refused with retry <= 1/3 s", okay, retry)
	}
	if okay, _ := l.Allow(uuid.New()); !okay {
		t.Error("another user is limited")
	}
	now = now.Add(time.Second) // +3 tokens
	n := 0
	for {
		if okay, _ := l.Allow(u); !okay {
			break
		}
		n++
	}
	if n != 3 {
		t.Errorf("after 1 s: %d requests allowed, want 3", n)
	}
	for range 1000 {
		l.Allow(uuid.New())
	}
	if got := l.Len(); got > 64 {
		t.Errorf("LRU holds %d users, capacity 64", got)
	}

	// Through the middleware: 429 with Retry-After and the RateLimit fields.
	h := l.Middleware("status", testUser, nil)(http.HandlerFunc(ok))
	v := uuid.New()
	codes := map[int]int{}
	var last *httptest.ResponseRecorder
	for range 8 {
		last = send(h, "198.51.100.50", v)
		codes[last.Code]++
	}
	if codes[http.StatusNoContent] != 6 || codes[http.StatusTooManyRequests] != 2 {
		t.Errorf("8 status polls: %v, want 6 allowed and 2 limited", codes)
	}
	if last.Header().Get("Retry-After") != "1" || last.Header().Get("RateLimit-Policy") != `"status-user";q=6;w=1` {
		t.Errorf("429 headers: %v", last.Header())
	}
}

// TestLocalConcurrent hammers the limiter from many goroutines (run with
// -race): for one user exactly Burst requests pass when the clock is frozen.
func TestLocalConcurrent(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := ratelimit.NewLocal(ratelimit.LocalConfig{Rate: 3, Burst: 6, Capacity: 10_000, Now: func() time.Time { return now }})
	users := make([]uuid.UUID, 50)
	for i := range users {
		users[i] = uuid.New()
	}
	allowed := make([]atomic.Int64, len(users))
	var wg sync.WaitGroup
	for g := range 64 {
		wg.Go(func() {
			for i := range 200 {
				k := (g + i) % len(users)
				if okay, _ := l.Allow(users[k]); okay {
					allowed[k].Add(1)
				}
			}
		})
	}
	wg.Wait()
	for i := range allowed {
		if got := allowed[i].Load(); got != 6 {
			t.Fatalf("user %d: %d allowed with a frozen clock, want 6", i, got)
		}
	}
}
