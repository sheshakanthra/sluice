package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/sheshakanthra/sluice/internal/obs"
)

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

// stack builds the production middleware order around rt.
func stack(rt *Router, log *slog.Logger, m *HTTPMetrics, trusted []netip.Prefix, origins []string) http.Handler {
	return Chain(rt, RequestID(trusted), Observe(log, m), Recover(log), CORS(origins), Timeout(time.Second))
}

func decode(t *testing.T, body io.Reader) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(body).Decode(&m); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return m
}

func TestProblemCatalogue(t *testing.T) {
	tests := []struct {
		code   Code
		status int
		typ    string
	}{
		{CodeValidation, 400, "https://sluice.dev/errors/validation"},
		{CodeIdempotencyKeyRequired, 400, "https://sluice.dev/errors/idempotency-key-required"},
		{CodeTokenInvalid, 401, "https://sluice.dev/errors/token-invalid"},
		{CodeTokenExpired, 401, "https://sluice.dev/errors/token-expired"},
		{CodeNotAdmitted, 403, "https://sluice.dev/errors/not-admitted"},
		{CodeAdmissionExpired, 403, "https://sluice.dev/errors/admission-expired"},
		{CodeNotFound, 404, "https://sluice.dev/errors/not-found"},
		{CodeSeatsTaken, 409, "https://sluice.dev/errors/seats-taken"},
		{CodeInsufficientSeats, 409, "https://sluice.dev/errors/insufficient-seats"},
		{CodeSoldOut, 409, "https://sluice.dev/errors/sold-out"},
		{CodeHoldLimit, 409, "https://sluice.dev/errors/hold-limit"},
		{CodeHoldExpired, 409, "https://sluice.dev/errors/hold-expired"},
		{CodeRejoinLimit, 409, "https://sluice.dev/errors/rejoin-limit"},
		{CodeEventState, 409, "https://sluice.dev/errors/event-state"},
		{CodeOrderState, 409, "https://sluice.dev/errors/order-state"},
		{CodeRequestInProgress, 409, "https://sluice.dev/errors/request-in-progress"},
		{CodeQuotaExceeded, 422, "https://sluice.dev/errors/quota-exceeded"},
		{CodeIdempotencyKeyReused, 422, "https://sluice.dev/errors/idempotency-key-reused"},
		{CodePaymentAttemptsExhausted, 422, "https://sluice.dev/errors/payment-attempts-exhausted"},
		{CodeRateLimited, 429, "https://sluice.dev/errors/rate-limited"},
		{CodeDependencyUnavailable, 503, "https://sluice.dev/errors/dependency-unavailable"},
		{CodeOverloaded, 503, "https://sluice.dev/errors/overloaded"},
		{CodeInternal, 500, "https://sluice.dev/errors/internal"},
	}
	if len(tests) != len(catalogue) {
		t.Fatalf("test table has %d codes, catalogue has %d", len(tests), len(catalogue))
	}
	for _, tc := range tests {
		t.Run(string(tc.code), func(t *testing.T) {
			p := NewProblem(tc.code, "")
			if p.Status != tc.status || tc.code.Status() != tc.status || p.Type != tc.typ || p.Title == "" {
				t.Fatalf("got %+v", p)
			}
		})
	}
	if Code("NOPE").Status() != 500 {
		t.Error("unknown codes must map to 500")
	}
}

func TestBusinessOutcomesAreNever5xx(t *testing.T) {
	// I8: "no" is always a 2xx/4xx. Only infra codes may be 5xx.
	for code, ci := range catalogue {
		if ci.status >= 500 && code != CodeDependencyUnavailable && code != CodeOverloaded && code != CodeInternal {
			t.Errorf("%s maps to %d", code, ci.status)
		}
	}
}

func TestWriteProblem(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/x", http.NoBody)
	r = r.WithContext(obs.WithRequestID(r.Context(), "req-42"))
	rec := httptest.NewRecorder()
	WriteProblem(rec, r, NewProblem(CodeSeatsTaken, "2 of 3 seats are no longer available").
		With("taken", []int{12, 13}).
		With("status", 999). // must not override the standard member
		WithRetryAfter(1500*time.Millisecond))

	if rec.Code != 409 {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != ProblemContentType {
		t.Errorf("content-type %q", ct)
	}
	if ra := rec.Header().Get("Retry-After"); ra != "2" {
		t.Errorf("Retry-After %q, want 2 (rounded up)", ra)
	}
	body := decode(t, rec.Body)
	want := map[string]any{
		"type": "https://sluice.dev/errors/seats-taken", "title": "Some seats were just taken",
		"status": float64(409), "code": "SEATS_TAKEN", "detail": "2 of 3 seats are no longer available",
		"request_id": "req-42",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v, want %v", k, body[k], v)
		}
	}
	if taken, _ := body["taken"].([]any); len(taken) != 2 {
		t.Errorf("taken extension missing: %v", body)
	}
}

