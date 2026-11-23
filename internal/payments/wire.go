// Package payments is SLUICE's side of the PSP boundary: the MockPay wire
// types (API.md §4, §5), webhook signing and verification, and the PSP
// client with timeouts, jittered retries and deterministic idempotency keys.
package payments

import (
	"strconv"
	"time"

	"github.com/google/uuid"
)

// Intent statuses at the PSP (MockPay).
const (
	StatusRequiresPayment = "requires_payment"
	StatusAuthorized      = "authorized"
	StatusCaptured        = "captured"
	StatusCanceled        = "canceled"
	StatusFailed          = "failed"
	StatusRefunded        = "refunded"
)

// Test payment methods (API.md §3.6).
const (
	MethodOK      = "pm_ok"
	MethodDecline = "pm_decline"
	MethodTimeout = "pm_timeout"
	MethodSlow    = "pm_slow"
)

// ValidMethod reports whether m is one of the test payment methods.
func ValidMethod(m string) bool {
	switch m {
	case MethodOK, MethodDecline, MethodTimeout, MethodSlow:
		return true
	}
	return false
}

// Webhook event types (API.md §4).
const (
	EventAuthorized = "intent.authorized"
	EventFailed     = "intent.failed"
	EventCaptured   = "intent.captured"
	EventCanceled   = "intent.canceled"
	EventRefunded   = "intent.refunded"
)

// EventTypeFor maps an intent status to the webhook type that announces it.
func EventTypeFor(status string) string {
	switch status {
	case StatusAuthorized:
		return EventAuthorized
	case StatusFailed:
		return EventFailed
	case StatusCaptured:
		return EventCaptured
	case StatusCanceled:
		return EventCanceled
	case StatusRefunded:
		return EventRefunded
	}
	return ""
}

// Intent is a PSP payment intent as MockPay returns it.
type Intent struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	MerchantRef string            `json:"merchant_ref"`
	AmountPaise int64             `json:"amount_paise"`
	Currency    string            `json:"currency"`
	Metadata    map[string]string `json:"metadata"`
	CheckoutURL string            `json:"checkout_url,omitempty"`
	// Authorized is true once money was ever authorized on the intent (the
	// ledger has an AUTH entry), even if it was voided later.
	Authorized bool      `json:"authorized"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// CreateIntentRequest is the body of POST /v1/intents.
type CreateIntentRequest struct {
	AmountPaise   int64             `json:"amount_paise"`
	Currency      string            `json:"currency"`
	MerchantRef   string            `json:"merchant_ref"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	ReturnURL     string            `json:"return_url,omitempty"`
	Confirm       bool              `json:"confirm,omitempty"`
	PaymentMethod string            `json:"payment_method,omitempty"`
}

// ConfirmRequest is the body of POST /v1/intents/{id}/confirm.
type ConfirmRequest struct {
	PaymentMethod string `json:"payment_method"`
}

// WebhookEvent is the body MockPay posts to SLUICE (API.md §4).
type WebhookEvent struct {
	ID      string      `json:"id"`
	Type    string      `json:"type"`
	Created int64       `json:"created"`
	Data    WebhookData `json:"data"`
}

// WebhookData wraps the intent snapshot of a webhook.
type WebhookData struct {
	Intent Intent `json:"intent"`
}

// ErrorBody is MockPay's error response.
type ErrorBody struct {
	Error  ErrorDetail `json:"error"`
	Intent *Intent     `json:"intent,omitempty"`
}

// ErrorDetail is the code and message of an error response.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// MockPay error codes.
const (
	ErrCodeUnauthorized    = "unauthorized"
	ErrCodeInvalid         = "invalid_request"
	ErrCodeNotFound        = "not_found"
	ErrCodeIntentState     = "intent_state"
	ErrCodeIdempotencyUsed = "idempotency_key_reused"
	ErrCodeUnavailable     = "unavailable"
)

// LedgerSummaryRow is one merchant_ref in GET /v1/ledger/summary.
type LedgerSummaryRow struct {
	MerchantRef string `json:"merchant_ref"`
	Intents     int64  `json:"intents"`
	Authorized  int64  `json:"authorized"`
	Captured    int64  `json:"captured"`
	Voided      int64  `json:"voided"`
	Refunded    int64  `json:"refunded"`
}

// LedgerSummary is the body of GET /v1/ledger/summary.
type LedgerSummary struct {
	Refs []LedgerSummaryRow `json:"refs"`
}

// Deterministic PSP idempotency keys (API.md §5). A retry of the same
// operation always sends the same key, so the PSP returns the same result.

// CreateKey is the key for creating attempt n of an order's payment.
func CreateKey(orderID uuid.UUID, attempt int32) string {
	return "order:" + orderID.String() + ":attempt:" + strconv.Itoa(int(attempt))
}

// ConfirmKey is the key for confirming an intent with a saved method.
func ConfirmKey(intentID string) string { return "confirm:" + intentID }

// CaptureKey is the key for capturing an intent.
func CaptureKey(intentID string) string { return "capture:" + intentID }

// VoidKey is the key for voiding (canceling) an intent.
func VoidKey(intentID string) string { return "void:" + intentID }

// RefundKey is the key for refunding an intent.
func RefundKey(intentID string) string { return "refund:" + intentID }
