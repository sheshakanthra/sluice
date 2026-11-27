package rush

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rawServer accepts TCP connections, captures each raw request (head plus
// Content-Length body) and answers with reply(i) for the i-th request.
type rawServer struct {
	ln    net.Listener
	mu    sync.Mutex
	reqs  [][]byte
	conns atomic.Int64
	reply func(i int) string
	wg    sync.WaitGroup
	open  []net.Conn // guarded by mu; closed at cleanup
}

func newRawServer(t *testing.T, reply func(i int) string) *rawServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &rawServer{ln: ln, reply: reply}
	s.wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			s.conns.Add(1)
			s.mu.Lock()
			s.open = append(s.open, c)
			s.mu.Unlock()
			s.wg.Go(func() { s.serve(c) })
		}
	})
	t.Cleanup(func() {
		_ = ln.Close()
		s.mu.Lock()
		for _, c := range s.open {
			_ = c.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
	return s
}

func (s *rawServer) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	for {
		var raw bytes.Buffer
		cl := 0
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				return
			}
			raw.WriteString(l)
			if k, v, ok := strings.Cut(strings.TrimSpace(l), ":"); ok && strings.EqualFold(k, "Content-Length") {
				cl, _ = strconv.Atoi(strings.TrimSpace(v))
			}
			if l == "\r\n" {
				break
			}
		}
		body := make([]byte, cl)
		if _, err := io.ReadFull(br, body); err != nil {
			return
		}
		raw.Write(body)
		s.mu.Lock()
		i := len(s.reqs)
		s.reqs = append(s.reqs, raw.Bytes())
		s.mu.Unlock()
		resp := s.reply(i)
		if resp == "" { // hang up without answering
			return
		}
		if _, err := io.WriteString(c, resp); err != nil {
			return
		}
		if strings.Contains(resp, "Connection: close") {
			return
		}
	}
}

func (s *rawServer) base() string { return "http://" + s.ln.Addr().String() + "/api/v1" }

func (s *rawServer) captured() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([][]byte(nil), s.reqs...)
}

const okJSON = "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 11\r\n\r\n{\"ok\":true}"

// Every request shape the swarm sends (user.go), with the headers it sets.
var wireCases = []req{
	{rt: rSession, method: http.MethodPost, path: "/sessions", timeout: callTimeout},
	{rt: rJoin, method: http.MethodPost, path: "/events/rush-1/queue", token: "tok.s", timeout: callTimeout},
	{rt: rJoin, method: http.MethodPost, path: "/events/rush-1/queue?rejoin=true", token: "tok.s", timeout: callTimeout},
	{rt: rStatus, method: http.MethodGet, path: "/events/rush-1/queue/status", token: "tok.q", timeout: statusTimeout},
	{rt: rAvail, method: http.MethodGet, path: "/events/rush-1/availability", timeout: callTimeout},
	{rt: rHold, method: http.MethodPost, path: "/events/rush-1/holds", token: "tok.a", ik: "0190f0e0-0000-7000-8000-000000000001",
		body: map[string]any{"quantity": 2}, timeout: callTimeout},
	{rt: rHold, method: http.MethodPost, path: "/events/rush-1/holds", token: "tok.a", ik: "0190f0e0-0000-7000-8000-000000000002",
		body: map[string]any{"seat_nos": []int{3, 17}}, timeout: callTimeout},
	{rt: rOrder, method: http.MethodPost, path: "/orders", token: "tok.a", ik: "ik-3", body: map[string]string{"hold_id": "h-1"}, timeout: callTimeout},
	{rt: rPay, method: http.MethodPost, path: "/orders/o-1/pay", token: "tok.s", ik: "ik-4", body: map[string]string{"payment_method": "pm_ok"}, timeout: callTimeout},
	{rt: rPayments, method: http.MethodPost, path: "/orders/o-1/payments", token: "tok.s", ik: "ik-5", timeout: callTimeout},
	{rt: rConfirm, method: http.MethodPost, path: "/orders/o-1/confirm", token: "tok.s", ik: "ik-6", timeout: callTimeout},
	{rt: rLeave, method: http.MethodDelete, path: "/events/rush-1/queue", token: "tok.q", timeout: callTimeout},
	{rt: rRelease, method: http.MethodDelete, path: "/holds/h-1", token: "tok.a", timeout: callTimeout},
}

