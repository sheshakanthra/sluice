-- Events: catalogue reads, seed insert and admin schedule/status changes.

-- name: ListEventsWithCounts :many
-- Seat counts come from Postgres in P1; P2 serves live counts from Redis.
SELECT e.id, e.slug, e.name, e.venue, e.starts_at, e.prequeue_opens_at, e.sale_opens_at,
       e.capacity, e.price_paise, e.max_per_user, e.status, e.created_at,
       (count(s.seat_no) FILTER (WHERE s.state = 'AVAILABLE'))::int AS available,
       (count(s.seat_no) FILTER (WHERE s.state = 'HELD'))::int      AS held,
       (count(s.seat_no) FILTER (WHERE s.state = 'SOLD'))::int      AS sold
FROM events e
LEFT JOIN seats s ON s.event_id = e.id
GROUP BY e.id
ORDER BY e.sale_opens_at, e.slug;

-- name: GetEventByID :one
SELECT * FROM events WHERE id = $1;

-- name: GetEventBySlug :one
SELECT * FROM events WHERE slug = $1;

-- name: EventSeatCounts :one
SELECT (count(*) FILTER (WHERE state = 'AVAILABLE'))::int AS available,
       (count(*) FILTER (WHERE state = 'HELD'))::int      AS held,
       (count(*) FILTER (WHERE state = 'SOLD'))::int      AS sold,
       count(*)::int                                       AS total
FROM seats WHERE event_id = $1;

-- name: InsertEventIfAbsent :execrows
-- Seed is idempotent: an existing slug is left untouched (0 rows).
INSERT INTO events (slug, name, venue, starts_at, prequeue_opens_at, sale_opens_at,
                    capacity, price_paise, max_per_user, status, layout)
VALUES (@slug, @name, @venue, @starts_at, @prequeue_opens_at, @sale_opens_at,
        @capacity, @price_paise, @max_per_user, @status, @layout)
ON CONFLICT (slug) DO NOTHING;

-- name: SetEventSchedule :execrows
UPDATE events
SET prequeue_opens_at = @prequeue_opens_at, sale_opens_at = @sale_opens_at,
    starts_at = @starts_at, status = @status
WHERE id = @id;

-- name: SetEventStatus :execrows
UPDATE events SET status = @status WHERE id = @id;

-- name: TransitionEventStatus :execrows
-- Admission controller (P4): move an event forward only from an expected
-- status, so concurrent controllers and admin re-timing never fight.
UPDATE events SET status = @to_status
WHERE id = @id AND status::text = ANY(@from_statuses::text[]);

-- name: ListOpenEvents :many
-- Events whose inventory the worker keeps loaded and swept.
SELECT id, capacity, max_per_user, price_paise, sale_opens_at, status FROM events
WHERE status <> 'CLOSED' ORDER BY id;
