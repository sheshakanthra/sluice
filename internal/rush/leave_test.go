package rush

import (
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// TestNoSeatAndAbandonLeaveQueue: LOADTEST §3.2. A user who ends as NO_SEAT
// or ABANDONED calls DELETE /queue (with the admission token) exactly once;
// SOLD_OUT and TICKET-bound paths never do. "INSUFFICIENT_SEATS, 0
// available" means every seat is held, not sold, so it is NO_SEAT.
func TestNoSeatAndAbandonLeaveQueue(t *testing.T) {
	tests := []struct {
		name      string
		holdCode  int
		holdBody  string
		abandon   bool
		want      Outcome
		wantLeave int
	}{
		{"insufficient, 0 available", http.StatusConflict, `{"code":"INSUFFICIENT_SEATS","available":0}`, false, OutNoSeat, 1},
		{"taken on every try", http.StatusConflict, `{"code":"SEATS_TAKEN","taken":[1]}`, false, OutNoSeat, 1},
		{"sold out", http.StatusConflict, `{"code":"SOLD_OUT"}`, false, OutSoldOut, 0},
		{"abandon after hold", http.StatusCreated, `{"hold_id":"h1"}`, true, OutAbandoned, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu         sync.Mutex
				holds      int
				leaves     int
				leaveToken string
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodPost && r.URL.Path == "/events/e/holds":
					holds++
					w.WriteHeader(tc.holdCode)
					_, _ = w.Write([]byte(tc.holdBody))
				case r.Method == http.MethodDelete && r.URL.Path == "/events/e/queue":
					leaves++
					leaveToken = r.Header.Get("Authorization")
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusTeapot)
				}
			}))
			defer srv.Close()
			rec := NewRecorder(time.Now(), time.Minute)
			s := &swarm{o: &Options{Scenario: Scenarios["SMOKE"]}, rec: rec, eventPath: "/events/e",
				anomalies: map[string]int64{}}
			u := &user{s: s, adm: "adm-token", queue: "queue-token",
				p: plan{qty: 1, abandon: tc.abandon, rng: rand.New(rand.NewPCG(1, 2))}, //nolint:gosec // test
				c: &client{hc: &http.Client{Transport: newTransport(ConnDedicated)}, base: srv.URL, xff: "198.18.0.1", rec: rec}}
			got := u.end(t.Context(), u.shop(t.Context(), u.adm))
			mu.Lock()
			defer mu.Unlock()
			if got != tc.want || leaves != tc.wantLeave {
				t.Fatalf("outcome %s with %d leaves (%d holds), want %s with %d", got, leaves, holds, tc.want, tc.wantLeave)
			}
			if leaves == 1 && leaveToken != "Bearer adm-token" {
				t.Errorf("leave sent %q, want the admission token", leaveToken)
			}
			if len(s.anomalies) != 0 {
				t.Errorf("anomalies %v", s.anomalies)
			}
		})
	}
}
