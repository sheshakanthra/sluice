package orders

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/sheshakanthra/sluice/internal/db"
)

// River job kinds (internal/jobs registers the workers).
const (
	KindProcessWebhook = "process_webhook"
	KindCapture        = "capture_payment"
	KindVoid           = "void_payment"
	KindRefund         = "refund_payment"
	KindReconcile      = "reconcile"
	KindAuditCache     = "audit_cache"
)

// jobMaxAttempts: payment jobs retry for a long time (backoff caps at 1 min);
// the reconciler is the safety net after that.
const jobMaxAttempts = 25

// uniqueLive makes a job unique per (kind, args) among jobs that have not
// finished: a second void for the same intent while one is queued or running
// is dropped, but a later one (after the first completed) is accepted. River
// requires available, pending, running and scheduled; retryable is added so
// a failing job is not doubled (ADR 0007).
var uniqueLive = river.UniqueOpts{
	ByArgs: true,
	ByState: []rivertype.JobState{
		rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning,
		rivertype.JobStateScheduled, rivertype.JobStateRetryable,
	},
}

// ProcessWebhookArgs processes one inbox row. Unique per inbox event, not
// per intent: a later event for the same intent (captured after
// authorized) must not be dropped while the first is queued (ADR 0007).
type ProcessWebhookArgs struct {
	Provider string `json:"provider"`
	EventID  string `json:"event_id"`
}

// Kind implements river.JobArgs.
func (ProcessWebhookArgs) Kind() string { return KindProcessWebhook }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (ProcessWebhookArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: jobMaxAttempts, UniqueOpts: uniqueLive}
}

// CaptureArgs captures an intent (unique per intent).
type CaptureArgs struct {
	IntentID string `json:"intent_id"`
}

// Kind implements river.JobArgs.
func (CaptureArgs) Kind() string { return KindCapture }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (CaptureArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: jobMaxAttempts, UniqueOpts: uniqueLive}
}

// VoidArgs voids (cancels) an intent (unique per intent).
type VoidArgs struct {
	IntentID string `json:"intent_id"`
}

// Kind implements river.JobArgs.
func (VoidArgs) Kind() string { return KindVoid }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (VoidArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: jobMaxAttempts, UniqueOpts: uniqueLive}
}

// RefundArgs refunds a captured intent (unique per intent).
type RefundArgs struct {
	IntentID string `json:"intent_id"`
}

// Kind implements river.JobArgs.
func (RefundArgs) Kind() string { return KindRefund }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (RefundArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: jobMaxAttempts, UniqueOpts: uniqueLive}
}

// ReconcileArgs is the periodic reconciliation (ARCHITECTURE §4.5).
type ReconcileArgs struct{}

// Kind implements river.JobArgs.
func (ReconcileArgs) Kind() string { return KindReconcile }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (ReconcileArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 1, UniqueOpts: uniqueLive}
}

// AuditCacheArgs refreshes the live audit cache.
type AuditCacheArgs struct{}

// Kind implements river.JobArgs.
func (AuditCacheArgs) Kind() string { return KindAuditCache }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (AuditCacheArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{MaxAttempts: 1, UniqueOpts: uniqueLive}
}

// enqueueTx inserts a job in tx (the transactional outbox).
func (s *Service) enqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs) error {
	if _, err := s.jobs.InsertTx(ctx, tx, args, nil); err != nil {
		return fmt.Errorf("enqueue %s: %w", args.Kind(), err)
	}
	return nil
}

// enqueue inserts a job in its own transaction.
func (s *Service) enqueue(ctx context.Context, args river.JobArgs) error {
	return db.WithTx(ctx, s.pg, func(tx pgx.Tx) error { return s.enqueueTx(ctx, tx, args) })
}
