package orders

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/payments"
)

// ConfirmOrder is the single convergence point (ARCHITECTURE §4.3): /pay,
// /confirm, the webhook job and the reconciler all call it, any number of
// times, in any order. For every attempt that is CREATED in our DB and has
// a PSP intent, it pulls the intent from the PSP and makes our state agree
// with it; then it settles an order whose hold is gone.
func (s *Service) ConfirmOrder(ctx context.Context, orderID uuid.UUID) error {
	o, err := s.q.GetOrder(ctx, orderID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrOrderNotFound
	}
	if err != nil {
		return fmt.Errorf("get order: %w", err)
	}
	pays, err := s.q.ListPaymentsByOrder(ctx, orderID)
	if err != nil {
		return fmt.Errorf("list payments: %w", err)
	}
	for i := range pays {
		p := &pays[i]
		if p.Status != gen.PaymentStatusCREATED || p.ProviderIntentID == nil {
			continue
		}
		in, err := s.psp.GetIntent(ctx, *p.ProviderIntentID)
		if err != nil {
			return fmt.Errorf("pull intent: %w", err)
		}
		if err := s.syncIntent(ctx, *p, in); err != nil {
			return err
		}
	}
	if o.Status == gen.OrderStatusPENDINGPAYMENT {
		return s.settle(ctx, orderID)
	}
	return nil
}

// syncIntent makes payment p agree with the PSP's intent in. It never moves
// a payment backwards.
func (s *Service) syncIntent(ctx context.Context, p gen.Payment, in payments.Intent) error {
	switch in.Status {
	case payments.StatusRequiresPayment:
		return nil
	case payments.StatusAuthorized:
		return s.confirmAuthorized(ctx, p)
	case payments.StatusCaptured:
		if err := s.confirmAuthorized(ctx, p); err != nil {
			return err
		}
		return s.markCaptured(ctx, in.ID)
	case payments.StatusFailed, payments.StatusCanceled:
		return s.markDead(ctx, in)
	case payments.StatusRefunded:
		return s.markRefunded(ctx, in.ID)
	}
	return nil
}

