// Package inventory owns seats and holds. This file holds the pure seat and
// hold state machines from docs/DATA-MODEL.md section 3; the Redis fast path
// and the Postgres hold flow arrive in P2.
package inventory

import "github.com/sheshakanthra/sluice/internal/fsm"

// SeatState mirrors the Postgres enum seat_state.
type SeatState string

// Seat states.
const (
	SeatAvailable SeatState = "AVAILABLE"
	SeatHeld      SeatState = "HELD"
	SeatSold      SeatState = "SOLD"
)

// HoldStatus mirrors the Postgres enum hold_status.
type HoldStatus string

// Hold statuses.
const (
	HoldActive         HoldStatus = "ACTIVE"
	HoldPaymentPending HoldStatus = "PAYMENT_PENDING"
	HoldConsumed       HoldStatus = "CONSUMED"
	HoldReleased       HoldStatus = "RELEASED"
	HoldExpired        HoldStatus = "EXPIRED"
)

// SeatMachine:
//
//	AVAILABLE -hold-> HELD -confirm-> SOLD
//	HELD -release/reclaim-> AVAILABLE
var SeatMachine = fsm.New("seat",
	[]SeatState{SeatAvailable, SeatHeld, SeatSold},
	[]fsm.Edge[SeatState]{
		{From: SeatAvailable, To: SeatHeld},
		{From: SeatHeld, To: SeatSold},
		{From: SeatHeld, To: SeatAvailable},
	})

// HoldMachine:
//
//	ACTIVE -order created-> PAYMENT_PENDING -confirm-> CONSUMED
//	ACTIVE|PAYMENT_PENDING -release-> RELEASED
//	ACTIVE|PAYMENT_PENDING -reclaim (expired)-> EXPIRED
var HoldMachine = fsm.New("hold",
	[]HoldStatus{HoldActive, HoldPaymentPending, HoldConsumed, HoldReleased, HoldExpired},
	[]fsm.Edge[HoldStatus]{
		{From: HoldActive, To: HoldPaymentPending},
		{From: HoldPaymentPending, To: HoldConsumed},
		{From: HoldActive, To: HoldReleased},
		{From: HoldPaymentPending, To: HoldReleased},
		{From: HoldActive, To: HoldExpired},
		{From: HoldPaymentPending, To: HoldExpired},
	})

// CanTransition reports whether a seat may move from -> to.
func (s SeatState) CanTransition(to SeatState) bool { return SeatMachine.CanTransition(s, to) }

// Hold is AVAILABLE -> HELD.
func (s SeatState) Hold() (SeatState, error) { return SeatMachine.Transition(s, SeatHeld) }

// Confirm is HELD -> SOLD.
func (s SeatState) Confirm() (SeatState, error) { return SeatMachine.Transition(s, SeatSold) }

// Release is HELD -> AVAILABLE (user release or sweeper reclaim).
func (s SeatState) Release() (SeatState, error) { return SeatMachine.Transition(s, SeatAvailable) }

// CanTransition reports whether a hold may move from -> to.
func (h HoldStatus) CanTransition(to HoldStatus) bool { return HoldMachine.CanTransition(h, to) }

// Live reports whether the hold still owns its seats (ACTIVE or PAYMENT_PENDING).
func (h HoldStatus) Live() bool { return h == HoldActive || h == HoldPaymentPending }

// OrderCreated is ACTIVE -> PAYMENT_PENDING.
func (h HoldStatus) OrderCreated() (HoldStatus, error) {
	return HoldMachine.Transition(h, HoldPaymentPending)
}

// Confirm is PAYMENT_PENDING -> CONSUMED.
func (h HoldStatus) Confirm() (HoldStatus, error) { return HoldMachine.Transition(h, HoldConsumed) }

// Release is ACTIVE|PAYMENT_PENDING -> RELEASED.
func (h HoldStatus) Release() (HoldStatus, error) { return HoldMachine.Transition(h, HoldReleased) }

// Reclaim is ACTIVE|PAYMENT_PENDING -> EXPIRED (the hold timed out).
func (h HoldStatus) Reclaim() (HoldStatus, error) { return HoldMachine.Transition(h, HoldExpired) }