// TestUserConnWireFormat: the dedicated client puts the same bytes on the
// wire as the net/http transport it replaces, for every request the swarm
// makes, and both keep using one connection.
func TestUserConnWireFormat(t *testing.T) {
	ctx := t.Context()
	old := newRawServer(t, func(int) string { return okJSON })
	neu := newRawServer(t, func(int) string { return okJSON })
	rec := NewRecorder(time.Now(), time.Minute)
	oc := &client{hc: &http.Client{Transport: newTransport(ConnDedicated)}, base: old.base(), xff: "198.18.0.1", rec: rec}
	uc, err := newUserConn(ctx, neu.base())
	if err != nil {
		t.Fatal(err)
	}
	nc := &client{uc: uc, base: neu.base(), xff: "198.18.0.1", rec: rec}
	defer nc.close()
	for _, q := range wireCases {
		q.intended = time.Now()
		if r := oc.do(ctx, q); r.status != 200 {
			t.Fatalf("net/http %s %s: %d %v", q.method, q.path, r.status, r.err)
		}
		r := nc.do(ctx, q)
		if r.status != 200 || string(r.body) != `{"ok":true}` {
			t.Fatalf("userConn %s %s: %d %q %v", q.method, q.path, r.status, r.body, r.err)
		}
	}
	a, b := old.captured(), neu.captured()
	if len(a) != len(wireCases) || len(b) != len(wireCases) {
		t.Fatalf("captured %d / %d requests, want %d", len(a), len(b), len(wireCases))
	}
	oldHost, newHost := old.ln.Addr().String(), neu.ln.Addr().String()
	for i := range a {
		want := strings.Replace(string(a[i]), "Host: "+oldHost, "Host: "+newHost, 1)
		if want != string(b[i]) {
			t.Errorf("request %d differs\nnet/http:\n%s\nuserConn:\n%s", i, want, b[i])
		}
	}
	if old.conns.Load() != 1 || neu.conns.Load() != 1 {
		t.Fatalf("connections: net/http %d, userConn %d; want 1 each (keep-alive)", old.conns.Load(), neu.conns.Load())
	}
}

func TestUserConnResponses(t *testing.T) {
	tests := []struct {
		name       string
		resp       string
		wantStatus int
		wantBody   string
		wantRA     time.Duration
		wantConns  int64 // after two requests
	}{
		{"content-length", okJSON, 200, `{"ok":true}`, 0, 1},
		{"chunked with trailer", "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n4\r\n{\"a\"\r\n3\r\n:1}\r\n0\r\nX-T: 1\r\n\r\n", 200, `{"a":1}`, 0, 1},
		{"204 no body", "HTTP/1.1 204 No Content\r\n\r\n", 204, "", 0, 1},
		{"503 retry-after", "HTTP/1.1 503 Service Unavailable\r\nRetry-After: 3\r\nContent-Length: 2\r\n\r\n{}", 503, "{}", 3 * time.Second, 1},
		{"connection close", "HTTP/1.1 200 OK\r\nConnection: close\r\nContent-Length: 2\r\n\r\n{}", 200, "{}", 0, 2},
		{"100 continue first", "HTTP/1.1 100 Continue\r\n\r\nHTTP/1.1 201 Created\r\nContent-Length: 2\r\n\r\n{}", 201, "{}", 0, 1},
		{"http/1.0 no keep-alive", "HTTP/1.0 200 OK\r\nContent-Length: 2\r\n\r\n{}", 200, "{}", 0, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newRawServer(t, func(int) string { return tc.resp })
			uc, err := newUserConn(t.Context(), srv.base())
			if err != nil {
				t.Fatal(err)
			}
			defer uc.Close()
			q := req{method: http.MethodGet, path: "/x", timeout: time.Second}
			for range 2 {
				ex, err := uc.roundTrip(t.Context(), &q, nil, "198.18.0.1")
				if err != nil {
					t.Fatal(err)
				}
				if ex.status != tc.wantStatus || string(ex.body) != tc.wantBody || ex.retryAfter != tc.wantRA {
					t.Fatalf("got %d %q %s, want %d %q %s", ex.status, ex.body, ex.retryAfter, tc.wantStatus, tc.wantBody, tc.wantRA)
				}
			}
			if got := srv.conns.Load(); got != tc.wantConns {
				t.Fatalf("connections %d, want %d", got, tc.wantConns)
			}
		})
	}
}

