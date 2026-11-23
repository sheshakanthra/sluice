package orders

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/sheshakanthra/sluice/internal/db"
	"github.com/sheshakanthra/sluice/internal/db/gen"
	"github.com/sheshakanthra/sluice/internal/inventory"
	"github.com/sheshakanthra/sluice/internal/payments"
)

// Create creates the order for the caller's hold (POST /orders, ARCHITECTURE
// §4.3): one transaction moves the hold to PAYMENT_PENDING with
// PAYMENT_HOLD_TTL and inserts the order and payment attempt 1; then Redis
// mirrors the extension; then the PSP intent is created outside the
// transaction under the deterministic key order:<id>:attempt:1. A second
// call for the same hold returns the existing order, and retries the PSP
// create with the same key if it failed before.
func (s *Service) Create(ctx context.Context, userID, holdID uuid.UUID) (View, error) {
	h, err := s.q.GetHold(ctx, holdID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && h.UserID != userID) {
		return View{}, inventory.ErrHoldNotFound
	}
	if err != nil {
		return View{}, fmt.Errorf("get hold: %w", err)
	}
	if o, err := s.q.GetOrderByHold(ctx, holdID); err == nil {
		return s.resume(ctx, o)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return View{}, fmt.Errorf("order by hold: %w", err)
	}
	ev, err := s.inv.Event(ctx, h.EventID.String())
	if err != nil {
		return View{}, err
	}
	now := s.o.Now()
	exp := now.Add(s.o.PaymentHoldTTL).Truncate(time.Millisecond)
	var (
		order gen.Order
		pay   gen.Payment
	)
	err = db.WithTx(ctx, s.pg, func(tx pgx.Tx) error {
		q := gen.New(tx)
		held, err := s.inv.ExtendInTx(ctx, q, holdID, exp, now)
		if err != nil {
			return err
		}
		amount := ev.PricePaise * int64(len(held.SeatNos))
		order, err = q.InsertOrder(ctx, gen.InsertOrderParams{EventID: held.EventID, UserID: userID, HoldID: holdID,
			SeatNos: held.SeatNos, AmountPaise: amount})
		if db.IsUniqueViolation(err, "") {
			return errConcurrentCreation
		}
		if err != nil {
			return fmt.Errorf("insert order: %w", err)
		}
		pay, err = q.InsertPayment(ctx, gen.InsertPaymentParams{OrderID: order.ID, Attempt: 1,
			PspIdempotencyKey: payments.CreateKey(order.ID, 1), AmountPaise: amount})
		if err != nil {
			return fmt.Errorf("insert payment: %w", err)
		}
		return nil
	})
	if errors.Is(err, errConcurrentCreation) || errors.Is(err, inventory.ErrHoldState) {
		// Another request created the order first (the hold is already
		// PAYMENT_PENDING): answer with that order.
		if o, gerr := s.q.GetOrderByHold(ctx, holdID); gerr == nil {
			return s.resume(ctx, o)
		}
	}
	if err != nil {
		return View{}, err
	}
	s.countOrder(gen.OrderStatusPENDINGPAYMENT)
	s.countPayment(gen.PaymentStatusCREATED)
	s.inv.MirrorExtend(ctx, order.EventID, holdID, exp)
	if err := s.ensureIntent(ctx, order, pay); err != nil {
		return View{}, err
	}
	return s.view(ctx, order.ID)
}

// resume answers a repeated create: make sure the current attempt has its
// PSP intent, then return the order.
func (s *Service) resume(ctx context.Context, o gen.Order) (View, error) {
	if o.Status == gen.OrderStatusPENDINGPAYMENT {
		pays, err := s.q.ListPaymentsByOrder(ctx, o.ID)
		if err != nil {
			return View{}, fmt.Errorf("list payments: %w", err)
		}
		if n := len(pays); n > 0 {
			if err := s.ensureIntent(ctx, o, pays[n-1]); err != nil {
				return View{}, err
			}
		}
	}
	return s.view(ctx, o.ID)
}

// ensureIntent creates the PSP intent for a CREATED attempt that has none,
// under the attempt's deterministic key, and stores its id. Calling it again
// after a failure (or a crash) replays the same create at the PSP.
func (s *Service) ensureIntent(ctx context.Context, o gen.Order, p gen.Payment) error {
	if p.ProviderIntentID != nil || p.Status != gen.PaymentStatusCREATED {
		return nil
	}
	in, err := s.psp.CreateIntent(ctx, p.PspIdempotencyKey, payments.CreateIntentRequest{
		AmountPaise: p.AmountPaise, Currency: o.Currency, MerchantRef: o.ID.String(),
		Metadata:  map[string]string{"event_id": o.EventID.String(), "attempt": strconv.Itoa(int(p.Attempt))},
		ReturnURL: s.returnURL(o.ID),
	})
	if err != nil {
		return fmt.Errorf("create intent: %w", err)
	}
	if _, err := s.q.SetPaymentIntent(ctx, gen.SetPaymentIntentParams{ProviderIntentID: &in.ID, ID: p.ID}); err != nil {
		return fmt.Errorf("store intent id: %w", err)
	}
	return nil
}

