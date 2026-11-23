# P2: Seat holds with TTL and fencing: Report

| Field | Value |
|---|---|
| Status | DONE |
| Date (IST) | 2026-11-23 |
| Time spent vs timebox | about 35m wall-clock / 4h (`date` at start 16:34 IST, report written 17:05 IST) |
| Commit base | `ea6b4c0` (`git rev-parse --short HEAD`) |

## 1. What was built
- **`internal/auth`**: HS256 JWTs via `golang-jwt/jwt/v5` (as STACK.md specifies), signed with the first `TOKEN_KEYS` key and verified by `kid` against the whole ring. Audiences are `session`, `queue` and `admission`; `iss=sluice`; exp/iat get 5 s of leeway; only HS256 is accepted.
  - `Require` middleware: a missing token gives NOT_ADMITTED or TOKEN_INVALID; a bad token gives 401.
  - `RequireAdmin`: constant-time compare of the bearer against `ADMIN_TOKEN`.
  - Handlers: `POST /api/v1/sessions` (anonymous UUIDv7 user, 24 h token) and `POST /api/v1/test/admit`, which is mounted only when `TEST_ENDPOINTS_ENABLED` is on and `APP_ENV` is rush or test.
- **Idempotency middleware** (`internal/httpx/idempotency.go`), backed by Postgres, Stripe-style:
  - STARTED rows with `locked_until` = now + 30 s.
  - A COMPLETED key replays with `Idempotent-Replayed: true`; a different request hash gets 422; a request in flight gets 409 `REQUEST_IN_PROGRESS` + `Retry-After: 1`; a lapsed lock gets reclaimed and re-executed.
  - Responses under 500 are stored; a 5xx deletes the key. The request hash is over canonical JSON.
  - `GCIdempotencyKeys` runs hourly in the worker (the GC stub).
- **`internal/redisx`**:
  - `NewClient` from `REDIS_URL` with 2 s timeouts.
  - `Scripts` loader: `go:embed`, EVALSHA with NOSCRIPT fallback (go-redis `Script.Run`), `Load` preloads.
  - `redis_script_duration_seconds{script}`.
  - `keys/` is the single key builder (`keys.Avail(eid)`, `keys.Hold(eid, hid)` and so on, all `sl:{e:<eid>}:`).
  - Scripts:
    - `hold_seats`, `hold_best`, `hold_extend`, `hold_consume`, `hold_release`, `inventory_load`, `snapshot` follow the DATA-MODEL §5 contracts.
    - `inventory_repair` is new, for "mark seats for repair" (ADR 0006).
- **`internal/inventory`**:
  - `Service.Hold` (`hold.go`): hid = UUIDv5(NS_HOLD, uid:key), then the Redis script (or nothing with `pg_only`), then the PG transaction.
    - The transaction runs the quota pre-check, `InsertHold` and `TakeSeats`. `TakeSeats` must return a row count equal to n; it can take over an expired HELD seat, which marks the old hold EXPIRED and frees its other seats in the same tx.
    - On a PG refusal (SEATS_TAKEN, HOLD_LIMIT, QUOTA_EXCEEDED) the service compensates with `hold_release` + `inventory_repair`.
    - `pg_only` takes the fence from `hold_fence_seq` and does best-available with `FOR UPDATE SKIP LOCKED`.
  - `Release`, `Extend` (ACTIVE → PAYMENT_PENDING, used in P3) and `Consume` (the Redis mirror of a confirm, P3).
  - `Rebuild` / `EnsureLoaded` (`rebuild.go`): reclaim in PG, read a REPEATABLE READ snapshot, then `inventory_load`. A SET NX lock means one rebuilder at a time. An API request that hits NOT_LOADED triggers a rebuild on demand.
  - Sweeper (`sweeper.go`): `RunSweeper` / `SweepOnce`. The PG pass uses `SKIP LOCKED` batches of 200 and conditional updates, then `hold_release`. The Redis pass resolves the `holds` ZSET with PG as judge (release, extend or consume).
  - `AvailabilityCache` (`availability.go`): per replica, ≤ 1 refresh per event per 100 ms, reads `ver` first, packs 2 bits per seat as base64, sets `ETag`; the source is PG for `pg_only` or when Redis isn't loaded.
  - Handlers (`handler.go`): `POST /events/{id}/holds` (admission + IK), `DELETE /holds/{id}`, `GET /events/{id}/availability` (ETag/304), `POST /admin/events/{id}/rebuild-cache`.
  - Metrics: `holds_requests_total{result}`, `holds_expired_total`, `holds_compensations_total`, `inventory_seats{event,state}`.
