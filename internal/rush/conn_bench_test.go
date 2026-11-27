package rush

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// statusBody is the size and shape of a WAITING status answer.
var statusBody = `{"state":"WAITING","position":12345,"eta_s":61,"retry_after_ms":15000,"queue_token":"` +
	strings.Repeat("x", 240) + `","admission_token":"","admission_expires_at":null}`

// BenchmarkStatusPoll compares the swarm's per-request cost for one status
// poll on a dedicated keep-alive connection: the net/http transport it
// replaced versus userConn. Same in-process server for both, so the
// difference is the client. Run with -benchmem; ns/op is wall time over all
// GOMAXPROCS, which with every core busy is about CPU per request.
func BenchmarkStatusPoll(b *testing.B) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, statusBody)
	}))
	defer srv.Close()
	base := srv.URL + "/api/v1"
	q := req{rt: rStatus, method: http.MethodGet, path: "/events/rush-1/queue/status", token: "tok.q", timeout: statusTimeout}
	for _, mode := range []string{"nethttp", "userconn"} {
		b.Run(mode, func(b *testing.B) {
			rec := NewRecorder(time.Now(), time.Hour)
			b.SetParallelism(4) // 4 x GOMAXPROCS users, one connection each
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				c := &client{base: base, xff: "198.18.0.1", rec: rec}
				if mode == "nethttp" {
					c.hc = &http.Client{Transport: newTransport(ConnDedicated)}
				} else {
					uc, err := newUserConn(b.Context(), base)
					if err != nil {
						b.Error(err)
						return
					}
					c.uc = uc
				}
				defer func() {
					c.close()
					if c.hc != nil {
						c.hc.CloseIdleConnections()
					}
				}()
				for pb.Next() {
					q := q
					q.intended = time.Now()
					if r := c.do(b.Context(), q); r.status != 200 {
						b.Errorf("status %d %v", r.status, r.err)
						return
					}
				}
			})
		})
	}
}
