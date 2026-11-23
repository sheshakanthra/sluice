-- Queue snapshots for R1 (queue restore after Redis loss).

-- name: UpsertQueueSnapshot :exec
INSERT INTO queue_snapshots (event_id, presize, tail, cursor, sold_out, taken_at)
VALUES (@event_id, @presize, @tail, @cursor, @sold_out, now())
ON CONFLICT (event_id) DO UPDATE
SET presize = EXCLUDED.presize, tail = EXCLUDED.tail, cursor = EXCLUDED.cursor,
    sold_out = EXCLUDED.sold_out, taken_at = EXCLUDED.taken_at;

-- name: GetQueueSnapshot :one
SELECT * FROM queue_snapshots WHERE event_id = $1;
