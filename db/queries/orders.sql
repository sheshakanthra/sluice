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

-- name: GetOrderByHoldSkipLocked :one
-- Reclaim hook (sweeper, PG takeover): lock the hold's order without
-- waiting. No row means there is no order, or ConfirmOrder holds it and
-- will itself see the reclaimed hold and fail the order (no deadlock).
SELECT * FROM orders WHERE hold_id = $1 FOR UPDATE SKIP LOCKED;

-- name: ListPendingOrdersWithDeadHold :many
-- Reconciler: PENDING_PAYMENT orders whose hold was released or reclaimed.
SELECT o.id FROM orders o JOIN holds h ON h.id = o.hold_id
WHERE o.status = 'PENDING_PAYMENT' AND h.status IN ('EXPIRED', 'RELEASED')
ORDER BY o.created_at
LIMIT @batch_size;

-- name: ListAuthorizedConfirmedPayments :many
-- Reconciler: confirmed orders whose payment is still only AUTHORIZED.
SELECT p.* FROM payments p JOIN orders o ON o.id = p.order_id
WHERE o.status = 'CONFIRMED' AND p.status = 'AUTHORIZED' AND p.updated_at < @before
ORDER BY p.updated_at
LIMIT @batch_size;

-- name: AuditCounts :one
-- Live audit cache (every 5 s per ON_SALE event): oversold seats, orders
-- with more than one captured payment, and live payments on orders that are
-- not CONFIRMED.
SELECT
  ((SELECT count(*) FROM (SELECT t.seat_no FROM tickets t WHERE t.event_id = @event_id
                          GROUP BY t.seat_no HAVING count(*) > 1) x)
   + GREATEST((SELECT count(*) FROM tickets t WHERE t.event_id = @event_id)
              - (SELECT e.capacity FROM events e WHERE e.id = @event_id)::bigint, 0))::bigint AS oversold,
  (SELECT count(*) FROM (SELECT p.order_id FROM payments p JOIN orders o ON o.id = p.order_id
                         WHERE o.event_id = @event_id AND p.status = 'CAPTURED'
                         GROUP BY p.order_id HAVING count(*) > 1) d)::bigint AS duplicates,
  (SELECT count(*) FROM payments p JOIN orders o ON o.id = p.order_id
   WHERE o.event_id = @event_id AND p.status IN ('AUTHORIZED', 'CAPTURED') AND o.status <> 'CONFIRMED')::bigint AS orphans,
  (SELECT count(*) FROM tickets t WHERE t.event_id = @event_id)::bigint AS tickets;
