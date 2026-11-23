package orders

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/payments"
)

// Domain outcomes. Handlers map them to API.md §1.3 codes.
var (
	ErrOrderNotFound      = errors.New("order not found")
	ErrOrderState         = errors.New("action not allowed in the order's current state")
	ErrAttemptsExhausted  = errors.New("no payment attempts left")
	ErrInvalidMethod      = errors.New("invalid payment method")
	errHoldLost           = errors.New("hold lost")
	errQuotaExceeded      = errors.New("quota exceeded at confirm")
	errConcurrentCreation = errors.New("order created concurrently")
)

// Failure codes stored on FAILED orders.
const (
	FailureHoldLost      = "HOLD_LOST"
	FailureQuotaExceeded = "QUOTA_EXCEEDED"
	FailurePayment       = "PAYMENT_FAILED"
)

// MaxAttempts is the payment attempt limit per order (payments.attempt CHECK).
const MaxAttempts = 3

// Provider is the only PSP.
const Provider = "mockpay"

// Webhook results (webhooks_total{result}).
const (
	WebhookProcessed         = "processed"
	WebhookDuplicate         = "duplicate"
	WebhookBadSignature      = "bad_signature"
	WebhookRegressionIgnored = "regression_ignored"
	WebhookUnknownIntent     = "unknown_intent"
)

// Inserter enqueues River jobs inside a transaction (a *river.Client).
type Inserter interface {
	InsertTx(ctx context.Context, tx pgx.Tx, args river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error)
}

// PG is what the service needs from Postgres (a *pgxpool.Pool).
type PG interface {
	db.Beginner
	gen.DBTX
}

// Metrics are the order and payment metrics.
type Metrics struct {
	orders     *prometheus.CounterVec
	payments   *prometheus.CounterVec
	webhooks   *prometheus.CounterVec
	duplicates prometheus.Counter
	orphans    prometheus.Counter
}

// NewMetrics registers the P3 order/payment metrics on reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		orders: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "orders_total", Help: "Order transitions by resulting status.",
		}, []string{"status"}),
		payments: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "payments_total", Help: "Payment transitions by resulting status.",
		}, []string{"status"}),
		webhooks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "webhooks_total", Help: "PSP webhooks by type and result.",
		}, []string{"type", "result"}),
		duplicates: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "duplicate_charges_prevented_total", Help: "Second authorizations for one order that were voided or refunded.",
		}),
		orphans: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "orphans_fixed_total", Help: "PSP intents holding money without a confirmed order, voided or refunded by the reconciler.",
		}),
	}
	for _, c := range []prometheus.Collector{m.orders, m.payments, m.webhooks, m.duplicates, m.orphans} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("orders metrics: %w", err)
		}
	}
	return m, nil
}

// Options configure a Service.
type Options struct {
	// PaymentHoldTTL is the hold's new lifetime once an order exists.
	PaymentHoldTTL time.Duration
	// ReturnURL is where the hosted checkout sends the browser back
	// (PUBLIC_APP_URL/order); "?o=<order_id>" is appended.
	ReturnURL string
	// OrphanAge: PSP intents holding money for longer than this without a
	// confirmed order are voided or refunded by the reconciler (30 s).
	OrphanAge time.Duration
	// CaptureAge: confirmed orders whose payment is AUTHORIZED for longer
	// than this get their capture re-enqueued (60 s).
	CaptureAge time.Duration
	Now        func() time.Time
	Log        *slog.Logger
	Metrics    *Metrics
}

// Service owns orders and payments.
type Service struct {
	pg   PG
	q    *gen.Queries
	inv  *inventory.Service
	psp  *payments.Client
	jobs Inserter
	rdb  redis.Cmdable
	o    Options
}

