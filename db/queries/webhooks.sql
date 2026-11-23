-- Webhook inbox: the primary key (provider, event_id) dedupes deliveries.

-- name: InsertWebhookEvent :execrows
-- 0 rows means a duplicate delivery.
INSERT INTO webhook_events (provider, event_id, type, payload)
VALUES (@provider, @event_id, @type, @payload)
ON CONFLICT (provider, event_id) DO NOTHING;

-- name: MarkWebhookProcessed :execrows
UPDATE webhook_events SET processed_at = now()
WHERE provider = @provider AND event_id = @event_id AND processed_at IS NULL;

-- name: ListUnprocessedWebhooks :many
SELECT * FROM webhook_events WHERE processed_at IS NULL
ORDER BY received_at LIMIT @batch_size;

-- name: GetWebhookEvent :one
SELECT * FROM webhook_events WHERE provider = @provider AND event_id = @event_id;
