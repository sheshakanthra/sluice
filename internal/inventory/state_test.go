package inventory

import (
	"testing"

	"github.com/sheshakanthra/sluice/internal/fsm"
	"github.com/sheshakanthra/sluice/internal/fsm/fsmtest"
)

// The allowed edges below are copied by hand from docs/DATA-MODEL.md section 3,
// independently of the tables in state.go.

func TestSeatStateMachine(t *testing.T) {
	fsmtest.Run(t, fsmtest.Spec[SeatState]{
		Machine: SeatMachine,
		States:  []SeatState{"AVAILABLE", "HELD", "SOLD"},
		Allowed: []fsm.Edge[SeatState]{
			{From: "AVAILABLE", To: "HELD"},
			{From: "HELD", To: "SOLD"},
			{From: "HELD", To: "AVAILABLE"},
		},
		Initial: SeatAvailable,
		Events: []fsmtest.Event[SeatState]{
			{Name: "Hold", To: SeatHeld, Fn: SeatState.Hold},
			{Name: "Confirm", To: SeatSold, Fn: SeatState.Confirm},
			{Name: "Release", To: SeatAvailable, Fn: SeatState.Release},
		},
	})
}

func TestHoldStateMachine(t *testing.T) {
	fsmtest.Run(t, fsmtest.Spec[HoldStatus]{
		Machine: HoldMachine,
		States:  []HoldStatus{"ACTIVE", "PAYMENT_PENDING", "CONSUMED", "RELEASED", "EXPIRED"},
		Allowed: []fsm.Edge[HoldStatus]{
			{From: "ACTIVE", To: "PAYMENT_PENDING"},
			{From: "PAYMENT_PENDING", To: "CONSUMED"},
			{From: "ACTIVE", To: "RELEASED"},
			{From: "PAYMENT_PENDING", To: "RELEASED"},
			{From: "ACTIVE", To: "EXPIRED"},
			{From: "PAYMENT_PENDING", To: "EXPIRED"},
		},
		Initial: HoldActive,
		Events: []fsmtest.Event[HoldStatus]{
			{Name: "OrderCreated", To: HoldPaymentPending, Fn: HoldStatus.OrderCreated},
			{Name: "Confirm", To: HoldConsumed, Fn: HoldStatus.Confirm},
			{Name: "Release", To: HoldReleased, Fn: HoldStatus.Release},
			{Name: "Reclaim", To: HoldExpired, Fn: HoldStatus.Reclaim},
		},
	})
}

func TestHoldLive(t *testing.T) {
	tests := []struct {
		s    HoldStatus
		live bool
	}{
		{HoldActive, true},
		{HoldPaymentPending, true},
		{HoldConsumed, false},
		{HoldReleased, false},
		{HoldExpired, false},
	}
	for _, tt := range tests {
		if got := tt.s.Live(); got != tt.live {
			t.Errorf("%s.Live() = %v, want %v", tt.s, got, tt.live)
		}
	}
}

func TestTerminalStates(t *testing.T) {
	for _, s := range []HoldStatus{HoldConsumed, HoldReleased, HoldExpired} {
		if !HoldMachine.Terminal(s) {
			t.Errorf("hold %s should be terminal", s)
		}
	}
	if !SeatMachine.Terminal(SeatSold) {
		t.Error("seat SOLD should be terminal (no refund path frees a seat in v1)")
	}
}
