# P4b: Virtual waiting room (tasks 4–7): Report

| Field | Value |
|---|---|
| Status | DONE for session P4b (tasks 4–7 of `docs/phases/P4-queue.md`). With P4a (tasks 1–3), every P4 acceptance check is now met. |
| Date (IST) | 2026-11-23 |
| Time spent vs timebox | Not measured precisely; one session. P4a + P4b together fit inside the 4 h P4 timebox. |
| Commit base | `002bd04` (`git rev-parse --short HEAD`) |

## 1. What was built
- **Task 4: snapshots and restore (R1).**
  - **Snapshot data:**
    - Migration `0002_queue_snapshot_state.sql` adds `admit_rate`, the outstanding `offers` and the live admissions (`active_uids`, `active_exp_ms`) to `queue_snapshots`.
    - `UpsertQueueSnapshot` never replaces a newer row (`taken_at` guard).
  - **Scripts:** `queue_dump.lua`, `queue_restore.lua` and `queue_reopen.lua` (new); `queue_join.lua`, `queue_tick.lua`, `queue_claim.lua` and `snapshot.lua` (extended).
  - **Restore gate `q:init`:**
    - `queue_join` and `queue_tick` answer `NOT_INIT` until the counters have been restored from PG.
    - `Restorer.Restore` (`internal/queue/restore.go`) runs from both the API join path and the worker's `Ticker` and is idempotent.
    - Without the gate, a flushed Redis would reissue ticket numbers and re-offer from cursor 0.
  - **Snapshot writer:** `Snapshotter` (worker) writes every open event's state every 1 s. It skips a flushed queue that hasn't been restored yet, so a flush can't overwrite the last good snapshot.
- **Task 5: rate limiting** (`internal/ratelimit`, `internal/app/ratelimit.go`).
  - **Redis GCRA (`redis_rate` v10) per API.md §2:**
    - sessions 20/min/IP
    - join 6/min/user + 30/min/IP
    - holds 20/min/user
    - orders, pay (and new payment attempts) and confirm 10/min/user
  - **Status limiter:** in-process, 3/s, burst 6, kept in a 16-shard LRU bounded to 200k users per replica.
  - **Client IP:** taken from `TRUSTED_PROXIES` plus `X-Forwarded-For`, using the right-most hop that isn't a trusted proxy.
  - **Responses:** a 429 is `RATE_LIMITED` with `Retry-After` and the IETF `RateLimit-Policy`/`RateLimit` fields.
  - **Who counts as the user:** the verified token subject (`auth.Keyring.Subject`).
  - **Failure mode:** fails open if Redis is down.
  - **Wiring:** a new `httpx.Router.Wrap` per-route decorator, so no handler package changed.
- **Task 6: `/stats`** (`internal/queue/stats.go`). `GET /api/v1/stats/{eventId}` per API.md §3.9, with each replica computing it at most once per second per event. Sources:
  - event status from PG
  - queue numbers from the snapshot cache
  - seat counts from the live counts
  - correctness from `sl:{e}:audit`
- **open-sale.** `POST /api/v1/admin/events/{eventId}/open-sale` was listed in API.md but had never been built, and the manual check needs it.
  - It only re-times a queue nobody has joined; otherwise it returns 409 with the new code `EVENT_STATE`.
  - It sets the `q:opens` T0 override, so stale event caches can't re-freeze the queue at the old T0.
- **Task 7: metrics.**
  - API: `queue_joins_total{result}`, `queue_status_total{state}`, `queue_admitted_total` (new admissions only: `queue_claim` now returns a `new` flag) and `queue_status_redis_calls_total`.
  - Worker: `queue_waiting|cursor|offers|active{event}` and `queue_offer_expired_total`.
  - Plus `ratelimit_decisions_total{rule,result}`.
- **Docs:**
  - ADR 0009.
  - DATA-MODEL §1/§4/§5/§6, API §1.3/§1.5/§2/§3.9, ARCHITECTURE §5 and the STACK layout.
  - The P5 migration was renumbered to 0003.
  - Manual-check script: `loadtest/manual/p4-flow.py`.

## 2. Acceptance checklist
Gate (final, after the fixes in §10): `task lint` → `0 issues.`, then `task test:race -- -count=1 -v` (now `go test -race -p 2 -timeout 20m ./...`) **twice in a row**: run A `exit=0` in 294 s, run B `exit=0` in 261 s; each **154 `--- PASS`, 0 `--- FAIL`, 0 `DATA RACE`, 19/19 packages `ok`, 0 `infra: container failed to start`**. (The first P4b gate was `CGO_ENABLED=1 go test -race -count=1 -p 1 -v ./...` → 150 PASS; see §3.)