func TestRouterAndHealth(t *testing.T) {
	var redisErr error
	var mu sync.Mutex
	rt := NewRouter()
	RegisterHealth(rt, NewReadiness(discardLogger(), 0,
		Check{Name: "postgres", Ping: func(context.Context) error { return nil }},
		Check{Name: "redis", Ping: func(context.Context) error { mu.Lock(); defer mu.Unlock(); return redisErr }},
	))
	h := stack(rt, discardLogger(), NewHTTPMetrics(prometheus.NewRegistry(), "test"), nil, nil)

	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody))
		return rec
	}

	if rec := get("/healthz"); rec.Code != 200 || decode(t, rec.Body)["status"] != "ok" {
		t.Errorf("/healthz: %d", rec.Code)
	}
	rec := get("/api/v1/healthz")
	body := decode(t, rec.Body)
	if rec.Code != 200 || body["status"] != "ok" || body["mode"] != "live" {
		t.Errorf("/api/v1/healthz: %d %v", rec.Code, body)
	}
	if rec := get("/readyz"); rec.Code != 200 {
		t.Errorf("/readyz healthy: %d", rec.Code)
	}

	mu.Lock()
	redisErr = errors.New("dial tcp 127.0.0.1:6380: connection refused")
	mu.Unlock()
	rec = get("/readyz")
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("/readyz unhealthy: %d", rec.Code)
	}
	raw := rec.Body.String()
	if strings.Contains(raw, "6380") {
		t.Errorf("readyz leaked error details: %s", raw)
	}
	body = decode(t, strings.NewReader(raw))
	checks, _ := body["checks"].(map[string]any)
	if body["code"] != "DEPENDENCY_UNAVAILABLE" || checks["redis"] != "unavailable" || checks["postgres"] != "ok" {
		t.Errorf("readyz body: %v", body)
	}

	rec = get("/nope")
	if rec.Code != 404 || decode(t, rec.Body)["code"] != "NOT_FOUND" {
		t.Errorf("unmatched route: %d", rec.Code)
	}
}

func TestRequestID(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	tests := []struct {
		name       string
		remote     string
		header     string
		wantHeader bool // incoming header is kept
	}{
		{"generated when absent", "10.1.1.1:5000", "", false},
		{"kept from trusted proxy", "10.1.1.1:5000", "abc-123", true},
		{"ignored from untrusted client", "203.0.113.9:5000", "abc-123", false},
		{"ignored when malformed", "10.1.1.1:5000", "bad id\n", false},
		{"ignored when too long", "10.1.1.1:5000", strings.Repeat("a", 129), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			h := RequestID(trusted)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seen = obs.RequestID(r.Context())
			}))
			r := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			r.RemoteAddr = tc.remote
			if tc.header != "" {
				r.Header.Set(HeaderRequestID, tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			got := rec.Header().Get(HeaderRequestID)
			if got == "" || got != seen {
				t.Fatalf("response id %q, context id %q", got, seen)
			}
			if (got == tc.header) != tc.wantHeader {
				t.Fatalf("got id %q (incoming %q), wantHeader=%v", got, tc.header, tc.wantHeader)
			}
		})
	}
}

func TestRecover(t *testing.T) {
	var logBuf bytes.Buffer
	log := obs.NewLogger(&logBuf, slog.LevelInfo)
	rt := NewRouter()
	rt.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("kaboom") })
	reg := prometheus.NewRegistry()
	m := NewHTTPMetrics(reg, "test")
	h := stack(rt, log, m, nil, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", http.NoBody))
	if rec.Code != 500 {
		t.Fatalf("status %d", rec.Code)
	}
	body := decode(t, rec.Body)
	if body["code"] != "INTERNAL" || body["request_id"] == "" || body["request_id"] != rec.Header().Get(HeaderRequestID) {
		t.Errorf("body %v", body)
	}
	if !strings.Contains(logBuf.String(), "kaboom") || !strings.Contains(logBuf.String(), `"request_id"`) {
		t.Errorf("panic not logged with request_id: %s", logBuf.String())
	}
	if got := testutil.ToFloat64(m.requests.WithLabelValues("GET /boom", "GET", "500")); got != 1 {
		t.Errorf("panic not counted as 500 on its route: %v", got)
	}
}

