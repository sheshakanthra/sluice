package httpx

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/sheshakanthra/sluice/internal/obs"
)

// MaxBodyBytes caps every API request body (API.md §1: 64 KiB).
const MaxBodyBytes = 64 << 10

// BodyLimit caps request bodies at n bytes. A handler that reads past the
// cap gets an *http.MaxBytesError and answers 400 VALIDATION.
func BodyLimit(n int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil && r.Body != http.NoBody {
				r.Body = http.MaxBytesReader(w, r.Body, n)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// probePaths are never shed: shedding them would turn a busy replica into
// an "unhealthy" one, which is exactly the P5b cascade (ADR 0013).
var probePaths = map[string]bool{
	"/healthz": true, "/readyz": true, "/metrics": true, "/api/v1/healthz": true,
}

// ShedMetrics counts requests refused by Shed.
type ShedMetrics struct{ shed *prometheus.CounterVec }

// NewShedMetrics registers sluice_http_shed_total{method}.
func NewShedMetrics(reg prometheus.Registerer, server string) *ShedMetrics {
	m := &ShedMetrics{shed: prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: obs.Namespace, Subsystem: "http", Name: "shed_total",
		Help:        "Requests refused with 503 OVERLOADED because the replica was at its in-flight write limit.",
		ConstLabels: prometheus.Labels{"server": server},
	}, []string{"method"})}
	reg.MustRegister(m.shed)
	return m
}

// shedExempt: reads are cheap (Redis, or short-lived caches) and the queue
// status poll is the waiting room's heartbeat, so only writes (POST,
// DELETE, PUT, PATCH), which hold Postgres connections, count against
// the limit. Probes and /metrics always pass. P7a SMOKE showed that
// counting reads shed status polls at the sale-open burst (ADR 0013).
func shedExempt(r *http.Request) bool {
	if probePaths[r.URL.Path] {
		return true
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// Shed bounds the write requests a replica serves at once (ADR 0013). A request
// that finds no free slot within wait gets 503 OVERLOADED with
// Retry-After: 1, so a saturated replica answers fast instead of queueing
// on its Postgres pool until the request deadline. maxInFlight <= 0
// disables shedding. Reads, probes and metrics always pass (shedExempt).
func Shed(maxInFlight int, wait time.Duration, m *ShedMetrics) Middleware {
	return func(next http.Handler) http.Handler {
		if maxInFlight <= 0 {
			return next
		}
		slots := make(chan struct{}, maxInFlight)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if shedExempt(r) {
				next.ServeHTTP(w, r)
				return
			}
			select {
			case slots <- struct{}{}:
			default:
				if !acquireWithin(r, slots, wait) {
					if m != nil {
						m.shed.WithLabelValues(normMethod(r.Method)).Inc()
					}
					WriteProblem(w, r, NewProblem(CodeOverloaded, "this replica is at capacity").WithRetryAfter(time.Second))
					return
				}
			}
			defer func() { <-slots }()
			next.ServeHTTP(w, r)
		})
	}
}

func acquireWithin(r *http.Request, slots chan struct{}, wait time.Duration) bool {
	if wait <= 0 {
		return false
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case slots <- struct{}{}:
		return true
	case <-t.C:
		return false
	case <-r.Context().Done():
		return false
	}
}
