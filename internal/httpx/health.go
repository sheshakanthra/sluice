package httpx

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Check is one named readiness dependency (Postgres, Redis, ...).
type Check struct {
	Name string
	Ping func(ctx context.Context) error
}

// readyCheckTimeout bounds each dependency ping inside /readyz.
const readyCheckTimeout = 2 * time.Second

// RegisterHealth mounts the liveness and readiness endpoints:
//
//	GET /healthz         200 {"status":"ok"} while the process runs
//	GET /api/v1/healthz  200 {"status":"ok","mode":"live"} (web LIVE/REPLAY detection)
//	GET /readyz          200 when every check passes, else 503 DEPENDENCY_UNAVAILABLE
func RegisterHealth(rt *Router, log *slog.Logger, checks ...Check) {
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
		results, ok := runChecks(r.Context(), log, checks)
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
