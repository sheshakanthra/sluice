// Package queue is SLUICE's virtual waiting room (ARCHITECTURE §4.1, §5):
// joining costs one Redis call, waiting costs none, and admission is fair
// (lottery order for the pre-queue, then FIFO), inventory-aware and bounded.
//
//   - Service: join, status and leave for the API (one replica's view).
//   - Cache: a per-replica, per-event snapshot of the queue and inventory
//     counters, refreshed with one snapshot.lua call per event every 100 ms
//     and read lock-free through an atomic.Pointer. WAITING users are answered
//     from it plus their signed queue token, with zero Redis calls.
//   - Ticker / Controller: the admission controller the worker runs, one
//     loop per open event, each step a single atomic queue_tick.lua call.
//
// Every Redis mutation is a Lua script from internal/redisx; time and
// randomness come from Go through ARGV.
package queue

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/sheshakanthra/sluice/internal/db/gen"
)

// MaxRejoins is how many times a user may rejoin after OFFER_EXPIRED.
const MaxRejoins = 2

// rateTau is the time constant of the admit-rate EMA kept by queue_tick.
const rateTau = 5 * time.Second

// State is a queue state as returned by join and status (API.md §3.3).
type State string

// Queue states.
const (
	StatePrequeue     State = "PREQUEUE"
	StateWaiting      State = "WAITING"
	StatePaused       State = "PAUSED"
	StateAdmitted     State = "ADMITTED"
	StateSoldOut      State = "SOLD_OUT"
	StateOfferExpired State = "OFFER_EXPIRED"
	StateClosed       State = "CLOSED"
)

// Outlook is the coarse chance of getting a seat, shown to WAITING users.
type Outlook string

// Outlooks (ARCHITECTURE §5).
const (
	OutlookLikely   Outlook = "LIKELY"
	OutlookPossible Outlook = "POSSIBLE"
	OutlookUnlikely Outlook = "UNLIKELY"
)

// ErrRejoinLimit: the user already rejoined MaxRejoins times (409 REJOIN_LIMIT).
var ErrRejoinLimit = errors.New("queue: rejoin limit reached")

// Event is what the queue needs to know about an event.
type Event struct {
	ID          uuid.UUID
	Capacity    int32
	SaleOpensAt time.Time
	StartsAt    time.Time
	Status      gen.EventStatus
}

// Metrics are the API side's queue metrics (P4 task 7).
type Metrics struct {
	statusRedisCalls prometheus.Counter
	joins            *prometheus.CounterVec
	statuses         *prometheus.CounterVec
	admissions       prometheus.Counter
}

// NewMetrics registers the API-side queue metrics on reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		statusRedisCalls: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "queue_status_redis_calls_total",
			Help: "Redis calls made while answering GET queue/status (stays ~0 for WAITING users).",
		}),
		joins: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "queue_joins_total",
			Help: "Queue joins by result (PREQUEUE, WAITING, SOLD_OUT, CLOSED, REJOIN_LIMIT, ERROR).",
		}, []string{"result"}),
		statuses: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "queue_status_total",
			Help: "Queue status answers by state.",
		}, []string{"state"}),
		admissions: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "queue_admitted_total",
			Help: "Offers claimed into a new admission (idempotent re-claims are not counted).",
		}),
	}
	for _, c := range []prometheus.Collector{m.statusRedisCalls, m.joins, m.statuses, m.admissions} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("queue: register metrics: %w", err)
		}
	}
	return m, nil
}

func (m *Metrics) statusRedisCall() {
	if m != nil {
		m.statusRedisCalls.Inc()
	}
}

func (m *Metrics) join(st State, err error) {
	if m == nil {
		return
	}
	result := string(st)
	switch {
	case errors.Is(err, ErrRejoinLimit):
		result = "REJOIN_LIMIT"
	case err != nil:
		result = "ERROR"
	}
	m.joins.WithLabelValues(result).Inc()
}

func (m *Metrics) status(st State) {
	if m != nil {
		m.statuses.WithLabelValues(string(st)).Inc()
	}
}

func (m *Metrics) admitted() {
	if m != nil {
		m.admissions.Inc()
	}
}

// WorkerMetrics are the worker side's queue metrics: per-event gauges set by
// the Snapshotter every second, and offer expiries counted by the controller.
type WorkerMetrics struct {
	waiting, cursor, offers, active *prometheus.GaugeVec
	offerExpired                    prometheus.Counter
}

// NewWorkerMetrics registers the worker-side queue metrics on reg.
func NewWorkerMetrics(reg prometheus.Registerer) (*WorkerMetrics, error) {
	gauge := func(name, help string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, []string{"event"})
	}
	m := &WorkerMetrics{
		waiting: gauge("queue_waiting", "Tickets not yet offered (before T0: lottery entries)."),
		cursor:  gauge("queue_cursor", "Highest ticket offered."),
		offers:  gauge("queue_offers", "Outstanding (unclaimed) offers."),
		active:  gauge("queue_active", "Admitted users."),
		offerExpired: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "queue_offer_expired_total",
			Help: "Offers that passed their claim deadline unclaimed (dropped by queue_tick).",
		}),
	}
	for _, c := range []prometheus.Collector{m.waiting, m.cursor, m.offers, m.active, m.offerExpired} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("queue: register worker metrics: %w", err)
		}
	}
	return m, nil
}

func (m *WorkerMetrics) observe(eid uuid.UUID, d Dump) {
	if m == nil || !d.Init {
		return
	}
	ev := eid.String()
	m.waiting.WithLabelValues(ev).Set(float64(d.Waiting()))
	m.cursor.WithLabelValues(ev).Set(float64(d.Cursor))
	m.offers.WithLabelValues(ev).Set(float64(len(d.Offers)))
	m.active.WithLabelValues(ev).Set(float64(len(d.Active)))
}

func (m *WorkerMetrics) expired(n int64) {
	if m != nil && n > 0 {
		m.offerExpired.Add(float64(n))
	}
}

// randScore returns a uniform lottery score in [0, 1) with 53 random bits,
// formatted exactly (Redis parses it with strtod).
func randScore(r io.Reader) (string, error) {
	var b [8]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", fmt.Errorf("queue: random score: %w", err)
	}
	f := float64(binary.BigEndian.Uint64(b[:])>>11) / (1 << 53)
	return strconv.FormatFloat(f, 'g', -1, 64), nil
}
