-- BASELINE ONLY (LOADTEST §4 B0/B1, ADR 0011). Queries for the deliberately
-- unsafe baselines in internal/inventory/naive and internal/payments/naive.
-- They have no locks, no constraints and no transactions on purpose.

-- name: NaiveSoldSeats :many
-- Every seat number sold so far (duplicates included).
SELECT seat_no FROM naive_sales WHERE event_id = $1;

-- name: NaiveInsertOrder :exec
INSERT INTO naive_orders (order_ref, event_id, user_id, seat_nos, amount_paise)
VALUES ($1, $2, $3, $4, $5);

-- name: NaiveGetOrder :one
SELECT * FROM naive_orders WHERE order_ref = $1;

-- name: NaiveTakenSeats :many
-- The check half of check-then-insert: seats of this order already sold to
-- another order.
SELECT DISTINCT seat_no FROM naive_sales
WHERE event_id = $1 AND seat_no = ANY(sqlc.arg(seat_nos)::int[]) AND order_ref <> $2;

-- name: NaiveInsertSales :exec
-- The insert half, as a separate statement: nothing stops a second buyer
-- between the check and this insert.
INSERT INTO naive_sales (event_id, seat_no, order_ref, user_id)
SELECT $1, unnest(sqlc.arg(seat_nos)::int[]), $2, $3;

-- name: NaiveDeleteSales :exec
DELETE FROM naive_sales WHERE order_ref = $1;

-- name: NaiveSetOrderStatus :exec
UPDATE naive_orders SET status = $2, pay_status = $3 WHERE order_ref = $1;

-- name: NaiveNewAttempt :one
UPDATE naive_orders SET attempts = attempts + 1, pay_status = 'CREATED', status = 'PENDING_PAYMENT'
WHERE order_ref = $1 AND attempts < 3
RETURNING *;

-- name: NaiveRelinkPayment :execrows
-- holds_only (B1): point the order's open attempt at the charge the naive
-- /pay just made, whatever the attempt pointed at before.
UPDATE payments SET provider_intent_id = $2, updated_at = now()
WHERE order_id = $1 AND status = 'CREATED';