// confirmAuthorized handles one authorized payment: transaction A sells the
// seats under the I6 fence and confirms; if the hold was lost or the quota
// would be exceeded, transaction B fails the order and voids (§4.3 steps
// 2–4). Idempotent.
func (s *Service) confirmAuthorized(ctx context.Context, p gen.Payment) error {
	var (
		outcome string
		order   gen.Order
	)
	errA := db.WithTx(ctx, s.pg, func(tx pgx.Tx) error {
		outcome = ""
		q := gen.New(tx)
		var err error
		if order, err = q.GetOrderForUpdate(ctx, p.OrderID); err != nil {
			return fmt.Errorf("lock order: %w", err)
		}
		cur, err := q.GetPayment(ctx, p.ID)
		if err != nil {
			return fmt.Errorf("get payment: %w", err)
		}
		if cur.Status != gen.PaymentStatusCREATED && cur.Status != gen.PaymentStatusAUTHORIZED {
			outcome = "noop" // already voided, failed, captured or refunded
			return nil
		}
		live, err := q.GetLivePayment(ctx, order.ID)
		haveLive := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("live payment: %w", err)
		}
		if haveLive && live.ID == cur.ID && order.Status == gen.OrderStatusCONFIRMED {
			outcome = "replay"
			return nil
		}
		if (haveLive && live.ID != cur.ID) || order.Status == gen.OrderStatusCONFIRMED {
			// A second authorization for one order: never keep it (I2).
			outcome = "duplicate"
			return s.enqueueTx(ctx, tx, VoidArgs{IntentID: *cur.ProviderIntentID})
		}
		if order.Status != gen.OrderStatusPENDINGPAYMENT {
			// The order already failed or expired: record the authorization
			// so `charged` tells the truth, and void it.
			outcome = "late"
			if cur.Status == gen.PaymentStatusCREATED {
				if _, err := q.AuthorizePayment(ctx, cur.ID); err != nil {
					return fmt.Errorf("authorize late payment: %w", err)
				}
			}
			return s.enqueueTx(ctx, tx, VoidArgs{IntentID: *cur.ProviderIntentID})
		}
		if cur.Status == gen.PaymentStatusCREATED {
			// The partial unique index payments_one_live_per_order is the
			// backstop: a violation aborts and the caller retries into the
			// duplicate branch above.
			if _, err := q.AuthorizePayment(ctx, cur.ID); err != nil {
				return fmt.Errorf("authorize payment: %w", err)
			}
		}
		h, err := q.GetHoldForUpdate(ctx, order.HoldID)
		if err != nil {
			return fmt.Errorf("lock hold: %w", err)
		}
		if !inventory.HoldStatus(h.Status).Live() {
			return errHoldLost
		}
		n, err := q.SellSeats(ctx, gen.SellSeatsParams{OrderID: order.ID, EventID: order.EventID, SeatNos: order.SeatNos, HoldID: h.ID})
		if err != nil {
			return fmt.Errorf("sell seats: %w", err)
		}
		if n != int64(len(order.SeatNos)) { // I6: every seat still HELD by this hold
			return errHoldLost
		}
		if _, err := q.InsertTickets(ctx, gen.InsertTicketsParams{EventID: order.EventID, SeatNos: order.SeatNos,
			OrderID: order.ID, UserID: order.UserID, Codes: ticketCodes(len(order.SeatNos))}); err != nil {
			return fmt.Errorf("insert tickets: %w", err)
		}
		ev, err := s.inv.Event(ctx, order.EventID.String())
		if err != nil {
			return err
		}
		//nolint:gosec // G115: seats per order <= MAX_SEATS_PER_ORDER
		if _, err := q.AddQuota(ctx, gen.AddQuotaParams{EventID: order.EventID, UserID: order.UserID,
			Seats: int32(len(order.SeatNos)), MaxAllowed: ev.MaxPerUser}); err != nil {
			if db.IsCheckViolation(err, "quota_cap") {
				return errQuotaExceeded // I4
			}
			return fmt.Errorf("add quota: %w", err)
		}
		if n, err := q.ConfirmOrder(ctx, order.ID); err != nil || n != 1 {
			return fmt.Errorf("confirm order (rows %d): %w", n, err)
		}
		if n, err := q.ConsumeHold(ctx, h.ID); err != nil || n != 1 {
			return fmt.Errorf("consume hold (rows %d): %w", n, err)
		}
		outcome = "confirmed"
		return s.enqueueTx(ctx, tx, CaptureArgs{IntentID: *cur.ProviderIntentID})
	})
	switch {
	case errA == nil:
	case errors.Is(errA, errHoldLost):
		return s.failAuthorized(ctx, p, FailureHoldLost)
	case errors.Is(errA, errQuotaExceeded):
		return s.failAuthorized(ctx, p, FailureQuotaExceeded)
	default:
		return errA
	}
	switch outcome {
	case "confirmed":
		s.countPayment(gen.PaymentStatusAUTHORIZED)
		s.countOrder(gen.OrderStatusCONFIRMED)
		if err := s.inv.Consume(ctx, order.EventID, order.HoldID); err != nil {
			// The sweeper's Redis pass consumes it from PG later.
			s.o.Log.WarnContext(ctx, "redis consume after confirm", slog.String("error", err.Error()))
		}
	case "duplicate":
		s.countDuplicate()
		s.o.Log.WarnContext(ctx, "duplicate authorization voided", slog.String("order_id", p.OrderID.String()),
			slog.String("intent_id", *p.ProviderIntentID))
	case "late":
		s.countPayment(gen.PaymentStatusAUTHORIZED)
	}
	return nil
}

// failAuthorized is transaction B: the order fails with code, the hold is
// released if it is still ours, the authorization is recorded and a void is
// enqueued. The user is told "you were not charged".
func (s *Service) failAuthorized(ctx context.Context, p gen.Payment, code string) error {
	var (
		failed, released bool
		order            gen.Order
	)
	err := db.WithTx(ctx, s.pg, func(tx pgx.Tx) error {
		failed, released = false, false
		q := gen.New(tx)
		var err error
		if order, err = q.GetOrderForUpdate(ctx, p.OrderID); err != nil {
			return fmt.Errorf("lock order: %w", err)
		}
		if order.Status == gen.OrderStatusPENDINGPAYMENT {
			c := code
			if _, err := q.FailOrder(ctx, gen.FailOrderParams{FailureCode: &c, ID: order.ID}); err != nil {
				return fmt.Errorf("fail order: %w", err)
			}
			failed = true
		}
		if released, err = releaseIfLive(ctx, q, order); err != nil {
			return err
		}
		cur, err := q.GetPayment(ctx, p.ID)
		if err != nil {
			return fmt.Errorf("get payment: %w", err)
		}
		switch cur.Status {
		case gen.PaymentStatusCREATED:
			if _, err := q.GetLivePayment(ctx, order.ID); errors.Is(err, pgx.ErrNoRows) {
				if _, err := q.AuthorizePayment(ctx, cur.ID); err != nil {
					return fmt.Errorf("record authorization: %w", err)
				}
			} else if err != nil {
				return fmt.Errorf("live payment: %w", err)
			}
		case gen.PaymentStatusAUTHORIZED:
		default:
			return nil
		}
		return s.enqueueTx(ctx, tx, VoidArgs{IntentID: *cur.ProviderIntentID})
	})
	if err != nil {
		return err
	}
	if failed {
		s.countOrder(gen.OrderStatusFAILED)
		s.o.Log.InfoContext(ctx, "order failed, authorization will be voided", slog.String("order_id", order.ID.String()),
			slog.String("failure_code", code))
	}
	if released {
		s.inv.MirrorRelease(ctx, order.EventID, order.HoldID)
	}
	return nil
}

