// Package orders owns orders and payments. This file holds the pure order and
// payment state machines from docs/DATA-MODEL.md section 3; ConfirmOrder and
// the quota logic arrive in P3.
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
var PaymentMachine = fsm.New("payment",
	[]PaymentStatus{PaymentCreated, PaymentAuthorized, PaymentCaptured, PaymentVoided, PaymentFailed, PaymentRefunded},
	[]fsm.Edge[PaymentStatus]{
		{From: PaymentCreated, To: PaymentAuthorized},
		{From: PaymentAuthorized, To: PaymentCaptured},
		{From: PaymentCaptured, To: PaymentRefunded},
		{From: PaymentCreated, To: PaymentFailed},
		{From: PaymentAuthorized, To: PaymentVoided},
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

// Void is AUTHORIZED -> VOIDED.
func (p PaymentStatus) Void() (PaymentStatus, error) {
	return PaymentMachine.Transition(p, PaymentVoided)
}
