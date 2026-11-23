-- Tickets. UNIQUE (event_id, seat_no) is the last line of defence for I1.

-- name: InsertTickets :many
INSERT INTO tickets (event_id, seat_no, order_id, user_id, code)
SELECT @event_id::uuid, unnest(@seat_nos::int[]), @order_id::uuid, @user_id::uuid, unnest(@codes::text[])
RETURNING *;

-- name: ListTicketsByOrder :many
SELECT * FROM tickets WHERE order_id = $1 ORDER BY seat_no;

-- name: CountTicketsByEvent :one
SELECT count(*)::int AS tickets FROM tickets WHERE event_id = $1;
