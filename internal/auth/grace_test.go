package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/httpx"
)

// TestAdmissionSpendGrace (ADR 0018): past the admission's cap (its token
// exp) and inside HOLD_TTL, an admission token may still finish or give back
// what it spent, and nothing else. Beyond the grace it is 401 TOKEN_EXPIRED,
// as before; without a grace every route behaves as before.
func TestAdmissionSpendGrace(t *testing.T) {
	const (
		capTTL = 3 * time.Minute
		grace  = time.Minute // HOLD_TTL in the rush profile
	)
	tests := []struct {
		name     string
		gate     string        // any | live | holdBacked
		grace    time.Duration // keyring spend grace
		after    time.Duration // time since issue
		lease    AdmissionState
		wantCode int
		wantErr  string // problem code, when not 2xx
		wantPast bool   // handler saw AdmissionPastCap
	}{
		{"before cap: hold, live lease", "live", grace, time.Minute, AdmissionLive, 204, "", false},
		{"before cap: hold, lapsed lease", "live", grace, time.Minute, AdmissionLapsed, 403, "ADMISSION_EXPIRED", false},
		{"before cap: order, lapsed lease", "holdBacked", grace, time.Minute, AdmissionLapsed, 403, "ADMISSION_EXPIRED", false},
		{"before cap: order, live lease", "holdBacked", grace, time.Minute, AdmissionLive, 204, "", false},
		{"past cap: hold is refused as ADMISSION_EXPIRED", "live", grace, capTTL + 30*time.Second, AdmissionLapsed, 403, "ADMISSION_EXPIRED", false},
		{"past cap: order goes on, marked", "holdBacked", grace, capTTL + 30*time.Second, AdmissionLapsed, 204, "", true},
		{"past cap: release/leave accepted", "any", grace, capTTL + 30*time.Second, AdmissionLapsed, 204, "", false},
		{"past cap + grace: order", "holdBacked", grace, capTTL + ClockSkew + grace + time.Second, AdmissionLapsed, 401, "TOKEN_EXPIRED", false},
		{"past cap + grace: release", "any", grace, capTTL + ClockSkew + grace + time.Second, AdmissionLapsed, 401, "TOKEN_EXPIRED", false},
		{"no grace: past-cap order is 401", "holdBacked", 0, capTTL + 30*time.Second, AdmissionLapsed, 401, "TOKEN_EXPIRED", false},
		{"no grace: past-cap release is 401", "any", 0, capTTL + 30*time.Second, AdmissionLapsed, 401, "TOKEN_EXPIRED", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &clock{t: time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)}
			var touches atomic.Int64
			kr := ring(t, c, k1).WithSpendGrace(tc.grace).WithAdmissionTouch(func(context.Context, Claims) AdmissionState {
				touches.Add(1)
				return tc.lease
			})
			tok, _, err := kr.Issue(AudAdmission, uuid.New(), uuid.New(), nil, capTTL)
			if err != nil {
				t.Fatal(err)
			}
			var sawPast bool
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sawPast = AdmissionPastCap(r.Context())
				w.WriteHeader(http.StatusNoContent)
			})
			var h http.Handler
			switch tc.gate {
			case "live":
				h = kr.RequireLive(httpx.CodeNotAdmitted, next, AudAdmission)
			case "holdBacked":
				h = kr.RequireHoldBacked(httpx.CodeNotAdmitted, next, AudAdmission)
			default:
				h = kr.Require(httpx.CodeTokenInvalid, next, AudQueue, AudAdmission)
			}
			c.t = c.t.Add(tc.after)
			req := httptest.NewRequest(http.MethodPost, "/x", http.NoBody)
			req.Header.Set("Authorization", "Bearer "+tok)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode {
				t.Fatalf("HTTP %d (%s), want %d", rec.Code, rec.Body, tc.wantCode)
			}
			if tc.wantErr != "" {
				var p struct {
					Code string `json:"code"`
				}
				_ = json.Unmarshal(rec.Body.Bytes(), &p)
				if p.Code != tc.wantErr {
					t.Fatalf("code %q, want %q", p.Code, tc.wantErr)
				}
			}
			if sawPast != tc.wantPast {
				t.Fatalf("AdmissionPastCap %v, want %v", sawPast, tc.wantPast)
			}
			if tc.after > capTTL+ClockSkew && touches.Load() != 0 {
				t.Fatal("a token past its cap must never renew the lease")
			}
		})
	}
}

// TestAdmissionSpendGraceConcurrent runs the gates from many goroutines (-race).
func TestAdmissionSpendGraceConcurrent(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)}
	kr := ring(t, c, k1).WithSpendGrace(time.Minute).WithAdmissionTouch(func(context.Context, Claims) AdmissionState { return AdmissionLive })
	tok, _, err := kr.Issue(AudAdmission, uuid.New(), uuid.New(), nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	c.t = c.t.Add(90 * time.Second) // past the cap, inside the grace
	var past atomic.Int64
	h := kr.RequireHoldBacked(httpx.CodeNotAdmitted, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if AdmissionPastCap(r.Context()) {
			past.Add(1)
		}
		w.WriteHeader(http.StatusNoContent)
	}), AudAdmission)
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			for range 50 {
				req := httptest.NewRequest(http.MethodPost, "/x", http.NoBody)
				req.Header.Set("Authorization", "Bearer "+tok)
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, req)
				if rec.Code != http.StatusNoContent {
					t.Errorf("HTTP %d", rec.Code)
					return
				}
			}
		})
	}
	wg.Wait()
	if past.Load() != 64*50 {
		t.Fatalf("past-cap marks %d, want %d", past.Load(), 64*50)
	}
}
