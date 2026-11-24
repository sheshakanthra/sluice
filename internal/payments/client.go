package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Client defaults (API.md §5).
const (
	DefaultTimeout    = 2 * time.Second
	DefaultMaxRetries = 3
	// MaxConnsPerHost bounds the PSP connections one process opens. Without
	// it a burst of order creates dials one connection per call; on a busy
	// host the PSP's accept queue overflows, refused dials are retried, and
	// the retries keep it overflowing (seen in P4b's 200-goroutine order
	// test: ~9k refused dials over 45 s). Calls beyond the bound wait for a
	// free connection inside their own per-attempt timeout.
	MaxConnsPerHost = 64
	retryBase       = 50 * time.Millisecond
	retryCap        = 500 * time.Millisecond
	maxResponseBody = 1 << 20
)

// PSP operations (psp_requests_total{op}).
const (
	OpCreate  = "create"
	OpGet     = "get"
	OpList    = "list"
	OpConfirm = "confirm"
	OpCapture = "capture"
	OpVoid    = "void"
	OpRefund  = "refund"
	OpLedger  = "ledger"
)

// Typed PSP errors.
var (
	// ErrUnavailable: every attempt timed out, failed to connect or got a
	// 5xx. The operation may or may not have happened; retrying with the
	// same key is always safe.
	ErrUnavailable = errors.New("psp unavailable")
	// ErrNotFound: the intent does not exist.
	ErrNotFound = errors.New("psp: intent not found")
	// ErrIntentState: the intent's state does not allow the operation (for
	// example confirming a canceled intent). Intent carries the current state.
	ErrIntentState = errors.New("psp: intent state does not allow this")
	// ErrKeyReused: the idempotency key was used with a different body.
	ErrKeyReused = errors.New("psp: idempotency key reused with a different request")
	// ErrRejected: any other 4xx (a bug on our side).
	ErrRejected = errors.New("psp: request rejected")
)

// StateError is ErrIntentState with the intent's current state.
type StateError struct {
	Intent  *Intent
	Message string
}

func (e *StateError) Error() string { return "psp: " + e.Message }

// Is makes errors.Is(err, ErrIntentState) true.
func (e *StateError) Is(target error) bool { return target == ErrIntentState }

// Metrics are the PSP client metrics.
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// NewMetrics registers psp_requests_total and psp_request_duration_seconds.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "psp_requests_total", Help: "PSP HTTP attempts by operation and result.",
		}, []string{"op", "result"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "psp_request_duration_seconds", Help: "PSP HTTP attempt latency.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2, 4},
		}, []string{"op"}),
	}
	for _, c := range []prometheus.Collector{m.requests, m.duration} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("psp metrics: %w", err)
		}
	}
	return m, nil
}

// Options configure a Client.
type Options struct {
	BaseURL string
	// PublicURL is MockPay as browsers see it (MOCKPAY_PUBLIC_URL).
	PublicURL string
	APIKey    string
	// Timeout per attempt (default 2 s).
	Timeout time.Duration
	// MaxRetries after the first attempt (default 3).
	MaxRetries int
	// Transport overrides the HTTP transport. Tests wrap NewTransport with
	// fault injection, so they run on the production connection pool.
	Transport http.RoundTripper
	Metrics   *Metrics
}

// Client calls MockPay. Every mutating call carries a deterministic
// Idempotency-Key, so its retries are safe.
type Client struct {
	base    string
	public  string
	apiKey  string
	timeout time.Duration
	retries int
	http    *http.Client
	metrics *Metrics
}

// NewTransport is the client's HTTP transport: a keep-alive pool of up to
// MaxConnsPerHost connections per PSP host, all kept idle between calls.
// dialTimeout <= 0 means DefaultTimeout.
func NewTransport(dialTimeout time.Duration) *http.Transport {
	if dialTimeout <= 0 {
		dialTimeout = DefaultTimeout
	}
	return &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: dialTimeout}).DialContext,
		MaxConnsPerHost:     MaxConnsPerHost,
		MaxIdleConns:        MaxConnsPerHost,
		MaxIdleConnsPerHost: MaxConnsPerHost,
		IdleConnTimeout:     90 * time.Second,
	}
}

