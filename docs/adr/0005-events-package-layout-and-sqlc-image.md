# ADR 0005: Events package, seat-layout JSON, seed re-run rules, sqlc via Docker

- **Status:** accepted
- **Date:** 2026-11-23
- **Phase:** P1

## Context
P1 needed four things the specs leave open:
1. **Where the event catalogue lives.** STACK.md §2 has no package for the event read model or the seed. `GET /events`, `GET /events/{id}` and `sluice seed` do not belong in `inventory` (seats and holds) or `orders`.
2. **The shape of `events.layout`.** DATA-MODEL §1 only says "sections/rows/cols for rendering", and §7 says the JSON "carries coach, row and col so the UI can draw it".
3. **What re-running `sluice seed` does** to an event that already exists. §7 says the rush event opens at `now + 10 min` on seed and that seed is idempotent.
4. **How sqlc is installed.** STACK.md says "sqlc latest". sqlc links `pg_query_go`, which needs cgo, and building that on Windows is slow and fragile.

## Decision
1. Add `internal/events` (layout model, seed, store, HTTP handlers). Also add `internal/fsm`, a tiny generic transition table used by the `inventory` and `orders` state machines, with its test helper `internal/fsm/fsmtest`, and `internal/testutil/pgtest` (a shared testcontainers Postgres for each test package, one database per test).
2. `events.layout` is a compact descriptor rather than a list of seats: `{version, kind:"train", cols, aisle_after_col, seats_per_coach, coaches:[{coach, label, first_seat_no, rows, seats}]}`. Any seat's coach, row and col follow from `seat_no = first_seat_no + (row-1)*cols + (col-1)`. The per-seat truth stays in `seats` (label, section, row_no, col_no). For 500 seats the compact JSON is 749 bytes; for the 50,000-seat `loadtest-big` event it's 71,653 bytes (1,000 coach entries), not a 50,000-entry seat list. Measured from the marshalled descriptor; Postgres returns `layout::text` with extra spaces (859 bytes for 500 seats, `SELECT octet_length(layout::text) FROM events`).
3. Seed only inserts what's missing. An existing event is never modified: no re-timing and no capacity change. A capacity mismatch is an error, and missing seats are filled in. Re-timing a sale belongs to `POST /admin/events/{id}/open-sale` (P4/P5). `--opens-in` applies to every event seeded in that run, `--capacity` needs `--event` plus `APP_ENV=rush|test`, and an unknown `--event` slug creates a custom event with the same train geometry. `prequeue_opens_at` is the seed time and `starts_at` is `sale_opens_at + 24h`.
4. `task gen` runs sqlc from the pinned image `sqlc/sqlc:1.31.1` (Docker is already required), and `task gen:check` (`sqlc diff`) runs in CI.

## Consequences
- Nobody needs a local sqlc install, and CI catches generated code that has drifted from the queries or migrations.
- The UI (P6) computes seat positions from the coach descriptor. The availability bitmap (P2) is indexed by `seat_no`, which matches.
- A `task seed` run after the rush event's sale time has passed leaves it as is. Use open-sale (or drop the database volume) to re-run a rush.
- Docs updated: STACK.md §1.1 and §2, DATA-MODEL.md §7, API.md §3.10–3.11.