- **SQL (sqlc)**: `TakeSeats` (a CTE that locks rows in seat order, then a conditional update returning the previous hold), `PickFreeSeats`, `CountFreeSeats`, `ListSeatStates`, `MaxHoldFence`, `ListLiveHoldsByEvent`, `GetHoldsByIDs`, `ListOpenEvents`. Every hand-written SQL comment states why. No schema change.
- **`sluice worker`** (`internal/app/worker.go`) runs the sweeper (which loads every open event at start-up), the idempotency GC, and `/healthz`, `/readyz` and `/metrics` on `WORKER_HTTP_ADDR`. Wiring for both processes is in `internal/app/inventory.go`. `GET /events` now serves live counts from the availability cache (`events.Store.WithLiveCounts`).
- **Tests infra**: `internal/testutil/redistest` (Redis 8 container per package, FLUSHALL per test, AOF + noeviction flags).
- **Taskfile**: `dev:worker` replaces the P0 stub.
- **Docs**: ADR 0006; DATA-MODEL §4–6; API §1.2, §3.4, §3.8, new §3.12; ARCHITECTURE §4.2; STACK §1.1 (golang-jwt v5.3.1) and §2.

## 2. Acceptance checklist
- [x] **All the required tests pass under `-race`.** `task lint test:race` gave `0 issues.` and `ok` for all 13 test packages (exit 0, 17:00:59 to 17:01:59 IST). An uncached rerun of the P2 packages, `CGO_ENABLED=1 go test -race -count=1 -v ./internal/inventory/ ./internal/redisx/... ./internal/auth/ ./internal/httpx/`, gave exit 0: 142 PASS lines, 0 FAIL, 0 `DATA RACE`. gcc 16.2.0 is present, so **race is verified locally**.

  | Required test | Test | Race-run evidence |
  |---|---|---|
  | Race, single seat: 2,000 users hold seat 7 → 1 OK, 1,999 TAKEN; PG 1 HELD seat, 1 hold row | `TestRaceSingleSeat/{redis_pg,pg_only}` | PASS 0.79 s / 2.55 s; also asserts Redis avail and holds ZSET, 0 compensations |
  | Race, best-available: 10,000 × qty 1 on 500 seats → exactly 500 OK, no seat in two holds, avail = 0 | `TestRaceBestAvailable/{redis_pg,pg_only}` | PASS 1.64 s / 7.72 s; 9,500 INSUFFICIENT; every PG seat HELD by its unique owner; PG free = 0; Redis SCARD avail = 0 |
  | Multi-seat all-or-nothing: [1,2,3] with 2 taken → TAKEN [2], 1 and 3 still free | `TestMultiSeatAllOrNothing/*`, plus `TestHoldSeats` (script) | PASS (PG and Redis checked) |
  | Idempotency: same IK twice → same hold_id/response + replay header; two concurrent → one 201 and the rest 409/replay, never two holds | `TestHoldAPI`, `TestHoldAPIConcurrentSameKey`, `TestIdempotentHold/*` | 20 concurrent same-key requests: `executed=1 replayed=0 in_progress=19`, 1 hold row; 50 concurrent service calls → 1 hold |
  | One active hold per user → HOLD_LIMIT (Redis and PG agree) | `TestOneActiveHoldPerUser/*` | PASS; also removes the user from Redis `holding` → PG index answers HOLD_LIMIT and the grant is compensated |
  | Expiry: TTL 2 s, sweeper returns seats within ≤ 2 intervals, hold EXPIRED, second hold takes the seats | `TestExpirySweeper/{redis_pg,pg_only}` | `reclaimed 1.019s after expires_at (interval 1s)` / `27ms`; both ≤ 2 s (single run; the lag depends on tick phase) |
  | PG takeover: expired HELD seat missing from Redis → new hold takes it; old hold EXPIRED in the same tx | `TestPGTakeover/*` | PASS; the old hold's other seat is freed in PG and Redis |
  | Compensation: PG conflict after the Redis grant → Redis hold released, user gets 409 | `TestCompensation` | PASS: `SEATS_TAKEN [3]`, Redis hold and holding entry gone, seat 4 back in avail, seat 3 repaired out, `holds_compensations_total` = 1 |
  | R1: 300 holds, FLUSHALL, rebuild → Redis matches PG seat by seat | `TestRebuildAfterFlushAll` | `rebuild: {Available:125 Held:360 Sold:15 Holds:240 Reclaimed:0 Fence:300}`; seat-state maps equal before and after; 240 hold hashes match PG (uid, seats, fence, state, exp_ms); next fence > 300 |
  | pg_only path: same race tests | every `*/pg_only` subtest above | PASS |
  | Each Lua script has a direct Redis 8 unit test | `internal/redisx/scripts_test.go`: `TestHoldSeats`, `TestHoldBest`, `TestHoldExtend`, `TestHoldConsume`, `TestHoldRelease`, `TestInventoryLoad` (12,000 seats, crosses the 5,000 chunk), `TestInventoryRepair`, `TestSnapshot`, `TestNoScriptFallback` | PASS against `redis:8.10.3` |

  Extra: `TestSweeperConcurrentWorkers` (3 sweepers over 100 expired holds reclaim exactly 100), `TestSweeperRedisPass` (orphan, extended and consumed resolution), `TestEnsureLoadedOnDemand`, `TestQuotaPreCheck/*`, `TestHoldValidation`, `TestReleaseExtendConsume`, `TestAvailability/*` (bitmap decode, ETag stability, 100 ms cache).
