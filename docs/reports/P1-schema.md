# P1: Schema, domain model, seed: Report

| Field | Value |
|---|---|
| Status | DONE |
| Date (IST) | 2026-11-23 |
| Time spent vs timebox | about 30m of build time / 2h30m (first P1 file written 15:55 IST, report finished 16:23 IST, from `ls` mtime and `date`) |
| Commit base | `4b9f800` (`git rev-parse --short HEAD`) |

## 1. What was built
- **Migrations** `db/migrations/0001_init.sql`: the DATA-MODEL §1 DDL copied verbatim (extracted from the doc, not retyped), plus a complete `-- +goose Down` that drops every table, the sequence and the 5 enum types in reverse order. `db/embed.go` (package `dbfs`) embeds them.
- **`internal/db`:**
  - `NewPool` (honours `DB_MAX_CONNS`; lazy connect; never echoes the URL).
  - `WithTx(ctx, b, fn, opts...)` retries `40001` serialization failures and `40P01` deadlocks with full-jitter exponential backoff (5 ms → 200 ms cap, default 5 attempts). Options: `Isolation`, `ReadOnly`, `MaxAttempts`, `NoRetry` (for closures that aren't idempotent). Errors wrap `ErrRetriesExhausted`.
  - `IsRetryable`, `IsUniqueViolation`, `IsCheckViolation` (matched by constraint name).
  - `Migrator`: goose v3 provider with a Postgres advisory session lock, then River's `rivermigrate`. `Up` is idempotent, `DownAll` is used by tests, and `Check` (no lock) feeds `/readyz`.
- **sqlc** `db/sqlc.yaml` (pgx/v5, `uuid.UUID` and `time.Time` overrides, pointer nulls, enum `Valid`/`All*Values`) and 10 query files in `db/queries/`: events (list with counts, get by id/slug, counts, insert-if-absent, schedule/status), seats (bulk `unnest` insert, list, hold/extend/sell/release as conditional updates that bump `version`), holds (fence `nextval`, insert, get / for-update, live-for-user, ACTIVE→PAYMENT_PENDING→CONSUMED, RELEASED, EXPIRED-only-if-past, sweeper `SKIP LOCKED` batch), orders (insert, get / for-update / by-hold, confirm/fail/expire/refund), payments (insert, get, by-intent, list, set intent, authorize/capture/void/fail/refund), tickets (bulk insert, list, count), quota (add with upsert, release, get), idempotency keys (insert-if-absent, get, reclaim lapsed, complete, delete, GC), webhook inbox (dedupe insert, mark processed, list unprocessed), queue snapshots (upsert, get). Output is in `internal/db/gen`.
- **Domain state machines** (pure Go): `internal/fsm` is a generic immutable transition table with a typed `*TransitionError` (`errors.Is(err, fsm.ErrInvalidTransition)`). `internal/inventory/state.go` covers seat and hold, and `internal/orders/state.go` covers order and payment, each with `CanTransition` plus typed transition functions (`Hold`, `Confirm`, `Release`, `OrderCreated`, `Reclaim`, `Fail`, `Expire`, `Refund`, `Authorize`, `Capture`, `Decline`, `Void`), with `Live()` matching the partial-index predicates.
- **`internal/events`:**
  - Train layout model: compact coach descriptor (ADR 0005).
  - Idempotent `Seed`: one `WithTx` per event; inserts only what's missing; refuses a capacity mismatch; fills seat gaps.
  - `SeedOptions` for `--event`, `--capacity` (rush/test only, needs `--event`) and `--opens-in`.
  - `Store` (list with counts; get by UUID or slug with layout and counts), and handlers for `GET /api/v1/events` and `GET /api/v1/events/{eventId}` (404 `NOT_FOUND`, 503 `DEPENDENCY_UNAVAILABLE` + `Retry-After: 1` when PG is unreachable, 500 `INTERNAL` only for SQLSTATE bugs).
- **CLI / wiring:** `sluice migrate`, `sluice seed [--event --capacity --opens-in]` (`cmd/sluice/main.go`, `internal/app/cli.go`). The API mounts the events routes, and `/readyz` gained the **migrations** check that P0 left as debt (goose has nothing pending and River `Validate` is OK). `runService` now logs to the writer it is given (testability).
- **Tests infra:** `internal/testutil/pgtest`: one `postgres:18.7` container per test package (TestMain); each test gets its own database, empty or cloned from a template that `Migrator.Up` migrated once. `internal/fsm/fsmtest` checks each machine against a hand-written edge list (exhaustive and rapid).
- **Taskfile:** `gen` (sqlc via `sqlc/sqlc:1.31.1` Docker image), `gen:check` (`sqlc diff`), `migrate`, `seed` (`task seed -- <flags>`), `db:psql`. CI runs `task gen:check` after lint.
- **Docs:** ADR 0005; STACK.md §1.1 (versions) and §2 (layout); DATA-MODEL.md §7 (layout JSON, seed rules); API.md §3.10–3.11 (event shapes).

## 2. Acceptance checklist
- [x] **`task up migrate seed`, then `task seed` again → still 2 events and 1,000 seats.**
  ```
  task migrate  → "migrations applied" goose_applied:[1] goose_version:1 river_applied:[1,2,3,4,5,6,7,8]
  task seed     → tatkal-chn-sbc created:true seats_inserted:500 status:SCHEDULED sale_opens_at:2026-11-23T10:56:27Z
                  quiet-tuesday  created:true seats_inserted:500 status:ON_SALE   sale_opens_at:2026-11-23T10:46:27Z
  task migrate (2nd) → "No migrations to apply"; goose_applied:null goose_version:1 river_applied:null
  task seed    (2nd) → both created:false seats_inserted:0 seats:500 (sale_opens_at unchanged)
  docker compose -f deploy/compose.yml exec -T postgres psql -U sluice -d sluice \
    -c "SELECT (SELECT count(*) FROM events) AS events, (SELECT count(*) FROM seats) AS seats;"
   events | seats
        2 |  1000
  ```
  Per-coach check (same psql session): C1..C10 each `50` seats, `rows 10`, `cols 5`; spot seats `0 → C1-1 r1 c1`, `126 → C3-27 r6 c2`, `212 → C5-13 r3 c3`, `499 → C10-50 r10 c5`.
- [x] **`curl localhost:8080/api/v1/events` lists both; `/events/tatkal-chn-sbc` returns a layout with 10 coaches × 50 seats.** (built binary `sluice api` against the compose stack)
  ```
  curl -s localhost:8080/readyz → {"checks":{"migrations":"ok","postgres":"ok","redis":"ok"},"status":"ready"}
  curl -s localhost:8080/api/v1/events → {"events":[{"slug":"quiet-tuesday",...,"status":"ON_SALE",...,"counts":{"available":500,"held":0,"sold":0,"capacity":500}},
                                                    {"slug":"tatkal-chn-sbc",...,"status":"SCHEDULED",...,"counts":{"available":500,"held":0,"sold":0,"capacity":500}}]}
  curl -s localhost:8080/api/v1/events/tatkal-chn-sbc → layout: version 1, kind train, cols 5, aisle_after_col 3, seats_per_coach 50
                                                         coaches: 10 x seats: [50] rows: [10]; C1 first_seat_no 0 … C10 first_seat_no 450
  curl -s localhost:8080/api/v1/events/01a20803-a80f-79d5-a37d-02834e75d1b6 → tatkal-chn-sbc 10 coaches   (by uuid)
  curl -si localhost:8080/api/v1/events/no-such-event → HTTP/1.1 404, application/problem+json, "code":"NOT_FOUND"
  ```
- [x] **Every constraint test above passes, and `task lint test` is green.** `task lint test` → `0 issues.` and `ok` for all 11 test packages. `task lint test:race` → same, under `-race` (gcc 16.2.0 present, **race verified locally**). Tests by required item:
  | Required test | Test (`internal/db`) |
  |---|---|
  | `tickets` rejects a 2nd ticket for (event, seat) → I1 | `TestTicketsRejectSecondTicketForSeat`, `TestTicketsConcurrentSameSeatOneWinner` (64 goroutines → 1 win, 63 `tickets_event_id_seat_no_key`) |
  | `payments_one_live_per_order` → I2 | `TestPaymentsOneLivePerOrder` (9-row table + generated `AuthorizePayment`), `TestPaymentsConcurrentAuthorizeOneWinner` (20 rounds × 3 racing attempts → exactly 1 live) |
  | `quota_cap` → I4 | `TestQuotaCap` (5 rows + release below zero), `TestQuotaConcurrentAddsStopAtCap` (64 goroutines × 1 seat, cap 4 → 4 ok, 60 `quota_cap`, final 4) |
  | `seat_state_shape` rejects all 6+ invalid combos | `TestSeatStateShape`: **9** invalid shapes rejected by name, 3 legal shapes accepted |
  | `holds_one_active_per_user` | `TestHoldsOneActivePerUser`: ACTIVE/PAYMENT_PENDING block a 2nd; RELEASED/EXPIRED/CONSUMED don't; other user / other event unaffected; 64 concurrent inserts → 1 |
  | Migrations up → down → up clean | `TestMigrationsUpDownUp`: 17 relations + 5 types present / absent / present; 2nd `Up` is a no-op; `Check` fails on empty DB, after Down (`ErrMigrationsPending`), passes after Up |
  | Seeding twice gives the same counts | `internal/events` `TestSeedIsIdempotent`: 2/1000 after 1st, 2nd (1 h later), and 8 concurrent seeds; gap fill; capacity change refused |
- [x] **sqlc output is generated and up to date (`task gen` produces no diff).** `sha256sum internal/db/gen/*.go` before and after `task gen` → `GEN: byte-identical after task gen (12 files)`. `task gen:check` (`sqlc diff`) → exit 0. Proof that it catches drift: adding one comment line to `db/queries/queue.sql` made `task gen:check` exit `201`; I restored the file and it was clean again. Note: `internal/db/gen` is new and untracked until this commit, so `git status` lists it under `?? internal/db/`. The byte-identical check is the meaningful "no diff".

Extra gates: `govulncheck ./...` → `No vulnerabilities found.` (exit 0); see §6 for one module-level advisory. `go vet ./...` clean.

## 3. Tests
| Suite | Count | Result | Notes |
|---|---|---|---|
| unit | 46 top-level (`go test -short -v ./... \| grep -E "^--- (PASS\|FAIL\|SKIP)"` → `46 PASS`, `16 SKIP`) | pass | state machines, fsm, layout, seed options, handler error mapping, tx helpers, CLI flags |
| integration (testcontainers) | 16 top-level (skipped by `-short`) | pass | real `postgres:18.7` (+ `redis:8.10.3` for `/readyz`); 62 top-level PASS overall, 222 PASS lines incl. subtests (`go test -v ./... \| grep -cE "^\s*--- PASS"`) |
| race (`-race`) | all 11 test packages | pass | local (`task test:race`); CI runs it too |
| property (rapid) | 8 properties (2 per machine × 4) | pass | default 100 cases each; also run with `-args -rapid.checks=10000` → `[rapid] OK, passed 10000 tests` ×8 |
| concurrency | 7 tests | pass | tickets, live payment, quota, one-hold-per-user, overlapping multi-seat holds (200 racers, 20 seats), serializable `WithTx` (32 increments → `133 attempts ... (101 retries)`, final 32), concurrent seeds |
| e2e (Playwright) | 0 | n/a | P6 |

Coverage (`go test -count=1 -cover ...`): inventory 80.0%, orders 83.3%, db 82.2%, events 92.2%, fsm 94.7%, cmd/sluice 87.5%, app 20.3% (`RunAPI`/`RunMigrate`/`SeedFunc` run only via the CLI checks above). queue doesn't exist yet.

## 4. Numbers
No performance numbers in P1. Data sizes quoted in ADR 0005: layout JSON is 749 bytes (500 seats) and 71,653 bytes (50,000 seats), computed from the descriptor; Postgres `SELECT slug, octet_length(layout::text) FROM events` → `859` for both demo events (jsonb text output adds spaces). The retry count above (101 retries for 32 increments) is a single run's output and will vary from run to run.

## 5. Deviations and decisions
| What changed vs the spec | Why | ADR |
|---|---|---|
| New packages `internal/events`, `internal/fsm` (+`fsmtest`), `internal/testutil/pgtest` | STACK §2 had no home for the catalogue/seed; a shared table keeps the four machines uniform | 0005 |
| `events.layout` is a coach descriptor, not a seat list | DATA-MODEL left the shape open; 71,653 B vs a 50k-entry list for `loadtest-big` | 0005 |
| Seed never modifies an existing event (no re-time, capacity mismatch = error) | Idempotency; re-timing is the admin open-sale endpoint's job | 0005 |
| sqlc runs from the `sqlc/sqlc:1.31.1` image; `task gen:check` added to CI | sqlc needs cgo; Docker is already required | 0005 |
| Hold transitions are separate queries (`MarkHoldPaymentPending`, `ConsumeHold`, `ReleaseHold`, `ExpireHold`) rather than one generic "set status" | Encoding `hold_status[]` params needs pgx enum-array registration; explicit queries also mirror the state machine 1:1 | none (implementation detail) |
| `ExpireHold` and `MarkHoldPaymentPending` take `@now` | Testable time; P2's sweeper passes the clock | none |
| `quiet-tuesday` `prequeue_opens_at = sale_opens_at = seed time`; `starts_at = sale + 24h` | The CHECK needs `prequeue ≤ sale < starts`; §7 only fixes the sale time | DATA-MODEL §7 updated |

Assumptions (also in code comments): fixed venue text "Chennai → Bengaluru"; custom `--event` slugs get the default price/cap and open in 10 min unless `--opens-in`; `--capacity` max 1,000,000.

## 6. Known issues and debt
- **govulncheck module-level finding:** `GO-2026-5932` in `golang.org/x/crypto@v0.58.0` (indirect). Symbol scan: `Your code is affected by 0 vulnerabilities`. The only fix is a pseudo-version (`v0.58.1-0.20261009181718-2f1f834330b2`); I left it alone and will bump x/crypto once a tagged release exists.
- `go mod tidy` pulled goose's indirect deps (incl. `modernc.org/*` for goose's sqlite dialect) into `go.sum`. They are not linked into the binary: `go version -m` on a fresh `go build ./cmd/sluice` lists 0 `modernc`/`sqlite` modules.
- `internal/app` coverage is 20.3%; the CLI paths were verified by the acceptance commands, not by unit tests.
- River logs each applied migration at INFO through our logger (8 lines on the first `task migrate`). Harmless.
- Rush-event re-timing on a dev DB needs open-sale (P4/P5) or `task down` + volume removal.

