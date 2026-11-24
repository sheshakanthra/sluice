package orders

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/payments"
	"github.com/sheshakanthra/sluice/internal/redisx/keys"
)

const reconcileBatch = 500

// ReconcileResult counts what one reconciliation did.
type ReconcileResult struct {
	OrphansFixed      int `json:"orphans_fixed"`
	DuplicatesFixed   int `json:"duplicates_fixed"`
	Converged         int `json:"converged"`
	CapturesRequeued  int `json:"captures_requeued"`
	DeadHoldsSettled  int `json:"dead_holds_settled"`
	InventoryRepaired int `json:"inventory_repaired"`
}

// Reconcile is the safety net (ARCHITECTURE §4.5), run every
// RECONCILE_INTERVAL by the worker and synchronously by POST
// /admin/reconcile:
//  1. PSP intents holding money (authorized or captured) for longer than
//     OrphanAge: a PENDING_PAYMENT order is converged through ConfirmOrder
//     first (it may still confirm); money without a confirmed owner is
//     voided (authorized) or refunded (captured).
//  2. CONFIRMED orders whose payment is AUTHORIZED for longer than
//     CaptureAge: capture is re-enqueued.
//  3. PENDING_PAYMENT orders whose hold is gone: settled (voids, then
//     EXPIRED or FAILED).
//  4. Redis vs PG inventory drift for every open event: Redis repaired.
func (s *Service) Reconcile(ctx context.Context) (ReconcileResult, error) {
	var (
		res  ReconcileResult
		errs []error
	)
	now := s.o.Now()
	pspStates := []string{payments.StatusAuthorized, payments.StatusCaptured}
	if s.o.NoPSPReconcile {
		pspStates = nil // baseline only (Options.NoPSPReconcile)
	}
	for _, st := range pspStates {
		ins, err := s.psp.ListIntents(ctx, payments.ListFilter{Status: st})
		if err != nil {
			errs = append(errs, fmt.Errorf("list %s intents: %w", st, err))
			continue
		}
		for i := range ins {
			in := &ins[i]
			if now.Sub(in.UpdatedAt) < s.o.OrphanAge {
				continue
			}
			if err := s.reconcileIntent(ctx, *in, &res); err != nil {
				errs = append(errs, fmt.Errorf("intent %s: %w", in.ID, err))
			}
		}
	}

	stale, err := s.q.ListAuthorizedConfirmedPayments(ctx, gen.ListAuthorizedConfirmedPaymentsParams{
		Before: now.Add(-s.o.CaptureAge), BatchSize: reconcileBatch})
	if err != nil {
		errs = append(errs, fmt.Errorf("stale captures: %w", err))
	}
	for i := range stale {
		p := &stale[i]
		if err := s.enqueue(ctx, CaptureArgs{IntentID: *p.ProviderIntentID}); err != nil {
			errs = append(errs, err)
			continue
		}
		res.CapturesRequeued++
	}

	dead, err := s.q.ListPendingOrdersWithDeadHold(ctx, reconcileBatch)
	if err != nil {
		errs = append(errs, fmt.Errorf("dead holds: %w", err))
	}
	for _, id := range dead {
		if err := s.settle(ctx, id); err != nil {
			errs = append(errs, err)
			continue
		}
		res.DeadHoldsSettled++
	}

	evs, err := s.q.ListOpenEvents(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("open events: %w", err))
	}
	for _, ev := range evs {
		n, err := s.inv.RepairDrift(ctx, ev.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("drift %s: %w", ev.ID, err))
		}
		res.InventoryRepaired += n
	}
	if res != (ReconcileResult{}) {
		s.o.Log.InfoContext(ctx, "reconcile", slog.Int("orphans_fixed", res.OrphansFixed),
			slog.Int("duplicates_fixed", res.DuplicatesFixed), slog.Int("converged", res.Converged),
			slog.Int("captures_requeued", res.CapturesRequeued), slog.Int("dead_holds_settled", res.DeadHoldsSettled),
			slog.Int("inventory_repaired", res.InventoryRepaired))
	}
	return res, errors.Join(errs...)
}

