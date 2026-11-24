package ratelimit

import (
	"container/list"
	"encoding/binary"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// localShards spreads the status limiter over independent locks.
const localShards = 16

// LocalConfig configures the in-process limiter.
type LocalConfig struct {
	Rate     float64 // tokens per second
	Burst    int     // bucket size
	Capacity int     // max users tracked (across all shards); LRU beyond that
	Now      func() time.Time
}

// Local is an in-process token bucket per user, kept in a bounded LRU split
// into shards. Evicting a user only forgets their bucket (they start full),
// so memory stays bounded and the failure mode is lenient.
type Local struct {
	cfg    LocalConfig
	shards [localShards]localShard
}

type localShard struct {
	mu    sync.Mutex
	cap   int
	order *list.List // front = most recently used
	items map[uuid.UUID]*list.Element
}

type bucket struct {
	uid    uuid.UUID
	tokens float64
	last   time.Time
}

// NewLocal builds the in-process limiter.
func NewLocal(cfg LocalConfig) *Local {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	cfg.Capacity = max(cfg.Capacity, localShards)
	l := &Local{cfg: cfg}
	for i := range l.shards {
		l.shards[i] = localShard{cap: cfg.Capacity / localShards, order: list.New(), items: map[uuid.UUID]*list.Element{}}
	}
	return l
}

// Allow takes one token from uid's bucket. When the bucket is empty it
// returns false and how long until a token is available.
func (l *Local) Allow(uid uuid.UUID) (bool, time.Duration) {
	s := &l.shards[binary.BigEndian.Uint64(uid[8:])%localShards]
	now := l.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var b *bucket
	if e, ok := s.items[uid]; ok {
		s.order.MoveToFront(e)
		b, _ = e.Value.(*bucket)
	}
	if b == nil {
		if s.order.Len() >= s.cap {
			if old := s.order.Back(); old != nil {
				if ob, ok := old.Value.(*bucket); ok {
					delete(s.items, ob.uid)
				}
				s.order.Remove(old)
			}
		}
		b = &bucket{uid: uid, tokens: float64(l.cfg.Burst), last: now}
		s.items[uid] = s.order.PushFront(b)
	}
	if dt := now.Sub(b.last).Seconds(); dt > 0 {
		b.tokens = min(float64(l.cfg.Burst), b.tokens+dt*l.cfg.Rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.cfg.Rate * float64(time.Second))
}

// Len is the number of users tracked (for tests).
func (l *Local) Len() int {
	n := 0
	for i := range l.shards {
		s := &l.shards[i]
		s.mu.Lock()
		n += s.order.Len()
		s.mu.Unlock()
	}
	return n
}

// Middleware limits by verified user id; requests without one pass through
// (auth rejects them).
func (l *Local) Middleware(name string, user UserFunc, m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			uid, ok := user(r)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			if allowed, retry := l.Allow(uid); !allowed {
				m.count(name, "limited")
				writeLimited(w, r, decision{
					policy: name + "-user", quota: l.cfg.Burst, window: time.Second,
					reset: retry, retry: retry, limited: true,
				})
				return
			}
			m.count(name, "allowed")
			next.ServeHTTP(w, r)
		})
	}
}
