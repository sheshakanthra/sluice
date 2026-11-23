// Package jobs wires River (the Postgres job queue) for SLUICE: an
// insert-only client for transactional enqueues from the API, and the
// worker client that runs process_webhook, capture/void/refund, the periodic
// reconciler and the live audit cache.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"

	"github.com/sheshakanthra/sluice/internal/orders"
)

// Config tunes the worker client.
type Config struct {
	// ReconcileInterval is RECONCILE_INTERVAL (30 s by default).
	ReconcileInterval time.Duration
	// AuditInterval refreshes the live audit cache (5 s).
	AuditInterval time.Duration
	// MaxWorkers bounds concurrent jobs in this process (default 50).
	MaxWorkers int
	// FetchPollInterval / FetchCooldown: River polling (tests go faster).
	FetchPollInterval time.Duration
	FetchCooldown     time.Duration
	// RetryBase and RetryMax shape the retry backoff (1 s .. 60 s).
	RetryBase, RetryMax time.Duration
	Log                 *slog.Logger
}

// NewInsertClient is a River client that only inserts jobs (the API).
func NewInsertClient(pool *pgxpool.Pool, log *slog.Logger) (*river.Client[pgx.Tx], error) {
	c, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Logger: quiet(log)})
	if err != nil {
		return nil, fmt.Errorf("river insert client: %w", err)
	}
	return c, nil
}

// NewWorkerClient is the River client of `sluice worker`. Start it with
// Start(ctx) and stop it with Stop(ctx).
func NewWorkerClient(pool *pgxpool.Pool, svc *orders.Service, cfg Config) (*river.Client[pgx.Tx], error) {
	if cfg.MaxWorkers <= 0 {
		cfg.MaxWorkers = 50
	}
	if cfg.ReconcileInterval <= 0 {
		cfg.ReconcileInterval = 30 * time.Second
	}
	if cfg.AuditInterval <= 0 {
		cfg.AuditInterval = 5 * time.Second
	}
	if cfg.RetryBase <= 0 {
		cfg.RetryBase = time.Second
	}
	if cfg.RetryMax <= 0 {
		cfg.RetryMax = time.Minute
	}
	workers := river.NewWorkers()
	if err := register(workers, svc); err != nil {
		return nil, err
	}
	rc := &river.Config{
		Logger:            quiet(cfg.Log),
		Queues:            map[string]river.QueueConfig{river.QueueDefault: {MaxWorkers: cfg.MaxWorkers}},
		Workers:           workers,
		FetchPollInterval: cfg.FetchPollInterval,
		FetchCooldown:     cfg.FetchCooldown,
		RetryPolicy:       &Backoff{Base: cfg.RetryBase, Max: cfg.RetryMax},
		PeriodicJobs: []*river.PeriodicJob{
			river.NewPeriodicJob(river.PeriodicInterval(cfg.ReconcileInterval),
				func() (river.JobArgs, *river.InsertOpts) { return orders.ReconcileArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true}),
			river.NewPeriodicJob(river.PeriodicInterval(cfg.AuditInterval),
				func() (river.JobArgs, *river.InsertOpts) { return orders.AuditCacheArgs{}, nil },
				&river.PeriodicJobOpts{RunOnStart: true}),
		},
	}
	c, err := river.NewClient(riverpgxv5.New(pool), rc)
	if err != nil {
		return nil, fmt.Errorf("river worker client: %w", err)
	}
	return c, nil
}

// quiet keeps River's own logs at warn and above.
func quiet(log *slog.Logger) *slog.Logger {
	if log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return slog.New(levelFloor{log.Handler(), slog.LevelWarn})
}

type levelFloor struct {
	slog.Handler
	min slog.Level
}

func (h levelFloor) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= h.min && h.Handler.Enabled(ctx, l)
}

func (h levelFloor) WithAttrs(a []slog.Attr) slog.Handler {
	return levelFloor{h.Handler.WithAttrs(a), h.min}
}

func (h levelFloor) WithGroup(n string) slog.Handler {
	return levelFloor{h.Handler.WithGroup(n), h.min}
}

func register(w *river.Workers, svc *orders.Service) error {
	adds := []func() error{
		func() error {
			return river.AddWorkerSafely(w, river.WorkFunc(func(ctx context.Context, j *river.Job[orders.ProcessWebhookArgs]) error {
				return svc.ProcessWebhook(ctx, j.Args.Provider, j.Args.EventID)
			}))
		},
		func() error {
			return river.AddWorkerSafely(w, river.WorkFunc(func(ctx context.Context, j *river.Job[orders.CaptureArgs]) error {
				return svc.Capture(ctx, j.Args.IntentID)
			}))
		},
		func() error {
			return river.AddWorkerSafely(w, river.WorkFunc(func(ctx context.Context, j *river.Job[orders.VoidArgs]) error {
				return svc.Void(ctx, j.Args.IntentID)
			}))
		},
		func() error {
			return river.AddWorkerSafely(w, river.WorkFunc(func(ctx context.Context, j *river.Job[orders.RefundArgs]) error {
				return svc.Refund(ctx, j.Args.IntentID)
			}))
		},
		func() error {
			return river.AddWorkerSafely(w, river.WorkFunc(func(ctx context.Context, _ *river.Job[orders.ReconcileArgs]) error {
				_, err := svc.Reconcile(ctx)
				return err
			}))
		},
		func() error {
			return river.AddWorkerSafely(w, river.WorkFunc(func(ctx context.Context, _ *river.Job[orders.AuditCacheArgs]) error {
				return svc.RefreshAuditCache(ctx)
			}))
		},
	}
	for _, add := range adds {
		if err := add(); err != nil {
			return fmt.Errorf("register worker: %w", err)
		}
	}
	return nil
}

// Dispatch runs one job by kind with its encoded args, exactly as the River
// workers would. Tests use it to drain jobs in a chosen order.
func Dispatch(ctx context.Context, svc *orders.Service, kind string, args []byte) error {
	switch kind {
	case orders.KindProcessWebhook:
		var a orders.ProcessWebhookArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return err
		}
		return svc.ProcessWebhook(ctx, a.Provider, a.EventID)
	case orders.KindCapture:
		var a orders.CaptureArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return err
		}
		return svc.Capture(ctx, a.IntentID)
	case orders.KindVoid:
		var a orders.VoidArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return err
		}
		return svc.Void(ctx, a.IntentID)
	case orders.KindRefund:
		var a orders.RefundArgs
		if err := json.Unmarshal(args, &a); err != nil {
			return err
		}
		return svc.Refund(ctx, a.IntentID)
	case orders.KindReconcile:
		_, err := svc.Reconcile(ctx)
		return err
	case orders.KindAuditCache:
		return svc.RefreshAuditCache(ctx)
	}
	return fmt.Errorf("unknown job kind %q", kind)
}

// Backoff is the retry policy: full-jitter exponential from Base, capped
// at Max (River's default grows as attempt^4 seconds, too slow for a
// payment that a user is waiting on).
type Backoff struct{ Base, Max time.Duration }

// NextRetry implements river.ClientRetryPolicy.
func (b *Backoff) NextRetry(job *rivertype.JobRow) time.Time {
	n := max(job.Attempt, 1)
	d := min(b.Base<<min(n-1, 16), b.Max)
	d = d/2 + time.Duration(rand.Int64N(int64(d/2)+1)) //nolint:gosec // G404: jitter
	return time.Now().Add(d)
}
