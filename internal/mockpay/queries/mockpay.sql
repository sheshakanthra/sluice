-- MockPay queries (sqlc -> internal/mockpay/mpdb).

-- name: UpsertMerchant :exec
INSERT INTO merchants (id, api_key_hash, webhook_url, webhook_secret)
VALUES (@id, @api_key_hash, @webhook_url, @webhook_secret)
ON CONFLICT (id) DO UPDATE
SET api_key_hash = EXCLUDED.api_key_hash, webhook_url = EXCLUDED.webhook_url,
    webhook_secret = EXCLUDED.webhook_secret;

-- name: GetMerchant :one
SELECT * FROM merchants WHERE id = $1;

-- name: LockKey :exec
-- Serialises requests that share an idempotency key (or an intent) for the
-- rest of the transaction. hashtextextended gives a 64-bit advisory key.
SELECT pg_advisory_xact_lock(hashtextextended(@k::text, 0));

-- name: GetIdempotency :one
SELECT * FROM idempotency WHERE merchant_id = @merchant_id AND key = @key;

-- name: InsertIdempotency :exec
INSERT INTO idempotency (merchant_id, key, request_hash, response_code, response_body)
VALUES (@merchant_id, @key, @request_hash, @response_code, @response_body);

-- name: InsertIntent :one
INSERT INTO intents (id, merchant_id, idempotency_key, request_hash, merchant_ref, metadata,
                     amount_paise, currency, status, return_url)
VALUES (@id, @merchant_id, @idempotency_key, @request_hash, @merchant_ref, @metadata,
        @amount_paise, @currency, @status, @return_url)
RETURNING *;

-- name: GetIntent :one
SELECT * FROM intents WHERE id = $1;

-- name: GetIntentForUpdate :one
SELECT * FROM intents WHERE id = $1 FOR UPDATE;

-- name: SetIntentStatus :exec
UPDATE intents SET status = @status, updated_at = now() WHERE id = @id;

-- name: IntentAuthorized :one
-- Whether money was ever authorized on the intent (ledger has an AUTH).
SELECT EXISTS (SELECT 1 FROM ledger_entries WHERE intent_id = $1 AND kind = 'AUTH') AS authorized;

-- name: ListIntents :many
-- The reconciler's view. Every filter is optional.
SELECT i.*, EXISTS (SELECT 1 FROM ledger_entries l WHERE l.intent_id = i.id AND l.kind = 'AUTH')::bool AS authorized
FROM intents i
WHERE i.merchant_id = @merchant_id
  AND (sqlc.narg('merchant_ref')::text IS NULL OR i.merchant_ref = sqlc.narg('merchant_ref')::text)
  AND (sqlc.narg('status')::text IS NULL OR i.status = sqlc.narg('status')::text)
  AND (sqlc.narg('updated_since')::timestamptz IS NULL OR i.updated_at >= sqlc.narg('updated_since')::timestamptz)
ORDER BY i.updated_at, i.id
LIMIT @max_rows;

-- name: InsertLedger :execrows
-- 0 rows: this kind of movement already exists for the intent.
INSERT INTO ledger_entries (intent_id, kind, amount_paise, idempotency_key)
VALUES (@intent_id, @kind, @amount_paise, @idempotency_key)
ON CONFLICT (intent_id, kind) DO NOTHING;

-- name: LedgerSummary :many
-- Per merchant_ref (SLUICE order): intents and money movements, for the auditor.
SELECT i.merchant_ref,
       count(DISTINCT i.id)::bigint AS intents,
       count(l.id) FILTER (WHERE l.kind = 'AUTH')::bigint AS authorized,
       count(l.id) FILTER (WHERE l.kind = 'CAPTURE')::bigint AS captured,
       count(l.id) FILTER (WHERE l.kind = 'VOID')::bigint AS voided,
       count(l.id) FILTER (WHERE l.kind = 'REFUND')::bigint AS refunded
FROM intents i
LEFT JOIN ledger_entries l ON l.intent_id = i.id
WHERE i.merchant_id = @merchant_id
  AND (sqlc.narg('event_id')::text IS NULL OR i.metadata ->> 'event_id' = sqlc.narg('event_id')::text)
  AND (sqlc.narg('since')::timestamptz IS NULL OR i.created_at >= sqlc.narg('since')::timestamptz)
GROUP BY i.merchant_ref
ORDER BY i.merchant_ref;

-- name: ListLedgerByIntent :many
SELECT * FROM ledger_entries WHERE intent_id = $1 ORDER BY id;

-- name: InsertDelivery :exec
INSERT INTO webhook_deliveries (event_id, intent_id, type, payload, next_attempt_at)
VALUES (@event_id, @intent_id, @type, @payload, @next_attempt_at);

-- name: ClaimDueDeliveries :many
-- Leases due deliveries to this dispatcher: SKIP LOCKED lets several
-- dispatchers share the table, and the lease (next_attempt_at in the future)
-- keeps a row from being sent twice while its HTTP call is in flight.
UPDATE webhook_deliveries d SET next_attempt_at = @lease_until
WHERE d.id IN (
  SELECT w.id FROM webhook_deliveries w
  WHERE w.delivered_at IS NULL AND w.attempt < @max_attempts AND w.next_attempt_at <= @now
  ORDER BY w.next_attempt_at, w.id
  LIMIT @batch_size
  FOR UPDATE SKIP LOCKED
)
RETURNING d.*;

-- name: MarkDelivered :exec
UPDATE webhook_deliveries SET delivered_at = now(), attempt = attempt + 1, last_error = NULL WHERE id = $1;

-- name: MarkDeliveryFailed :exec
UPDATE webhook_deliveries SET attempt = attempt + 1, next_attempt_at = @next_attempt_at, last_error = @last_error
WHERE id = @id;

-- name: ListPendingDeliveries :many
SELECT * FROM webhook_deliveries WHERE delivered_at IS NULL ORDER BY id;
