// Package ratelimit is SLUICE's per-route rate limiting (API.md §2, P4):
//
//   - GCRA: a shared limiter in Redis (go-redis/redis_rate), keyed by client
//     IP and/or verified user id, for sessions, join, holds, orders, pay and
//     confirm.
//   - Local: an in-process token bucket per user for queue status (3/s,
//     burst 6), held in a bounded, sharded LRU, so the hot polling path never
//     touches Redis.
//   - ClientIP: the client address, honouring X-Forwarded-For only when the
//     connection comes from a TRUSTED_PROXIES CIDR.
//
// Both limiters answer 429 RATE_LIMITED with Retry-After plus the IETF
// draft RateLimit-Policy / RateLimit fields.
package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis_rate/v10"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/httpx"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

// redisTimeout bounds one GCRA call. On timeout or error the request is let
// through (fail open): the limiter protects capacity, it is not a
// correctness mechanism, and the routes behind it fail on their own if Redis
// is really down.
const redisTimeout = 250 * time.Millisecond

// Subject is what a rule counts by.
type Subject int

// Subjects.
const (
	ByIP Subject = iota
	ByUser
)

func (s Subject) String() string {
	if s == ByUser {
		return "user"
	}
	return "ip"
}

// Rule is one limit on one route. Rules of the same Name share a bucket.
type Rule struct {
	Name  string
	By    Subject
	Limit redis_rate.Limit
}

// PerMinute builds a rule of n requests per minute with burst n.
func PerMinute(name string, by Subject, n int) Rule {
	return Rule{Name: name, By: by, Limit: redis_rate.PerMinute(n)}
}

// UserFunc returns the verified user id of a request, if it carries a valid
// token. Requests without one skip ByUser rules (auth rejects them later).
type UserFunc func(r *http.Request) (uuid.UUID, bool)

// Metrics count limiter decisions.
type Metrics struct {
	decisions *prometheus.CounterVec
}

// NewMetrics registers ratelimit_decisions_total{rule,result}.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ratelimit_decisions_total",
		Help: "Rate-limit decisions by rule and result (allowed, limited, error = failed open).",
	}, []string{"rule", "result"})}
	if err := reg.Register(m.decisions); err != nil {
		return nil, fmt.Errorf("ratelimit: register metrics: %w", err)
	}
	return m, nil
}

func (m *Metrics) count(rule, result string) {
	if m != nil {
		m.decisions.WithLabelValues(rule, result).Inc()
	}
}

// GCRA is the Redis-backed limiter.
type GCRA struct {
	lim     *redis_rate.Limiter
	trusted []netip.Prefix
	user    UserFunc
	log     *slog.Logger
	metrics *Metrics
}

// NewGCRA builds the Redis limiter. user, log and m may be nil.
func NewGCRA(rdb *redis.Client, trusted []netip.Prefix, user UserFunc, log *slog.Logger, m *Metrics) *GCRA {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &GCRA{lim: redis_rate.NewLimiter(rdb), trusted: trusted, user: user, log: log, metrics: m}
}

// Middleware enforces rules in order. The first rule that is exceeded
// answers 429; otherwise the most constrained rule's fields are set on the
// response.
func (g *GCRA) Middleware(rules ...Rule) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var best *decision
			for _, rule := range rules {
				subject, ok := g.subject(r, rule.By)
				if !ok {
					continue
				}
				d, ok := g.allow(r.Context(), rule, subject)
				if !ok {
					continue
				}
				if d.limited {
					writeLimited(w, r, d)
					return
				}
				if best == nil || d.remaining < best.remaining {
					best = &d
				}
			}
			if best != nil {
				setFields(w.Header(), *best)
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (g *GCRA) subject(r *http.Request, by Subject) (string, bool) {
	if by == ByUser {
		if g.user == nil {
			return "", false
		}
		uid, ok := g.user(r)
		if !ok {
			return "", false
		}
		return uid.String(), true
	}
	ip := ClientIP(r, g.trusted)
	if !ip.IsValid() {
		return "", false
	}
	return ip.String(), true
}

// allow runs one GCRA step. ok is false when Redis failed (fail open).
func (g *GCRA) allow(ctx context.Context, rule Rule, subject string) (decision, bool) {
	ctx, cancel := context.WithTimeout(ctx, redisTimeout)
	defer cancel()
	key := keys.RateLimitPrefix + rule.Name + ":" + rule.By.String() + ":" + subject
	res, err := g.lim.Allow(ctx, key, rule.Limit)
	if err != nil {
		g.metrics.count(rule.Name, "error")
		g.log.WarnContext(ctx, "rate limit check failed open", slog.String("rule", rule.Name),
			slog.String("error", err.Error()))
		return decision{}, false
	}
	d := decision{
		policy: rule.Name + "-" + rule.By.String(), quota: rule.Limit.Burst, window: rule.Limit.Period,
		remaining: res.Remaining, reset: res.ResetAfter, retry: res.RetryAfter, limited: res.Allowed == 0,
	}
	if d.limited {
		g.metrics.count(rule.Name, "limited")
	} else {
		g.metrics.count(rule.Name, "allowed")
	}
	return d, true
}

// decision is one limiter answer, in the units the response fields use.
type decision struct {
	policy    string
	quota     int
	window    time.Duration
	remaining int
	reset     time.Duration
	retry     time.Duration
	limited   bool
}

// setFields writes the IETF draft (draft-ietf-httpapi-ratelimit-headers)
// RateLimit-Policy and RateLimit fields.
func setFields(h http.Header, d decision) {
	h.Set("RateLimit-Policy", fmt.Sprintf("%q;q=%d;w=%d", d.policy, d.quota, ceilSeconds(d.window)))
	h.Set("RateLimit", fmt.Sprintf("%q;r=%d;t=%d", d.policy, max(d.remaining, 0), ceilSeconds(d.reset)))
}

func writeLimited(w http.ResponseWriter, r *http.Request, d decision) {
	setFields(w.Header(), d)
	retry := time.Duration(ceilSeconds(d.retry)) * time.Second
	httpx.WriteProblem(w, r, httpx.NewProblem(httpx.CodeRateLimited,
		"rate limit "+d.policy+" exceeded; retry in "+strconv.Itoa(ceilSeconds(d.retry))+" s").WithRetryAfter(retry))
}

// ceilSeconds rounds d up to whole seconds, at least 1.
func ceilSeconds(d time.Duration) int {
	return max(1, int(math.Ceil(d.Seconds())))
}

// ClientIP returns the client's address. X-Forwarded-For is honoured only
// when the direct peer is a trusted proxy; then the right-most entry that is
// not itself a trusted proxy is the client (entries to its left are
// client-supplied and could be forged). An invalid address means unknown.
func ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	addr := peer.Addr().Unmap()
	if !inAny(addr, trusted) {
		return addr
	}
	hops := r.Header.Values("X-Forwarded-For")
	for i := len(hops) - 1; i >= 0; i-- {
		parts := strings.Split(hops[i], ",")
		for j := len(parts) - 1; j >= 0; j-- {
			a, err := netip.ParseAddr(strings.TrimSpace(parts[j]))
			if err != nil {
				return addr // malformed hop: stop trusting the chain here
			}
			a = a.Unmap()
			if !inAny(a, trusted) {
				return a
			}
			addr = a
		}
	}
	return addr
}

func inAny(a netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