// NewClient builds a Client.
func NewClient(o Options) (*Client, error) {
	u, err := url.Parse(o.BaseURL)
	if err != nil || u.Host == "" {
		return nil, errors.New("payments: invalid PSP base URL")
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.MaxRetries < 0 {
		o.MaxRetries = 0
	} else if o.MaxRetries == 0 {
		o.MaxRetries = DefaultMaxRetries
	}
	tr := o.Transport
	if tr == nil {
		tr = NewTransport(o.Timeout)
	}
	return &Client{
		base: strings.TrimRight(o.BaseURL, "/"), public: strings.TrimRight(o.PublicURL, "/"), apiKey: o.APIKey, timeout: o.Timeout, retries: o.MaxRetries,
		http: &http.Client{Transport: tr}, metrics: o.Metrics,
	}, nil
}

// CheckoutURL is the hosted checkout page of an intent, as browsers see it.
func (c *Client) CheckoutURL(intentID string) string {
	base := c.public
	if base == "" {
		base = c.base
	}
	return base + "/checkout/" + url.PathEscape(intentID)
}

// CreateIntent creates an intent (POST /v1/intents) under key.
func (c *Client) CreateIntent(ctx context.Context, key string, req CreateIntentRequest) (Intent, error) {
	var out Intent
	err := c.do(ctx, OpCreate, http.MethodPost, "/v1/intents", key, req, &out)
	return out, err
}

// GetIntent reads an intent.
func (c *Client) GetIntent(ctx context.Context, id string) (Intent, error) {
	var out Intent
	err := c.do(ctx, OpGet, http.MethodGet, "/v1/intents/"+url.PathEscape(id), "", nil, &out)
	return out, err
}

// ListFilter filters GET /v1/intents. Zero values are omitted.
type ListFilter struct {
	MerchantRef  string
	Status       string
	UpdatedSince time.Time
}

// ListIntents lists intents (the reconciler's view).
func (c *Client) ListIntents(ctx context.Context, f ListFilter) ([]Intent, error) {
	q := url.Values{}
	if f.MerchantRef != "" {
		q.Set("merchant_ref", f.MerchantRef)
	}
	if f.Status != "" {
		q.Set("status", f.Status)
	}
	if !f.UpdatedSince.IsZero() {
		q.Set("updated_since", f.UpdatedSince.UTC().Format(time.RFC3339Nano))
	}
	var out struct {
		Intents []Intent `json:"intents"`
	}
	err := c.do(ctx, OpList, http.MethodGet, "/v1/intents?"+q.Encode(), "", nil, &out)
	return out.Intents, err
}

// Confirm authorizes an intent with a saved test method (key confirm:<id>).
func (c *Client) Confirm(ctx context.Context, id, method string) (Intent, error) {
	var out Intent
	err := c.do(ctx, OpConfirm, http.MethodPost, "/v1/intents/"+url.PathEscape(id)+"/confirm",
		ConfirmKey(id), ConfirmRequest{PaymentMethod: method}, &out)
	return out, err
}

// Capture captures an authorized intent (key capture:<id>).
func (c *Client) Capture(ctx context.Context, id string) (Intent, error) {
	var out Intent
	err := c.do(ctx, OpCapture, http.MethodPost, "/v1/intents/"+url.PathEscape(id)+"/capture", CaptureKey(id), struct{}{}, &out)
	return out, err
}

// Void cancels an intent, releasing any authorization (key void:<id>).
func (c *Client) Void(ctx context.Context, id string) (Intent, error) {
	var out Intent
	err := c.do(ctx, OpVoid, http.MethodPost, "/v1/intents/"+url.PathEscape(id)+"/void", VoidKey(id), struct{}{}, &out)
	return out, err
}

// Refund refunds a captured intent (key refund:<id>).
func (c *Client) Refund(ctx context.Context, id string) (Intent, error) {
	var out Intent
	err := c.do(ctx, OpRefund, http.MethodPost, "/v1/intents/"+url.PathEscape(id)+"/refund", RefundKey(id), struct{}{}, &out)
	return out, err
}

// LedgerSummary reads GET /v1/ledger/summary (eventID may be empty).
func (c *Client) LedgerSummary(ctx context.Context, eventID string) (LedgerSummary, error) {
	q := url.Values{}
	if eventID != "" {
		q.Set("metadata.event_id", eventID)
	}
	var out LedgerSummary
	err := c.do(ctx, OpLedger, http.MethodGet, "/v1/ledger/summary?"+q.Encode(), "", nil, &out)
	return out, err
}

// do runs one PSP call with per-attempt timeouts and up to c.retries
// jittered retries on timeouts, connection errors and 5xx. The body and
// key are identical on every attempt.
func (c *Client) do(ctx context.Context, op, method, path, key string, in, out any) error {
	var body []byte
	if in != nil {
		var err error
		if body, err = json.Marshal(in); err != nil {
			return fmt.Errorf("psp %s: encode: %w", op, err)
		}
	}
	var lastErr error
	for attempt := 0; attempt <= c.retries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, jitter(attempt)); err != nil {
				return fmt.Errorf("psp %s: %w: %w (last: %w)", op, ErrUnavailable, err, lastErr)
			}
		}
		retry, err := c.once(ctx, op, method, path, key, body, out)
		if err == nil {
			return nil
		}
		if !retry {
			return err
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return fmt.Errorf("psp %s: %w after %d attempts: %w", op, ErrUnavailable, c.retries+1, lastErr)
}

