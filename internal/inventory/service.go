package inventory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/events"
	"github.com/sheshakanthra/sluice/internal/redisx"
)

// Domain outcomes. Handlers map them to API.md §1.3 codes.
var (
	ErrSeatsTaken     = errors.New("seats taken")
	ErrInsufficient   = errors.New("insufficient seats")
	ErrSoldOut        = errors.New("sold out")
	ErrHoldLimit      = errors.New("user already holds seats for this event")
	ErrHoldExpired    = errors.New("hold expired")
	ErrHoldState      = errors.New("hold is not in a state that allows this")
	ErrHoldNotFound   = errors.New("hold not found")
	ErrQuotaExceeded  = errors.New("per-user quota exceeded")
	ErrEventNotFound  = errors.New("event not found")
	ErrInvalidRequest = errors.New("invalid hold request")
	// ErrNotLoaded means the event's Redis inventory is missing and a rebuild
	// is already running elsewhere; retry shortly (503).
	ErrNotLoaded = errors.New("inventory not loaded")
)

// SeatsTakenError lists the requested seats that are unavailable.
type SeatsTakenError struct{ Taken []int32 }

func (e *SeatsTakenError) Error() string { return fmt.Sprintf("seats taken: %v", e.Taken) }

// Is makes errors.Is(err, ErrSeatsTaken) true.
func (e *SeatsTakenError) Is(target error) bool { return target == ErrSeatsTaken }

// InsufficientError carries how many seats are left.
type InsufficientError struct{ Available int64 }

func (e *InsufficientError) Error() string {
	return fmt.Sprintf("insufficient seats: %d available", e.Available)
}

// Is makes errors.Is(err, ErrInsufficient) true.
func (e *InsufficientError) Is(target error) bool { return target == ErrInsufficient }

// nsHold is NS_HOLD, the UUIDv5 namespace of hold ids (DATA-MODEL §1):
// UUIDv5(NameSpaceURL, "https://sluice.dev/ns/hold").
var nsHold = uuid.NewSHA1(uuid.NameSpaceURL, []byte("https://sluice.dev/ns/hold"))

// HoldID is the deterministic hold id for (user, idempotency key), so a
// retried request lands on the same hold in Redis and in Postgres.
func HoldID(userID uuid.UUID, idemKey string) uuid.UUID {
	return uuid.NewSHA1(nsHold, []byte(userID.String()+":"+idemKey))
}

// Request is a hold request: specific seats or a quantity (best available).
type Request struct {
	SeatNos  []int32
	Quantity int
}

// Hold is a hold as returned to callers.
type Hold struct {
	ID        uuid.UUID
	EventID   uuid.UUID
	UserID    uuid.UUID
	Status    HoldStatus
	SeatNos   []int32
	Fence     int64
	ExpiresAt time.Time
	// Replayed is true when the hold already existed (a retry).
	Replayed bool
}

func holdFromRow(h gen.Hold) Hold {
	return Hold{ID: h.ID, EventID: h.EventID, UserID: h.UserID, Status: HoldStatus(h.Status),
		SeatNos: h.SeatNos, Fence: h.Fence, ExpiresAt: h.ExpiresAt.UTC()}
}

// Metrics are the inventory metrics (one set per process registry).
type Metrics struct {
	requests      *prometheus.CounterVec
	expired       prometheus.Counter
	compensations prometheus.Counter
	seats         *prometheus.GaugeVec
	repairs       prometheus.Counter
	// admissionsFreed counts queue admissions released when a hold or its
	// order ended (ADR 0010 addendum).
	admissionsFreed prometheus.Counter
}

// Hold request results (holds_requests_total{result}).
const (
	ResultOK           = "ok"
	ResultReplay       = "replay"
	ResultTaken        = "taken"
	ResultInsufficient = "insufficient"
	ResultSoldOut      = "sold_out"
	ResultHoldLimit    = "hold_limit"
	ResultQuota        = "quota"
	ResultInvalid      = "invalid"
	ResultError        = "error"
)

// NewMetrics registers the inventory metrics on reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "holds_requests_total", Help: "Hold requests by outcome.",
		}, []string{"result"}),
		expired: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "holds_expired_total", Help: "Holds reclaimed after expiry (sweeper or PG takeover).",
		}),
		compensations: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "holds_compensations_total", Help: "Redis grants undone because Postgres refused them.",
		}),
		seats: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "inventory_seats", Help: "Seats by state, from the availability cache.",
		}, []string{"event", "state"}),
		repairs: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "inventory_drift_repairs_total", Help: "Redis seats repaired from Postgres by the reconciler.",
		}),
		admissionsFreed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "queue_admissions_released_total",
			Help: "Queue admissions released because the user's hold or order ended (sold, failed, released, expired).",
		}),
	}
	for _, c := range []prometheus.Collector{m.requests, m.expired, m.compensations, m.seats, m.repairs, m.admissionsFreed} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("inventory metrics: %w", err)
		}
	}
	for _, r := range []string{ResultOK, ResultReplay, ResultTaken, ResultInsufficient, ResultSoldOut,
		ResultHoldLimit, ResultQuota, ResultInvalid, ResultError} {
		m.requests.WithLabelValues(r)
	}
	return m, nil
}

// PG is what the service needs from Postgres (a *pgxpool.Pool).
type PG interface {
	db.Beginner
	gen.DBTX
}

