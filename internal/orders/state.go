// Package orders owns orders and payments: order creation, the PSP calls,
// ConfirmOrder (the single convergence point, ARCHITECTURE §4.3), the
// webhook inbox, the payment jobs and the reconciler. This file holds the
// pure order and payment state machines from docs/DATA-MODEL.md section 3.
package orders

import "github.com/sheshakanthra/sluice/internal/fsm"

// OrderStatus mirrors the Postgres enum order_status.
type OrderStatus string

// Order statuses.
const (
	OrderPendingPayment OrderStatus = "PENDING_PAYMENT"
	OrderConfirmed      OrderStatus = "CONFIRMED"
	OrderFailed         OrderStatus = "FAILED"
	OrderExpired        OrderStatus = "EXPIRED"
	OrderRefunded       OrderStatus = "REFUNDED"
)

// PaymentStatus mirrors the Postgres enum payment_status.
type PaymentStatus string

// Payment statuses.
const (
	PaymentCreated    PaymentStatus = "CREATED"
	PaymentAuthorized PaymentStatus = "AUTHORIZED"
	PaymentCaptured   PaymentStatus = "CAPTURED"
	PaymentVoided     PaymentStatus = "VOIDED"
	PaymentFailed     PaymentStatus = "FAILED"
	PaymentRefunded   PaymentStatus = "REFUNDED"
)

// OrderMachine:
//
//	PENDING_PAYMENT -confirm-> CONFIRMED
//	PENDING_PAYMENT -hold lost / quota / 3 failed attempts-> FAILED
//	PENDING_PAYMENT -hold expired-> EXPIRED
//	CONFIRMED -admin refund-> REFUNDED
var OrderMachine = fsm.New("order",
	[]OrderStatus{OrderPendingPayment, OrderConfirmed, OrderFailed, OrderExpired, OrderRefunded},
	[]fsm.Edge[OrderStatus]{
		{From: OrderPendingPayment, To: OrderConfirmed},
		{From: OrderPendingPayment, To: OrderFailed},
		{From: OrderPendingPayment, To: OrderExpired},
		{From: OrderConfirmed, To: OrderRefunded},
	})

// PaymentMachine:
//
//	CREATED -authorized-> AUTHORIZED -captured-> CAPTURED -refund-> REFUNDED
//	CREATED -declined-> FAILED
//	AUTHORIZED -void-> VOIDED
//	CREATED -void-> VOIDED (the PSP authorized it, SLUICE voided it before
//	                        recording the authorization; ADR 0007)
var PaymentMachine = fsm.New("payment",
	[]PaymentStatus{PaymentCreated, PaymentAuthorized, PaymentCaptured, PaymentVoided, PaymentFailed, PaymentRefunded},
	[]fsm.Edge[PaymentStatus]{
		{From: PaymentCreated, To: PaymentAuthorized},
		{From: PaymentAuthorized, To: PaymentCaptured},
		{From: PaymentCaptured, To: PaymentRefunded},
		{From: PaymentCreated, To: PaymentFailed},
		{From: PaymentAuthorized, To: PaymentVoided},
		{From: PaymentCreated, To: PaymentVoided},
	})

// CanTransition reports whether an order may move from -> to.
func (o OrderStatus) CanTransition(to OrderStatus) bool { return OrderMachine.CanTransition(o, to) }

// Confirm is PENDING_PAYMENT -> CONFIRMED.
func (o OrderStatus) Confirm() (OrderStatus, error) {
	return OrderMachine.Transition(o, OrderConfirmed)
}

// Fail is PENDING_PAYMENT -> FAILED.
func (o OrderStatus) Fail() (OrderStatus, error) { return OrderMachine.Transition(o, OrderFailed) }

// Expire is PENDING_PAYMENT -> EXPIRED.
func (o OrderStatus) Expire() (OrderStatus, error) { return OrderMachine.Transition(o, OrderExpired) }

// Refund is CONFIRMED -> REFUNDED.
func (o OrderStatus) Refund() (OrderStatus, error) {
	return OrderMachine.Transition(o, OrderRefunded)
}

// CanTransition reports whether a payment may move from -> to.
func (p PaymentStatus) CanTransition(to PaymentStatus) bool {
	return PaymentMachine.CanTransition(p, to)
}

// Live reports whether the payment holds the customer's money (I2: at most one
// live payment per order).
func (p PaymentStatus) Live() bool { return p == PaymentAuthorized || p == PaymentCaptured }

// Authorize is CREATED -> AUTHORIZED.
func (p PaymentStatus) Authorize() (PaymentStatus, error) {
	return PaymentMachine.Transition(p, PaymentAuthorized)
}

// Capture is AUTHORIZED -> CAPTURED.
func (p PaymentStatus) Capture() (PaymentStatus, error) {
	return PaymentMachine.Transition(p, PaymentCaptured)
}

// Refund is CAPTURED -> REFUNDED.
func (p PaymentStatus) Refund() (PaymentStatus, error) {
	return PaymentMachine.Transition(p, PaymentRefunded)
}

// Decline is CREATED -> FAILED.
func (p PaymentStatus) Decline() (PaymentStatus, error) {
	return PaymentMachine.Transition(p, PaymentFailed)
}

// Rank orders payment states for webhook handling: an event whose state
// ranks below the payment's current state is a regression and is ignored
// (DATA-MODEL §3: transitions only move forward).
func (p PaymentStatus) Rank() int {
	switch p {
	case PaymentCreated:
		return 0
	case PaymentAuthorized, PaymentFailed:
		return 1
	case PaymentCaptured, PaymentVoided:
		return 2
	case PaymentRefunded:
		return 3
	}
	return -1
}

// Void is AUTHORIZED -> VOIDED (or CREATED -> VOIDED, see PaymentMachine).
func (p PaymentStatus) Void() (PaymentStatus, error) {
	return PaymentMachine.Transition(p, PaymentVoided)
}
