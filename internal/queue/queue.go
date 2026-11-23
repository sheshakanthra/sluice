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

// Metrics are the queue's Prometheus metrics (the full set lands with the
// stats work; this is the hot-path counter the zero-Redis test relies on).
type Metrics struct {
	statusRedisCalls prometheus.Counter
}

// NewMetrics registers the queue metrics on reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		statusRedisCalls: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "queue_status_redis_calls_total",
			Help: "Redis calls made while answering GET queue/status (stays ~0 for WAITING users).",
		}),
	}
	if err := reg.Register(m.statusRedisCalls); err != nil {
		return nil, fmt.Errorf("queue: register metrics: %w", err)
	}
	return m, nil
}

func (m *Metrics) statusRedisCall() {
	if m != nil {
		m.statusRedisCalls.Inc()
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