// Options configure a Service.
type Options struct {
	HoldTTL time.Duration
	// MaxSeats caps one hold (MAX_SEATS_PER_ORDER).
	MaxSeats int
	// PGOnly skips Redis (HOLD_PATH=pg_only); the fence comes from
	// hold_fence_seq.
	PGOnly  bool
	Now     func() time.Time
	Log     *slog.Logger
	Metrics *Metrics
	// OnReclaim runs inside the transaction that reclaims an expired hold
	// (sweeper or PG takeover), after the hold is EXPIRED and its seats are
	// freed. The orders package uses it to expire the hold's order and void
	// its payments (ARCHITECTURE §4.4). It must not wait on order locks.
	OnReclaim ReclaimHook
}

// ReclaimHook is called with the reclaiming transaction and the hold id.
type ReclaimHook func(ctx context.Context, tx pgx.Tx, holdID uuid.UUID) error

// Service holds, releases, extends and consumes seats. Redis is the fast
// path; Postgres has the final say (ARCHITECTURE §2, §4.2).
type Service struct {
	pg      PG
	q       *gen.Queries
	rdb     redis.Cmdable
	sc      *redisx.Scripts
	ttl     time.Duration
	max     int
	pgOnly  bool
	now     func() time.Time
	log     *slog.Logger
	metrics *Metrics
	reclaim ReclaimHook

	events *eventCache
}

// NewService wires a Service. rdb and sc may be nil only with PGOnly.
func NewService(pg PG, rdb redis.Cmdable, sc *redisx.Scripts, o Options) (*Service, error) {
	if !o.PGOnly && (rdb == nil || sc == nil) {
		return nil, errors.New("inventory: redis is required unless PGOnly")
	}
	if o.HoldTTL <= 0 {
		return nil, errors.New("inventory: HoldTTL must be > 0")
	}
	if o.MaxSeats <= 0 {
		o.MaxSeats = 4
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	q := gen.New(pg)
	return &Service{
		pg: pg, q: q, rdb: rdb, sc: sc, ttl: o.HoldTTL, max: o.MaxSeats, pgOnly: o.PGOnly,
		now: o.Now, log: o.Log, metrics: o.Metrics, reclaim: o.OnReclaim, events: newEventCache(q, 5*time.Second, o.Now),
	}, nil
}

// PGOnly reports whether Redis is bypassed.
func (s *Service) PGOnly() bool { return s.pgOnly }

func (s *Service) count(result string) {
	if s.metrics != nil {
		s.metrics.requests.WithLabelValues(result).Inc()
	}
}

// EventInfo is the cached part of an event the hold path needs.
type EventInfo struct {
	ID          uuid.UUID
	Slug        string
	Capacity    int32
	MaxPerUser  int32
	PricePaise  int64
	SaleOpensAt time.Time
	StartsAt    time.Time
	Status      gen.EventStatus
	labels      []string
}

// Label returns the seat label (e.g. C3-27).
func (e *EventInfo) Label(seat int32) string {
	if seat >= 0 && int(seat) < len(e.labels) {
		return e.labels[seat]
	}
	return strconv.Itoa(int(seat))
}

// Labels maps seat numbers to labels.
func (e *EventInfo) Labels(seats []int32) []string {
	out := make([]string, len(seats))
	for i, s := range seats {
		out[i] = e.Label(s)
	}
	return out
}

// Event resolves an event by UUID or slug (cached for a few seconds).
func (s *Service) Event(ctx context.Context, idOrSlug string) (*EventInfo, error) {
	return s.events.get(ctx, idOrSlug)
}

// eventCache caches event rows by id and slug. Seat count, price and labels
// never change after seeding; status may lag by up to ttl.
type eventCache struct {
	q   *gen.Queries
	ttl time.Duration
	now func() time.Time

	mu sync.Mutex
	m  map[string]cachedEvent
}

type cachedEvent struct {
	ev *EventInfo
	at time.Time
}

func newEventCache(q *gen.Queries, ttl time.Duration, now func() time.Time) *eventCache {
	return &eventCache{q: q, ttl: ttl, now: now, m: map[string]cachedEvent{}}
}

func (c *eventCache) get(ctx context.Context, idOrSlug string) (*EventInfo, error) {
	key := strings.ToLower(idOrSlug)
	c.mu.Lock()
	ce, ok := c.m[key]
	c.mu.Unlock()
	if ok && c.now().Sub(ce.at) < c.ttl {
		return ce.ev, nil
	}
	var (
		row gen.Event
		err error
	)
	if id, perr := uuid.Parse(idOrSlug); perr == nil {
		row, err = c.q.GetEventByID(ctx, id)
	} else if events.ValidSlug(idOrSlug) {
		row, err = c.q.GetEventBySlug(ctx, idOrSlug)
	} else {
		return nil, ErrEventNotFound
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrEventNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get event: %w", err)
	}
	ev := &EventInfo{ID: row.ID, Slug: row.Slug, Capacity: row.Capacity, MaxPerUser: row.MaxPerUser,
		PricePaise: row.PricePaise, SaleOpensAt: row.SaleOpensAt.UTC(), StartsAt: row.StartsAt.UTC(), Status: row.Status}
	var l events.Layout
	if err := json.Unmarshal(row.Layout, &l); err == nil {
		seats := l.Seats()
		ev.labels = make([]string, len(seats))
		for _, st := range seats {
			if int(st.SeatNo) < len(ev.labels) {
				ev.labels[st.SeatNo] = st.Label
			}
		}
	}
	now := c.now()
	c.mu.Lock()
	c.m[strings.ToLower(row.ID.String())] = cachedEvent{ev: ev, at: now}
	c.m[row.Slug] = cachedEvent{ev: ev, at: now}
	c.mu.Unlock()
	return ev, nil
}
