package payments_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/sheshakanthra/sluice/internal/payments"
)

// Vector computed independently:
// python -c "import hmac,hashlib;print(hmac.new(b'whsec_test_fake',b'1700000000.'+b'{\"id\":\"evt_1\"}',hashlib.sha256).hexdigest())"
const (
	vecSecret = "whsec_test_fake"
	vecBody   = `{"id":"evt_1"}`
	vecHeader = "t=1700000000,v1=4bf2879bd67aad3c3d23a58ec95cb40f8c0c07b1c513c701ffe72c85dc62a603"
)

func TestSignVector(t *testing.T) {
	if got := payments.Sign(vecSecret, time.Unix(1700000000, 0), []byte(vecBody)); got != vecHeader {
		t.Fatalf("Sign = %s, want %s", got, vecHeader)
	}
}

func TestVerifySignature(t *testing.T) {
	at := time.Unix(1700000000, 0)
	tol := payments.SignatureTolerance
	tests := []struct {
		name   string
		secret string
		header string
		body   string
		now    time.Time
		want   error
	}{
		{"valid", vecSecret, vecHeader, vecBody, at, nil},
		{"valid at +tolerance", vecSecret, vecHeader, vecBody, at.Add(tol), nil},
		{"valid at -tolerance", vecSecret, vecHeader, vecBody, at.Add(-tol), nil},
		{"too old", vecSecret, vecHeader, vecBody, at.Add(tol + time.Second), payments.ErrSignatureTimestamp},
		{"from the future", vecSecret, vecHeader, vecBody, at.Add(-tol - time.Second), payments.ErrSignatureTimestamp},
		{"tampered body", vecSecret, vecHeader, `{"id":"evt_2"}`, at, payments.ErrSignatureMismatch},
		{"wrong secret", "other_secret", vecHeader, vecBody, at, payments.ErrSignatureMismatch},
		{"tampered timestamp", vecSecret, strings.Replace(vecHeader, "1700000000", "1700000001", 1), vecBody, at, payments.ErrSignatureMismatch},
		{"second v1 matches (rotation)", vecSecret, "t=1700000000,v1=00ff," + vecHeader[len("t=1700000000,"):], vecBody, at, nil},
		{"missing", vecSecret, "", vecBody, at, payments.ErrSignatureMissing},
		{"no v1", vecSecret, "t=1700000000", vecBody, at, payments.ErrSignatureMissing},
		{"no t", vecSecret, vecHeader[len("t=1700000000,"):], vecBody, at, payments.ErrSignatureMissing},
		{"bad t", vecSecret, "t=abc,v1=00", vecBody, at, payments.ErrSignatureMissing},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := payments.VerifySignature(tc.secret, tc.header, []byte(tc.body), tc.now, tol)
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Fatalf("VerifySignature = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestKeysDeterministic(t *testing.T) {
	o := uuid.MustParse("01890000-0000-7000-8000-000000000001")
	if got := payments.CreateKey(o, 2); got != "order:01890000-0000-7000-8000-000000000001:attempt:2" {
		t.Fatalf("CreateKey = %s", got)
	}
	for name, got := range map[string]string{
		"confirm:pi_1": payments.ConfirmKey("pi_1"), "capture:pi_1": payments.CaptureKey("pi_1"),
		"void:pi_1": payments.VoidKey("pi_1"), "refund:pi_1": payments.RefundKey("pi_1"),
	} {
		if got != name {
			t.Errorf("key = %s, want %s", got, name)
		}
	}
}

type fakePSP struct {
	calls atomic.Int32
	keys  chan string
	h     func(n int32, w http.ResponseWriter, r *http.Request)
}

func newClient(t *testing.T, f *fakePSP, timeout time.Duration) (*payments.Client, *prometheus.Registry) {
	t.Helper()
	f.keys = make(chan string, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := f.calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("missing API key")
		}
		f.keys <- r.Header.Get("Idempotency-Key")
		f.h(n, w, r)
	}))
	t.Cleanup(srv.Close)
	reg := prometheus.NewRegistry()
	m, err := payments.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	c, err := payments.NewClient(payments.Options{BaseURL: srv.URL, PublicURL: "https://pay.example", APIKey: "test-key",
		Timeout: timeout, Metrics: m})
	if err != nil {
		t.Fatal(err)
	}
	return c, reg
}

func intentJSON(w http.ResponseWriter, status string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payments.Intent{ID: "pi_1", Status: status})
}

