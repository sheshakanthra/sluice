package orders

import (
	"testing"

	"github.com/sheshakanthra/sluice/internal/fsm"
	"github.com/sheshakanthra/sluice/internal/fsm/fsmtest"
)

// The allowed edges below are copied by hand from docs/DATA-MODEL.md section 3,
// independently of the tables in state.go.

func TestOrderStateMachine(t *testing.T) {
	fsmtest.Run(t, fsmtest.Spec[OrderStatus]{
		Machine: OrderMachine,
		States:  []OrderStatus{"PENDING_PAYMENT", "CONFIRMED", "FAILED", "EXPIRED", "REFUNDED"},
		Allowed: []fsm.Edge[OrderStatus]{
			{From: "PENDING_PAYMENT", To: "CONFIRMED"},
			{From: "PENDING_PAYMENT", To: "FAILED"},
			{From: "PENDING_PAYMENT", To: "EXPIRED"},
			{From: "CONFIRMED", To: "REFUNDED"},
		},
		Initial: OrderPendingPayment,
		Events: []fsmtest.Event[OrderStatus]{
			{Name: "Confirm", To: OrderConfirmed, Fn: OrderStatus.Confirm},
			{Name: "Fail", To: OrderFailed, Fn: OrderStatus.Fail},
			{Name: "Expire", To: OrderExpired, Fn: OrderStatus.Expire},
			{Name: "Refund", To: OrderRefunded, Fn: OrderStatus.Refund},
		},
	})
}

func TestPaymentStateMachine(t *testing.T) {
	fsmtest.Run(t, fsmtest.Spec[PaymentStatus]{
		Machine: PaymentMachine,
		States:  []PaymentStatus{"CREATED", "AUTHORIZED", "CAPTURED", "VOIDED", "FAILED", "REFUNDED"},
		Allowed: []fsm.Edge[PaymentStatus]{
			{From: "CREATED", To: "AUTHORIZED"},
			{From: "AUTHORIZED", To: "CAPTURED"},
			{From: "CAPTURED", To: "REFUNDED"},
			{From: "CREATED", To: "FAILED"},
			{From: "AUTHORIZED", To: "VOIDED"},
			{From: "CREATED", To: "VOIDED"}, // authorized at the PSP, voided before SLUICE recorded it (ADR 0007)
		},
		Initial: PaymentCreated,
		Events: []fsmtest.Event[PaymentStatus]{
			{Name: "Authorize", To: PaymentAuthorized, Fn: PaymentStatus.Authorize},
			{Name: "Capture", To: PaymentCaptured, Fn: PaymentStatus.Capture},
			{Name: "Refund", To: PaymentRefunded, Fn: PaymentStatus.Refund},
			{Name: "Decline", To: PaymentFailed, Fn: PaymentStatus.Decline},
			{Name: "Void", To: PaymentVoided, Fn: PaymentStatus.Void},
		},
	})
}

// Live must match the partial unique index payments_one_live_per_order
// (status IN ('AUTHORIZED','CAPTURED')).
func TestPaymentLive(t *testing.T) {
	want := map[PaymentStatus]bool{
		PaymentCreated: false, PaymentAuthorized: true, PaymentCaptured: true,
		PaymentVoided: false, PaymentFailed: false, PaymentRefunded: false,
	}
	for _, s := range PaymentMachine.States() {
		if got := s.Live(); got != want[s] {
			t.Errorf("%s.Live() = %v, want %v", s, got, want[s])
		}
	}
}

// Regressions (for example captured -> authorized from a reordered webhook)
// are never allowed by the table; P3 ignores and counts them.
func TestPaymentNoRegressions(t *testing.T) {
	for _, e := range []fsm.Edge[PaymentStatus]{
		{From: PaymentCaptured, To: PaymentAuthorized},
		{From: PaymentAuthorized, To: PaymentCreated},
		{From: PaymentRefunded, To: PaymentCaptured},
		{From: PaymentVoided, To: PaymentAuthorized},
	} {
		if PaymentMachine.CanTransition(e.From, e.To) {
			t.Errorf("regression %s -> %s must be rejected", e.From, e.To)
		}
	}
}
