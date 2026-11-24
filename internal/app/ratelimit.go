package app

import (
	"net/http"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/orders"
	"github.com/sheshakanthra/sluice/internal/queue"
	"github.com/sheshakanthra/sluice/internal/ratelimit"
)

// routeSessions is auth's session route (auth registers it by literal).
const routeSessions = "POST /api/v1/sessions"

// statusLimiterCapacity bounds the users one replica tracks for the status
// limiter (about 100 bytes each); least recently seen users are forgotten.
const statusLimiterCapacity = 200_000

// rateLimits returns the router decorator that applies API.md §2's limits.
// Rules with the same name share a bucket (pay and new payment attempts).
func rateLimits(d Deps, keyring *auth.Keyring) (func(string, http.Handler) http.Handler, error) {
	m, err := ratelimit.NewMetrics(d.Registry)
	if err != nil {
		return nil, err
	}
	user := keyring.RequestSubject
	g := ratelimit.NewGCRA(d.Redis, d.Config.TrustedProxies, user, d.Log, m)
	status := ratelimit.NewLocal(ratelimit.LocalConfig{Rate: 3, Burst: 6, Capacity: statusLimiterCapacity}).
		Middleware("status", user, m)
	byRoute := routeLimits(g)
	return func(pattern string, h http.Handler) http.Handler {
		if pattern == queue.RouteStatus {
			return status(h)
		}
		if mw, ok := byRoute[pattern]; ok {
			return mw(h)
		}
		return h
	}, nil
}

func routeLimits(g *ratelimit.GCRA) map[string]func(http.Handler) http.Handler {
	pay := g.Middleware(ratelimit.PerMinute("pay", ratelimit.ByUser, 10))
	return map[string]func(http.Handler) http.Handler{
		routeSessions: g.Middleware(ratelimit.PerMinute("sessions", ratelimit.ByIP, 20)),
		queue.RouteJoin: g.Middleware(
			ratelimit.PerMinute("join", ratelimit.ByUser, 6),
			ratelimit.PerMinute("join", ratelimit.ByIP, 30),
		),
		inventory.RouteHold:    g.Middleware(ratelimit.PerMinute("holds", ratelimit.ByUser, 20)),
		orders.RouteCreate:     g.Middleware(ratelimit.PerMinute("orders", ratelimit.ByUser, 10)),
		orders.RoutePay:        pay,
		orders.RouteNewAttempt: pay,
		orders.RouteConfirm:    g.Middleware(ratelimit.PerMinute("confirm", ratelimit.ByUser, 10)),
	}
}
