-- Client idempotency keys (API.md section 1.2).

-- name: InsertIdempotencyKey :execrows
-- 0 rows means the key already exists; the caller then reads it.
INSERT INTO idempotency_keys (user_id, key, method, route, request_hash, status, locked_until)
VALUES (@user_id, @key, @method, @route, @request_hash, 'STARTED', @locked_until)
ON CONFLICT (user_id, key) DO NOTHING;

-- name: GetIdempotencyKey :one
SELECT * FROM idempotency_keys WHERE user_id = @user_id AND key = @key;

-- name: ReclaimIdempotencyKey :execrows
-- A STARTED key whose lock lapsed (crash) becomes re-executable.
UPDATE idempotency_keys SET locked_until = @locked_until
WHERE user_id = @user_id AND key = @key AND status = 'STARTED' AND locked_until < @now;

-- name: CompleteIdempotencyKey :execrows
UPDATE idempotency_keys
SET status = 'COMPLETED', response_code = @response_code, response_body = @response_body
WHERE user_id = @user_id AND key = @key AND status = 'STARTED';

-- name: DeleteIdempotencyKey :execrows
-- Drops a STARTED key whose request failed before doing anything durable.
DELETE FROM idempotency_keys WHERE user_id = @user_id AND key = @key AND status = 'STARTED';

-- name: GCIdempotencyKeys :execrows
DELETE FROM idempotency_keys WHERE created_at < @before;
