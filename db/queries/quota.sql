-- Per-user quota. The quota_cap CHECK enforces I4 inside the confirm tx.

-- name: AddQuota :one
-- Adds n purchased seats; a quota_cap violation aborts the transaction.
INSERT INTO event_user_quota (event_id, user_id, seats_purchased, max_allowed)
VALUES (@event_id, @user_id, @seats, @max_allowed)
ON CONFLICT (event_id, user_id)
DO UPDATE SET seats_purchased = event_user_quota.seats_purchased + EXCLUDED.seats_purchased
RETURNING *;

-- name: ReleaseQuota :execrows
-- Gives n seats back after a refund (seats_purchased >= 0 is checked).
UPDATE event_user_quota SET seats_purchased = seats_purchased - @seats
WHERE event_id = @event_id AND user_id = @user_id;

-- name: GetQuota :one
SELECT * FROM event_user_quota WHERE event_id = @event_id AND user_id = @user_id;
