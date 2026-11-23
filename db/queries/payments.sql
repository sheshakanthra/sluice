-- Payments. Transitions mirror the payment state machine in internal/orders;
-- the partial unique index payments_one_live_per_order is the I2 backstop.

-- name: InsertPayment :one
INSERT INTO payments (order_id, attempt, psp_idempotency_key, amount_paise)
VALUES (@order_id, @attempt, @psp_idempotency_key, @amount_paise)
RETURNING *;

-- name: GetPayment :one
SELECT * FROM payments WHERE id = $1;

-- name: GetPaymentByIntent :one
SELECT * FROM payments WHERE provider_intent_id = $1;

-- name: ListPaymentsByOrder :many
SELECT * FROM payments WHERE order_id = $1 ORDER BY attempt;

-- name: SetPaymentIntent :execrows
UPDATE payments SET provider_intent_id = @provider_intent_id, updated_at = now()
WHERE id = @id AND status = 'CREATED' AND provider_intent_id IS NULL;

-- name: AuthorizePayment :execrows
-- CREATED -> AUTHORIZED.
UPDATE payments SET status = 'AUTHORIZED', updated_at = now()
WHERE id = @id AND status = 'CREATED';

-- name: CapturePayment :execrows
-- AUTHORIZED -> CAPTURED.
UPDATE payments SET status = 'CAPTURED', updated_at = now()
WHERE id = @id AND status = 'AUTHORIZED';

-- name: VoidPayment :execrows
-- AUTHORIZED -> VOIDED, or CREATED -> VOIDED when the PSP had authorized the
-- intent but SLUICE never recorded it before voiding (ADR 0007).
UPDATE payments SET status = 'VOIDED', updated_at = now()
WHERE id = @id AND status IN ('CREATED', 'AUTHORIZED');

-- name: FailPayment :execrows
-- CREATED -> FAILED (declined).
UPDATE payments SET status = 'FAILED', updated_at = now()
WHERE id = @id AND status = 'CREATED';

-- name: RefundPayment :execrows
-- CAPTURED -> REFUNDED.
UPDATE payments SET status = 'REFUNDED', updated_at = now()
WHERE id = @id AND status = 'CAPTURED';

-- name: GetLivePayment :one
-- The order's AUTHORIZED or CAPTURED payment (at most one: I2 index).
SELECT * FROM payments WHERE order_id = $1 AND status IN ('AUTHORIZED', 'CAPTURED');
