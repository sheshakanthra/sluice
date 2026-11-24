package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	kr := base.WithAdmissionTouch(func(_ context.Context, cl Claims) {
		mu.Lock()
		defer mu.Unlock()
		touched = append(touched, cl)
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
