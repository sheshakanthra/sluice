package rush

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptrace"
	"strconv"
	"sync"
	"time"
)

// Per-call client timeouts (LOADTEST §3.3). Each one runs from the moment
// the request has its connection and is about to be written (see do).
const (
	statusTimeout = 2 * time.Second
	callTimeout   = 5 * time.Second
	dialTimeout   = 5 * time.Second
	maxBody       = 1 << 20
)

// Client backoff for a failed attempt (LOADTEST §3.3, ADR 0017): full
// jitter, U(0, min(backoffCap, backoffBase x 2^attempt)), never below the
// server's own pacing (its last retry_after or Retry-After).
const (
	backoffBase = 500 * time.Millisecond
	backoffCap  = 30 * time.Second
)

// errCallTimeout cancels a request whose per-call timeout ran out.
var errCallTimeout = errors.New("rush: call timeout")

// newTransport returns the per-user transport (dedicated: one keep-alive
// connection per user, like a real browser tab) or a pooled one.
func newTransport(conn string) *http.Transport {
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	t := &http.Transport{
		DialContext:         d.DialContext,
		ForceAttemptHTTP2:   false,
		DisableCompression:  true,
		IdleConnTimeout:     10 * time.Minute,
		TLSHandshakeTimeout: 5 * time.Second,
	}
	if conn == ConnDedicated {
		t.MaxConnsPerHost = 1
		t.MaxIdleConnsPerHost = 1
		t.MaxIdleConns = 1
	} else {
		t.MaxIdleConnsPerHost = 1024
		t.MaxIdleConns = 1024
	}
	return t
}

// client is one user's view of the API.
type client struct {
	hc   *http.Client
	base string // target + /api/v1
	xff  string
	rec  *Recorder
}

// result is a finished HTTP exchange.
type result struct {
	status     int
	body       []byte
	retryAfter time.Duration
	oc         outcome
	err        error // transport or timeout error (status 0)
}

// problem is the subset of RFC 9457 bodies the swarm acts on.
type problem struct {
	Code      string  `json:"code"`
	Available *int    `json:"available"`
	Taken     []int32 `json:"taken"`
}

func (r *result) code() string {
	if r.status < 400 {
		return ""
	}
	var p problem
	_ = json.Unmarshal(r.body, &p) // non-JSON bodies have no code
	return p.Code
}

func (r *result) problem() problem {
	var p problem
	_ = json.Unmarshal(r.body, &p)
	return p
}

// req describes one request.
type req struct {
	rt       route
	method   string
	path     string // after /api/v1
	token    string
	ik       string
	body     any
	timeout  time.Duration
	intended time.Time     // when the request was meant to go out
	floor    time.Duration // server-given pacing: no retry sooner than this
}

// sendDeadline arms q's timeout when the request gets its connection, so the
// budget measures the server (and the network), not the swarm's own dial or
// scheduling delay. A hard stop at dial + timeout bounds a connection that
// never comes. Latency is still recorded from q.intended (LOADTEST §3.4).
type sendDeadline struct {
	mu     sync.Mutex
	timer  *time.Timer
	cancel context.CancelCauseFunc
}

func (d *sendDeadline) arm(timeout time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer == nil {
		d.timer = time.AfterFunc(timeout, func() { d.cancel(errCallTimeout) })
	}
}

func (d *sendDeadline) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
	}
}

// do sends one request and records it against its intended start. A
// cancelled run context is not recorded (the run is over, not the server).
func (c *client) do(ctx context.Context, q req) result {
	var rd io.Reader
	if q.body != nil {
		b, err := json.Marshal(q.body)
		if err != nil {
			return result{err: err, oc: transport}
		}
		rd = bytes.NewReader(b)
	}
	cctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	dl := &sendDeadline{cancel: cancel}
	defer dl.stop()
	hard := time.AfterFunc(dialTimeout+q.timeout, func() { cancel(errCallTimeout) })
	defer hard.Stop()
	tctx := httptrace.WithClientTrace(cctx, &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) { dl.arm(q.timeout) },
	})
	hr, err := http.NewRequestWithContext(tctx, q.method, c.base+q.path, rd)
	if err != nil {
		return result{err: err, oc: transport}
	}
	if q.body != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	if q.token != "" {
		hr.Header.Set("Authorization", "Bearer "+q.token)
	}
	if q.ik != "" {
		hr.Header.Set("Idempotency-Key", q.ik)
	}
	hr.Header.Set("X-Forwarded-For", c.xff)
	resp, err := c.hc.Do(hr)
	var res result
	if err == nil {
		res.status = resp.StatusCode
		res.body, err = io.ReadAll(io.LimitReader(resp.Body, maxBody))
		_ = resp.Body.Close()
		if s := resp.Header.Get("Retry-After"); s != "" {
			if n, perr := strconv.Atoi(s); perr == nil && n >= 0 {
				res.retryAfter = time.Duration(n) * time.Second
			}
		}
	}
	end := time.Now()
	switch {
	case ctx.Err() != nil:
		res.err, res.oc = ctx.Err(), transport
		return res // run over: not a measurement
	case err != nil && (errors.Is(context.Cause(cctx), errCallTimeout) || errors.Is(err, context.DeadlineExceeded) || isTimeout(err)):
		res.status, res.err, res.oc = 0, err, timedOut
	case err != nil:
		res.status, res.err, res.oc = 0, err, transport
	default:
		res.oc = classify(res.status)
	}
	c.rec.Record(q.rt, q.intended, end, res.status, res.oc)
	return res
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// backoff is full-jitter exponential backoff, U(0, min(backoffCap,
// backoffBase x 2^attempt)), floored at floor. u is U(0,1).
func backoff(attempt int, floor time.Duration, u float64) time.Duration {
	ceil := backoffCap
	if attempt < 16 {
		ceil = min(backoffCap, backoffBase<<attempt)
	}
	return max(floor, time.Duration(u*float64(ceil)))
}

// retryable reports whether the same request (same IK) should be sent again,
// and after how long. Failures (timeout, transport, 5xx, 429) back off with
// full jitter, never sooner than the server's pacing: q's floor (the last
// retry_after) or the response's Retry-After. REQUEST_IN_PROGRESS is not a
// failure (the first attempt is still running) and keeps its short wait.
func retryable(r *result, attempt int, floor time.Duration, u float64) (time.Duration, bool) {
	switch {
	case r.oc == timedOut || r.oc == transport:
		return backoff(attempt, floor, u), true
	case r.status >= 500, r.status == http.StatusTooManyRequests:
		return backoff(attempt, max(floor, r.retryAfter), u), true
	case r.status == http.StatusConflict && r.code() == "REQUEST_IN_PROGRESS":
		return max(r.retryAfter, 500*time.Millisecond), true
	}
	return 0, false
}

// jitterU is the U(0,1) draw for backoff. math/rand/v2's top-level source is
// safe for concurrent use, so a user and its double-submit twin can share it.
func jitterU() float64 { return rand.Float64() } //nolint:gosec // G404: retry jitter, not a secret
