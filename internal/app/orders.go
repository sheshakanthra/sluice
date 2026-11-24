package app

import (
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/orders"
	"github.com/sheshakanthra/sluice/internal/payments"
)

// newOrders builds the PSP client and the orders service with their
// metrics registered on reg.
func newOrders(cfg *config.Config, log *slog.Logger, reg prometheus.Registerer, pg *pgxpool.Pool, rdb *redis.Client,
	inv *inventory.Service, ins orders.Inserter,
) (*orders.Service, error) {
	pm, err := payments.NewMetrics(reg)
	if err != nil {
		return nil, err
	}
	psp, err := payments.NewClient(payments.Options{
		BaseURL: cfg.MockpayURL, PublicURL: cfg.MockpayPublicURL, APIKey: cfg.MockpayAPIKey, MaxConns: cfg.PSPMaxConns,
		Metrics: pm,
	})
	if err != nil {
		return nil, err
	}
	om, err := orders.NewMetrics(reg)
	if err != nil {
		return nil, err
	}
	return orders.NewService(pg, inv, psp, ins, rdb, orders.Options{
		PaymentHoldTTL: cfg.PaymentHoldTTL, ReturnURL: strings.TrimRight(cfg.PublicAppURL, "/") + "/order",
		Log: log, Metrics: om,
		// Baselines model a team without payment reconciliation (ADR 0011).
		NoPSPReconcile: cfg.Baseline != config.BaselineOff,
	})
}
