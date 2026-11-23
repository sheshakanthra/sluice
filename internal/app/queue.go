package app

import (
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/sheshakanthra/sluice/internal/auth"
	"github.com/sheshakanthra/sluice/internal/config"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/queue"
	"github.com/sheshakanthra/sluice/internal/redisx"
)

// queueSnapshotInterval is how often each API replica re-reads every event's
// queue counters (one snapshot.lua call per event).
const queueSnapshotInterval = 100 * time.Millisecond

// newQueueService builds the API side of the waiting room.
func newQueueService(cfg *config.Config, log *slog.Logger, reg prometheus.Registerer,
	sc *redisx.Scripts, keyring *auth.Keyring,
) (*queue.Service, error) {
	m, err := queue.NewMetrics(reg)
	if err != nil {
		return nil, err
	}
	return queue.NewService(sc, keyring, queue.NewCache(sc, nil, log), queue.Options{
		AdmissionTTL: cfg.AdmissionTTL, Overadmit: cfg.QueueOveradmit, Log: log, Metrics: m,
	})
}

// newQueueController builds the worker's admission controller.
func newQueueController(cfg *config.Config, log *slog.Logger, q *gen.Queries, sc *redisx.Scripts) (*queue.Controller, error) {
	t, err := queue.NewTicker(sc, queue.TickConfig{
		MaxActive: cfg.QueueMaxActive, Overadmit: cfg.QueueOveradmit, MinAdmit: cfg.QueueMinAdmit,
		MaxPerTick: cfg.QueueMaxAdmitPerTick, OfferTTL: cfg.OfferClaimTTL,
	})
	if err != nil {
		return nil, err
	}
	return queue.NewController(q, t, queue.ControllerOptions{Tick: cfg.QueueTick, Log: log})
}
