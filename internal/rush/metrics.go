package rush

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
)

// route identifies one API call for per-route histograms.
type route int

const (
	rSession route = iota
	rJoin
	rStatus
	rLeave
	rAvail
	rHold
	rOrder
	rPay
	rPayments
	rConfirm
	rRelease
	numRoutes
)

var routeNames = [numRoutes]string{"session", "join", "status", "leave", "availability", "hold", "order", "pay", "payments", "confirm", "release"}

func (r route) String() string { return routeNames[r] }

// isCheckout: SPEC §6 checkout p95 = holds + orders + pay + confirm.
func (r route) isCheckout() bool { return r == rHold || r == rOrder || r == rPay || r == rConfirm }

// Histogram range: 1 µs .. 60 s at 3 significant digits (LOADTEST §3.4).
const (
	histMinUS = 1
	histMaxUS = 60_000_000
	histSig   = 3
	secSig    = 2 // per-second histograms only feed the time series
)

// outcome classifies one finished request.
type outcome int

const (
	okResp    outcome = iota // 2xx/3xx/4xx other than the business codes
	business                 // 409, 422, 429: expected rush answers
	serverErr                // 5xx
	timedOut                 // client timeout
	transport                // connection error
)

// hist is a mutex-guarded HdrHistogram (the library is not goroutine-safe).
type hist struct {
	mu sync.Mutex
	h  *hdrhistogram.Histogram
}

func newHist(sig int) *hist { return &hist{h: hdrhistogram.New(histMinUS, histMaxUS, sig)} }

func (h *hist) record(d time.Duration) {
	us := min(max(d.Microseconds(), histMinUS), histMaxUS)
	h.mu.Lock()
	_ = h.h.RecordValue(us) // in range by construction
	h.mu.Unlock()
}

// Quantiles is a latency summary in milliseconds.
type Quantiles struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
	N   int64   `json:"n"`
}

func (h *hist) quantiles() Quantiles {
	h.mu.Lock()
	defer h.mu.Unlock()
	return Quantiles{
		P50: usToMS(h.h.ValueAtQuantile(50)),
		P95: usToMS(h.h.ValueAtQuantile(95)),
		P99: usToMS(h.h.ValueAtQuantile(99)),
		Max: usToMS(h.h.Max()),
		N:   h.h.TotalCount(),
	}
}

func usToMS(us int64) float64 { return math.Round(float64(us)/100) / 10 } // 0.1 ms precision

// second is one 1 s bucket of the time series (keyed by completion time).
type second struct {
	n        [numRoutes]atomic.Int64
	errors   atomic.Int64
	once     sync.Once
	status   *hist
	checkout *hist
}

func (s *second) hists() (status, checkout *hist) {
	s.once.Do(func() {
		s.status = newHist(secSig)
		s.checkout = newHist(secSig)
	})
	return s.status, s.checkout
}

// Recorder collects every client-side measurement of a run.
type Recorder struct {
	base     time.Time // second 0 of the time series
	routes   [numRoutes]*hist
	checkout *hist
	lag      *hist // schedule lag: actual - intended for every scheduled action
	secs     []second

	requests, s5xx, timeouts, transportErrs atomic.Int64
	b409, b422, b429                        atomic.Int64
}

// NewRecorder covers [base, base+span) with 1 s buckets; later samples land in the last bucket.
func NewRecorder(base time.Time, span time.Duration) *Recorder {
	r := &Recorder{
		base:     base,
		checkout: newHist(histSig),
		lag:      newHist(histSig),
		secs:     make([]second, int(span/time.Second)+1),
	}
	for i := range r.routes {
		r.routes[i] = newHist(histSig)
	}
	return r
}

func (r *Recorder) bucket(t time.Time) *second {
	i := int(t.Sub(r.base) / time.Second)
	return &r.secs[min(max(i, 0), len(r.secs)-1)]
}

// Record adds one finished request. Latency runs from the intended start
// (coordinated-omission safe); timeouts and errors are recorded at their
// elapsed time and also counted separately.
func (r *Recorder) Record(rt route, intended, end time.Time, status int, oc outcome) {
	lat := end.Sub(intended)
	r.routes[rt].record(lat)
	if rt.isCheckout() {
		r.checkout.record(lat)
	}
	r.requests.Add(1)
	s := r.bucket(end)
	s.n[rt].Add(1)
	st, co := s.hists()
	if rt == rStatus {
		st.record(lat)
	}
	if rt.isCheckout() {
		co.record(lat)
	}
	switch oc {
	case serverErr:
		r.s5xx.Add(1)
		s.errors.Add(1)
	case timedOut:
		r.timeouts.Add(1)
		s.errors.Add(1)
	case transport:
		r.transportErrs.Add(1)
		s.errors.Add(1)
	case business:
		switch status {
		case 409:
			r.b409.Add(1)
		case 422:
			r.b422.Add(1)
		case 429:
			r.b429.Add(1)
		}
	case okResp:
	}
}

// RecordLag adds one schedule-lag sample (how late a scheduled action ran).
func (r *Recorder) RecordLag(d time.Duration) { r.lag.record(max(d, 0)) }

// classify maps an HTTP status to an outcome.
func classify(status int) outcome {
	switch {
	case status >= 500:
		return serverErr
	case status == 409 || status == 422 || status == 429:
		return business
	default:
		return okResp
	}
}
