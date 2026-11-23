# P1: Schema, domain model, seed
**Timebox:** 2.5 h · **Read first:** DATA-MODEL.md §1, 3, 7 · API.md §2–3 (events endpoints) · ARCHITECTURE.md §3

## Goal
Put the source of truth in place. Postgres constraints make the bad states (I1, I2, I4, the seat state shape) **unrepresentable**. Events and seats are seeded, and two read endpoints work.

## Tasks
1. **Migrations** with goose v3 embedded through `db/embed.go`:
   - `0001_init.sql`, exactly as in DATA-MODEL §1, with a complete Down section.
   - `sluice migrate` runs goose up, then River's migrator (`rivermigrate`). Running it twice is safe.
2. **sqlc** (`db/sqlc.yaml`, `sql_package: pgx/v5`, output in `internal/db/gen`). Queries for:
   - events: list, get by id or slug
   - seats: by event, bulk insert, plus the state transitions (conditional updates; write them now even though P2 and P3 use them)
   - holds, orders, payments, tickets, quota upsert, idempotency keys, webhook inbox, queue snapshots
3. **`internal/db`:** pool constructor with `DB_MAX_CONNS`, plus `WithTx(ctx, func(pgx.Tx) error)` with serialization-failure-aware retry (only for idempotent closures).
4. **Domain:** pure Go state machines for seat, hold, order and payment, from DATA-MODEL §3, as `CanTransition(from, to)` plus typed transition functions. Unit-test every allowed transition, and every disallowed one with `rapid`.
5. **`sluice seed`** (idempotent): the two events from DATA-MODEL §7 with layout JSON and 500 seats each, labels `C1-1..C10-50`, and `seat_no` as specified. Flags: `--event`, `--capacity` (rush/test only), `--opens-in`.
6. **Endpoints:** `GET /api/v1/events` and `GET /api/v1/events/{eventId}` (accepts the uuid or the slug; returns the layout). Live counts come in P2; return the DB counts for now.
7. **Taskfile:** `migrate`, `seed`, `gen` (sqlc generate), `db:psql` (opens psql in the container).

## Required tests (integration, testcontainers PG 18)
- `tickets` rejects a second ticket for the same (event, seat) → I1.
- `payments_one_live_per_order` rejects a second AUTHORIZED/CAPTURED payment → I2.
- `quota_cap` rejects `seats_purchased > max_allowed` → I4.
- `seat_state_shape` rejects all 6+ invalid combinations (table test).
- `holds_one_active_per_user` rejects a second active hold for the same user and event.
- Migrations up → down → up is clean. Seeding twice gives the same counts.

## Acceptance
- [ ] `task up migrate seed`, then `task seed` again → still 2 events and 1,000 seats.
- [ ] `curl localhost:8080/api/v1/events` lists both. `/events/tatkal-chn-sbc` returns a layout with 10 coaches × 50 seats.
- [ ] Every constraint test above passes, and `task lint test` is green.
- [ ] sqlc output is generated and up to date (`task gen` produces no diff in `git status`).

**Commit hint:** `feat(db): schema with invariant constraints, sqlc, seed`