## 7. Understand this before moving on (Sheshakanth's interview prep)
1. **Q:** Redis says a seat is free and two users race to buy it. What actually stops an oversell?
   **A:** Postgres, in three layers. Confirm uses `SellSeats` (`db/queries/seats.sql`): `UPDATE ... WHERE state='HELD' AND hold_id=$hold`, and the caller requires the affected rows to equal the seat count (I6 fencing), so a stale or wrong holder moves 0 rows. `seat_state_shape` makes "SOLD without an order" or "HELD by nobody" unrepresentable (`TestSeatStateShape`, 9 shapes). And `tickets UNIQUE(event_id, seat_no)` is the last line: 64 goroutines racing to ticket one seat give exactly one row (`TestTicketsConcurrentSameSeatOneWinner`).
2. **Q:** Why does `WithTx` retry, and why only for some closures?
   **A:** Under SERIALIZABLE (and with lock-order deadlocks) Postgres aborts one side of a conflict with `40001`/`40P01` and rolls the whole transaction back, so re-running it is the correct response (`internal/db/tx.go:WithTx`, `IsRetryable`). A rerun repeats the closure, so the closure must be idempotent: no HTTP calls, no Redis writes, no state carried between attempts. Anything else passes `NoRetry()`. The test drives 32 serializable read-modify-write increments; one run logged 101 retries and the final counter was exactly 32.
