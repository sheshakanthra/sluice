package rush

import (
	"context"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestBackoffFullJitterExponentialCapped(t *testing.T) {
	tests := []struct {
		name    string
		attempt int
		floor   time.Duration
		u       float64
		want    time.Duration
	}{
		{"first, top", 0, 0, 1, 500 * time.Millisecond},
		{"first, middle", 0, 0, 0.5, 250 * time.Millisecond},
		{"full jitter reaches 0", 3, 0, 0, 0},
		{"doubles", 1, 0, 1, time.Second},
		{"doubles again", 4, 0, 1, 8 * time.Second},
		{"cap", 6, 0, 1, backoffCap},
		{"cap holds", 29, 0, 1, backoffCap},
		{"huge attempt, no overflow", 200, 0, 1, backoffCap},
		{"server floor wins", 0, 15 * time.Second, 1, 15 * time.Second},
		{"floor under the draw", 6, 10 * time.Second, 0.5, 15 * time.Second},
		{"floor above the cap", 9, 60 * time.Second, 1, 60 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := backoff(tt.attempt, tt.floor, tt.u); got != tt.want {
				t.Fatalf("backoff(%d, %v, %v) = %v, want %v", tt.attempt, tt.floor, tt.u, got, tt.want)
			}
		})
	}
}

// Draws spread over [0, ceiling]: no two users retry in lockstep.
func TestBackoffSpreads(t *testing.T) {
	const n = 10_000
	var lo, hi int
	for range n {
		d := backoff(4, 0, jitterU()) // ceiling 8 s
		if d < 0 || d > 8*time.Second {
			t.Fatalf("draw %v outside [0, 8s]", d)
		}
		if d < 4*time.Second {
			lo++
		} else {
			hi++
		}
	}
	if lo < n*4/10 || hi < n*4/10 {
		t.Fatalf("draws are not spread: %d below 4 s, %d above", lo, hi)
	}
}

func TestRetryableHonoursServerPacing(t *testing.T) {
	floor := 20 * time.Second
	tests := []struct {
		name      string
		r         result
		floor     time.Duration
		want      time.Duration
		wantRetry bool
	}{
		{"timeout floored at retry_after", result{oc: timedOut}, floor, floor, true},
		{"transport floored", result{oc: transport}, floor, floor, true},
		{"5xx floored", result{status: 503, oc: serverErr}, floor, floor, true},
		{"5xx Retry-After above floor", result{status: 503, oc: serverErr, retryAfter: 30 * time.Second}, floor, 30 * time.Second, true},
		{"429 Retry-After", result{status: 429, oc: business, retryAfter: 2 * time.Second}, 0, 2 * time.Second, true},
		{"timeout, no floor: jittered", result{oc: timedOut}, 0, 250 * time.Millisecond, true},
		{"in progress keeps its short wait", result{status: 409, oc: business, body: []byte(`{"code":"REQUEST_IN_PROGRESS"}`)}, floor, 500 * time.Millisecond, true},
		{"business answer: no retry", result{status: 409, oc: business, body: []byte(`{"code":"SEATS_TAKEN"}`)}, floor, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, again := retryable(&tt.r, 0, tt.floor, 0.5)
			if again != tt.wantRetry || got != tt.want {
				t.Fatalf("retryable = (%v, %t), want (%v, %t)", got, again, tt.want, tt.wantRetry)
			}
		})
	}
}

func TestStretchNeverSooner(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2)) //nolint:gosec // G404: test
	for range 1000 {
		d := stretch(r, 10*time.Second, pollJitter)
		if d < 10*time.Second || d > 11*time.Second {
			t.Fatalf("stretch = %v, want within [10s, 11s]", d)
		}
	}
}

// LOADTEST §3.3: the per-call timeout runs from the moment the request has
// its connection. A slow dial (here 600 ms, the swarm's own reconnect on a
// starved box) must not eat a 300 ms budget; latency still counts the dial
// because it is measured from the intended start.
func TestTimeoutRunsFromSendNotFromDial(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer srv.Close()
	tr := newTransport(ConnDedicated)
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		time.Sleep(600 * time.Millisecond)
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	rec := NewRecorder(time.Now(), time.Minute)
	c := &client{hc: &http.Client{Transport: tr}, base: srv.URL, xff: "198.18.0.1", rec: rec}
	intended := time.Now()
	r := c.do(t.Context(), req{rt: rStatus, method: http.MethodGet, path: "/", timeout: 300 * time.Millisecond, intended: intended})
	if r.oc != okResp || r.status != http.StatusOK {
		t.Fatalf("got %+v (err %v), want 200: the dial must not count against the timeout", r, r.err)
	}
	if q := rec.routes[rStatus].quantiles(); q.P50 < 600 {
		t.Fatalf("latency p50 = %.1f ms, want >= 600 (measured from the intended start)", q.P50)
	}
}

func TestTimeoutStillFiresOnASlowServer(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(release)
	rec := NewRecorder(time.Now(), time.Minute)
	c := &client{hc: &http.Client{Transport: newTransport(ConnDedicated)}, base: srv.URL, xff: "198.18.0.1", rec: rec}
	start := time.Now()
	r := c.do(t.Context(), req{rt: rStatus, method: http.MethodGet, path: "/", timeout: 200 * time.Millisecond, intended: start})
	if r.oc != timedOut {
		t.Fatalf("outcome %v (err %v), want timedOut", r.oc, r.err)
	}
	if el := time.Since(start); el < 200*time.Millisecond || el > 2*time.Second {
		t.Fatalf("timed out after %v, want about 200 ms", el)
	}
}

// Many users timing out at once from the same server: the deadline helpers
// must be race-free (run with -race).
func TestSendDeadlineConcurrent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(150 * time.Millisecond):
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	rec := NewRecorder(time.Now(), time.Minute)
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			c := &client{hc: &http.Client{Transport: newTransport(ConnDedicated)}, base: srv.URL, xff: "198.18.0.1", rec: rec}
			to := 50 * time.Millisecond
			if i%2 == 0 {
				to = time.Second
			}
			r := c.do(t.Context(), req{rt: rStatus, method: http.MethodGet, path: "/", timeout: to, intended: time.Now()})
			if i%2 == 0 && r.oc != okResp {
				t.Errorf("user %d: %v (err %v), want ok", i, r.oc, r.err)
			}
			if i%2 == 1 && r.oc != timedOut {
				t.Errorf("user %d: %v, want timedOut", i, r.oc)
			}
		})
	}
	wg.Wait()
}
