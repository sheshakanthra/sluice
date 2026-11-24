// Package mockpay is a purpose-built PSP simulator (ARCHITECTURE §7): payment
// intents with Stripe-style idempotency keys, an append-only ledger, signed
// at-least-once webhooks with retries, deterministic fault injection and a
// hosted checkout page. It owns its own database (`mockpay`) and migrations,
// and talks to SLUICE only over HTTP.
package mockpay

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/mockpay/mpdb"
	"github.com/sheshakanthra/sluice/internal/obs"
	"github.com/sheshakanthra/sluice/internal/payments"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// MerchantID is the single seeded merchant.
const MerchantID = "sluice"

// FaultHeader forces a fault on one request (test/rush only, API.md §5).
const FaultHeader = "X-MockPay-Fault"

// Faults accepted in FaultHeader.
const (
	FaultTimeoutAfterCommit = "timeout_after_commit"
	FaultFail               = "fail"
	FaultDupWebhook         = "dup_webhook"
	FaultReorderWebhook     = "reorder_webhook"
)

// Migrate applies MockPay's goose migrations to its own database.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("mockpay migrations fs: %w", err)
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("mockpay goose locker: %w", err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func(d *sql.DB) { _ = d.Close() }(sqlDB)
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("mockpay goose provider: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("mockpay migrate: %w", err)
	}
	return nil
}

// Options configure the simulator. Rates are probabilities in [0, 1].
type Options struct {
	APIKey        string
	WebhookURL    string
	WebhookSecret string
	// PublicURL is what browsers see (checkout_url = PublicURL/checkout/{id}).
	PublicURL string
	// LatencyMin/Max: uniform latency added to every API call.
	LatencyMin, LatencyMax time.Duration
	FailRate               float64
	TimeoutAfterCommitRate float64
	// Hang is how long a timeout-after-commit response is held back.
	Hang             time.Duration
	WebhookDupRate   float64
	WebhookReorder   float64
	WebhookMaxDelay  time.Duration
	AllowFaultHeader bool
	// SlowDelay is how long pm_slow waits before authorizing (default 1 s).
	SlowDelay time.Duration
	// DispatchInterval is how often the dispatcher polls (default 100 ms).
	DispatchInterval time.Duration
	// DispatchWorkers bounds concurrent deliveries (default 16).
	DispatchWorkers int
	// WebhookTimeout bounds one delivery (default 5 s).
	WebhookTimeout time.Duration
	// HTTPClient sends webhooks (default: a client without a global timeout).
	HTTPClient *http.Client
	Now        func() time.Time
	Log        *slog.Logger
}

// Metrics are MockPay's own metrics.
type Metrics struct {
	requests   *prometheus.CounterVec
	ledger     *prometheus.CounterVec
	deliveries *prometheus.CounterVec
	faults     *prometheus.CounterVec
	// duration is the server-side view of each API call (handler time,
	// including the injected MOCKPAY_LATENCY_MS), by the client's op names.
	duration *prometheus.HistogramVec
}

func newMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mockpay_requests_total", Help: "MockPay API operations by result.",
		}, []string{"op", "result"}),
		ledger: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mockpay_ledger_entries_total", Help: "Ledger entries written, by kind.",
		}, []string{"kind"}),
		deliveries: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mockpay_webhook_deliveries_total", Help: "Webhook delivery attempts by result.",
		}, []string{"result"}),
		faults: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "mockpay_faults_total", Help: "Injected faults by kind.",
		}, []string{"fault"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "mockpay_request_duration_seconds", Help: "MockPay API handler latency (server side), by operation.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2, 4},
		}, []string{"op"}),
	}
	for _, c := range []prometheus.Collector{m.requests, m.ledger, m.deliveries, m.faults, m.duration} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("mockpay metrics: %w", err)
		}
	}
	return m, nil
}

// Server is the MockPay service.
type Server struct {
	pool    *pgxpool.Pool
	q       *mpdb.Queries
	o       Options
	keyHash []byte
	metrics *Metrics
	reg     *prometheus.Registry
	client  *http.Client
}

// New wires a Server and upserts the merchant row from o.
func New(ctx context.Context, pool *pgxpool.Pool, reg *prometheus.Registry, o Options) (*Server, error) {
	if o.APIKey == "" || o.WebhookSecret == "" || o.WebhookURL == "" {
		return nil, errors.New("mockpay: APIKey, WebhookURL and WebhookSecret are required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.SlowDelay <= 0 {
		o.SlowDelay = time.Second
	}
	if o.DispatchInterval <= 0 {
		o.DispatchInterval = 100 * time.Millisecond
	}
	if o.DispatchWorkers <= 0 {
		o.DispatchWorkers = 16
	}
	if o.WebhookTimeout <= 0 {
		o.WebhookTimeout = 5 * time.Second
	}
	o.PublicURL = strings.TrimRight(o.PublicURL, "/")
	m, err := newMetrics(reg)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256([]byte(o.APIKey))
	s := &Server{pool: pool, q: mpdb.New(pool), o: o, keyHash: h[:], metrics: m, reg: reg, client: o.HTTPClient}
	if s.client == nil {
		s.client = &http.Client{}
	}
	if err := s.q.UpsertMerchant(ctx, mpdb.UpsertMerchantParams{
		ID: MerchantID, ApiKeyHash: s.keyHash, WebhookUrl: o.WebhookURL, WebhookSecret: o.WebhookSecret,
	}); err != nil {
		return nil, fmt.Errorf("mockpay: upsert merchant: %w", err)
	}
	return s, nil
}

// Handler returns the HTTP API, the hosted checkout and /metrics.
func (s *Server) Handler() http.Handler {
	rt := httpx.NewRouter()
	httpx.RegisterHealth(rt, s.o.Log, httpx.Check{Name: "postgres", Ping: s.pool.Ping})
	rt.Handle("GET /metrics", obs.MetricsHandler(s.reg))
	api := func(op, pattern string, h http.HandlerFunc) { rt.Handle(pattern, s.timed(op, s.requireKey(h))) }
	api(payments.OpCreate, "POST /v1/intents", s.handleCreate)
	api(payments.OpList, "GET /v1/intents", s.handleList)
	api(payments.OpGet, "GET /v1/intents/{id}", s.handleGet)
	api(payments.OpConfirm, "POST /v1/intents/{id}/confirm", s.handleOp(opConfirm))
	api(payments.OpCapture, "POST /v1/intents/{id}/capture", s.handleOp(opCapture))
	api(payments.OpVoid, "POST /v1/intents/{id}/void", s.handleOp(opVoid))
	api(payments.OpRefund, "POST /v1/intents/{id}/refund", s.handleOp(opRefund))
	api(payments.OpLedger, "GET /v1/ledger/summary", s.handleLedgerSummary)
	rt.HandleFunc("GET /checkout/{id}", s.handleCheckoutPage)
	rt.HandleFunc("POST /checkout/{id}/pay", s.handleCheckoutPay)
	return httpx.Chain(rt, httpx.Recover(s.o.Log))
}

// timed observes mockpay_request_duration_seconds{op} for every API call.
func (s *Server) timed(op string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.metrics.duration.WithLabelValues(op).Observe(time.Since(start).Seconds())
	})
}

// requireKey checks `Authorization: Bearer <MOCKPAY_API_KEY>` in constant time.
func (s *Server) requireKey(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		h := sha256.Sum256([]byte(tok))
		if !ok || subtle.ConstantTimeCompare(h[:], s.keyHash) != 1 {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "a valid API key is required", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}
