package app

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/inventory"
	inaive "github.com/sheshakanthra/sluice/internal/inventory/naive"
	"github.com/sheshakanthra/sluice/internal/orders"
	"github.com/sheshakanthra/sluice/internal/payments"
	pnaive "github.com/sheshakanthra/sluice/internal/payments/naive"
)

// naivePSPTimeout is the baselines' PSP timeout: one try, no retries.
const naivePSPTimeout = 5 * time.Second

// baseline holds the route overrides of a BASELINE mode (ADR 0011); both
// are empty when BASELINE=off.
type baseline struct {
	hold   http.Handler
	orders map[string]http.Handler
}

// newBaseline wires the deliberately unsafe B0/B1 code, and only when the
// config asks for it in an environment that allows it (config.Load already
// refuses BASELINE outside rush/test; this is the second lock).
func newBaseline(cfg *config.Config, log *slog.Logger, q *gen.Queries, inv *inventory.Service, ord *orders.Service) (baseline, error) {
	if cfg.Baseline == config.BaselineOff {
		return baseline{}, nil
	}
	if !cfg.AppEnv.AllowsUnsafeModes() {
		return baseline{}, errors.New("api: BASELINE is only allowed when APP_ENV is rush or test")
	}
	log.Warn("BASELINE MODE: deliberately unsafe payment code is mounted (ADR 0011)", slog.String("baseline", string(cfg.Baseline)))
	psp, err := payments.NewClient(payments.Options{
		BaseURL: cfg.MockpayURL, PublicURL: cfg.MockpayPublicURL, APIKey: cfg.MockpayAPIKey,
		Timeout: naivePSPTimeout, MaxRetries: -1, MaxConns: cfg.PSPMaxConns,
	})
	if err != nil {
		return baseline{}, err
	}
	switch cfg.Baseline {
	case config.BaselineNaive:
		o := &pnaive.Orders{Inv: inv, Q: q, PSP: psp, Log: log}
		return baseline{
			hold: &inaive.Hold{Inv: inv, Q: q, HoldTTL: cfg.HoldTTL, MaxSeats: cfg.MaxSeatsPerOrder, Log: log},
			orders: map[string]http.Handler{
				orders.RouteCreate:     http.HandlerFunc(o.Create),
				orders.RoutePay:        http.HandlerFunc(o.Pay),
				orders.RouteNewAttempt: http.HandlerFunc(o.NewAttempt),
				orders.RouteConfirm:    http.HandlerFunc(o.Get),
				orders.RouteGet:        http.HandlerFunc(o.Get),
			},
		}, nil
	case config.BaselineHoldsOnly:
		h := &pnaive.HoldsOnly{Orders: ord, Q: q, PSP: psp, Log: log}
		return baseline{orders: map[string]http.Handler{orders.RoutePay: http.HandlerFunc(h.Pay)}}, nil
	}
	return baseline{}, errors.New("api: unknown BASELINE " + string(cfg.Baseline))
}