- [x] **All P4 required tests green.** The P4a nine, re-run in the gate above, all pass:
  - `lottery: n=10000 spearman(join order, ticket) rho=0.0072`
  - `freeze race: ... tickets 1..3000 with no duplicates or gaps`
  - `bounded admission ... max (seeking+outstanding)/target ... = 1.000`
  - `1 controller(s): cursor 750` and `3 controller(s): cursor 750`
  - `zero-redis status: 10000 WAITING status calls -> 0 Redis commands`
- [x] **Scale (integration)**, `TestScaleJoins`: 25k joins before T0 into the lottery and 25k after it, 64 at a time, under `-race`. Budget: 120 s.
  - Gate run: `scale: 50000 joins (par 64) in 8.318s = 6011 joins/s; Redis used_memory 2212784 -> 12004376 bytes (+9.3 MiB, 196 B/user)`.
  - The two parallel full runs earlier: 10.369 s and 10.275 s (CPU shared with the other packages).
- [x] **Restore after FLUSHALL**, `TestRestoreAfterFlushAll`: `restore: 50 tickets, cursor 10, 7 admitted + 3 offers; after FLUSHALL: next ticket 51, 40 waiting positions unchanged, 7 admissions kept (+0 new), 3 offers claimed (+3), active 10`. It also checks two other things:
  - The writer skips the flushed queue.
  - An older snapshot row can't overwrite a newer one.
- [x] **Rate-limit tests**, `internal/ratelimit` (92.4% coverage):
  - `TestClientIP`: 11 XFF cases.
  - `TestGCRA`:
    - Per-IP limit: 20 allowed, then a 429 with `Retry-After: 3` and the RateLimit fields.
    - Join: user 6/min and IP 30/min.
    - **60 concurrent requests through 3 limiter instances → exactly 20 allowed / 40 limited.**
    - Redis down → fails open.
  - `TestLocal`: burst 6, then 3/s; the LRU stays ≤ 64.
  - `TestLocalConcurrent`: 64 goroutines with a frozen clock → exactly 6 allowed per user.
  - Script contracts: `TestQueueRestoreDumpReopen`.
- [x] **Manual flow**, `python loadtest/manual/p4-flow.py tatkal-chn-sbc 30`. Setup: `sluice api`, `sluice mockpay`, and `sluice worker` run with `QUEUE_MAX_ACTIVE=1 QUEUE_OVERADMIT=1 QUEUE_MIN_ADMIT=1 QUEUE_MAX_ADMIT_PER_TICK=1`, so the second user visibly waits. Output excerpt:
  ```
  [  0.13s] admin: open-sale -> T0 2026-11-23T17:16:16.906Z (30 s)
  [  0.16s] A: join -> PREQUEUE ticket=None retry_after_ms=500
  [  0.16s] B: join -> PREQUEUE ticket=None retry_after_ms=500
  [ 30.71s] A: PREQUEUE -> WAITING ticket=2 position=1 ahead=0 outlook=LIKELY
  [ 30.73s] B: PREQUEUE -> ADMITTED ticket=1
  [ 30.76s] B: hold a9209bae-... seats=['C9-35', 'C10-31'] expires=2026-11-23T17:21:17.534Z
  [ 30.94s] B: order 01a20968-914d-... PENDING_PAYMENT amount_paise=250000 payment=CREATED
  [ 31.11s] B: pay -> CONFIRMED charged=True tickets=[('C9-35', 'SLC-85GY-YJKB'), ('C10-31', 'SLC-J975-WQJP')]
  [ 31.62s] A: WAITING -> ADMITTED ticket=2
  [ 31.93s] A: pay -> CONFIRMED charged=True tickets=[('C3-26', 'SLC-MJ46-7MAD'), ('C4-15', 'SLC-BUWZ-4PM5')]
  ```
  - Worker log: `queue restored ... fresh:true`, and `event status SCHEDULED -> ON_SALE` at `22:46:16.996` IST, 90 ms after T0.
  - Live rate limits (`curl`): after 20 × 201, the next `POST /sessions` returned `429`, `RateLimit-Policy: "sessions-ip";q=20;w=60` and `Retry-After: 2`. Two requests from the trusted IPv4 peer with different `X-Forwarded-For` values got `201` while that bucket was exhausted.
