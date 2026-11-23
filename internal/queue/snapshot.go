package queue

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/sheshakanthra/sluice/internal/redisx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// snapshotTimeout bounds one snapshot.lua call.
const snapshotTimeout = time.Second

// Snapshot is one consistent read of an event's queue and inventory
// counters (snapshot.lua). It is immutable once published.
type Snapshot struct {
	Cursor  int64 // highest ticket offered
	PreSize int64 // frozen pre-queue size; 0 until T0 is processed
	Tail    int64 // post-T0 joins
	SoldOut bool
	Avail   int64 // seats free to hold
	Holds   int64 // live holds (not seats)
	Sold    int64 // seats sold
	Ver     int64
	Rate    float64 // admit_rate_ema, admissions per second
	Offers  int64   // outstanding offers
	Active  int64   // admitted users
	At      time.Time
}

// Cache holds the latest Snapshot of every event this replica has served.
// Readers never block and never touch Redis once an event is warm: Get is a
// sync.Map lookup plus an atomic.Pointer load. Run refreshes every known
// event with one snapshot.lua call each per interval.
type Cache struct {
	sc      *redisx.Scripts
	now     func() time.Time
	log     *slog.Logger
	entries sync.Map // uuid.UUID -> *atomic.Pointer[Snapshot]
}

// NewCache builds a cache. now and log may be nil.
func NewCache(sc *redisx.Scripts, now func() time.Time, log *slog.Logger) *Cache {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Cache{sc: sc, now: now, log: log}
}

// Peek returns the cached snapshot of eid, or nil if the event is cold.
// It never calls Redis.
func (c *Cache) Peek(eid uuid.UUID) *Snapshot {
	v, ok := c.entries.Load(eid)
	if !ok {
		return nil
	}
	p, ok := v.(*atomic.Pointer[Snapshot])
	if !ok {
		return nil
	}
	return p.Load()
}

// Refresh reads eid's counters from Redis and publishes them.
func (c *Cache) Refresh(ctx context.Context, eid uuid.UUID) (*Snapshot, error) {
	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()
	r, err := c.sc.Run(ctx, redisx.Snapshot, keys.SnapshotKeys(eid))
	if err != nil {
		return nil, err
	}
	s, err := parseSnapshot(append([]string{r.Status}, r.Fields...), c.now())
	if err != nil {
		return nil, err
	}
	v, _ := c.entries.LoadOrStore(eid, new(atomic.Pointer[Snapshot]))
	if p, ok := v.(*atomic.Pointer[Snapshot]); ok {
		p.Store(s)
	}
	return s, nil
}

// Run refreshes every event the cache has seen, each interval, until ctx is
// done. A failed refresh keeps the previous snapshot (status keeps working
// on slightly stale numbers while Redis is briefly unavailable).
func (c *Cache) Run(ctx context.Context, interval time.Duration) error {
	t := time.NewTicker(interval)
	defer t.Stop()
	var lastErr time.Time
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		c.entries.Range(func(k, _ any) bool {
			eid, _ := k.(uuid.UUID)
			if _, err := c.Refresh(ctx, eid); err != nil && ctx.Err() == nil && time.Since(lastErr) > 5*time.Second {
				lastErr = time.Now()
				c.log.WarnContext(ctx, "queue snapshot refresh", slog.String("event_id", eid.String()),
					slog.String("error", err.Error()))
			}
			return ctx.Err() == nil
		})
	}
}

func parseSnapshot(v []string, at time.Time) (*Snapshot, error) {
	if len(v) != 11 {
		return nil, fmt.Errorf("queue: snapshot has %d fields, want 11", len(v))
	}
	n := make([]int64, len(v))
	for i, s := range v {
		if i == 8 { // rate is a float
			continue
		}
		x, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("queue: snapshot field %d: %w", i, err)
		}
		n[i] = x
	}
	rate, err := strconv.ParseFloat(v[8], 64)
	if err != nil {
		return nil, fmt.Errorf("queue: snapshot rate: %w", err)
	}
	return &Snapshot{
		Cursor: n[0], PreSize: n[1], Tail: n[2], SoldOut: n[3] == 1, Avail: n[4], Holds: n[5],
		Sold: n[6], Ver: n[7], Rate: rate, Offers: n[9], Active: n[10], At: at,
	}, nil
}