- [x] **Manual check** (built `bin/sluice.exe`, compose infra, `APP_ENV=test TEST_ENDPOINTS_ENABLED=true HOLD_TTL=15s SWEEPER_INTERVAL=1s`, API on :8080, worker on :9091):
  ```
  worker log  → "inventory loaded into redis" event_id 01a20803-a80f… available 500 / 01a20803-a823… available 500
  POST /api/v1/sessions   → {"user_id":"01a20829-3d7e-716d-bdee-7c68d3af15c3","session_token":"eyJhbGciOiJIUzI1NiIs…
  POST /api/v1/test/admit → {"event_id":"01a20803-a823-7582-9e34-788dafe9cdfd",…,"admission_token":"eyJ…
  POST /api/v1/events/quiet-tuesday/holds {"quantity":2}
      → HTTP/1.1 201 Created
        {"hold_id":"17004086-6703-50b1-b074-640c18a9f0e2","status":"ACTIVE","seat_nos":[156,423],"labels":["C4-7","C9-24"],"fence":1,"expires_at":"2026-11-23T11:27:45.736Z"}
  same Idempotency-Key again → HTTP/1.1 201 Created, Idempotent-Replayed: true, same values
  11:27:30Z GET …/availability → "version":1795433240523,"counts":{"available":498,"held":2,"sold":0,"capacity":500}
  GET /api/v1/events (quiet-tuesday) → "counts":{"available":498,"held":2,"sold":0,"capacity":500}
  poll every ~0.5 s → 11:27:46.882Z "counts":{"available":500,"held":0,"sold":0,"capacity":500}
  psql: 17004086-… | EXPIRED | expires_at 11:27:45.736+00 | updated_at 11:27:46.426758+00
  ```
  The seats were back 1.15 s after `expires_at` (the PG row flipped after 0.69 s).
  Extra live R1 check: I held seats [10,11,12], ran `redis-cli FLUSHALL`, and `meta.loaded` was 1 again on the first poll. The worker log showed `available 497, held 3, holds 1`, and availability showed `held 3`. `POST …/rebuild-cache` with the admin token returned `{"available":497,"held":3,"sold":0,"holds":1,"reclaimed":0,"fence_floor":2}`; without it, `401`.