3. **Q:** How do you know the Go state machines and the database agree?
   **A:** Three ways. `TestDomainStatesMatchPostgresEnums` compares each machine's states with sqlc's `All*Values()` (generated from the enums). Each machine is checked against an edge list copied by hand from DATA-MODEL §3, exhaustively and with rapid, including random event walks (`internal/fsm/fsmtest`). And every DB transition query encodes the same `from` state in its `WHERE` (for example `ConsumeHold ... WHERE status='PAYMENT_PENDING'`), so the DB refuses moves the machine refuses, returning 0 rows.

## 8. Files changed
`git status --short`:
```
 M .github/workflows/ci.yml
 M Taskfile.yml
 M cmd/sluice/main.go
 M cmd/sluice/main_test.go
 M docs/API.md
 M docs/DATA-MODEL.md
 M docs/STACK.md
 M go.mod
 M go.sum
 M internal/app/api.go
 M internal/app/api_integration_test.go
?? db/
?? docs/adr/0005-events-package-layout-and-sqlc-image.md
?? internal/app/cli.go
?? internal/db/
?? internal/events/
?? internal/fsm/
?? internal/inventory/
?? internal/orders/
?? internal/testutil/
```
(plus this report). By area:
- Schema/codegen: `db/` (embed, migration, sqlc.yaml, queries), `internal/db/gen`.
- Go: `internal/db`, `internal/events`, `internal/fsm`, `internal/inventory`, `internal/orders`, `internal/testutil/pgtest`, `internal/app`, `cmd/sluice`, `go.mod`, `go.sum`.
- Tooling: `Taskfile.yml`, `.github/workflows/ci.yml`.
- Docs: ADR 0005, STACK, DATA-MODEL, API.

