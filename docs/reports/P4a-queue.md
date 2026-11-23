# P4a: Virtual waiting room (tasks 1–3): Report

| Field | Value |
|---|---|
| Status | DONE for session P4a (tasks 1–3 of `docs/phases/P4-queue.md`). Tasks 4–7 (snapshots/restore, rate limiting, `/stats`, the full metric set) come in P4b. |
| Date (IST) | 2026-11-23 |
| Time spent vs timebox | Not measured precisely; one session, inside the 4 h P4 timebox. |
| Commit base | `5755b5d` (`git rev-parse --short HEAD`) |

## 1. What was built
- **Start-of-session clean-up:** the previous, stopped attempt had left only a stray `github.com/go-redis/redis_rate/v10` (indirect) entry in `go.mod`/`go.sum`, with no partial source files. `go mod tidy` removed it; rate limiting is P4b's task 5.
- **Task 1 — scripts** (`internal/redisx/scripts/`): `queue_join.lua`, `queue_resolve.lua`, `queue_claim.lua`, `queue_leave.lua`, `queue_tick.lua`. They are registered in `redisx.go`. Key lists for each script live in `internal/redisx/keys` (`QueueJoinKeys`, ..., `SnapshotKeys`), which also gains the new keys `QPre`, `QUser` and `QRateAt`. Time, the lottery score (53 bits from crypto/rand) and the rate τ all come from Go via ARGV.
- **Task 2 — `internal/queue`:**
  - `service.go`: `Join` (idempotent; `rejoin` at most 2 times), `Status` and `Leave`, plus the status math from ARCHITECTURE §5 (position, ahead, eta, jittered retry_after, outlook, PAUSED).
  - `snapshot.go`: `Cache`, per event, behind `sync.Map` + `atomic.Pointer[Snapshot]`. `Run` refreshes each known event with one `snapshot.lua` call every 100 ms. `Peek` never touches Redis.
  - `handler.go`:
    - `POST /api/v1/events/{eventId}/queue` (session token)
    - `GET .../queue/status` (queue token)
    - `DELETE .../queue` (queue or admission token)
    Tokens for another event get 401; a dependency failure gets 503 `DEPENDENCY_UNAVAILABLE`.
  - WAITING status costs zero Redis calls. A pre-queue token after T0 costs one `queue_resolve` and gets its token re-issued with the ticket. ticket ≤ cursor costs one `queue_claim` and returns the admission token (TTL = the claim's expiry). SOLD_OUT is answered straight from the snapshot.
  - `/holds` and `/orders` already required an admission token under `QUEUE_ENFORCED` (P2/P3, `inventory/handler.go`, `orders/handler.go`). Nothing new was needed there.
- **Task 3 — admission controller** (`internal/queue/controller.go`):
  - `Ticker` is the Redis side (`queue_tick.lua`, stateless, safe to run concurrently).
  - `Controller` re-reads the open events every 1 s and runs one loop per non-CLOSED event every `QUEUE_TICK`.
  - PG transitions go through the new sqlc query `TransitionEventStatus` (a conditional UPDATE): SCHEDULED → ON_SALE on the first admitting or paused tick, and → SOLD_OUT on the sold-out tick.
  - admit_rate_ema is kept inside `queue_tick.lua`.
- **Wiring:**
  - `internal/app/queue.go` (`newQueueService`, `newQueueController`).
  - `api.go` mounts the routes and runs the snapshot refresher.
  - `worker.go` runs the controller.
  - `newScripts` gives the API and worker one shared script set. Previously `newInventory` built it, and a second `NewScripts` would have registered the histogram twice.
  - `inventory.EventInfo` gains `StartsAt`, because queue tokens live until the event starts.
- **Supporting changes:**
  - Metric `queue_status_redis_calls_total`. The rest of the P4 metric set is task 7.
  - Error code `REJOIN_LIMIT` (409).
  - ADR 0008, plus matching edits to DATA-MODEL §4/§5, ARCHITECTURE §5 and API §1.3.

## 2. Acceptance checklist (this session's scope)
The gate run was `task lint` → `0 issues`, then `CGO_ENABLED=1 go test -race -count=1 -v ./...` → every package `ok`, 142 `--- PASS`, 0 `--- FAIL`.

| Required test | Test | Evidence from the race run above |
|---|---|---|
| ✅ Lottery | `queue_test.go:TestLottery` | Every ticket equals score rank + 1 (asserted for all 10,000). `lottery: n=10000 spearman(join order, ticket) rho=-0.0142` |
| ✅ FIFO after T0 | `TestFIFOAfterT0` | 200 post-T0 joins get tickets 101..300 in order, with ticks interleaved |
| ✅ Freeze race | `TestFreezeRace` | `freeze race: 1529 pre-queue + 1471 post-T0 joins, tickets 1..3000 with no duplicates or gaps` (32 joiners, the clock flipped mid-stream, 3 concurrent tick loops; a repeated join returns the same ticket) |
| ✅ Admission order | `TestAdmissionOrder` | 300 tickets at 7 per tick: every tick's new offers are exactly cursor+1..cursor+k, the combined sequence is 1..300, and the next in line reads WAITING position 1 |
| ✅ Bounded admission | `TestBoundedAdmission` | `150 rounds (150 admitting, 0 paused), cursor 2000, 1073 holds, 1073 releases, max (seeking+outstanding)/target after an admitting tick = 1.000`. Uses real `inventory.Hold`/`Release` against PG and Redis. Every tick was checked; a tick that admitted nobody had to have had no room |
| ✅ Concurrent controllers | `TestConcurrentControllers` | `1 controller(s): cursor 750, offers 750, worst sampled seeking+outstanding 750` / `3 controller(s): cursor 750, offers 750, worst sampled ... 750`, and PG ends ON_SALE in both runs |
| ✅ Sold out | `TestSoldOut` | Tick → SOLD_OUT, the `q:soldout` flag set, PG goes SOLD_OUT, the cursor stops moving, all 30 tickets' status is SOLD_OUT, every `queue_claim` returns SOLD_OUT, a new join gets SOLD_OUT with no token, and a hold fails |
| ✅ Offer expiry and rejoin | `TestOfferExpiryAndRejoin` | The expired offer's permit goes to ticket 2. Status is OFFER_EXPIRED. Rejoin 1 gets ticket 3 (the back) and rejoin 2 gets ticket 4. The 3rd rejoin over HTTP returns 409 `REJOIN_LIMIT` |
| ✅ Zero-Redis status | `TestZeroRedisStatus` | `10000 WAITING status calls -> 0 Redis commands, queue_status_redis_calls_total +0`. Counted by a go-redis hook on the client, through the full HTTP handler |
| ✅ Script contracts | `redisx/queue_scripts_test.go` (3 tests) | Every queue script was tested directly against Redis 8, including `NOT_LOADED` (never mistaken for sold out) |
| ⏭ Scale, Restore, manual curl flow, `/stats` | — | P4b (tasks 4–7), as instructed |

## 3. Tests
| Suite | Count | Result | Notes |
|---|---|---|---|
| queue integration (testcontainers PG 18 + Redis 8) | 9 | pass | `-race`, local |
| redisx script tests (new) | 3 | pass | `-race`, local |
| full repo | 142 top-level `--- PASS` | pass | `-race`, local (gcc available) |

Coverage (`go test -count=1 -cover ./internal/queue/ ./internal/redisx/`): queue 71.0%, redisx 56.5%.

**An earlier run in this session was not green.** The first full race run failed in two places:
- `httpx` `TestProblemCatalogue` (`test table has 19 codes, catalogue has 20`): I hadn't added `REJOIN_LIMIT` to the test table. Fixed.
- `events`: testcontainers could not start Postgres (`rootless Docker is not supported on Windows, failed to create Docker provider`). That is a Docker-provider error at container start, not a test assertion. The package passed on the rerun and in the full gate run.

## 4. Numbers
The only numbers are the test log lines quoted in §2, from `CGO_ENABLED=1 go test -race -count=1 -v ./...`. There are no load or benchmark numbers in this session; the 50k scale test is P4b.

## 5. Deviations and decisions
| What changed vs the spec | Why | ADR |
|---|---|---|
| Pre-path ZADD only while `presize` is unset, rather than whenever `now < T0` | Closes the freeze race (a late pre join would collide with a tail ticket) | 0008 |
| `queue_tick` returns `NOT_LOADED` when `meta.loaded` ≠ 1 | Empty inventory must never read as sold out | 0008 |
| `queue_tick` ARGV gains `sale_opens_ms` and `rate_tau_ms`; ADMITTING returns `target, seeking, outstanding, expired_offers` | T0 comes from PG (re-timing safe); the extra fields are for tests and metrics | 0008 |
| seeking = `ZCARD active − ZINTERCARD(active, holding)` | Exact count; holders outside `active` no longer undercount it | 0008 |
| admit_rate_ema = time-decayed counter (τ 5 s), plus new key `q:rate_at` | Well-defined for concurrent or irregular ticks | 0008 |
| Rejoin eligibility enforced in `queue_join`; new 409 `REJOIN_LIMIT` | The spec said "only after OFFER_EXPIRED" but nothing enforced it | 0008 |
| `queue_resolve` → `PENDING`; `queue_claim` → `WAITING`; `queue_leave(uid, ticket)` | Undefined cases; leave now frees the offer too | 0008 |
| Status transitions via the conditional `TransitionEventStatus` | Concurrent controllers and re-timing never flip SOLD_OUT back | 0008 |
| `TransitionEventStatus` compares `status::text = ANY(text[])` | pgx cannot encode an array of an unregistered enum type | (in the query comment) |

**Assumptions:**
- Join for an event whose PG status is SOLD_OUT or CLOSED answers SOLD_OUT or CLOSED without calling Redis.
- A status call for a token whose Redis entry is gone re-joins the user at the back. Full restore semantics are P4b.

## 6. Known issues and debt
- The snapshot refresher runs `snapshot.lua` once per event per 100 ms, sequentially, for every event a replica has served. That's fine for a handful of events; many events would want this batched or pipelined.
- The controller logs PG or Redis errors at most once per 5 s per event. There are no controller metrics yet (task 7).
- R1 is still open for the queue. After a FLUSHALL, ticks return NOT_LOADED until the inventory rebuild, and queue counters restart from 0 until restore lands (task 4).

## 7. Understand this before moving on (interview Q&A)
1. **Q:** How do you serve 50k pollers without hammering Redis?
   **A:** `queue/service.go:Status`. A WAITING user's ticket is inside their signed queue token, and the cursor and rate come from `queue/snapshot.go:Cache`, an `atomic.Pointer[Snapshot]` per event that one goroutine refreshes every 100 ms with a single `snapshot.lua`. The status path is a JWT verify plus arithmetic. `TestZeroRedisStatus` counts 0 Redis commands over 10,000 calls via a client hook.
2. **Q:** How is the lottery fair, and how do you stop the T0 race from creating duplicate tickets?
   **A:** Each pre-queue join gets a 53-bit crypto/rand score (`queue.go:randScore`) and lands in a ZSET; ticket = ZRANK+1. `queue_join.lua` and `queue_tick.lua` freeze `presize` atomically, and after the freeze no join may take the pre path. So pre ranks are exactly 1..presize and post-T0 tickets are presize + INCR tail. `TestFreezeRace` (3000 joins straddling T0 with 3 ticking loops) shows 1..3000 with no duplicates or gaps.
3. **Q:** Several workers run the admission controller. Why can't they over-admit?
   **A:** One step is one atomic Lua script (`queue_tick.lua`). It reads seeking, outstanding and avail, computes k = min(target − seeking − outstanding, max_per_tick, waiting), and advances a monotonic cursor, all inside one script execution. Concurrent tickers just serialize. `controller.go:Controller` has no admission state of its own. `TestConcurrentControllers` reaches cursor 750 = ⌈500 × 1.5⌉ with both 1 and 3 controllers.

## 8. Files changed (`git status --short`)
- **Queue (new):**
  - `internal/queue/`: queue.go, service.go, snapshot.go, handler.go, controller.go, fixture_test.go, queue_test.go
  - `internal/app/queue.go`
- **Scripts:**
  - `?? internal/redisx/scripts/queue_{join,resolve,claim,leave,tick}.lua`
  - `?? internal/redisx/queue_scripts_test.go`
  - `M internal/redisx/redisx.go`
  - `M internal/redisx/keys/keys.go`
- **Wiring:**
  - `M internal/app/api.go`
  - `M internal/app/worker.go`
  - `M internal/app/inventory.go`
  - `M internal/inventory/service.go`
- **SQL:**
  - `M db/queries/events.sql`
  - `M internal/db/gen/events.sql.go` (`task gen`)
- **HTTP:**
  - `M internal/httpx/problem.go`
  - `M internal/httpx/httpx_test.go`
- **Docs:**
  - `M docs/API.md`
  - `M docs/ARCHITECTURE.md`
  - `M docs/DATA-MODEL.md`
  - `?? docs/adr/0008-queue-script-details.md`
  - `?? docs/reports/P4a-queue.md`

## 9. Next session readiness (P4b: tasks 4–7)
- **Task 4:** the worker writes `queue_snapshots` (the sqlc queries already exist in `db/queries/queue.sql`), and restore runs when queue keys are missing for an ON_SALE event.
- **Task 5:** redis_rate, `go get` again at that point.
- **Task 6:** `/stats`, reading the snapshot cache and the `audit` hash.
- **Task 7:** the remaining metrics; `TickResult` already carries the fields they need.
- **Also in P4b:** the scale and restore tests, and the manual curl flow.

## 10. Git commands
See the end of the session summary (PowerShell-safe, one command per line).
