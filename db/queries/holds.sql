-- Holds. Transitions mirror the hold state machine in internal/inventory.

-- name: NextHoldFence :one
-- Fence source when HOLD_PATH=pg_only (Redis INCR otherwise).
SELECT nextval('hold_fence_seq')::bigint AS fence;

-- name: InsertHold :one
INSERT INTO holds (id, event_id, user_id, seat_nos, fence, expires_at)
VALUES (@id, @event_id, @user_id, @seat_nos::int[], @fence, @expires_at)
RETURNING *;

-- name: GetHold :one
SELECT * FROM holds WHERE id = $1;

-- name: GetHoldForUpdate :one
SELECT * FROM holds WHERE id = $1 FOR UPDATE;

-- name: GetLiveHoldForUser :one
SELECT * FROM holds
WHERE event_id = @event_id AND user_id = @user_id AND status IN ('ACTIVE', 'PAYMENT_PENDING');

-- name: MarkHoldPaymentPending :execrows
-- ACTIVE -> PAYMENT_PENDING (order created); only an unexpired hold moves.
UPDATE holds
SET status = 'PAYMENT_PENDING', expires_at = @expires_at, updated_at = now()
WHERE id = @id AND status = 'ACTIVE' AND expires_at > @now;

-- name: ConsumeHold :execrows
-- PAYMENT_PENDING -> CONSUMED (order confirmed).
UPDATE holds SET status = 'CONSUMED', updated_at = now()
WHERE id = @id AND status = 'PAYMENT_PENDING';

-- name: ReleaseHold :execrows
-- ACTIVE|PAYMENT_PENDING -> RELEASED (user released or the order failed).
UPDATE holds SET status = 'RELEASED', updated_at = now()
WHERE id = @id AND status IN ('ACTIVE', 'PAYMENT_PENDING');

-- name: ExpireHold :execrows
-- ACTIVE|PAYMENT_PENDING -> EXPIRED, only once expires_at has passed (I5).
UPDATE holds SET status = 'EXPIRED', updated_at = now()
WHERE id = @id AND status IN ('ACTIVE', 'PAYMENT_PENDING') AND expires_at <= @now;

-- name: ListExpiredHolds :many
-- Sweeper batch; SKIP LOCKED lets several workers share the work.
SELECT id, event_id, seat_nos, fence, expires_at FROM holds
WHERE status IN ('ACTIVE', 'PAYMENT_PENDING') AND expires_at <= @now
ORDER BY expires_at
LIMIT @batch_size
FOR UPDATE SKIP LOCKED;

-- name: MaxHoldFence :one
-- Floor for the Redis fence counter after a rebuild (fences never go back).
SELECT coalesce(max(fence), 0)::bigint AS fence FROM holds WHERE event_id = $1;

-- name: ListLiveHoldsByEvent :many
SELECT id, user_id, seat_nos, status, fence, expires_at FROM holds
WHERE event_id = $1 AND status IN ('ACTIVE', 'PAYMENT_PENDING')
ORDER BY id;

-- name: GetHoldsByIDs :many
-- Sweeper: what PG says about holds found in the Redis expiry index.
SELECT id, status, expires_at FROM holds WHERE id = ANY(@ids::uuid[]);
