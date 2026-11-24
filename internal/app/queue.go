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

// queuePersistInterval is how often the worker writes queue_snapshots (R1).
const queuePersistInterval = time.Second

// newQueueRestorer builds the R1 restorer shared by joins and ticks.
func newQueueRestorer(cfg *config.Config, log *slog.Logger, q *gen.Queries, sc *redisx.Scripts) (*queue.Restorer, error) {
	return queue.NewRestorer(q, sc, cfg.OfferClaimTTL, nil, log)
}

// newQueueService builds the API side of the waiting room.
func newQueueService(cfg *config.Config, log *slog.Logger, reg prometheus.Registerer,
	sc *redisx.Scripts, keyring *auth.Keyring, restorer *queue.Restorer,
) (*queue.Service, error) {
	m, err := queue.NewMetrics(reg)
	if err != nil {
		return nil, err
	}
	return queue.NewService(sc, keyring, queue.NewCache(sc, nil, log), queue.Options{
		AdmissionTTL: cfg.AdmissionTTL, AdmissionIdleTTL: cfg.AdmissionIdleTTL, Overadmit: cfg.QueueOveradmit, Log: log,
		Metrics: m, Restorer: restorer,
	})
}

// newQueueWorker builds the worker's admission controller and the snapshot
// writer that also publishes the per-event queue gauges.
func newQueueWorker(cfg *config.Config, log *slog.Logger, reg prometheus.Registerer, q *gen.Queries,
	sc *redisx.Scripts,
) (*queue.Controller, *queue.Snapshotter, error) {
	wm, err := queue.NewWorkerMetrics(reg)
	if err != nil {
		return nil, nil, err
	}
	restorer, err := newQueueRestorer(cfg, log, q, sc)
	if err != nil {
		return nil, nil, err
	}
	t, err := queue.NewTicker(sc, queue.TickConfig{
		MaxActive: cfg.QueueMaxActive, Overadmit: cfg.QueueOveradmit, MinAdmit: cfg.QueueMinAdmit,
		MaxPerTick: cfg.QueueMaxAdmitPerTick, OfferTTL: cfg.OfferClaimTTL,
	})
	if err != nil {
		return nil, nil, err
	}
	ctrl, err := queue.NewController(q, t.WithRestorer(restorer, wm), queue.ControllerOptions{Tick: cfg.QueueTick, Log: log})
	if err != nil {
		return nil, nil, err
	}
	snap, err := queue.NewSnapshotter(q, sc, nil, log, wm)
	if err != nil {
		return nil, nil, err
	}
	return ctrl, snap, nil
}