func TestClientRetries(t *testing.T) {
	tests := []struct {
		name      string
		h         func(n int32, w http.ResponseWriter, r *http.Request)
		wantCalls int32
		wantErr   error
		wantOK    bool
	}{
		{"5xx then ok", func(n int32, w http.ResponseWriter, _ *http.Request) {
			if n < 3 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			intentJSON(w, payments.StatusCaptured)
		}, 3, nil, true},
		{"429 retried", func(n int32, w http.ResponseWriter, _ *http.Request) {
			if n == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			intentJSON(w, payments.StatusCaptured)
		}, 2, nil, true},
		{"always 5xx: 1 + 3 retries", func(_ int32, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}, 4, payments.ErrUnavailable, false},
		{"timeout then ok", func(n int32, w http.ResponseWriter, r *http.Request) {
			if n == 1 {
				select {
				case <-time.After(time.Second):
				case <-r.Context().Done():
				}
				return
			}
			intentJSON(w, payments.StatusCaptured)
		}, 2, nil, true},
		{"404 not retried", func(_ int32, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}, 1, payments.ErrNotFound, false},
		{"409 typed state error", func(_ int32, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(payments.ErrorBody{Intent: &payments.Intent{ID: "pi_1", Status: payments.StatusCanceled}})
		}, 1, payments.ErrIntentState, false},
		{"422 key reused", func(_ int32, w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			var eb payments.ErrorBody
			eb.Error.Code = payments.ErrCodeIdempotencyUsed
			_ = json.NewEncoder(w).Encode(eb)
		}, 1, payments.ErrKeyReused, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakePSP{h: tc.h}
			c, reg := newClient(t, f, 150*time.Millisecond)
			in, err := c.Capture(context.Background(), "pi_1")
			if !errors.Is(err, tc.wantErr) || (tc.wantErr == nil && err != nil) {
				t.Fatalf("Capture err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantOK && in.Status != payments.StatusCaptured {
				t.Fatalf("intent %+v", in)
			}
			if got := f.calls.Load(); got != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", got, tc.wantCalls)
			}
			// Every retry reuses the same deterministic key.
			for range f.calls.Load() {
				if k := <-f.keys; k != payments.CaptureKey("pi_1") {
					t.Fatalf("key %q", k)
				}
			}
			if n := testutil.CollectAndCount(reg, "psp_requests_total"); n == 0 {
				t.Fatal("psp_requests_total not recorded")
			}
		})
	}
}

func TestClientStateErrorCarriesIntent(t *testing.T) {
	f := &fakePSP{h: func(_ int32, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(payments.ErrorBody{Intent: &payments.Intent{ID: "pi_1", Status: payments.StatusCaptured}})
	}}
	c, _ := newClient(t, f, time.Second)
	_, err := c.Void(context.Background(), "pi_1")
	var se *payments.StateError
	if !errors.As(err, &se) || se.Intent == nil || se.Intent.Status != payments.StatusCaptured {
		t.Fatalf("err = %v", err)
	}
	if got := c.CheckoutURL("pi_1"); got != "https://pay.example/checkout/pi_1" {
		t.Fatalf("CheckoutURL = %s", got)
	}
}

func TestClientHonoursCallerContext(t *testing.T) {
	f := &fakePSP{h: func(_ int32, w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }}
	c, _ := newClient(t, f, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.GetIntent(ctx, "pi_1"); !errors.Is(err, payments.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if n := f.calls.Load(); n > 1 {
		t.Fatalf("%d calls after cancel", n)
	}
}

// TestConnWaitShowsPoolQueueing: with MaxConns = 1 and a PSP that takes
// 50 ms, 4 concurrent calls queue for the single connection, and
// psp_conn_wait_seconds records that wait (at least 50+100+150 ms in all)
// separately from the PSP's own latency.
func TestConnWaitShowsPoolQueueing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		intentJSON(w, payments.StatusCaptured)
	}))
	t.Cleanup(srv.Close)
	reg := prometheus.NewRegistry()
	m, err := payments.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	c, err := payments.NewClient(payments.Options{BaseURL: srv.URL, APIKey: "test-key", Timeout: 2 * time.Second,
		MaxConns: 1, Metrics: m})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := c.Capture(t.Context(), "pi_1"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "psp_conn_wait_seconds" {
			continue
		}
		h := mf.GetMetric()[0].GetHistogram()
		if h.GetSampleCount() != 4 || h.GetSampleSum() < 0.29 {
			t.Fatalf("conn wait: %d samples, %.3f s in all; want 4 and >= 0.3 s of queueing", h.GetSampleCount(), h.GetSampleSum())
		}
		return
	}
	t.Fatal("psp_conn_wait_seconds not recorded")
}
