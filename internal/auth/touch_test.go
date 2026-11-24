package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/httpx"
)

// TestRequireTouchesAdmissionTokens: the admission-lease hook runs for
// every request Require lets through with a valid admission token, before
// the handler, and for nothing else (session or queue tokens, expired or
// rejected tokens). The original keyring is unchanged.
func TestRequireTouchesAdmissionTokens(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)}
	base := ring(t, c, k1)
	uid, eid := uuid.New(), uuid.New()
	var (
		mu      sync.Mutex
		touched []Claims
	)
	kr := base.WithAdmissionTouch(func(_ context.Context, cl Claims) AdmissionState {
		mu.Lock()
		defer mu.Unlock()
		touched = append(touched, cl)
		return AdmissionLapsed // Require ignores the state
	})
	issue := func(aud Audience, ttl time.Duration) string {
		tok, _, err := base.Issue(aud, uid, eid, nil, ttl)
		if err != nil {
			t.Fatal(err)
		}
		return tok
	}
	adm, sess, queueTok := issue(AudAdmission, time.Minute), issue(AudSession, time.Minute), issue(AudQueue, time.Minute)
	expired := issue(AudAdmission, time.Second)

	h := kr.Require(httpx.CodeTokenInvalid, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), AudAdmission, AudSession, AudQueue)
	plain := base.Require(httpx.CodeTokenInvalid, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), AudAdmission)

	tests := []struct {
		name      string
		h         http.Handler
		tok       string
		advance   time.Duration
		wantCode  int
		wantTouch int
	}{
		{"admission token touches", h, adm, 0, http.StatusNoContent, 1},
		{"session token does not", h, sess, 0, http.StatusNoContent, 0},
		{"queue token does not", h, queueTok, 0, http.StatusNoContent, 0},
		{"garbage token does not", h, "nope", 0, http.StatusUnauthorized, 0},
		{"expired admission token does not", h, expired, time.Second + ClockSkew + time.Second, http.StatusUnauthorized, 0},
		{"keyring without the hook does not", plain, adm, 0, http.StatusNoContent, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mu.Lock()
			touched = nil
			mu.Unlock()
			c.t = c.t.Add(tc.advance)
			req := httptest.NewRequest(http.MethodPost, "/x", http.NoBody)
			req.Header.Set("Authorization", "Bearer "+tc.tok)
			rec := httptest.NewRecorder()
			tc.h.ServeHTTP(rec, req)
			mu.Lock()
			defer mu.Unlock()
			if rec.Code != tc.wantCode || len(touched) != tc.wantTouch {
				t.Fatalf("HTTP %d with %d touches, want %d with %d", rec.Code, len(touched), tc.wantCode, tc.wantTouch)
			}
			if tc.wantTouch == 1 && (touched[0].Subject != uid || touched[0].EventID != eid || touched[0].Audience != AudAdmission) {
				t.Errorf("touched %+v", touched[0])
			}
		})
	}
}

// TestRequireLiveGate (ADR 0010 addendum part 3): RequireLive refuses an
// admission token whose server-side lease lapsed (403 ADMISSION_EXPIRED)
// and fails closed when the check errors (503 with Retry-After). A live
// lease, or a queue state Redis lost (UNKNOWN, R1), lets the request
// through, and a session token (direct mode) is never checked. Require
// renews the same way but never refuses on the lease state.
func TestRequireLiveGate(t *testing.T) {
	c := &clock{t: time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)}
	base := ring(t, c, k1)
	uid, eid := uuid.New(), uuid.New()
	adm, _, err := base.Issue(AudAdmission, uid, eid, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	sess, _, err := base.Issue(AudSession, uid, uuid.Nil, nil, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })

	tests := []struct {
		name      string
		state     AdmissionState
		tok       string
		live      bool
		wantCode  int
		wantError string
		wantCalls int
	}{
		{"live lease passes", AdmissionLive, adm, true, http.StatusNoContent, "", 1},
		{"lapsed lease is refused", AdmissionLapsed, adm, true, http.StatusForbidden, "ADMISSION_EXPIRED", 1},
		{"failed check fails closed", AdmissionError, adm, true, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", 1},
		{"queue state lost: the token decides", AdmissionUnknown, adm, true, http.StatusNoContent, "", 1},
		{"session token is not checked", AdmissionLapsed, sess, true, http.StatusNoContent, "", 0},
		{"Require ignores a lapsed lease", AdmissionLapsed, adm, false, http.StatusNoContent, "", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			kr := base.WithAdmissionTouch(func(context.Context, Claims) AdmissionState {
				calls++
				return tc.state
			})
			h := kr.Require(httpx.CodeNotAdmitted, ok, AudAdmission, AudSession)
			if tc.live {
				h = kr.RequireLive(httpx.CodeNotAdmitted, ok, AudAdmission, AudSession)
			}
			req := httptest.NewRequest(http.MethodPost, "/x", http.NoBody)
			req.Header.Set("Authorization", "Bearer "+tc.tok)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantCode || calls != tc.wantCalls {
				t.Fatalf("HTTP %d after %d checks, want %d after %d (%s)", rec.Code, calls, tc.wantCode, tc.wantCalls, rec.Body)
			}
			if tc.wantError != "" && !strings.Contains(rec.Body.String(), `"code":"`+tc.wantError+`"`) {
				t.Errorf("body %s, want code %s", rec.Body, tc.wantError)
			}
			if tc.wantCode == http.StatusServiceUnavailable && rec.Header().Get("Retry-After") == "" {
				t.Error("503 without Retry-After")
			}
		})
	}
}