- [x] **`/stats` reflects queue and inventory live.** The script's last line, and `curl -s localhost:8080/api/v1/stats/tatkal-chn-sbc` 1.8 s later, returned:
  `"queue":{"joined":2,"waiting":0,"cursor":2,"offers":0,"active":2,...},"inventory":{"available":496,"held":0,"sold":4,"capacity":500},"correctness":{"oversold":0,"duplicate_charges":0,"orphan_charges":0,"checked_at":"2026-11-23T17:16:32.502Z"}`.
  - Some intermediate lines in the script show values up to 1 s old. That's the documented per-replica cache.
  - `TestStatsAndOpenSale` covers the cache boundary, the audit block and the open-sale rules.

## 3. Tests
| Suite | Count | Result | Notes |
|---|---|---|---|
| full repo, `task test:race` (`-race -p 2`), twice | 154 top-level `--- PASS` each | pass | local (gcc available); first P4b gate was `-p 1`, 150 PASS |
| added by the fixes (§10) | 4: `TestCreateOrdersConcurrent`, `TestErrorAttrs`, `tcretry.TestStart`, `TestIssueUntil` | pass | |
| new this session | 9: `TestRestoreAfterFlushAll`, `TestScaleJoins`, `TestStatsAndOpenSale`, `TestQueueRestoreDumpReopen`, `TestClientIP`, `TestGCRA`, `TestLocal`, `TestLocalConcurrent`, plus updated `TestSnapshot` / `TestMigrationsUpDownUp` | pass | testcontainers PG 18 + Redis 8 |

Coverage (`go test -count=1 -cover`): queue 69.7%, ratelimit 92.4%, redisx 56.5%.

**Not every run was green. The earlier runs, in order:**
1. **First parallel `-race` run (`./...`): 4 packages failed.**
   - Two were my stale tests: `TestSnapshot` still passed the old 11-key list, and `TestMigrationsUpDownUp` expected goose version 1. Both are fixed.
   - `events`: `rootless Docker is not supported on Windows` at container start. This is the same provider flake P4a hit.
   - `orders` `TestPayTimeoutAfterCommit`: `create order: HTTP 500 INTERNAL`. The handler maps a PG error carrying a SQLSTATE to 500, and the harness discards that log, so **the root cause is unknown**. It did not reproduce: 3/3 alone and 20/20 for the full package under `-race`. No file under `internal/orders`, `inventory` or `payments` changed this session.
2. **Second parallel run: 3 packages failed on infrastructure.**
   - `app` and `db`: the testcontainers Postgres start timed out (`context deadline exceeded` on the Docker pipe), and `rootless Docker is not supported`.
   - `orders` `TestPaymentsProperty`: `context deadline exceeded` at 240.02 s, which is the 4-minute harness deadline.
3. **Third run, packages one at a time (`-p 1`): fully green.** That was the first gate; the final gate (§2, §10) runs `-p 2`.
4. **Coverage run:** one `ratelimit` FAIL in 0.39 s, before any test ran. That matches a container start failure; its output was not captured. The rerun passed at 92.4%.

## 4. Numbers
All come from the first (`-p 1`) gate run unless noted:
- 50k joins: **8.318 s (6011 joins/s), +9.3 MiB Redis, 196 B/user**. In the parallel runs: 10.369 s and 10.275 s. Final `-p 2` gates: A 7.654 s (6532 joins/s, +9.3 MiB, 195 B/user), B 7.761 s (6443 joins/s, +9.3 MiB, 196 B/user).
- 300 concurrent order creates (200 holds + 100 racing duplicates): every final run `map[201:300]`, 200 orders / 200 payments / 200 intents.
- Lottery ρ = 0.0072.
- 10,000 WAITING status calls → 0 Redis commands.