func (c *Client) once(ctx context.Context, op, method, path, key string, body []byte, out any) (retry bool, err error) {
	actx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(actx, method, c.base+path, rd)
	if err != nil {
		return false, fmt.Errorf("psp %s: build request: %w", op, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	start := time.Now()
	resp, err := c.http.Do(req)
	c.observe(op, start)
	if err != nil {
		result := "network"
		if errors.Is(err, context.DeadlineExceeded) || actx.Err() != nil {
			result = "timeout"
		}
		c.count(op, result)
		return true, fmt.Errorf("psp %s: %s: %w", op, result, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		c.count(op, "network")
		return true, fmt.Errorf("psp %s: read body: %w", op, err)
	}
	switch {
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		c.count(op, "server_error")
		return true, fmt.Errorf("psp %s: HTTP %d", op, resp.StatusCode)
	case resp.StatusCode >= 400:
		c.count(op, "client_error")
		return false, decodeError(op, resp.StatusCode, raw)
	}
	c.count(op, "ok")
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return false, fmt.Errorf("psp %s: decode: %w", op, err)
		}
	}
	return false, nil
}

func decodeError(op string, status int, raw []byte) error {
	var eb ErrorBody
	_ = json.Unmarshal(raw, &eb)
	msg := eb.Error.Message
	switch {
	case status == http.StatusNotFound:
		return fmt.Errorf("psp %s: %w", op, ErrNotFound)
	case status == http.StatusConflict:
		return fmt.Errorf("psp %s: %w", op, &StateError{Intent: eb.Intent, Message: msg})
	case eb.Error.Code == ErrCodeIdempotencyUsed:
		return fmt.Errorf("psp %s: %w", op, ErrKeyReused)
	default:
		return fmt.Errorf("psp %s: HTTP %d %s: %w", op, status, msg, ErrRejected)
	}
}

func (c *Client) count(op, result string) {
	if c.metrics != nil {
		c.metrics.requests.WithLabelValues(op, result).Inc()
	}
}

func (c *Client) observe(op string, start time.Time) {
	if c.metrics != nil {
		c.metrics.duration.WithLabelValues(op).Observe(time.Since(start).Seconds())
	}
}

// jitter is full-jitter exponential backoff for retry n (1-based).
func jitter(n int) time.Duration {
	d := min(retryBase<<min(n-1, 6), retryCap)
	return time.Duration(rand.Int64N(int64(d))) + time.Millisecond //nolint:gosec // G404: jitter, not security
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