## 9. Next phase readiness
- P2 (holds) has what it needs: `HoldSeats`/`ExtendSeatHold`/`SellSeats`/`ReleaseSeats`, the hold transitions, `NextHoldFence`, `ListExpiredHolds ... SKIP LOCKED`, `WithTx`, the state machines and `pgtest`.
- P2 swaps the P1 Postgres counts in `events.Store` for live counts.
- Sheshakanth: commit, push and confirm CI is green. The new `sqlc up to date` step needs Docker on the runner, which `ubuntu-latest` has.
- Local DB now has both demo events; `tatkal-chn-sbc` sale time was `2026-11-23T10:56:27Z`.

## 10. Git commands (Sheshakanth runs these)
PowerShell-safe: one command per line, no shell-specific redirections.
```bash
git add -A
git status --short
git commit -m "feat(db): schema with invariant constraints, sqlc, seed" -m "- goose 0001_init with full Down; sluice migrate runs goose then River" -m "- sqlc queries for every table; WithTx with serialization-failure retry" -m "- Pure Go seat, hold, order and payment state machines with rapid tests" -m "- Idempotent sluice seed and GET events endpoints; readyz checks migrations" -m "- Constraint and concurrency tests for I1, I2, I4, seat shape, one hold per user" -m "Phase P1 - report: docs/reports/P1-schema.md"
```