## 5. Deviations and decisions (ADR 0009)
| What changed vs the spec | Why |
|---|---|
| `q:init` restore gate; `NOT_INIT` from `queue_join` / `queue_tick` | Otherwise a wiped Redis reissues ticket numbers and re-offers from cursor 0 |
| The snapshot stores the live offers and admissions; restore re-creates exactly those, not the spec's `(cursor − TTL × rate, cursor]` window | The window would re-offer claimed tickets, and `q:active` is lost, so users could be admitted twice |
| `queue_claim` → `ADMITTED exp new` | `queue_admitted_total` counts real admissions only |
| open-sale only for a queue nobody has joined (409 `EVENT_STATE`), plus the `q:opens` override | Re-timing a queue with entries breaks people's places; the 5 s and 1 s event caches could re-freeze the queue at the old T0 |
| `sold_out_at` is always `null` | `events` has no such column |
| GCRA keys `rate:sl:rl:...`; the limiters fail open; the user is verified before the route's `Require` (one extra HMAC) | Library prefix; the limiter protects capacity and isn't a correctness check; packages stay unchanged |
| Migration 0002 = queue snapshot state; P5's naive baseline → 0003 | Next free number |

## 6. Known issues and debt
- **Lottery entries aren't snapshotted.** After a flush, a pre-queue user who hasn't resolved their ticket rejoins at the back.
- **The last ≤ 1 s before a flush.** A claim made after the last snapshot can be repeated once by the same uid (same `active` entry; HOLD_LIMIT and the PG quota still bind).
- **`TRUSTED_PROXIES` doesn't include `::1`.** Locally, `curl localhost` arrives from `::1`, so its XFF is ignored. That's correct, but surprising; through Caddy (P5) the peer is 172.16/12.
- **`/stats` `waiting`** doesn't subtract users who left.
- **open-sale vs replica caches.** The API replica that served open-sale still shows the old `opens_at` for ≤ 5 s (event cache). Queue behaviour is correct because of `q:opens`.
- **The one `orders` 500 from the first gate never reproduced** (§10: about 600 package test runs and 37,200 concurrent order creates, 0 HTTP 500). Its cause stays unknown. Any future 500 is diagnosable: handlers log the wrapped error plus SQLSTATE, and the test harness prints the server log on failure.
- **Testcontainers on Docker Desktop/Windows.** Starts are now retried (3 attempts with backoff) and tests run `-p 2`; two consecutive `-p 2` gates were green with 0 infra failures. That is two runs, not proof that the flake is gone.

## 7. Interview Q&A
1. **Q:** Redis is wiped mid-rush. Why can't the queue hand out ticket 1,001 twice, or re-admit from the start?
   **A:** Two Lua checks stop it:
   - `queue_join.lua` and `queue_tick.lua` refuse to run (`NOT_INIT`) until `q:init` exists.
   - `queue.Restorer.Restore` sets it only after `queue_restore.lua` has raised tail and cursor to the PG snapshot's values. It's idempotent, so every replica can race to restore.

   The worker's `Snapshotter.SnapshotOnce` never writes while `q:init` is missing, so the flush can't erase the snapshot. `TestRestoreAfterFlushAll`: next ticket 51, 40 positions unchanged.
2. **Q:** Why snapshot the offers and admissions instead of re-offering a window below the cursor?
   **A:** After a flush `q:active` is gone too, so a window would re-offer tickets that were already claimed: a second admission. `queue_restore.lua` re-creates exactly the offers that were outstanding (with fresh deadlines) and the live admissions (with their old expiries). A re-claim returns `ADMITTED exp 0`: 7 kept, +0 new, 3 offers claimed (+3).