// TestUserConnStaleKeepAlive: a reused connection the server dropped is
// retried once on a new one for a replayable request (GET, or a POST with an
// Idempotency-Key), and not for a POST without one: net/http's rule.
func TestUserConnStaleKeepAlive(t *testing.T) {
	tests := []struct {
		name      string
		q         req
		wantRetry bool
	}{
		{"GET", req{method: http.MethodGet, path: "/s"}, true},
		{"POST with IK", req{method: http.MethodPost, path: "/h", ik: "ik-1"}, true},
		{"POST without IK", req{method: http.MethodPost, path: "/sessions"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Request 0 is answered; request 1 arrives on the same connection
			// and the server hangs up without answering (stale keep-alive).
			srv := newRawServer(t, func(i int) string {
				if i == 1 {
					return ""
				}
				return okJSON
			})
			uc, err := newUserConn(t.Context(), srv.base())
			if err != nil {
				t.Fatal(err)
			}
			defer uc.Close()
			q := tc.q
			q.timeout = time.Second
			if _, err := uc.roundTrip(t.Context(), &q, nil, "x"); err != nil {
				t.Fatal(err)
			}
			ex, err := uc.roundTrip(t.Context(), &q, nil, "x")
			if tc.wantRetry {
				if err != nil || ex.status != 200 {
					t.Fatalf("want a transparent retry, got %d %v", ex.status, err)
				}
				if n := len(srv.captured()); n != 3 {
					t.Fatalf("server saw %d requests, want 3", n)
				}
				return
			}
			if err == nil {
				t.Fatalf("want a transport error for a non-replayable request, got %d", ex.status)
			}
			if n := len(srv.captured()); n != 2 {
				t.Fatalf("server saw %d requests, want 2 (no replay)", n)
			}
		})
	}
}

// TestUserConnTimeoutAndRedial: a slow answer times out (recorded as a
// timeout), the connection is dropped and the next request dials again.
func TestUserConnTimeoutAndRedial(t *testing.T) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			select {
			case <-time.After(2 * time.Second):
			case <-r.Context().Done():
			}
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	var conns atomic.Int64
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			conns.Add(1)
		}
	}
	rec := NewRecorder(time.Now(), time.Minute)
	uc, err := newUserConn(t.Context(), srv.URL+"/api/v1")
	if err != nil {
		t.Fatal(err)
	}
	c := &client{uc: uc, base: srv.URL + "/api/v1", xff: "x", rec: rec}
	defer c.close()
	q := req{rt: rStatus, method: http.MethodGet, path: "/s", timeout: 200 * time.Millisecond, intended: time.Now()}
	if r := c.do(t.Context(), q); r.oc != timedOut {
		t.Fatalf("first: outcome %v err %v, want timedOut", r.oc, r.err)
	}
	q.intended = time.Now()
	if r := c.do(t.Context(), q); r.status != 200 {
		t.Fatalf("second: %d %v", r.status, r.err)
	}
	if rec.timeouts.Load() != 1 || conns.Load() != 2 {
		t.Fatalf("timeouts %d conns %d, want 1 and 2", rec.timeouts.Load(), conns.Load())
	}
}

// TestUserConnAbortsWithTheRun: ending the run context unblocks a request
// in flight, and it is not recorded as a measurement.
func TestUserConnAbortsWithTheRun(t *testing.T) {
	srv := newRawServer(t, func(int) string { time.Sleep(3 * time.Second); return okJSON })
	ctx, cancel := context.WithCancel(t.Context())
	rec := NewRecorder(time.Now(), time.Minute)
	uc, err := newUserConn(ctx, srv.base())
	if err != nil {
		t.Fatal(err)
	}
	c := &client{uc: uc, base: srv.base(), xff: "x", rec: rec}
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	r := c.do(ctx, req{rt: rStatus, method: http.MethodGet, path: "/s", timeout: 5 * time.Second, intended: start})
	if time.Since(start) > time.Second || r.err == nil {
		t.Fatalf("abort took %s, err %v", time.Since(start), r.err)
	}
	if rec.requests.Load() != 0 {
		t.Fatal("an aborted request must not be recorded")
	}
	if _, err := uc.roundTrip(context.Background(), &req{method: "GET", path: "/s", timeout: time.Second}, nil, "x"); err == nil {
		t.Fatal("no new dial after the run ended")
	}
}

// TestUserConnConcurrentUsers: many users, each on its own connection, under -race.
func TestUserConnConcurrentUsers(t *testing.T) {
	srv := newRawServer(t, func(int) string { return okJSON })
	ctx := t.Context()
	rec := NewRecorder(time.Now(), time.Minute)
	const users, each = 64, 20
	var wg sync.WaitGroup
	for i := range users {
		wg.Go(func() {
			uc, err := newUserConn(ctx, srv.base())
			if err != nil {
				t.Error(err)
				return
			}
			c := &client{uc: uc, base: srv.base(), xff: fmt.Sprint(i), rec: rec}
			defer c.close()
			for range each {
				if r := c.do(ctx, req{rt: rStatus, method: http.MethodGet, path: "/s", timeout: 2 * time.Second, intended: time.Now()}); r.status != 200 {
					t.Errorf("status %d %v", r.status, r.err)
					return
				}
			}
		})
	}
	wg.Wait()
	if got := rec.requests.Load(); got != users*each {
		t.Fatalf("requests %d, want %d", got, users*each)
	}
	if got := srv.conns.Load(); got != users {
		t.Fatalf("connections %d, want one per user (%d)", got, users)
	}
}
