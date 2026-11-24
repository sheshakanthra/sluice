package rush

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Per-call client timeouts (LOADTEST §3.3).
const (
	statusTimeout = 2 * time.Second
	callTimeout   = 5 * time.Second
	maxBody       = 1 << 20
)

// newTransport returns the per-user transport (dedicated: one keep-alive
// connection per user, like a real browser tab) or a pooled one.
func newTransport(conn string) *http.Transport {
	d := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
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
	intended time.Time // when the request was meant to go out
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
	cctx, cancel := context.WithTimeout(ctx, q.timeout)
	defer cancel()
	hr, err := http.NewRequestWithContext(cctx, q.method, c.base+q.path, rd)
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
	case err != nil && (errors.Is(err, context.DeadlineExceeded) || isTimeout(err)):
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

// retryable reports whether the same request (same IK) should be sent again,
// and after how long.
func retryable(r *result, attempt int) (time.Duration, bool) {
	backoff := min(time.Duration(250*(1<<min(attempt, 3)))*time.Millisecond, 2*time.Second)
	switch {
	case r.oc == timedOut || r.oc == transport:
		return backoff, true
	case r.status >= 500:
		return max(r.retryAfter, backoff), true
	case r.status == http.StatusTooManyRequests:
		return max(r.retryAfter, 500*time.Millisecond), true
	case r.status == http.StatusConflict && r.code() == "REQUEST_IN_PROGRESS":
		return max(r.retryAfter, 500*time.Millisecond), true
	}
	return 0, false
}
