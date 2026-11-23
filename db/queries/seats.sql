-- Seats. Every state change is a conditional UPDATE that bumps version (fencing);
-- callers compare the affected row count with the number of seats they expect.

-- name: InsertSeats :execrows
-- Bulk insert from parallel arrays; ON CONFLICT keeps re-seeding idempotent.
INSERT INTO seats (event_id, seat_no, label, section, row_no, col_no)
SELECT @event_id::uuid,
       unnest(@seat_nos::int[]), unnest(@labels::text[]), unnest(@sections::text[]),
       unnest(@row_nos::int[]), unnest(@col_nos::int[])
ON CONFLICT DO NOTHING;

-- name: ListSeatsByEvent :many
SELECT * FROM seats WHERE event_id = $1 ORDER BY seat_no;

-- name: GetSeatsByNos :many
SELECT * FROM seats WHERE event_id = @event_id AND seat_no = ANY(@seat_nos::int[]) ORDER BY seat_no;

-- name: HoldSeats :many
-- AVAILABLE -> HELD for every requested seat that is still available. The
-- caller rolls back unless len(result) == len(seat_nos) (all-or-nothing).
UPDATE seats
SET state = 'HELD', hold_id = @hold_id::uuid, held_until = @held_until::timestamptz, version = version + 1
WHERE event_id = @event_id AND seat_no = ANY(@seat_nos::int[]) AND state = 'AVAILABLE'
RETURNING seat_no;

-- name: ExtendSeatHold :execrows
-- HELD -> HELD with a later held_until (order created, PAYMENT_HOLD_TTL).
UPDATE seats
SET held_until = @held_until::timestamptz, version = version + 1
WHERE event_id = @event_id AND hold_id = @hold_id::uuid AND state = 'HELD';

-- name: SellSeats :execrows
-- HELD -> SOLD. I6 fencing: only seats still held by exactly this hold move;
-- the caller requires rows == len(seat_nos) or rolls back and voids.
UPDATE seats
SET state = 'SOLD', hold_id = NULL, held_until = NULL, order_id = @order_id::uuid, version = version + 1
WHERE event_id = @event_id AND seat_no = ANY(@seat_nos::int[])
  AND state = 'HELD' AND hold_id = @hold_id::uuid;

-- name: ReleaseSeats :execrows
-- HELD -> AVAILABLE for a hold that was released or reclaimed.
UPDATE seats
SET state = 'AVAILABLE', hold_id = NULL, held_until = NULL, version = version + 1
WHERE event_id = @event_id AND hold_id = @hold_id::uuid AND state = 'HELD';