// releaseIfLive releases the order's hold and frees its seats if the hold
// is still ACTIVE or PAYMENT_PENDING.
func releaseIfLive(ctx context.Context, q *gen.Queries, o gen.Order) (bool, error) {
	h, err := q.GetHoldForUpdate(ctx, o.HoldID)
	if err != nil {
		return false, fmt.Errorf("lock hold: %w", err)
	}
	if !inventory.HoldStatus(h.Status).Live() {
		return false, nil
	}
	if _, err := q.ReleaseHold(ctx, h.ID); err != nil {
		return false, fmt.Errorf("release hold: %w", err)
	}
	if _, err := q.ReleaseSeats(ctx, gen.ReleaseSeatsParams{EventID: o.EventID, HoldID: h.ID}); err != nil {
		return false, fmt.Errorf("release seats: %w", err)
	}
	return true, nil
}

// markCaptured records a capture the PSP reports for intentID. A capture on
// an order that is not confirmed is an orphan: refund it.
func (s *Service) markCaptured(ctx context.Context, intentID string) error {
	p, err := s.q.GetPaymentByIntent(ctx, &intentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("payment by intent: %w", err)
	}
	o, err := s.q.GetOrder(ctx, p.OrderID)
	if err != nil {
		return fmt.Errorf("get order: %w", err)
	}
	live, lerr := s.q.GetLivePayment(ctx, o.ID)
	if o.Status != gen.OrderStatusCONFIRMED || lerr != nil || live.ID != p.ID {
		return s.enqueue(ctx, RefundArgs{IntentID: intentID})
	}
	n, err := s.q.CapturePayment(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("capture payment: %w", err)
	}
	if n == 1 {
		s.countPayment(gen.PaymentStatusCAPTURED)
	}
	return nil
}

// markDead records a failed or canceled intent: CREATED -> FAILED (never
// authorized) or -> VOIDED (authorized, then canceled), AUTHORIZED ->
// VOIDED. Then the order is settled if it is still waiting.
func (s *Service) markDead(ctx context.Context, in payments.Intent) error {
	p, err := s.q.GetPaymentByIntent(ctx, &in.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("payment by intent: %w", err)
	}
	var to gen.PaymentStatus
	var n int64
	switch {
	case p.Status == gen.PaymentStatusAUTHORIZED || (p.Status == gen.PaymentStatusCREATED && in.Authorized):
		to = gen.PaymentStatusVOIDED
		n, err = s.q.VoidPayment(ctx, p.ID)
	case p.Status == gen.PaymentStatusCREATED:
		to = gen.PaymentStatusFAILED
		n, err = s.q.FailPayment(ctx, p.ID)
	default:
		return nil
	}
	if err != nil {
		return fmt.Errorf("mark payment %s: %w", to, err)
	}
	if n == 1 {
		s.countPayment(to)
	}
	return s.settle(ctx, p.OrderID)
}

func (s *Service) markRefunded(ctx context.Context, intentID string) error {
	p, err := s.q.GetPaymentByIntent(ctx, &intentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("payment by intent: %w", err)
	}
	n, err := s.q.RefundPayment(ctx, p.ID)
	if err != nil {
		return fmt.Errorf("refund payment: %w", err)
	}
	if n == 1 {
		s.countPayment(gen.PaymentStatusREFUNDED)
	}
	return nil
}

