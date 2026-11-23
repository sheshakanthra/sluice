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

-- name: TakeSeats :many
-- The hold write (ARCHITECTURE §4.2): AVAILABLE seats, or HELD seats whose
-- held_until has passed (PG takeover), become HELD by @hold_id. The CTE locks
-- the rows in seat order first so overlapping multi-seat holds cannot
-- deadlock. Each row returns the hold it was taken from (NULL if it was
-- AVAILABLE). The caller rolls back unless len(result) == len(seat_nos).
WITH cur AS (
  SELECT seat_no, hold_id AS prev_hold_id FROM seats
  WHERE event_id = @event_id AND seat_no = ANY(@seat_nos::int[])
  ORDER BY seat_no
  FOR UPDATE
)
UPDATE seats s
SET state = 'HELD', hold_id = @hold_id::uuid, held_until = @held_until::timestamptz, version = s.version + 1
FROM cur
WHERE s.event_id = @event_id AND s.seat_no = cur.seat_no
  AND (s.state = 'AVAILABLE' OR (s.state = 'HELD' AND s.held_until <= @now::timestamptz))
RETURNING s.seat_no, cur.prev_hold_id;

-- name: PickFreeSeats :many
-- HOLD_PATH=pg_only best-available: the lowest free seats, skipping rows
-- another transaction has locked so concurrent buyers do not queue.
SELECT seat_no FROM seats
WHERE event_id = @event_id
  AND (state = 'AVAILABLE' OR (state = 'HELD' AND held_until <= @now::timestamptz))
ORDER BY seat_no
LIMIT @qty
FOR UPDATE SKIP LOCKED;

-- name: CountFreeSeats :one
SELECT count(*)::int AS free FROM seats
WHERE event_id = @event_id
  AND (state = 'AVAILABLE' OR (state = 'HELD' AND held_until <= @now::timestamptz));

-- name: ListSeatStates :many
-- Seat map for rebuild, repair and the pg_only availability source. ver is
-- the sum of seat versions: every transition bumps one seat's version, so it
-- only grows.
SELECT seat_no, state, hold_id, held_until, (sum(version) OVER ())::bigint AS ver
FROM seats WHERE event_id = @event_id ORDER BY seat_no;
