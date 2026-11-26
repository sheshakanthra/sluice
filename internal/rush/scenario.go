package rush

import (
	"math/rand/v2"
	"time"
)

// Scenario is a preset behaviour mix and mode (LOADTEST §4).
type Scenario struct {
	Name  string
	Users int
	// Direct skips the waiting room: holds and orders use the session token
	// (the server must run with QUEUE_ENFORCED=false).
	Direct bool
	Mix    Mix
}

// Mix is the per-user behaviour distribution (LOADTEST §3.3).
type Mix struct {
	Qty          []weighted // quantity → share
	PickShare    float64    // pick seats from the bitmap (else best-available)
	ThinkAdmit   [2]time.Duration
	ThinkHold    [2]time.Duration
	Abandon      float64
	PMDecline    float64
	PMTimeout    float64
	DoubleSubmit float64 // /pay twice concurrently with the same IK
	Rogue        float64 // one extra /pay with a new IK
	NewAttempt   float64 // after a decline, try again with pm_ok
}

type weighted struct {
	v     int
	share float64
}

// HeadlineMix is scenario H's mix; the other presets reuse it.
var HeadlineMix = Mix{
	Qty:          []weighted{{1, 0.80}, {2, 0.15}, {4, 0.05}},
	PickShare:    0.30,
	ThinkAdmit:   [2]time.Duration{1 * time.Second, 4 * time.Second},
	ThinkHold:    [2]time.Duration{2 * time.Second, 6 * time.Second},
	Abandon:      0.08,
	PMDecline:    0.03,
	PMTimeout:    0.02,
	DoubleSubmit: 0.05,
	Rogue:        0.01,
	NewAttempt:   0.70,
}

// Scenarios are the --scenario presets. B0/B1/B2 differ only on the server
// side (BASELINE, QUEUE_ENFORCED); the swarm just goes direct.
var Scenarios = map[string]Scenario{
	"H":     {Name: "H", Users: 50_000, Mix: HeadlineMix},
	"SMOKE": {Name: "SMOKE", Users: 5_000, Mix: HeadlineMix},
	"B0":    {Name: "B0", Users: 10_000, Direct: true, Mix: HeadlineMix},
	"B1":    {Name: "B1", Users: 10_000, Direct: true, Mix: HeadlineMix},
	"B2":    {Name: "B2", Users: 10_000, Direct: true, Mix: HeadlineMix},
}

// Payment methods (API.md §3.6).
const (
	pmOK      = "pm_ok"
	pmDecline = "pm_decline"
	pmTimeout = "pm_timeout"
)

// plan is one user's pre-drawn behaviour, fixed by --seed and the user index.
type plan struct {
	idx        int
	arriveAt   time.Duration // relative to T0
	qty        int
	pick       bool
	thinkAdmit time.Duration
	thinkHold  time.Duration
	abandon    bool
	pm         string
	double     bool
	rogue      bool
	newAttempt bool
	rng        *rand.Rand // per-user stream for in-journey jitter
}

// makePlans draws every user's behaviour up front, so a seed reproduces the
// whole mix regardless of goroutine scheduling.
func makePlans(o *Options) []plan {
	plans := make([]plan, o.Users)
	// Window per user by cumulative share of the index range.
	bounds := make([]int, len(o.Arrival))
	acc := 0.0
	for i, w := range o.Arrival {
		acc += w.Share
		bounds[i] = int(acc*float64(o.Users) + 0.5)
	}
	bounds[len(bounds)-1] = o.Users
	m := o.Scenario.Mix
	wi := 0
	for i := range plans {
		for i >= bounds[wi] {
			wi++
		}
		w := o.Arrival[wi]
		r := rand.New(rand.NewPCG(o.Seed, uint64(i))) //nolint:gosec // G404: load-test behaviour mix, not security
		p := plan{idx: i, rng: r}
		p.arriveAt = w.From + time.Duration(r.Int64N(int64(w.To-w.From)))
		p.qty = pickWeighted(r, m.Qty)
		p.pick = r.Float64() < m.PickShare
		p.thinkAdmit = uniform(r, m.ThinkAdmit)
		p.thinkHold = uniform(r, m.ThinkHold)
		p.abandon = r.Float64() < m.Abandon
		switch x := r.Float64(); {
		case x < m.PMDecline:
			p.pm = pmDecline
		case x < m.PMDecline+m.PMTimeout:
			p.pm = pmTimeout
		default:
			p.pm = pmOK
		}
		p.double = r.Float64() < m.DoubleSubmit
		p.rogue = r.Float64() < m.Rogue
		p.newAttempt = r.Float64() < m.NewAttempt
		plans[i] = p
	}
	return plans
}

func pickWeighted(r *rand.Rand, ws []weighted) int {
	x := r.Float64()
	for _, w := range ws {
		if x < w.share {
			return w.v
		}
		x -= w.share
	}
	return ws[len(ws)-1].v
}

func uniform(r *rand.Rand, span [2]time.Duration) time.Duration {
	if span[1] <= span[0] {
		return span[0]
	}
	return span[0] + time.Duration(r.Int64N(int64(span[1]-span[0])))
}

// stretch returns d x U(1, 1+frac): jitter that never comes back sooner.
func stretch(r *rand.Rand, d time.Duration, frac float64) time.Duration {
	return time.Duration(float64(d) * (1 + frac*r.Float64()))
}

// jitter returns d ± frac·d.
func jitter(r *rand.Rand, d time.Duration, frac float64) time.Duration {
	return time.Duration(float64(d) * (1 - frac + 2*frac*r.Float64()))
}