// settle decides a PENDING_PAYMENT order that can no longer confirm:
//   - its hold is gone (reclaimed or released): while any attempt may still
//     hold money at the PSP, enqueue its void and wait (the void job settles
//     again). Then FAILED (HOLD_LOST) if money was ever authorized, else
//     EXPIRED.
//   - all MaxAttempts attempts failed: FAILED (PAYMENT_FAILED), hold released.
func (s *Service) settle(ctx context.Context, orderID uuid.UUID) error {
	var (
		final    gen.OrderStatus
		released bool
		order    gen.Order
	)
	err := db.WithTx(ctx, s.pg, func(tx pgx.Tx) error {
		final, released = "", false
		q := gen.New(tx)
		var err error
		if order, err = q.GetOrderForUpdate(ctx, orderID); err != nil {
			return fmt.Errorf("lock order: %w", err)
		}
		if order.Status != gen.OrderStatusPENDINGPAYMENT {
			return nil
		}
		h, err := q.GetHold(ctx, order.HoldID)
		if err != nil {
			return fmt.Errorf("get hold: %w", err)
		}
		if inventory.HoldStatus(h.Status).Live() {
			pays, err := q.ListPaymentsByOrder(ctx, orderID)
			if err != nil {
				return fmt.Errorf("list payments: %w", err)
			}
			last := pays[len(pays)-1]
			if last.Attempt < MaxAttempts || last.Status != gen.PaymentStatusFAILED {
				return nil
			}
			code := FailurePayment
			if _, err := q.FailOrder(ctx, gen.FailOrderParams{FailureCode: &code, ID: orderID}); err != nil {
				return fmt.Errorf("fail order: %w", err)
			}
			final = gen.OrderStatusFAILED
			released, err = releaseIfLive(ctx, q, order)
			return err
		}
		final, err = s.settleDeadHoldTx(ctx, tx, q, order)
		return err
	})
	if err != nil {
		return err
	}
	if final != "" {
		s.countOrder(final)
	}
	if released {
		s.inv.MirrorRelease(ctx, order.EventID, order.HoldID)
	}
	return nil
}

// settleDeadHoldTx is settle's dead-hold branch inside tx (also used by the
// reclaim hook). It returns the order's new status, or "" while voids are
// outstanding.
func (s *Service) settleDeadHoldTx(ctx context.Context, tx pgx.Tx, q *gen.Queries, order gen.Order) (gen.OrderStatus, error) {
	pays, err := q.ListPaymentsByOrder(ctx, order.ID)
	if err != nil {
		return "", fmt.Errorf("list payments: %w", err)
	}
	pending, authorized := false, false
	for i := range pays {
		p := &pays[i]
		switch p.Status {
		case gen.PaymentStatusCREATED:
			if p.ProviderIntentID != nil {
				pending = true
				if err := s.enqueueTx(ctx, tx, VoidArgs{IntentID: *p.ProviderIntentID}); err != nil {
					return "", err
				}
			}
		case gen.PaymentStatusAUTHORIZED:
			pending, authorized = true, true
			if err := s.enqueueTx(ctx, tx, VoidArgs{IntentID: *p.ProviderIntentID}); err != nil {
				return "", err
			}
		case gen.PaymentStatusCAPTURED:
			pending, authorized = true, true
			if err := s.enqueueTx(ctx, tx, RefundArgs{IntentID: *p.ProviderIntentID}); err != nil {
				return "", err
			}
		case gen.PaymentStatusVOIDED, gen.PaymentStatusREFUNDED:
			authorized = true
		}
	}
	if pending {
		return "", nil
	}
	if authorized {
		code := FailureHoldLost
		if _, err := q.FailOrder(ctx, gen.FailOrderParams{FailureCode: &code, ID: order.ID}); err != nil {
			return "", fmt.Errorf("fail order: %w", err)
		}
		return gen.OrderStatusFAILED, nil
	}
	if _, err := q.ExpireOrder(ctx, order.ID); err != nil {
		return "", fmt.Errorf("expire order: %w", err)
	}
	return gen.OrderStatusEXPIRED, nil
}

// OnReclaim is the inventory reclaim hook (sweeper or PG takeover): inside
// the reclaiming transaction, lock the hold's order without waiting and
// settle it. If ConfirmOrder holds the order lock, skip: it will find the
// hold reclaimed and fail the order itself, and the reconciler is the net.
func (s *Service) OnReclaim(ctx context.Context, tx pgx.Tx, holdID uuid.UUID) error {
	q := gen.New(tx)
	o, err := q.GetOrderByHoldSkipLocked(ctx, holdID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lock order of reclaimed hold: %w", err)
	}
	if o.Status != gen.OrderStatusPENDINGPAYMENT {
		return nil
	}
	final, err := s.settleDeadHoldTx(ctx, tx, q, o)
	if err == nil && final != "" {
		// Counted before commit: a rollback here is a retried sweep.
		s.countOrder(final)
	}
	return err
}
