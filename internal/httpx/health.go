package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Check is one named readiness dependency (Postgres, Redis, ...).
type Check struct {
	Name string
	Ping func(ctx context.Context) error
}

// readyCheckTimeout bounds each dependency ping inside a readiness probe.
const readyCheckTimeout = 2 * time.Second

// DefaultReadyTTL is how often a readiness result is refreshed. Caddy and
// the Docker healthcheck each probe every 2 s per replica.
const DefaultReadyTTL = time.Second

// readyStaleAfter: a cached result older than this is not served; the caller
// probes synchronously instead. Probe rounds are bounded by
// readyCheckTimeout, so this only trips if refreshes stop entirely.
const readyStaleAfter = 10 * time.Second

// Readiness caches the result of the dependency checks (ADR 0013).
//
// /readyz must answer quickly even when the replica is saturated: in P5b a
// replica whose request pool was exhausted could not ping Postgres within
// Caddy's health_timeout, so Caddy ejected every replica and 49,105
// requests got 503 "no upstreams available". So:
//   - the checks run against dedicated health clients (a 2-connection pgx
//     pool, a 2-connection Redis client), never the request pools;
//   - once warm, State never waits for a probe: it serves the cached result
//     and, when that is older than ttl, starts one refresh in the background.
//
// A busy replica therefore stays in rotation and sheds load itself (Shed)
// instead of being ejected. ttl 0 disables caching (every call probes).
type Readiness struct {
	log    *slog.Logger
	checks []Check
	ttl    time.Duration
	now    func() time.Time

	probe    sync.Mutex // one probe at a time
	draining atomic.Bool

	mu      sync.Mutex
	at      time.Time
	results map[string]string
	ok      bool
}

// NewReadiness builds a cached readiness state. ttl 0 probes on every call.
func NewReadiness(log *slog.Logger, ttl time.Duration, checks ...Check) *Readiness {
	return &Readiness{log: log, checks: checks, ttl: ttl, now: time.Now}
}

// SetChecks replaces the checks (call before serving).
func (rd *Readiness) SetChecks(checks ...Check) {
	rd.probe.Lock()
	defer rd.probe.Unlock()
	rd.checks = checks
}

// SetDraining makes /readyz answer 503 from now on, so the load balancer
// stops routing here before the server closes its listener (graceful
// shutdown, P7).
func (rd *Readiness) SetDraining() { rd.draining.Store(true) }

// State returns the readiness result. Warm (a result younger than
// readyStaleAfter): it returns immediately, refreshing in the background
// once the result is older than ttl. Cold or stale: it probes synchronously,
// and concurrent callers share that single round.
func (rd *Readiness) State(ctx context.Context) (map[string]string, bool) {
	if rd.draining.Load() {
		return map[string]string{"shutdown": "draining"}, false
	}
	if rd.ttl > 0 {
		if res, ok, age, have := rd.cached(); have && age < readyStaleAfter {
			if age >= rd.ttl && rd.probe.TryLock() {
				go func() {
					defer rd.probe.Unlock()
					rd.refresh(context.WithoutCancel(ctx))
				}()
			}
			return res, ok
		}
	}
	rd.probe.Lock()
	defer rd.probe.Unlock()
	if rd.ttl > 0 {
		if res, ok, age, have := rd.cached(); have && age < rd.ttl { // another caller probed meanwhile
			return res, ok
		}
	}
	// The probe outlives a cancelled caller: its result serves the next one.
	return rd.refresh(context.WithoutCancel(ctx))
}

// refresh runs one probe round; the caller holds rd.probe.
func (rd *Readiness) refresh(ctx context.Context) (map[string]string, bool) {
	res, ok := runChecks(ctx, rd.log, rd.checks)
	rd.mu.Lock()
	rd.at, rd.results, rd.ok = rd.now(), res, ok
	rd.mu.Unlock()
	return res, ok
}

func (rd *Readiness) cached() (res map[string]string, ok bool, age time.Duration, have bool) {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	if rd.results == nil {
		return nil, false, 0, false
	}
	return rd.results, rd.ok, rd.now().Sub(rd.at), true
}

// RegisterHealth mounts the liveness and readiness endpoints:
//
//	GET /healthz         200 {"status":"ok"} while the process runs
//	GET /api/v1/healthz  200 {"status":"ok","mode":"live"} (web LIVE/REPLAY detection)
//	GET /readyz          200 when every check passes, else 503 DEPENDENCY_UNAVAILABLE
func RegisterHealth(rt *Router, rd *Readiness) {
	rt.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		noStore(w)
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	rt.HandleFunc("GET /api/v1/healthz", func(w http.ResponseWriter, _ *http.Request) {
		noStore(w)
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "mode": "live"})
	})
	rt.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		noStore(w)
		results, ok := rd.State(r.Context())
		if ok {
			WriteJSON(w, http.StatusOK, map[string]any{"status": "ready", "checks": results})
			return
		}
		WriteProblem(w, r, NewProblem(CodeDependencyUnavailable, "one or more dependencies are not reachable").
			With("checks", results).
			WithRetryAfter(time.Second))
	})
}

// runChecks pings every dependency concurrently. Error details go to the
// server log only; the response says "ok" or "unavailable" so connection
// details never leak to clients.
func runChecks(ctx context.Context, log *slog.Logger, checks []Check) (map[string]string, bool) {
	results := make(map[string]string, len(checks))
	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)
	allOK := true
	for _, c := range checks {
		wg.Go(func() {
			cctx, cancel := context.WithTimeout(ctx, readyCheckTimeout)
			defer cancel()
			status := "ok"
			if err := c.Ping(cctx); err != nil {
				status = "unavailable"
				log.WarnContext(ctx, "readiness check failed", slog.String("check", c.Name), slog.String("error", err.Error()))
			}
			mu.Lock()
			defer mu.Unlock()
			results[c.Name] = status
			if status != "ok" {
				allOK = false
			}
		})
	}
	wg.Wait()
	return results, allOK
}

func noStore(w http.ResponseWriter) { w.Header().Set("Cache-Control", "no-store") }