func TestRecoverRepanicsAbortHandler(t *testing.T) {
	h := Recover(discardLogger())(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))
	defer func() {
		if v := recover(); v != http.ErrAbortHandler { //nolint:errorlint // identity check, as in net/http
			t.Fatalf("expected ErrAbortHandler re-panic, got %v", v)
		}
	}()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
}

func TestTimeoutSetsDeadline(t *testing.T) {
	var deadline time.Time
	var ok bool
	h := Timeout(250 * time.Millisecond)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		deadline, ok = r.Context().Deadline()
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	if !ok || time.Until(deadline) > 250*time.Millisecond {
		t.Fatalf("deadline %v ok=%v", deadline, ok)
	}
}

func TestMetricsUseRoutePattern(t *testing.T) {
	rt := NewRouter()
	rt.HandleFunc("GET /api/v1/orders/{orderId}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })
	m := NewHTTPMetrics(prometheus.NewRegistry(), "test")
	h := stack(rt, discardLogger(), m, nil, nil)
	for _, p := range []string{"/api/v1/orders/a", "/api/v1/orders/b", "/zzz"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, http.NoBody))
	}
	if got := testutil.ToFloat64(m.requests.WithLabelValues("GET /api/v1/orders/{orderId}", "GET", "204")); got != 2 {
		t.Errorf("pattern-labelled count = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.requests.WithLabelValues(RouteUnmatched, "GET", "404")); got != 1 {
		t.Errorf("unmatched count = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.inFlight); got != 0 {
		t.Errorf("in-flight gauge leaked: %v", got)
	}
}

func TestCORS(t *testing.T) {
	ok := "http://localhost:3000"
	h := CORS([]string{ok})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	tests := []struct {
		name, method, origin string
		preflight            bool
		wantStatus           int
		wantAllow            string
	}{
		{"simple allowed", http.MethodGet, ok, false, 200, ok},
		{"simple disallowed", http.MethodGet, "http://evil.test", false, 200, ""},
		{"no origin", http.MethodGet, "", false, 200, ""},
		{"preflight allowed", http.MethodOptions, ok, true, 204, ok},
		{"preflight disallowed", http.MethodOptions, "http://evil.test", true, 204, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/api/v1/orders", http.NoBody)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.preflight {
				r.Header.Set("Access-Control-Request-Method", http.MethodPost)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != tc.wantStatus || rec.Header().Get("Access-Control-Allow-Origin") != tc.wantAllow {
				t.Fatalf("status %d allow %q", rec.Code, rec.Header().Get("Access-Control-Allow-Origin"))
			}
			if tc.preflight && tc.wantAllow != "" && !strings.Contains(rec.Header().Get("Access-Control-Allow-Headers"), "Idempotency-Key") {
				t.Error("preflight must allow Idempotency-Key")
			}
		})
	}
}

func TestServeGracefulShutdown(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	rt := NewRouter()
	rt.HandleFunc("GET /slow", func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(200)
	})
	srv := NewServer("127.0.0.1:0", rt, discardLogger())
	if srv.ReadHeaderTimeout != 5*time.Second || srv.IdleTimeout != 120*time.Second {
		t.Fatalf("timeouts: %v %v", srv.ReadHeaderTimeout, srv.IdleTimeout)
	}

	ctx, cancel := context.WithCancel(context.Background())
	addrc := make(chan net.Addr, 1)
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, srv, discardLogger(), func(a net.Addr) { addrc <- a }) }()
	addr := <-addrc

	respc := make(chan int, 1)
	go func() {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+addr.String()+"/slow", http.NoBody)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			respc <- -1
			return
		}
		_ = resp.Body.Close()
		respc <- resp.StatusCode
	}()
	<-entered
	cancel() // SIGTERM equivalent while a request is in flight
	time.Sleep(50 * time.Millisecond)
	close(release)

	if code := <-respc; code != 200 {
		t.Fatalf("in-flight request was not drained: %d", code)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
}
