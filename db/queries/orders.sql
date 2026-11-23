-- Orders. Transitions mirror the order state machine in internal/orders.

-- name: InsertOrder :one
INSERT INTO orders (event_id, user_id, hold_id, seat_nos, amount_paise)
VALUES (@event_id, @user_id, @hold_id, @seat_nos::int[], @amount_paise)
RETURNING *;

-- name: GetOrder :one
SELECT * FROM orders WHERE id = $1;

-- name: GetOrderForUpdate :one
SELECT * FROM orders WHERE id = $1 FOR UPDATE;

-- name: GetOrderByHold :one
SELECT * FROM orders WHERE hold_id = $1;

-- name: ConfirmOrder :execrows
-- PENDING_PAYMENT -> CONFIRMED.
UPDATE orders SET status = 'CONFIRMED', confirmed_at = now(), updated_at = now()
WHERE id = @id AND status = 'PENDING_PAYMENT';

-- name: FailOrder :execrows
-- PENDING_PAYMENT -> FAILED (hold lost, quota, attempts exhausted).
UPDATE orders SET status = 'FAILED', failure_code = @failure_code, updated_at = now()
WHERE id = @id AND status = 'PENDING_PAYMENT';

-- name: ExpireOrder :execrows
-- PENDING_PAYMENT -> EXPIRED (hold expired).
UPDATE orders SET status = 'EXPIRED', updated_at = now()
WHERE id = @id AND status = 'PENDING_PAYMENT';

-- name: RefundOrder :execrows
-- CONFIRMED -> REFUNDED (admin only).
UPDATE orders SET status = 'REFUNDED', updated_at = now()
WHERE id = @id AND status = 'CONFIRMED';
