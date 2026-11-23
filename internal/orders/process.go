package orders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/payments"
)

// eventStatus is the payment status a webhook type announces.
func eventStatus(typ string, authorized bool) (PaymentStatus, bool) {
	switch typ {
	case payments.EventAuthorized:
		return PaymentAuthorized, true
	case payments.EventFailed:
		return PaymentFailed, true
	case payments.EventCaptured:
		return PaymentCaptured, true
	case payments.EventCanceled:
		if authorized {
			return PaymentVoided, true
		}
		return PaymentFailed, true
	case payments.EventRefunded:
		return PaymentRefunded, true
	}
	return "", false
}

// ProcessWebhook handles one inbox row (the process_webhook job). An event
// that would move the payment backwards (for example `authorized` after
// `captured`) is ignored and counted. Anything else pulls the intent from
// the PSP and converges through the same code as ConfirmOrder, so
// duplicated, late and reordered webhooks all end in the same state.
func (s *Service) ProcessWebhook(ctx context.Context, provider, eventID string) error {
	row, err := s.q.GetWebhookEvent(ctx, gen.GetWebhookEventParams{Provider: provider, EventID: eventID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get webhook: %w", err)
	}
	if row.ProcessedAt != nil {
		return nil
	}
	var ev payments.WebhookEvent
	if err := json.Unmarshal(row.Payload, &ev); err != nil {
		s.o.Log.ErrorContext(ctx, "undecodable webhook in inbox", slog.String("event_id", eventID))
		return s.markProcessed(ctx, provider, eventID, row.Type, WebhookUnknownIntent)
	}
	intentID := ev.Data.Intent.ID
	p, err := s.q.GetPaymentByIntent(ctx, &intentID)
	if errors.Is(err, pgx.ErrNoRows) {
		// No payment knows this intent: the reconciler treats it as an orphan.
		return s.markProcessed(ctx, provider, eventID, ev.Type, WebhookUnknownIntent)
	}
	if err != nil {
		return fmt.Errorf("payment by intent: %w", err)
	}
	if target, ok := eventStatus(ev.Type, ev.Data.Intent.Authorized); ok && target.Rank() < PaymentStatus(p.Status).Rank() {
		s.o.Log.InfoContext(ctx, "webhook regression ignored", slog.String("event_id", eventID), slog.String("type", ev.Type),
			slog.String("payment_status", string(p.Status)))
		return s.markProcessed(ctx, provider, eventID, ev.Type, WebhookRegressionIgnored)
	}
	in, err := s.psp.GetIntent(ctx, intentID)
	if err != nil {
		return fmt.Errorf("pull intent: %w", err)
	}
	if err := s.syncIntent(ctx, p, in); err != nil {
		return err
	}
	if err := s.ConfirmOrder(ctx, p.OrderID); err != nil {
		return err
	}
	return s.markProcessed(ctx, provider, eventID, ev.Type, WebhookProcessed)
}

func (s *Service) markProcessed(ctx context.Context, provider, eventID, typ, result string) error {
	n, err := s.q.MarkWebhookProcessed(ctx, gen.MarkWebhookProcessedParams{Provider: provider, EventID: eventID})
	if err != nil {
		return fmt.Errorf("mark webhook processed: %w", err)
	}
	if n == 1 {
		s.CountWebhook(typ, result)
	}
	return nil
}

// ReceiveWebhook stores a verified webhook in the inbox and enqueues its
// processing in the same transaction. It reports false for a duplicate
// delivery (the inbox primary key already has it).
func (s *Service) ReceiveWebhook(ctx context.Context, ev payments.WebhookEvent, payload []byte) (bool, error) {
	fresh := false
	err := db.WithTx(ctx, s.pg, func(tx pgx.Tx) error {
		q := gen.New(tx)
		n, err := q.InsertWebhookEvent(ctx, gen.InsertWebhookEventParams{Provider: Provider, EventID: ev.ID, Type: ev.Type, Payload: payload})
		if err != nil {
			return fmt.Errorf("insert webhook: %w", err)
		}
		if fresh = n == 1; !fresh {
			return nil
		}
		return s.enqueueTx(ctx, tx, ProcessWebhookArgs{Provider: Provider, EventID: ev.ID})
	})
	if err != nil {
		return false, err
	}
	if !fresh {
		s.CountWebhook(ev.Type, WebhookDuplicate)
	}
	return fresh, nil
}

// Capture is the capture_payment job: capture at the PSP (key
// capture:<intent>), then record it. Only a CONFIRMED order's live payment
// is captured; anything else is a no-op.
func (s *Service) Capture(ctx context.Context, intentID string) error {
	p, err := s.q.GetPaymentByIntent(ctx, &intentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("payment by intent: %w", err)
	}
	if p.Status != gen.PaymentStatusAUTHORIZED {
		return nil
	}
	o, err := s.q.GetOrder(ctx, p.OrderID)
	if err != nil {
		return fmt.Errorf("get order: %w", err)
	}
	if o.Status != gen.OrderStatusCONFIRMED {
		return nil
	}
	in, err := s.psp.Capture(ctx, intentID)
	if err != nil {
		return err
	}
	if in.Status != payments.StatusCaptured {
		return fmt.Errorf("capture: intent is %s", in.Status)
	}
	return s.markCaptured(ctx, intentID)
}

// Void is the void_payment job: cancel the intent at the PSP (key
// void:<intent>; an un-authorized intent is canceled so a late checkout
// cannot authorize it), then record the outcome and settle the order. A
// captured intent cannot be voided: it is refunded instead.
func (s *Service) Void(ctx context.Context, intentID string) error {
	if keep, err := s.ownedByConfirmed(ctx, intentID); err != nil || keep {
		return err
	}
	in, err := s.psp.Void(ctx, intentID)
	var se *payments.StateError
	if errors.As(err, &se) && se.Intent != nil && se.Intent.Status == payments.StatusCaptured {
		return s.enqueue(ctx, RefundArgs{IntentID: intentID})
	}
	if errors.Is(err, payments.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.markDead(ctx, in)
}

// Refund is the refund_payment job: refund a captured intent that no
// confirmed order owns (an orphan or a duplicate).
func (s *Service) Refund(ctx context.Context, intentID string) error {
	if keep, err := s.ownedByConfirmed(ctx, intentID); err != nil || keep {
		return err
	}
	if _, err := s.psp.Refund(ctx, intentID); err != nil {
		if errors.Is(err, payments.ErrNotFound) {
			return nil
		}
		return err
	}
	return s.markRefunded(ctx, intentID)
}

// ownedByConfirmed reports whether intentID is the live payment of a
// CONFIRMED order. Such money is the customer's ticket: never void or
// refund it (defence in depth; no code path should enqueue that).
func (s *Service) ownedByConfirmed(ctx context.Context, intentID string) (bool, error) {
	p, err := s.q.GetPaymentByIntent(ctx, &intentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("payment by intent: %w", err)
	}
	if !PaymentStatus(p.Status).Live() {
		return false, nil
	}
	o, err := s.q.GetOrder(ctx, p.OrderID)
	if err != nil {
		return false, fmt.Errorf("get order: %w", err)
	}
	if o.Status == gen.OrderStatusCONFIRMED {
		s.o.Log.ErrorContext(ctx, "refusing to void or refund a confirmed order's payment",
			slog.String("order_id", o.ID.String()), slog.String("intent_id", intentID))
		return true, nil
	}
	return false, nil
}