3. **Q:** How do three API replicas share one rate limit, and why isn't status limited the same way?
   **A:** `ratelimit.GCRA` keeps its state in Redis (`redis_rate`'s GCRA), so every replica draws from the same bucket: `TestGCRA` sent 60 concurrent requests through 3 instances and got exactly 20 allowed. Status is the hot path for 50k pollers and must cost zero Redis calls, so `ratelimit.Local` is a per-replica token bucket in a bounded sharded LRU. Its limit is per replica; the cost is that a user spread across 3 replicas gets up to 3× the rate, and that's acceptable for a read path.

## 8. Files changed (`git status --short`)
- **Migrations and SQL:**
  - `?? db/migrations/0002_queue_snapshot_state.sql`
  - `M db/queries/queue.sql`
  - `M internal/db/gen/{models,queue.sql}.go` (`task gen`)
- **Queue:**
  - `?? internal/queue/{restore,stats,restore_test}.go`
  - `M internal/queue/{controller,queue,service,snapshot,fixture_test}.go`
- **Scripts:**
  - `?? internal/redisx/scripts/{queue_dump,queue_restore,queue_reopen}.lua`
  - `M internal/redisx/scripts/{queue_join,queue_tick,queue_claim,snapshot}.lua`
  - `M internal/redisx/{redisx.go,keys/keys.go,queue_scripts_test.go,scripts_test.go}`
- **Rate limiting:**
  - `?? internal/ratelimit/` (ratelimit.go, local.go, ratelimit_test.go)
  - `?? internal/app/ratelimit.go`
  - `M internal/auth/{jwt,http}.go`
  - `M internal/httpx/router.go`
  - `M go.mod` / `M go.sum` (`go-redis/redis_rate/v10 v10.0.1`)
- **HTTP and wiring:**
  - `M internal/httpx/{problem.go,httpx_test.go}` (`EVENT_STATE`)
  - `M internal/app/{api,queue,worker}.go`
  - `M internal/db/migrate_test.go`
- **Docs and tools:**
  - `?? docs/adr/0009-queue-restore-and-rate-limits.md`
  - `M docs/{API,ARCHITECTURE,DATA-MODEL,STACK}.md`
  - `M docs/phases/P5-rush.md`
  - `?? loadtest/manual/` (p4-flow.py)
- **Fixes (§10):**
  - `?? internal/testutil/tcretry/` (tcretry.go, tcretry_test.go)
  - `M internal/testutil/{pgtest/pgtest,redistest/redistest}.go`, `M internal/app/api_integration_test.go`
  - `M Taskfile.yml` (`-p 2 -timeout 20m`)
  - `?? internal/db/errlog{,_test}.go`, `M internal/{orders,inventory}/handler.go`
  - `M internal/payments/client.go` (PSP connection bound)
  - `M internal/auth/{jwt,jwt_test}.go`, `M internal/queue/service.go` (`IssueUntil`)
  - `?? internal/orders/create_concurrent_test.go`, `M internal/orders/{fixture_test,payments_integration_test,property_test}.go`
  - `M internal/mockpay/mockpay_test.go`
  - `M docs/API.md`, `M docs/adr/0009-queue-restore-and-rate-limits.md` (addendum)

## 9. Next phase readiness
P5 (rush) can start:
- The queue, restore, rate limits and `/stats` are in place.
- P5 should run `sluice migrate` (it applies 0002).
- P5 should name its baseline migration `0003_naive_baseline.sql`.
- The swarm must arrive through a `TRUSTED_PROXIES` peer for per-client XFF limits.

## 10. Fixes (before closing P4b)

### 10.1 The order-creation 500: not reproduced; safeguard added
- **Server log kept.** The orders harness logs to a buffer (JSON, debug level) shared by the API handlers, services, Recover, idempotency and MockPay. When a test fails, the buffer is printed with `t.Log` (`fixture_test.go` `newServerLog`).
- **Stress, package.** `CGO_ENABLED=1 go test -race -count=30 -timeout 60m -skip TestPaymentsProperty -v ./internal/orders/` → 20 tests × 30 = 600 test runs, including 30 of `TestPayTimeoutAfterCommit`, which hit the 500 originally. **0 HTTP 500, 0 `DATA RACE`.** The run still failed, in the new concurrent test (13/30) and once in `TestHappyPath`; see 10.2.
- **Stress, concurrent.** New `TestCreateOrdersConcurrent`:
  - 200 users each hold one seat, then 300 goroutines create the orders at once; 100 of them race a duplicate with a different Idempotency-Key.
  - It calls the API handler in process, because 300 simultaneous TCP dials overflow the Windows listen backlog of `httptest.Server`.
  - Across 120 stress runs (4 × `-count=30`) plus the 4 full gates: 37,200 create requests, **0 HTTP 500**. Every run ended with exactly 200 orders and 200 payments.
- **Verdict.** The 500 did not reproduce, so its root cause is unknown.
- **Safeguard.**
  - `db.ErrorAttrs(err)` logs the full wrapped error, `sqlstate`, severity and message. It also logs detail, hint, where, schema, table, column, constraint and routine when they are set.
  - The 500 branches of `orders/handler.go` and `inventory/handler.go` log it as `"<pkg>: unexpected database error"` (unit test `TestErrorAttrs`).
  - A panic 500 already logs the value and the stack (`httpx.Recover`).

### 10.2 Real problems the stress test exposed
1. **PSP connection storm, fixed in the product.**
   - The payments client had no bound on connections, so 300 concurrent creates dialled one connection each.
   - MockPay's accept queue overflowed on the loaded Windows host, and refused dials plus their retries kept it overflowing. The worst run had 8,869 refused dials and 2,985 timeouts over about 45 s.
   - The API answered 503 `DEPENDENCY_UNAVAILABLE` throughout. PG stayed correct (200 orders, 200 payments), but up to 187 of the 200 payments still had no PSP intent when the test ended (13 intents in the worst run; the client is expected to retry).
   - Before the fix, 13/30 runs failed in the package stress, when the test still expected no 503 at all. With the test retrying 503 as a real client does, 3/30 failed. With only the harness pool enlarged, 5/30 failed.
   - **Fix:** `payments.NewTransport` with `MaxConnsPerHost = 64` per process and a keep-alive pool of the same size. The harness now wraps this production transport. It had used `http.DefaultTransport`, which keeps 2 idle connections per host and has no bound.
   - **After:** `-count=30` of `TestCreateOrdersConcurrent|TestHappyPath` → 60/60 PASS and **0 retried 503s**. All 30 runs answered `map[201:300]` and ended with 200 orders, 200 payments and 200 intents. Documented in API.md §5 and the ADR 0009 addendum.
   - The test retries only 503 `DEPENDENCY_UNAVAILABLE`, with the same key (5xx answers are never stored). It fails on any other non-2xx answer.
2. **Admission expiry jitter: a real bug that `TestRestoreAfterFlushAll` caught in gate run 2.**
   - A token's `exp` was the keyring's clock truncated to the second, plus a TTL measured from the service's clock. So the same admission's `admission_expires_at` could differ by up to 1 s between claims.
   - **Fix:** `Keyring.IssueUntil(exp)`. The claim issues the token with the admission's absolute expiry from `q:active`, floored to the second. The token never outlives the admission, and every claim reports the same value.
   - Regression coverage: `TestIssueUntil`, plus `TestRestoreAfterFlushAll` with its assertion unchanged (same expiry after restore). 20× `-race` → pass. API.md §3 states the behaviour.
3. **`TestHappyPath` webhook ordering (a test assumption).**
   - Under load, `intent.authorized` reached the webhook handler after the pay call had already moved the payment to CAPTURED. The handler correctly recorded it as `regression_ignored`, so the test's "2 processed" never arrived.
   - The test now waits for 2 handled webhooks (processed plus regression_ignored), at least 1 of them processed.
   - The ledger check (exactly 1 AUTH, 1 CAPTURE, 0 VOID/REFUND) is unchanged. 60 stress runs → pass.
4. **`mockpay` `TestTimeoutAfterCommit` (a timing budget in a P3 test; no P4b code is involved).**
   - In gate run 2, the same-key replay overran the test's 200 ms client timeout under `-race -p 2` load. It had passed in the 4 other logged runs.
   - I confirmed the replay path has no hang: in `intents.go` `serve`, replays skip the hang fault.
   - **Changed, and stated here because it widens a timing bound:** the hang goes from 600 ms to 3 s, and the replay uses a 2 s client and must finish in under hang/3 = 1 s. The old bound was 150 ms (hang/4). A replay that waited for the hang still fails. 20× `-race` → pass.

### 10.3 Flakiness
- **`tcretry.Start`.**
  - Every testcontainers start is tried up to 3 times, with 2 s and 4 s backoff. That covers `pgtest`, `redistest`, and the two containers `internal/app/api_integration_test.go` starts itself.
  - After a failed attempt, any partial container is terminated.
  - The final error reads `infra: container failed to start: <what> after 3 attempts: <last error>` and wraps `tcretry.ErrInfra`. A unit test covers it with an injected backoff.
- **Taskfile.** `test` and `test:race` now run `go test [-race] -p 2 -timeout 20m {{.CLI_ARGS}} ./...`.
- **Orders timeout.** `TestPaymentsProperty` legitimately takes 143–238 s under `-race`, and once died at the harness's 240.02 s deadline. Its harness context is now 12 min (`withTimeout`); the other orders tests keep 4 min.
- **Not every run was green.**
  - The pair of gate runs before the last pair: run 1 was green (153 PASS, 336 s), and run 2 failed 2 tests (10.2 items 2 and 4).
  - One stress run was suspended for about 3 h, apparently by machine sleep. Its rerun was killed for low memory before any test ran. Neither produced results.
  - After the fixes, two fresh consecutive `task test:race` runs were **both green** (§2).

## 11. Git commands
See the end of the session output (PowerShell-safe, one command per line).