// reconcileIntent handles one PSP intent that holds money.
func (s *Service) reconcileIntent(ctx context.Context, in payments.Intent, res *ReconcileResult) error {
	orderID, perr := uuid.Parse(in.MerchantRef)
	var (
		o    gen.Order
		oerr = pgx.ErrNoRows
	)
	if perr == nil {
		o, oerr = s.q.GetOrder(ctx, orderID)
		if oerr != nil && !errors.Is(oerr, pgx.ErrNoRows) {
			return fmt.Errorf("get order: %w", oerr)
		}
	}
	if oerr == nil && o.Status == gen.OrderStatusPENDINGPAYMENT {
		// Not an orphan yet: a lost webhook and no client pull. Attach the
		// intent if a crash lost its id, then converge.
		if err := s.attachIntent(ctx, o, in); err != nil {
			return err
		}
		res.Converged++
		return s.ConfirmOrder(ctx, o.ID)
	}
	if oerr == nil && o.Status == gen.OrderStatusCONFIRMED {
		live, err := s.q.GetLivePayment(ctx, o.ID)
		if err == nil && live.ProviderIntentID != nil && *live.ProviderIntentID == in.ID {
			return nil // the order's own payment
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("live payment: %w", err)
		}
		res.DuplicatesFixed++
		s.countDuplicate()
	}
	// No confirmed owner: give the money back.
	var err error
	if in.Status == payments.StatusCaptured {
		err = s.Refund(ctx, in.ID)
	} else {
		err = s.Void(ctx, in.ID)
	}
	if err != nil {
		return err
	}
	res.OrphansFixed++
	s.countOrphan()
	s.o.Log.WarnContext(ctx, "orphan payment fixed", slog.String("intent_id", in.ID), slog.String("merchant_ref", in.MerchantRef),
		slog.String("psp_status", in.Status))
	return nil
}

// attachIntent stores in's id on the attempt it was created for, if that
// attempt has no intent yet (the API crashed between the PSP create and
// storing the id).
func (s *Service) attachIntent(ctx context.Context, o gen.Order, in payments.Intent) error {
	if _, err := s.q.GetPaymentByIntent(ctx, &in.ID); err == nil {
		return nil
	}
	attempt, err := strconv.Atoi(in.Metadata["attempt"])
	if err != nil {
		return nil
	}
	pays, err := s.q.ListPaymentsByOrder(ctx, o.ID)
	if err != nil {
		return fmt.Errorf("list payments: %w", err)
	}
	for i := range pays {
		p := &pays[i]
		if int(p.Attempt) == attempt && p.ProviderIntentID == nil {
			id := in.ID
			if _, err := s.q.SetPaymentIntent(ctx, gen.SetPaymentIntentParams{ProviderIntentID: &id, ID: p.ID}); err != nil {
				return fmt.Errorf("attach intent: %w", err)
			}
		}
	}
	return nil
}

// auditTTL: the live audit cache expires if the worker stops refreshing it.
const auditTTL = 30 * time.Second

// RefreshAuditCache computes oversold, duplicate and orphan counts for every
// ON_SALE event into sl:{e:<eid>}:audit (read by /stats in P4).
func (s *Service) RefreshAuditCache(ctx context.Context) error {
	if s.rdb == nil {
		return nil
	}
	evs, err := s.q.ListOpenEvents(ctx)
	if err != nil {
		return fmt.Errorf("open events: %w", err)
	}
	var errs []error
	for _, ev := range evs {
		if ev.Status != gen.EventStatusONSALE {
			continue
		}
		c, err := s.q.AuditCounts(ctx, ev.ID)
		if err != nil {
			errs = append(errs, fmt.Errorf("audit %s: %w", ev.ID, err))
			continue
		}
		k := keys.Audit(ev.ID)
		pipe := s.rdb.TxPipeline()
		pipe.HSet(ctx, k, "oversold", c.Oversold, "duplicates", c.Duplicates, "orphans", c.Orphans,
			"tickets", c.Tickets, "at_ms", s.o.Now().UnixMilli())
		pipe.Expire(ctx, k, auditTTL)
		if _, err := pipe.Exec(ctx); err != nil {
			errs = append(errs, fmt.Errorf("audit cache %s: %w", ev.ID, err))
		}
	}
	return errors.Join(errs...)
}