- [x] **`task lint test` is green, and metrics are visible on `/metrics`.** `task lint test:race` (a superset) is above. Non-zero series after the manual run:
  ```
  api :8080/metrics    holds_requests_total{result="ok"} 1
                       inventory_seats{event="quiet-tuesday",state="available"} 500
                       redis_script_duration_seconds_count{script="hold_best"} 1
  worker :9091/metrics holds_expired_total 1
                       redis_script_duration_seconds_count{script="hold_release"} 1
                       redis_script_duration_seconds_count{script="inventory_load"} 2
  ```
  `TestReadyzAgainstRealDependencies` also asserts that `/metrics` exposes `holds_requests_total{result="ok"}`, `holds_expired_total`, `holds_compensations_total` and `redis_script_duration_seconds_count{script="hold_seats"}`.

Extra gates:
- `task gen:check` → exit 0.
- `govulncheck ./...` → `Your code is affected by 0 vulnerabilities`. The only module-level finding is `GO-2026-5932` in `golang.org/x/crypto@v0.58.0`, the same as in P1.
- `gofmt -l internal/` → empty.

## 3. Tests
| Suite | Count | Result | Notes |
|---|---|---|---|
| unit | `go test -short -count=1 -v ./...` → 61 top-level PASS, 34 SKIP (integration) | pass | P2 adds 6 unit tests: auth (4) and idempotency hash/key (2); everything else in P2 needs containers |
| integration (testcontainers) | 34 top-level (skipped by `-short`); full `go test -count=1 -v ./...` → 307 PASS lines incl. subtests, 0 FAIL | pass | real `postgres:18.7` + `redis:8.10.3`; the P2 packages alone: 142 PASS lines |
| race (`-race`) | all 13 test packages | pass | local (`task test:race`, gcc 16.2.0); CI runs it too |
| property (rapid) | unchanged from P1 (8) | pass | — |
| concurrency | 8 new: single-seat race ×2, best-available race ×2, concurrent idempotent hold ×2, concurrent same-key HTTP, concurrent sweepers; R1 uses 300 concurrent holds | pass | all under `-race` |
| e2e (Playwright) | 0 | n/a | P6 |

