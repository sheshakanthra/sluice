package queue

import (
	"testing"
	"time"
)

// Rush config (deploy/env/rush.env): OFFER_CLAIM_TTL=30s,
// QUEUE_MAX_ADMIT_PER_TICK=200, QUEUE_TICK=100ms.
const (
	rushOfferTTL = 30 * time.Second
	rushPerTick  = 200
	rushTick     = 100 * time.Millisecond
)

func TestWaitingRetryIsPositionAwareAndBounded(t *testing.T) {
	tests := []struct {
		name     string
		position int64
		rate     float64
		u        float64
		want     time.Duration
	}{
		// Near the front: eta/4 under the 1 s floor.
		{"front, floor", 1, 50, 0.5, time.Second},
		{"front, jitter low", 1, 50, 0, 800 * time.Millisecond},
		{"front, jitter high", 1, 50, 1, 1200 * time.Millisecond},
		// Mid queue: eta 400 s / 4 = 100 s -> 60 s cap, but the cursor could
		// reach position 2000 in 9 ticks: (0.9 s + 15 s) / 1.1.
		{"mid, cursor bound", 2000, 5, 0.5, div11(15900 * time.Millisecond)},
		// Far back: eta/4 capped at 60 s, safe bound 229 ticks: (22.9 s + 15 s) / 1.1.
		{"far, cursor bound", 46000, 5, 0.5, div11(37900 * time.Millisecond)},
		// Very far back: the 60 s hard cap wins.
		{"very far, hard cap", 500000, 5, 0.5, 60 * time.Second},
		{"very far, jitter", 500000, 5, 0, 48 * time.Second},
		// Fast queue: eta = 1000/100 = 10 s, /4 = 2.5 s.
		{"fast queue", 1000, 100, 0.5, 2500 * time.Millisecond},
		// Unknown rate (0) counts as 1/s.
		{"zero rate", 8, 0, 0.5, 2 * time.Second},
		{"negative position", -3, 5, 0.5, time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := waitingRetry(tt.position, tt.rate, tt.u, rushOfferTTL, rushPerTick, rushTick)
			if got != tt.want {
				t.Fatalf("waitingRetry(%d, %v, %v) = %v, want %v", tt.position, tt.rate, tt.u, got, tt.want)
			}
		})
	}
}

// The core safety property: whatever the rate estimate says, a user told to
// wait retry_after (plus up to 10% client jitter, API.md §3.3) polls again
// before an offer made at the earliest possible moment expires.
func TestWaitingRetryNeverSkipsAnOffer(t *testing.T) {
	for _, offerTTL := range []time.Duration{2 * time.Second, 30 * time.Second, time.Minute} {
		for _, tick := range []time.Duration{10 * time.Millisecond, 100 * time.Millisecond, time.Second} {
			for _, perTick := range []int64{1, 7, 200, 1000} {
				for _, rate := range []float64{0, 0.1, 1, 50, 5000} {
					for position := int64(1); position < 300_000; position = position*3 + 1 {
						for _, u := range []float64{0, 0.5, 1} {
							wait := waitingRetry(position, rate, u, offerTTL, perTick, tick)
							// Earliest offer: ceil(position/perTick)-1 ticks from now.
							earliest := time.Duration((position-1)/perTick) * tick
							if client := wait * 11 / 10; client > earliest+offerTTL {
								t.Fatalf("ttl %v tick %v perTick %d rate %v pos %d u %v: wait %v (client %v) misses an offer at %v + %v",
									offerTTL, tick, perTick, rate, position, u, wait, client, earliest, offerTTL)
							}
							if wait > maxWaitingRetry*12/10 || wait <= 0 {
								t.Fatalf("wait %v out of bounds", wait)
							}
						}
					}
				}
			}
		}
	}
}

func TestWaitingRetryGrowsWithPosition(t *testing.T) {
	prev := time.Duration(0)
	for _, p := range []int64{1, 10, 100, 1000, 10_000, 100_000} {
		got := waitingRetry(p, 5, 0.5, rushOfferTTL, rushPerTick, rushTick)
		if got < prev {
			t.Fatalf("position %d: %v < %v (farther back must not poll faster)", p, got, prev)
		}
		prev = got
	}
}

func TestPrequeueRetryJitteredAndSpreadOverT0(t *testing.T) {
	spread := 5 * time.Second
	tests := []struct {
		name    string
		untilT0 time.Duration
		u       float64
		want    time.Duration
	}{
		{"far, low", 60 * time.Second, 0, 4 * time.Second},
		{"far, high", 60 * time.Second, 1, 6 * time.Second},
		{"10 s out", 10 * time.Second, 0.5, 5 * time.Second},
		{"6 s out", 6 * time.Second, 0, 2400 * time.Millisecond},
		// Within 5 s: land in [T0, T0 + spread].
		{"5 s out, low", 5 * time.Second, 0, 5 * time.Second},
		{"5 s out, high", 5 * time.Second, 1, 10 * time.Second},
		{"300 ms out", 300 * time.Millisecond, 0.5, 2800 * time.Millisecond},
		// After T0, not frozen yet.
		{"at T0", 0, 0, 500 * time.Millisecond},
		{"after T0", -2 * time.Second, 1, 1500 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := prequeueRetry(tt.untilT0, tt.u, spread); got != tt.want {
				t.Fatalf("prequeueRetry(%v, %v) = %v, want %v", tt.untilT0, tt.u, got, tt.want)
			}
		})
	}
}

// The T0 herd: users whose last pre-queue poll falls in the final 5 s must
// come back spread over [T0, T0 + spread], never all before T0 or at once.
func TestPrequeueRetryDesynchronisesT0(t *testing.T) {
	spread := 5 * time.Second
	buckets := make([]int, 5)
	const n = 10_000
	for i := range n {
		untilT0 := time.Duration(i%5000) * time.Millisecond // polls in the last 5 s
		u := float64(i) / n
		at := prequeueRetry(untilT0, u, spread) - untilT0 // when the next poll lands, relative to T0
		if at < 0 || at > spread {
			t.Fatalf("untilT0 %v u %v: next poll at T0%+v, want within [0, %v]", untilT0, u, at, spread)
		}
		buckets[min(int(at/time.Second), 4)]++
	}
	for i, b := range buckets {
		if b < n/5*8/10 || b > n/5*12/10 {
			t.Fatalf("second %d after T0 got %d of %d polls, want about %d: %v", i, b, n, n/5, buckets)
		}
	}
}

func TestNewServiceRequiresPacing(t *testing.T) {
	base := Options{AdmissionTTL: time.Minute, Overadmit: 1.5, OfferTTL: time.Minute, MaxAdmitPerTick: 200, Tick: 100 * time.Millisecond}
	for name, mut := range map[string]func(*Options){
		"no offer ttl": func(o *Options) { o.OfferTTL = 0 },
		"no per tick":  func(o *Options) { o.MaxAdmitPerTick = 0 },
		"no tick":      func(o *Options) { o.Tick = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			o := base
			mut(&o)
			if err := o.validate(); err == nil {
				t.Fatal("want an error")
			}
		})
	}
	if err := base.validate(); err != nil {
		t.Fatalf("valid options: %v", err)
	}
}

// div11 is d / clientJitter, computed the way safeWait does.
func div11(d time.Duration) time.Duration {
	f := float64(d)
	return time.Duration(f / clientJitter)
}
