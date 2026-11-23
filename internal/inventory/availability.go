package inventory

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// Seat-map codes, 2 bits per seat (API.md §3.8).
const (
	codeAvailable byte = 0b00
	codeHeld      byte = 0b01
	codeSold      byte = 0b10
)

// Counts are seat counts by state.
type Counts struct {
	Available int `json:"available"`
	Held      int `json:"held"`
	Sold      int `json:"sold"`
	Capacity  int `json:"capacity"`
}

// Availability is one seat-map snapshot (GET /events/{id}/availability).
type Availability struct {
	Version int64  `json:"version"`
	Counts  Counts `json:"counts"`
	// States is base64 (std, padded) of 2 bits per seat_no, packed MSB
	// first: seat i lives in byte i/4 at bit offset 6-2*(i%4).
	States string `json:"states"`
	// ETag is the strong entity tag: "r<ver>" (Redis) or "p<ver>" (Postgres).
	ETag string `json:"-"`
}

// AvailabilityCache is a per-replica cache of seat maps. Each event is
// re-checked at most once per minAge; a refresh first reads only `ver` and
// rebuilds the bitmap only when it changed, so N replicas cost Redis at most
// N reads per event per minAge. With HOLD_PATH=pg_only (or while Redis is not
// loaded) the source is Postgres.
type AvailabilityCache struct {
	svc    *Service
	minAge time.Duration

	mu sync.Mutex
	m  map[uuid.UUID]*availEntry
}

type availEntry struct {
	mu      sync.Mutex
	checked time.Time
	a       *Availability
}

// NewAvailabilityCache returns a cache refreshing each event at most every
// minAge (100 ms in production).
func (s *Service) NewAvailabilityCache(minAge time.Duration) *AvailabilityCache {
	return &AvailabilityCache{svc: s, minAge: minAge, m: map[uuid.UUID]*availEntry{}}
}

// Get returns the event's seat map, refreshing it if older than minAge.
// Concurrent callers for one event share a single refresh.
func (c *AvailabilityCache) Get(ctx context.Context, ev *EventInfo) (*Availability, error) {
	c.mu.Lock()
	e, ok := c.m[ev.ID]
	if !ok {
		e = &availEntry{}
		c.m[ev.ID] = e
	}
	c.mu.Unlock()

	e.mu.Lock()
	defer e.mu.Unlock()
	now := c.svc.now()
	if e.a != nil && now.Sub(e.checked) < c.minAge {
		return e.a, nil
	}
	a, err := c.load(ctx, ev, e.a)
	if err != nil {
		return nil, err
	}
	e.a, e.checked = a, now
	if m := c.svc.metrics; m != nil {
		m.seats.WithLabelValues(ev.Slug, "available").Set(float64(a.Counts.Available))
		m.seats.WithLabelValues(ev.Slug, "held").Set(float64(a.Counts.Held))
		m.seats.WithLabelValues(ev.Slug, "sold").Set(float64(a.Counts.Sold))
	}
	return a, nil
}

// LiveCounts returns the cached counts for an event (for GET /events).
func (c *AvailabilityCache) LiveCounts(ctx context.Context, eventID uuid.UUID) (Counts, error) {
	ev, err := c.svc.events.get(ctx, eventID.String())
	if err != nil {
		return Counts{}, err
	}
	a, err := c.Get(ctx, ev)
	if err != nil {
		return Counts{}, err
	}
	return a.Counts, nil
}

func (c *AvailabilityCache) load(ctx context.Context, ev *EventInfo, prev *Availability) (*Availability, error) {
	if !c.svc.pgOnly {
		a, err := c.loadRedis(ctx, ev, prev)
		if !errors.Is(err, ErrNotLoaded) {
			return a, err
		}
	}
	return c.loadPG(ctx, ev)
}

func (c *AvailabilityCache) loadRedis(ctx context.Context, ev *EventInfo, prev *Availability) (*Availability, error) {
	rdb := c.svc.rdb
	ver, err := rdb.Get(ctx, keys.Ver(ev.ID)).Int64()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotLoaded
	}
	if err != nil {
		return nil, fmt.Errorf("availability ver: %w", err)
	}
	if prev != nil && prev.ETag == redisETag(ver) {
		return prev, nil
	}
	var (
		verCmd    *redis.StringCmd
		availCmd  *redis.StringSliceCmd
		soldCmd   *redis.StringSliceCmd
		loadedCmd *redis.StringCmd
	)
	// MULTI/EXEC: one consistent read of the version and both sets.
	if _, err := rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		verCmd = p.Get(ctx, keys.Ver(ev.ID))
		availCmd = p.SMembers(ctx, keys.Avail(ev.ID))
		soldCmd = p.SMembers(ctx, keys.Sold(ev.ID))
		loadedCmd = p.HGet(ctx, keys.Meta(ev.ID), "loaded")
		return nil
	}); err != nil && !errors.Is(err, redis.Nil) {
		return nil, fmt.Errorf("availability read: %w", err)
	}
	if loadedCmd.Val() != "1" {
		return nil, ErrNotLoaded
	}
	if ver, err = verCmd.Int64(); err != nil {
		return nil, fmt.Errorf("availability ver: %w", err)
	}
	n := int(ev.Capacity)
	bits := make([]byte, (n+3)/4)
	for i := range n {
		setCode(bits, i, codeHeld)
	}
	cnt := Counts{Capacity: n}
	for _, s := range availCmd.Val() {
		if i, err := strconv.Atoi(s); err == nil && i >= 0 && i < n {
			setCode(bits, i, codeAvailable)
			cnt.Available++
		}
	}
	for _, s := range soldCmd.Val() {
		if i, err := strconv.Atoi(s); err == nil && i >= 0 && i < n {
			setCode(bits, i, codeSold)
			cnt.Sold++
		}
	}
	cnt.Held = n - cnt.Available - cnt.Sold
	return &Availability{Version: ver, Counts: cnt, States: base64.StdEncoding.EncodeToString(bits), ETag: redisETag(ver)}, nil
}

func (c *AvailabilityCache) loadPG(ctx context.Context, ev *EventInfo) (*Availability, error) {
	rows, err := c.svc.q.ListSeatStates(ctx, ev.ID)
	if err != nil {
		return nil, fmt.Errorf("availability seats: %w", err)
	}
	n := int(ev.Capacity)
	bits := make([]byte, (n+3)/4)
	cnt := Counts{Capacity: n}
	var ver int64
	for _, r := range rows {
		ver = r.Ver
		i := int(r.SeatNo)
		if i >= n {
			continue
		}
		switch r.State {
		case gen.SeatStateAVAILABLE:
			cnt.Available++
		case gen.SeatStateHELD:
			setCode(bits, i, codeHeld)
			cnt.Held++
		case gen.SeatStateSOLD:
			setCode(bits, i, codeSold)
			cnt.Sold++
		}
	}
	return &Availability{Version: ver, Counts: cnt, States: base64.StdEncoding.EncodeToString(bits),
		ETag: `"p` + strconv.FormatInt(ver, 10) + `"`}, nil
}

func redisETag(ver int64) string { return `"r` + strconv.FormatInt(ver, 10) + `"` }

func setCode(bits []byte, i int, code byte) {
	shift := 6 - 2*(i%4)
	bits[i/4] = bits[i/4]&^(0b11<<shift) | code<<shift
}

// SeatCode decodes seat i from a States bitmap (used by tests and clients).
func SeatCode(bits []byte, i int) byte { return bits[i/4] >> (6 - 2*(i%4)) & 0b11 }