// NewService wires a Service. rdb may be nil (no live audit cache).
func NewService(pg PG, inv *inventory.Service, psp *payments.Client, jobs Inserter, rdb redis.Cmdable, o Options) (*Service, error) {
	if inv == nil || psp == nil || jobs == nil {
		return nil, errors.New("orders: inventory, psp and jobs are required")
	}
	if o.PaymentHoldTTL <= 0 {
		return nil, errors.New("orders: PaymentHoldTTL must be > 0")
	}
	if o.OrphanAge <= 0 {
		o.OrphanAge = 30 * time.Second
	}
	if o.CaptureAge <= 0 {
		o.CaptureAge = 60 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	return &Service{pg: pg, q: gen.New(pg), inv: inv, psp: psp, jobs: jobs, rdb: rdb, o: o}, nil
}

func (s *Service) countOrder(st gen.OrderStatus) {
	if s.o.Metrics != nil {
		s.o.Metrics.orders.WithLabelValues(string(st)).Inc()
	}
}

func (s *Service) countPayment(st gen.PaymentStatus) {
	if s.o.Metrics != nil {
		s.o.Metrics.payments.WithLabelValues(string(st)).Inc()
	}
}

// CountWebhook counts one webhook outcome.
func (s *Service) CountWebhook(typ, result string) {
	if s.o.Metrics != nil {
		s.o.Metrics.webhooks.WithLabelValues(typ, result).Inc()
	}
}

func (s *Service) countDuplicate() {
	if s.o.Metrics != nil {
		s.o.Metrics.duplicates.Inc()
	}
}

func (s *Service) countOrphan() {
	if s.o.Metrics != nil {
		s.o.Metrics.orphans.Inc()
	}
}

// View is the order as the API returns it (API.md §3.5–3.7).
type View struct {
	OrderID       uuid.UUID     `json:"order_id"`
	EventID       uuid.UUID     `json:"event_id"`
	Status        string        `json:"status"`
	AmountPaise   int64         `json:"amount_paise"`
	Currency      string        `json:"currency"`
	SeatNos       []int32       `json:"seat_nos"`
	HoldExpiresAt time.Time     `json:"hold_expires_at"`
	Payment       *PaymentView  `json:"payment"`
	Charged       bool          `json:"charged"`
	Tickets       []TicketView  `json:"tickets"`
	FailureCode   *string       `json:"failure_code"`
	ConfirmedAt   *time.Time    `json:"confirmed_at,omitempty"`
	Attempts      []PaymentView `json:"-"`
}

// PaymentView is the current payment attempt.
type PaymentView struct {
	Attempt     int32  `json:"attempt"`
	Status      string `json:"status"`
	IntentID    string `json:"intent_id,omitempty"`
	CheckoutURL string `json:"checkout_url,omitempty"`
}

// TicketView is one issued ticket.
type TicketView struct {
	SeatNo int32  `json:"seat_no"`
	Label  string `json:"label"`
	Code   string `json:"code"`
}

// view loads everything the API shows about an order.
func (s *Service) view(ctx context.Context, orderID uuid.UUID) (View, error) {
	o, err := s.q.GetOrder(ctx, orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return View{}, ErrOrderNotFound
	}
	if err != nil {
		return View{}, fmt.Errorf("get order: %w", err)
	}
	pays, err := s.q.ListPaymentsByOrder(ctx, orderID)
	if err != nil {
		return View{}, fmt.Errorf("list payments: %w", err)
	}
	h, err := s.q.GetHold(ctx, o.HoldID)
	if err != nil {
		return View{}, fmt.Errorf("get hold: %w", err)
	}
	ts, err := s.q.ListTicketsByOrder(ctx, orderID)
	if err != nil {
		return View{}, fmt.Errorf("list tickets: %w", err)
	}
	v := View{OrderID: o.ID, EventID: o.EventID, Status: string(o.Status), AmountPaise: o.AmountPaise,
		Currency: o.Currency, SeatNos: o.SeatNos, HoldExpiresAt: h.ExpiresAt.UTC(), FailureCode: o.FailureCode,
		ConfirmedAt: o.ConfirmedAt, Tickets: []TicketView{}}
	for i := range pays {
		p := &pays[i]
		pv := PaymentView{Attempt: p.Attempt, Status: string(p.Status)}
		if p.ProviderIntentID != nil {
			pv.IntentID = *p.ProviderIntentID
			if p.Status == gen.PaymentStatusCREATED && o.Status == gen.OrderStatusPENDINGPAYMENT {
				pv.CheckoutURL = s.psp.CheckoutURL(pv.IntentID)
			}
		}
		if PaymentStatus(p.Status).Live() {
			v.Charged = true
		}
		v.Attempts = append(v.Attempts, pv)
	}
	if n := len(v.Attempts); n > 0 {
		v.Payment = &v.Attempts[n-1]
		for i := range v.Attempts { // the live attempt wins over a later dead one
			if PaymentStatus(v.Attempts[i].Status).Live() {
				v.Payment = &v.Attempts[i]
			}
		}
	}
	if len(ts) > 0 {
		ev, err := s.inv.Event(ctx, o.EventID.String())
		if err != nil {
			return View{}, err
		}
		for _, t := range ts {
			v.Tickets = append(v.Tickets, TicketView{SeatNo: t.SeatNo, Label: ev.Label(t.SeatNo), Code: t.Code})
		}
	}
	return v, nil
}

// owned returns the order if it belongs to userID (ErrOrderNotFound otherwise).
func (s *Service) owned(ctx context.Context, orderID, userID uuid.UUID) (gen.Order, error) {
	o, err := s.q.GetOrder(ctx, orderID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && o.UserID != userID) {
		return gen.Order{}, ErrOrderNotFound
	}
	if err != nil {
		return gen.Order{}, fmt.Errorf("get order: %w", err)
	}
	return o, nil
}

// Get returns the caller's order (API GET /orders/{id}).
func (s *Service) Get(ctx context.Context, orderID, userID uuid.UUID) (View, error) {
	if _, err := s.owned(ctx, orderID, userID); err != nil {
		return View{}, err
	}
	return s.view(ctx, orderID)
}

// ticketAlphabet avoids look-alikes (no 0/O, 1/I/L).
const ticketAlphabet = "23456789ABCDEFGHJKMNPQRSTUVWXYZ"

// ticketCodes returns n random codes like SLC-7Q4K-92XM.
func ticketCodes(n int) []string {
	out := make([]string, n)
	for i := range out {
		var b [8]byte
		_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
		c := make([]byte, 0, 13)
		c = append(c, "SLC-"...)
		for j, x := range b {
			if j == 4 {
				c = append(c, '-')
			}
			c = append(c, ticketAlphabet[int(x)%len(ticketAlphabet)])
		}
		out[i] = string(c)
	}
	return out
}