Coverage (`go test -count=1 -cover`): inventory 81.0%, httpx 67.3%, redisx 56.5%, auth 32.5%, app 17.3%. redisx and auth look low because their scripts and handlers are exercised from other packages (`redisx_test`, `inventory_test`), which `-cover` doesn't credit. app's `RunWorker`/`RunAPI` ran only in the manual check. orders and queue are unchanged (queue doesn't exist yet).

## 4. Numbers
No benchmark in P2. Timings from single runs, quoted as-is:
- Race wall times under `-race` (one uncached run): single seat 2,000 goroutines: redis_pg 0.79 s, pg_only 2.55 s. Best-available 10,000 goroutines on 500 seats: redis_pg 1.64 s, pg_only 7.72 s. These include `-race` overhead and run on a laptop with Docker Desktop. They're not throughput claims; P5 measures properly.
- Sweeper reclaim lag after `expires_at` (interval 1 s): 1.019 s and 27 ms in the test run, and 0.69 s (PG `updated_at`) in the manual run. All are ≤ 2 intervals; the lag depends on where expiry falls in the tick.

## 5. Deviations and decisions
| What changed vs the spec | Why | ADR |
|---|---|---|
| Quota pre-check runs inside the hold's PG tx, after the Redis grant | Requests Redis rejects never touch PG; a refusal is compensated | 0006 |
| New `inventory_repair.lua`; compensation = `hold_release` + repair | §4.2's "mark seats for repair" had no mechanism; without it Redis keeps offering a seat PG refuses | 0006 |
| Hold scripts return `NOT_LOADED`; on-demand rebuild behind `rebuild_lock` (new key) | An empty Redis would otherwise answer TAKEN for every seat until the worker's next tick | 0006 |
| `inventory_load` keeps fence ≥ max PG fence and sets `ver` ≥ now_ms | Fences stay monotonic (I6); ETags never repeat across a flush | 0006 |
| Availability falls back to PG (`pg_only`, not loaded); `ETag` `"r<ver>"` / `"p<sum>"`; bit packing defined | Redis isn't maintained on `pg_only`; the packing was unspecified | 0006 |
| Replay body is semantically equal, not byte-equal | `response_body` is `jsonb` | 0006 |
| `DELETE /holds` on RELEASED or EXPIRED = 204 no-op; on PAYMENT_PENDING or CONSUMED = 409 `ORDER_STATE` | Unspecified; idempotent delete | 0006, API §3.12 |
| Holds aren't gated on `events.status` in P2 | Admission (P4) controls timing; test admit bypasses it on purpose | 0006 |
| `Consume` is Redis-only | PG's half (SellSeats + ConsumeHold) belongs to ConfirmOrder's tx in P3 | 0006 |
| The worker loads every non-CLOSED event into Redis (SCHEDULED too) | The rush event is ready before its sale opens | none (detail) |

Assumptions:
- `NS_HOLD` = UUIDv5(NameSpaceURL, `https://sluice.dev/ns/hold`).
- Hold expiries are whole milliseconds.
- In `pg_only`, best-available can answer INSUFFICIENT while the last seats are locked by in-flight transactions that later roll back (documented in ADR 0006).

## 6. Known issues and debt
- **P3 hooks:** the sweeper's PG pass carries a `// P3:` marker where it must also expire the hold's order and enqueue `void_payment`. Takeovers in `Service.commit` need the same.
- An admin rebuild during live traffic can drop a grant that commits between the snapshot and the load. PG still refuses a second hold on those seats (cost: compensations, never seats). This is documented on `Rebuild` and in ADR 0006, and P7 chaos C1 should exercise it.
- Rate limiting on the holds route (API §2, 20/min/user) isn't in P2's task list; P7 hardening.
- The `holds_requests_total` `invalid` result also counts an unknown event.
- Coverage of `internal/app` is 17.3%; the worker wiring was verified by the manual run, not unit tests.
- The govulncheck module-level advisory (`GO-2026-5932`, x/crypto) is still open from P1; there's no tagged fix yet.
- Logs print local time (+05:30) through slog's default time format, which predates P2.

## 7. Understand this before moving on (Sheshakanth's interview prep)
1. **Q:** Redis granted the seat, so why can the user still get 409?
   **A:** Redis is only the fast path. `Service.commit` (`internal/inventory/hold.go`) runs `TakeSeats`, a conditional `UPDATE ... WHERE state='AVAILABLE' OR (HELD AND held_until <= now)`, and rolls back unless the row count equals the seat count. If Redis drifted (a flush, a crash between Redis and PG, a manual edit), PG refuses. `compensate` then runs `hold_release` and `inventory_repair` with PG's seat states, and the user gets `SEATS_TAKEN`. `TestCompensation` injects exactly this.
2. **Q:** How does the same request retried after a crash not create a second hold?
   **A:** Three layers. (a) The idempotency row (`httpx/idempotency.go`) replays a completed response or answers 409 while one is in flight; 20 concurrent same-key requests executed once (`TestHoldAPIConcurrentSameKey`). (b) A lapsed STARTED row re-executes, which is safe because the hold id is deterministic: `HoldID` = UUIDv5(NS_HOLD, user:key). Redis answers REPLAY with the original fence and seats, and `commit` finds the PG row with `GetHold` and replays it. (c) Two inserts racing on the same id hit `holds_pkey`, and the loser re-runs as a replay (`errConcurrentReplay`).
3. **Q:** Redis loses everything mid-sale. What happens?
   **A:** Nothing is oversold, because I1 lives in PG constraints. The hold scripts answer `NOT_LOADED`, and `EnsureLoaded` (`rebuild.go`) takes `rebuild_lock` and runs `Rebuild`: reclaim expired holds in PG, take one REPEATABLE READ snapshot, then `inventory_load`. That restores avail, sold, every live hold hash, `holding`, a fence floor of max(PG fence) and `ver ≥ now_ms`. The worker also does this on every tick. `TestRebuildAfterFlushAll` shows a seat-by-seat match after 300 holds plus releases, extends and sales; live, the worker reloaded within one poll after `redis-cli FLUSHALL`.

## 8. Files changed
`git status --short` (before writing this report):
```
 M Taskfile.yml
 M cmd/sluice/main.go
 M cmd/sluice/main_test.go
 M db/queries/events.sql
 M db/queries/holds.sql
 M db/queries/seats.sql
 M docs/API.md
 M docs/ARCHITECTURE.md
 M docs/DATA-MODEL.md
 M docs/STACK.md
 M go.mod
 M go.sum
 M internal/app/api.go
 M internal/app/api_integration_test.go
 M internal/db/gen/events.sql.go
 M internal/db/gen/holds.sql.go
 M internal/db/gen/seats.sql.go
 M internal/events/store.go
?? docs/adr/0006-hold-path-details.md
?? internal/app/inventory.go
?? internal/app/worker.go
?? internal/auth/
?? internal/httpx/idempotency.go
?? internal/httpx/idempotency_test.go
?? internal/inventory/availability.go
?? internal/inventory/fixture_test.go
?? internal/inventory/handler.go
?? internal/inventory/handler_integration_test.go
?? internal/inventory/hold.go
?? internal/inventory/hold_integration_test.go
?? internal/inventory/rebuild.go
?? internal/inventory/service.go
?? internal/inventory/sweep_integration_test.go
?? internal/inventory/sweeper.go
?? internal/redisx/
?? internal/testutil/redistest/
```
Plus `internal/inventory/state.go` (package comment only) and this report. By area:
- Go: `internal/auth`, `internal/redisx` (+ `keys`, `scripts/*.lua`), `internal/inventory`, `internal/httpx/idempotency.go`, `internal/app` (api, worker, inventory wiring), `internal/events/store.go`, `cmd/sluice`.
- SQL/codegen: `db/queries/{seats,holds,events}.sql`, `internal/db/gen`.
- Tooling: `Taskfile.yml` (`dev:worker`), `go.mod`/`go.sum` (+ `golang-jwt/jwt/v5 v5.3.1`), `internal/testutil/redistest`.
- Docs: ADR 0006, DATA-MODEL, API, ARCHITECTURE, STACK.

## 9. Next phase readiness
- P3 can build on:
  - `Service.Extend` (ACTIVE → PAYMENT_PENDING, seats re-dated; Redis `hold_extend`).
  - `Service.Consume` (Redis mirror after ConfirmOrder commits).
  - `SellSeats` / `ConsumeHold` / `AddQuota` (P1) for the confirm tx.
  - The idempotency middleware for `POST /orders`, `/pay` and `/confirm`.
  - Admission and session auth via `Keyring.Require`.
- P3 must add order expiry and `void_payment` to the sweeper's PG pass and to PG takeovers (the `// P3:` marker in `sweeper.go`).
- Sheshakanth: commit, push and confirm CI is green. CI already runs `task test:race`, and the new tests need Docker, which `ubuntu-latest` has.
- The local dev DB keeps the manual check's three hold rows (all expired by the 15 s TTL, so harmless).

## 10. Git commands (Sheshakanth runs these)
PowerShell-safe: one command per line, no shell-specific redirections.
```bash
git add -A
git status --short
git commit -m "feat(holds): Lua-atomic TTL seat holds with Postgres fencing" -m "- Redis Lua all-or-nothing holds, best-available, extend, consume, release" -m "- Postgres hold tx with row-count fencing, expired-seat takeover, compensation" -m "- Sweeper in sluice worker, Redis rebuild from Postgres (R1), availability cache" -m "- JWT sessions and test admission, Postgres-backed idempotency middleware" -m "- Race tests: 2k on one seat, 10k on 500 seats, both hold paths, under -race" -m "Phase P2 - report: docs/reports/P2-holds.md"
```
