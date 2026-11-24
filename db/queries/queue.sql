-- Queue snapshots for R1 (queue restore after Redis loss).

-- name: UpsertQueueSnapshot :execrows
-- The worker writes this every second. taken_at comes from the caller and
-- only moves forward, so a slow writer can never replace a newer snapshot.
INSERT INTO queue_snapshots (event_id, presize, tail, cursor, sold_out, admit_rate, offers,
                             active_uids, active_exp_ms, taken_at)
VALUES (@event_id, @presize, @tail, @cursor, @sold_out, @admit_rate, @offers::bigint[],
        @active_uids::uuid[], @active_exp_ms::bigint[], @taken_at)
ON CONFLICT (event_id) DO UPDATE
SET presize = EXCLUDED.presize, tail = EXCLUDED.tail, cursor = EXCLUDED.cursor,
    sold_out = EXCLUDED.sold_out, admit_rate = EXCLUDED.admit_rate, offers = EXCLUDED.offers,
    active_uids = EXCLUDED.active_uids, active_exp_ms = EXCLUDED.active_exp_ms,
    taken_at = EXCLUDED.taken_at
WHERE queue_snapshots.taken_at <= EXCLUDED.taken_at;

-- name: GetQueueSnapshot :one
SELECT * FROM queue_snapshots WHERE event_id = $1;
