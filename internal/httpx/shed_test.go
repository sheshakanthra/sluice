package httpx

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestShed(t *testing.T) {
	release := make(chan struct{})
	var entered sync.WaitGroup
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if shedExempt(r) {
			w.WriteHeader(http.StatusOK)
			return
		}
		entered.Done()
		<-release
		w.WriteHeader(http.StatusOK)
	})
	const limit = 3
	h := Shed(limit, 20*time.Millisecond, NewShedMetrics(prometheus.NewRegistry(), "test"))(slow)

	var wg sync.WaitGroup
	entered.Add(limit)
	codes := make([]int, limit)
	for i := range limit {
		wg.Go(func() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/events/x/holds", http.NoBody))
			codes[i] = rec.Code
		})
	}
	entered.Wait()

	tests := []struct {
		name, method, path string
		want               int
	}{
		{"write overflow is shed", http.MethodPost, "/api/v1/events/x/holds", http.StatusServiceUnavailable},
		{"delete overflow is shed", http.MethodDelete, "/api/v1/holds/x", http.StatusServiceUnavailable},
		{"reads bypass", http.MethodGet, "/api/v1/events/x/queue/status", http.StatusOK},
		{"readyz bypasses", http.MethodGet, "/readyz", http.StatusOK},
		{"healthz bypasses", http.MethodGet, "/healthz", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, http.NoBody))
			if rec.Code != tc.want {
				t.Fatalf("%s = %d, want %d", tc.path, rec.Code, tc.want)
			}
			if tc.want == http.StatusServiceUnavailable {
				if rec.Header().Get("Retry-After") != "1" || !strings.Contains(rec.Body.String(), `"OVERLOADED"`) {
					t.Fatalf("shed response: Retry-After=%q body=%s", rec.Header().Get("Retry-After"), rec.Body.String())
				}
			}
		})
	}
	close(release)
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("in-limit request %d = %d", i, c)
		}
	}
}

func TestShedDisabled(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	h := Shed(0, 0, nil)(ok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("disabled shed = %d", rec.Code)
	}
}

func TestBodyLimit(t *testing.T) {
	h := BodyLimit(8)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	for _, tc := range []struct {
		body string
		want int
	}{{"small", http.StatusOK}, {"way more than eight bytes", http.StatusRequestEntityTooLarge}} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(tc.body)))
		if rec.Code != tc.want {
			t.Errorf("body %q = %d, want %d", tc.body, rec.Code, tc.want)
		}
	}
}

func TestReadinessCache(t *testing.T) {
	var calls atomic.Int32
	rd := NewReadiness(discardLogger(), time.Hour, Check{Name: "x", Ping: func(context.Context) error {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return nil
	}})
	var wg sync.WaitGroup
	for range 50 {
		wg.Go(func() {
			if _, ok := rd.State(context.Background()); !ok {
				t.Error("not ready")
			}
		})
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("50 concurrent callers ran %d probes, want 1", n)
	}
}