func (s *Service) returnURL(orderID uuid.UUID) string {
	if s.o.ReturnURL == "" {
		return ""
	}
	u, err := url.Parse(s.o.ReturnURL)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("o", orderID.String())
	u.RawQuery = q.Encode()
	return u.String()
}

// NewAttempt starts payment attempt n+1 (POST /orders/{id}/payments): only
// while the order is PENDING_PAYMENT with a live hold, only when the last
// attempt is FAILED or VOIDED, and at most MaxAttempts in total.
func (s *Service) NewAttempt(ctx context.Context, orderID, userID uuid.UUID) (View, error) {
	if _, err := s.owned(ctx, orderID, userID); err != nil {
		return View{}, err
	}
	var (
		order gen.Order
		pay   gen.Payment
	)
	err := db.WithTx(ctx, s.pg, func(tx pgx.Tx) error {
		q := gen.New(tx)
		var err error
		if order, err = q.GetOrderForUpdate(ctx, orderID); err != nil {
			return fmt.Errorf("lock order: %w", err)
		}
		if order.Status != gen.OrderStatusPENDINGPAYMENT {
			return fmt.Errorf("%w: order is %s", ErrOrderState, order.Status)
		}
		h, err := q.GetHold(ctx, order.HoldID)
		if err != nil {
			return fmt.Errorf("get hold: %w", err)
		}
		if !inventory.HoldStatus(h.Status).Live() || !h.ExpiresAt.After(s.o.Now()) {
			return fmt.Errorf("%w: hold %s", inventory.ErrHoldExpired, h.Status)
		}
		pays, err := q.ListPaymentsByOrder(ctx, orderID)
		if err != nil {
			return fmt.Errorf("list payments: %w", err)
		}
		last := pays[len(pays)-1]
		if last.Status != gen.PaymentStatusFAILED && last.Status != gen.PaymentStatusVOIDED {
			return fmt.Errorf("%w: attempt %d is %s", ErrOrderState, last.Attempt, last.Status)
		}
		if last.Attempt >= MaxAttempts {
			return ErrAttemptsExhausted
		}
		n := last.Attempt + 1
		pay, err = q.InsertPayment(ctx, gen.InsertPaymentParams{OrderID: orderID, Attempt: n,
			PspIdempotencyKey: payments.CreateKey(orderID, n), AmountPaise: order.AmountPaise})
		if err != nil {
			return fmt.Errorf("insert payment: %w", err)
		}
		return nil
	})
	if err != nil {
		return View{}, err
	}
	s.countPayment(gen.PaymentStatusCREATED)
	if err := s.ensureIntent(ctx, order, pay); err != nil {
		return View{}, err
	}
	return s.view(ctx, orderID)
}

// Pay confirms the current attempt at the PSP with a saved test method
// (key confirm:<intent>), then runs ConfirmOrder (POST /orders/{id}/pay).
// A terminal order is returned as it is, so a retry with a new key never
// charges again.
func (s *Service) Pay(ctx context.Context, orderID, userID uuid.UUID, method string) (View, error) {
	if !payments.ValidMethod(method) {
		return View{}, ErrInvalidMethod
	}
	o, err := s.owned(ctx, orderID, userID)
	if err != nil {
		return View{}, err
	}
	if o.Status != gen.OrderStatusPENDINGPAYMENT {
		return s.view(ctx, orderID)
	}
	pays, err := s.q.ListPaymentsByOrder(ctx, orderID)
	if err != nil {
		return View{}, fmt.Errorf("list payments: %w", err)
	}
	p := pays[len(pays)-1]
	if p.Status == gen.PaymentStatusCREATED {
		if err := s.ensureIntent(ctx, o, p); err != nil {
			return View{}, err
		}
		if p, err = s.q.GetPayment(ctx, p.ID); err != nil {
			return View{}, fmt.Errorf("get payment: %w", err)
		}
		if _, err := s.psp.Confirm(ctx, *p.ProviderIntentID, method); err != nil && !errors.Is(err, payments.ErrIntentState) {
			return View{}, err
		}
	}
	if err := s.ConfirmOrder(ctx, orderID); err != nil {
		return View{}, err
	}
	return s.view(ctx, orderID)
}

// Confirm pulls the PSP's view and runs ConfirmOrder (POST
// /orders/{id}/confirm, after the hosted checkout redirect or when polling).
func (s *Service) Confirm(ctx context.Context, orderID, userID uuid.UUID) (View, error) {
	o, err := s.owned(ctx, orderID, userID)
	if err != nil {
		return View{}, err
	}
	if o.Status == gen.OrderStatusPENDINGPAYMENT {
		if _, err := s.resume(ctx, o); err != nil {
			return View{}, err
		}
		if err := s.ConfirmOrder(ctx, orderID); err != nil {
			return View{}, err
		}
	}
	return s.view(ctx, orderID)
}
