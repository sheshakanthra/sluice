# P5a: The Rush harness (tasks 1–3): Report

| Field | Value |
|---|---|
| Status | DONE for the session scope plus both owner-approved fix rounds (see **Fixes**). The 2,000-user drain now meets its SLO: **216.5 s** (≤ 300 s), unresolved 0, checkout p95 133.2 ms. The race gate is green at the Taskfile's `-p 2`; the literal unbounded `go test -race -count=1 ./...` failed on load timeouts (Fixes → Round 2 → Race gate). Scope: session P5a (tasks 1–3 of `docs/phases/P5-rush.md`). Tasks 4–8 (auditor, baselines, rush Taskfile tasks, k6, harness self-tests beyond the ones below) are P5b. |
| Date (IST) | 2026-11-24 |
| Time spent vs timebox | One session, about 2.5 h of the 6 h P5 timebox (not timed precisely). |
| Commit base | `c9b4d55` (`git rev-parse --short HEAD`) |

## 1. What was built
- **Task 1: containers.**
  - `deploy/docker/Dockerfile`: multi-stage, `golang:1.27.4` → `gcr.io/distroless/static-debian12:nonroot`, `CGO_ENABLED=0`, `-trimpath`, version and commit via `-ldflags`. Image is 44.7 MB (`docker image ls sluice:local`). `.dockerignore` keeps the context to Go sources.
  - `deploy/compose.app.yml`: one-shot `migrate` → `seed`, then `api1..3`, `worker`, `mockpay` and `caddy`. Every long-running service has a healthcheck. The `sluice` containers use the new `sluice probe <url>` subcommand, because distroless has no curl. Caddy's healthcheck goes through the proxy to `/api/v1/healthz`.
  - `deploy/caddy/Caddyfile`: `/api/*` → api1..3 (`least_conn`, active `/readyz` checks every 2 s, `keepalive_idle_conns_per_host 1024`); `/pay/*` → mockpay with `/pay` stripped; everything else → `web/out` with an SPA fallback. `trusted_proxies static private_ranges` keeps the swarm's synthetic `X-Forwarded-For`.
  - `deploy/env/rush.env`: the STACK.md §4 rush column.
  - `deploy/compose.yml`: Postgres `max_connections=200` (5 pools × 24 = 120 > the default 100). `deploy/prometheus/prometheus.yml` also scrapes the in-network targets.
  - Taskfile: `stack:up` / `stack:down` (ENV, default `rush`; other profiles fail with a clear message) and `rush:run` (seeds a fresh event, then runs the swarm; vars USERS, SCENARIO, CONN, OPENS_IN, SEATS, EVENT).
- **Task 2: `sluice rush`** (`internal/rush`, about 1,200 lines plus tests).
  - **Flags** per LOADTEST §3.1. Presets H / SMOKE / B0 / B1 / B2; B* go direct (session token, no queue).
  - **Behaviour:** each user's mix is drawn up front from `--seed` and the user index (PCG), so a seed reproduces the mix whatever the scheduling. It covers quantity 1/2/4, pick vs best-available (pick decodes the availability bitmap), think times, 8% abandon, payment-method mix, 5% concurrent double-submit on a second connection, 1% rogue retry with a new IK, and 70% retry after a decline.
  - **State machine** per LOADTEST §3.2: session → join → PREQUEUE/WAITING polls (`retry_after_ms` ± 20%) → ADMITTED → hold (pick → best → fewer, ≤ 3 retries) → order → pay → TICKET, or `/payments` + `pm_ok`, or a `/confirm` poll. Rejoin ≤ 2 after OFFER_EXPIRED. Users leave the queue on NO_SEAT and ABANDONED.
  - **Connections:** `--conn dedicated` gives one `http.Transport` per user with `MaxConnsPerHost=1`; `--conn pooled` shares one.
  - **Client IPs:** synthetic `X-Forwarded-For` from `198.18.0.0/15`, 10 users per IP.
  - **IK discipline:** the same IK is retried on timeout, transport error, 5xx, 429 (after `Retry-After`) and `REQUEST_IN_PROGRESS`.
  - **Measurement:** one HdrHistogram per route (1 µs–60 s, 3 significant digits) plus a checkout histogram (hold + order + pay + confirm). Every request is timed from its **intended** start. Errors and timeouts are recorded at their elapsed time and counted separately; 409/422/429 count as business answers.
  - **Sampling and self-check:** a 1 s `/stats` sampler (not counted as load); the generator self-check reports schedule lag p99/max, CPU cores (getrusage) and goroutines, and the run is flagged generator-bound if lag p99 > 100 ms.
  - **Outputs:** a progress line every 5 s, then `summary.json`, `timeseries.csv` (1 s rows aligned to T0) and `report.md`. Exit code 1 if any user is unresolved.
- **Task 3: `deploy/compose.rush.yml`.** The `rush` service (profile `rush`, so `up` never starts it) runs on the stack network against `http://caddy:8000`, with `sysctls net.ipv4.ip_local_port_range: "1024 65535"`, `ulimits nofile 200000`, and `loadtest/results` mounted at `/results`.

## 2. Acceptance checklist (session scope, from the P5a brief)
- ✅ `task stack:up` brings the full stack up healthy behind Caddy on :8000. Exit 0; all 10 long-running containers `(healthy)`, migrate and seed `Exited (0)`. Routes checked with curl: `/api/v1/healthz` 200, `/pay/healthz` 200 (prefix stripped), `/` 200, `/some/deep/route` 200 (SPA fallback).
- ✅ A 2,000-user swarm run (`task rush:run USERS=2000`, `--conn pooled`) completed and wrote `summary.json`, `timeseries.csv` and `report.md` to `loadtest/results/smoke-20261124-081215/`.
- ✅ Unresolved users = 0.
- ✅ `task lint`: `0 issues.`
- ✅ `task test:race`: exit 0, no data races (second run; details in Fixes → Race gate). The first attempt in the original session was stopped for low memory.
- ✅ Admission release (owner-approved fix): implemented, tested (including a concurrent test and a mutation check) and documented (ADR 0010 addendum, ARCHITECTURE §5, DATA-MODEL §4/§5, API §3.3).
- ✅ 2,000-user rerun after the fix: unresolved users = **0**.
- ❌ 2,000-user rerun after round 1: drain **392.1 s**, target ≤ 300 s. Missed; cause in Fixes.
- ✅ Round 2, swarm compliance: users leave the queue on NO_SEAT and ABANDONED in one place; `INSUFFICIENT_SEATS` with 0 available is NO_SEAT. Tested; DESIGN-BRIEF §5 rule 9 for the P6 UI.
- ✅ Round 2, sliding admission lease (`ADMISSION_IDLE_TTL`, `ZADD XX GT`): implemented, tested (idle expiry and refill, active user kept to the hard cap, concurrency, mutation check), documented (ADR 0010 addendum part 2, ARCHITECTURE §5, DATA-MODEL, API, STACK).
- ✅ Round 2, PSP p95 check: client and MockPay server latency agree; pool wait ≤ 5 ms for all 408 creates, so the 64-connection cap is kept (now `PSP_MAX_CONNS`). Nothing to show before/after for a cap change, because none was needed.
- ✅ `go test -race -count=20 -run TestFreezeRace ./internal/queue/`: ok (171.1 s); a second `-v` run counted 20/20 PASS.
- ❌ `go test -race -count=1 ./...` (literal, unbounded parallelism): exit 1, five tests timed out under load, 0 data races. ✅ The same with `-p 2` (the `task test:race` setting): exit 0, every package ok.
- ✅ 2,000-user rerun after round 2 (`smoke-20261124-120318`, seed 42): drain **216.5 s**, unresolved **0**, checkout p95 **133.2 ms**.
- P5 acceptance as a whole (B0/B1/B2, audit exit codes, k6) is P5b and not claimed here.

## 3. Tests
| Suite | Count | Result | Notes |
|---|---|---|---|
| unit, `internal/rush` | 8 tests | pass | arrival parsing; flag validation; seeded mix is deterministic and within ±1.5 pp of LOADTEST §3.3 over 20k users; bitmap decode; percentiles on a known 1..10,000 ms distribution; **a 1 s server stall shows in intended-start p99 (≥ 900 ms)**; retry/classify rules; `summary.json` schema keys + CSV header + report.md |
| unit, `cmd/sluice` | +4 cases | pass | `rush` flag errors, `probe` usage and unreachable target |
| Lua, `internal/redisx` | +1 test (6 subtests), 1 extended | pass | `TestHoldEndFreesAdmission`: consume/release free the admission, compensation keeps it, repeat is a no-op, a MISSING hold with a known uid frees it, a user who holds again keeps it. `TestInventoryRepair` also checks the takeover frees it |
| integration, `internal/orders` | +4 tests | pass | `TestConfirmedBuyerFreesAdmission` (checked straight after the `/pay` response), `TestFailedPaymentFreesAdmission` (declines 1–2 keep it, the third, which makes the order FAILED, frees it), `TestExpiredHoldFreesAdmission`, `TestReleasedHoldFreesAdmission` |
| concurrency, `internal/queue` | +1 test | pass | `TestAdmissionRefillsAfterCheckout`: 300 admitted; 200 check out concurrently (100 consume, 100 release) while 2 controllers tick; clock frozen (no ADMISSION_TTL, no offer expiry). Cursor 300 → 350; seeking+outstanding 150 = target |
| unit, `internal/rush` (round 2) | +1 test (4 cases) | pass | `TestNoSeatAndAbandonLeaveQueue`: fake API; INSUFFICIENT with 0 available → NO_SEAT + one `DELETE /queue` with the admission token; SEATS_TAKEN on every try → NO_SEAT + leave; SOLD_OUT → no leave; abandon after hold → ABANDONED + leave |
| Lua, `internal/redisx` (round 2) | +1 test | pass | `TestAdmissionLease`: claim writes lease and cap; touch slides, never shortens, stops at the cap, never creates an admission that never existed or that left; the tick drops idle and capped leases and prunes `q:cap` |
| unit, `internal/auth` (round 2) | +1 test (6 cases) | pass | `TestRequireTouchesAdmissionTokens`: only valid admission tokens call the hook (not session/queue/garbage/expired tokens, not a keyring without the hook) |
| integration + concurrency, `internal/queue` (round 2) | +4 tests | pass | `TestIdleAdmissionExpiresAndSlotRefills` (15 admitted, 5 touch every 20 s; at exactly 60 s the 10 idle leave and the tick re-offers 10; cursor 15 → 25), `TestActiveAdmissionLivesToHardCap` (a touch every 50 s keeps the user for 11 renewals until the 10 min cap, never past it; gone the tick after), `TestConcurrentTouchesNeverResurrect` (300 users × 20 touches, 150 checking out, 2 controllers), `TestAdmissionRequestRenewsLease` (POST /holds through the real middleware renews) |
| unit, `internal/payments` (round 2) | +1 test | pass | `TestConnWaitShowsPoolQueueing`: `MaxConns` 1 and a 50 ms PSP; 4 concurrent calls record ≥ 0.3 s of pool wait in `psp_conn_wait_seconds` |
| race (`-race`) | whole repo | pass at `-p 2` (round 2); see Fixes | local, gcc 16.2.0 (WinLibs) |
| integration | whole repo | pass | inside `task test:race` |

## 4. Numbers
Command: `task rush:run USERS=2000` (2,000 users, 500 seats, `--conn pooled`, `--opens-in 30s`, seed 42, scenario SMOKE mix). Laptop: Windows 11, Docker Desktop 28.3.3, VM with 16 CPUs and 7.6 GB RAM, everything (swarm and stack) on the same VM. This is the second of two runs. The first (`smoke-20261124-075936`) ran before the CPU-meter fix and T0 bucket alignment; it also had unresolved 0, checkout p95 389.9 ms and drain 474.2 s. Both are reported; neither was picked.

From `loadtest/results/smoke-20261124-081215/summary.json`:

| Metric | Value |
|---|---|
| Outcomes | TICKET 398 · SOLD_OUT 1,555 · NO_SEAT 0 · PAYMENT_FAILED 7 · ABANDONED 40 · **UNRESOLVED 0** · seats sold 500 |
| `peak_in_system` | 1,585 |
| Requests / peak rps / mean rps | 58,058 / 2,795 / 141.6 |
| `generator_lag_p99_ms` | 2.2 (max 13.9); not generator-bound |
| Checkout p50 / p95 / p99 | 44.0 / 201.3 / 2,109.4 ms |
| Status p95 · join p95 · hold p95 | 4.9 · 7.3 · 37.1 ms |
| Order p99 · pay p95 | 2,121.7 · 2,080.8 ms |
| Errors | rate 0.0000%: 5xx 0, timeouts 0, transport 0 |
| Business | 409: 409, 422: 0, 429: 0 |
| First sale / sold out / drain (from T0) | 4.0 s / 385.0 s / 399.2 s |
| Swarm CPU max / mean | 1.41 / 0.14 cores; goroutines max 2,193 |

**What the numbers say:**
- *(Superseded in part by **Fixes** below: the diagnosis that follows covered only about half of the stale admissions.)* **The drain misses its SLO (≤ 5 min): it took 399.2 s, and the cause is server-side.** The time series shows `sold 443/500, held 0, waiting 1,250, shopping 0` for about 3 minutes, and then again at 491/500. The controller computes `seeking = ZCARD active − ZINTERCARD(active, holding)` (ARCHITECTURE §5). A user whose hold became a **sale** (or whose payment failed) no longer holds, so they count as "seeking" until their admission expires (`ADMISSION_TTL` = 3 m in rush). With ~400 such users, `target − seeking` stays negative and nobody new is offered, so seats freed by expired holds wait up to 3 minutes. Proposed fix (owner's call; it changes the admission contract): remove the user from `q:active` when ConfirmOrder succeeds and when the order reaches FAILED/EXPIRED, or count only admissions with no order. A 2,000-user run shows the gap; at 50k it will dominate the drain time.
- **The checkout p99 tail (~2.1 s) is the MockPay fault mix**, not the queue: order p99 2,121.7 ms and pay p95 2,080.8 ms match the API's 2 s PSP timeout, which fires on `pm_timeout` users (2%) and on `MOCKPAY_TIMEOUT_AFTER_COMMIT_RATE=0.02` / `MOCKPAY_FAIL_RATE=0.02`. Hold p99 is 55.9 ms.
- `confirm` n = 0: no `/pay` came back PENDING_PAYMENT in this run (timeouts came back as a failed attempt instead). The poll path is coded but was not exercised here; 50k runs will show whether it ever triggers.

## Fixes (owner-approved after the first report)

### What changed
An admission is released (`ZREM q:active uid`) when the user's order reaches CONFIRMED, FAILED or EXPIRED, or when their hold is released or expires. The `ZREM` runs inside the same Lua call that ends the hold in Redis, so it is atomic and idempotent:
- `hold_consume.lua`: always; the order is CONFIRMED.
- `hold_release.lua`: with the new `free_admission = 1`. Callers are `DELETE /holds`, the sweeper, order FAILED (transaction B or 3 declines) and `settle` reaching FAILED/EXPIRED. A compensation passes 0.
- `inventory_repair.lua`: a PG takeover of an expired hold.

The release is skipped while the user is still in `holding`. The scripts return `freed`, which is counted in `queue_admissions_released_total`. The sweeper queries now return `user_id` (`db/queries/holds.sql`, regenerated with `task gen`). Records: ADR 0010 addendum, ARCHITECTURE §5, DATA-MODEL §4/§5, API §3.3.

### Order of work, as requested
1. `task stack:down`, then `docker ps -a`: no SLUICE or testcontainers containers were left. Two containers from another project on this machine (`geniaichatbot-db-1`, `geniaichatbot-redis-1`, about 32 MiB together) were running and were left alone, since they are not SLUICE test containers.
2. `task test:race`: green (see Race gate).
3. `task stack:up` (all services healthy), then `task rush:run USERS=2000`, then `task stack:down`.

### Race gate
- **First run** of `task test:race`: every package passed except `internal/queue`. There, `TestFreezeRace` failed with `queue: transition ON_SALE: context deadline exceeded`: one of its three tight-loop tickers overran the controller's 1 s `tickTimeout` on the PG status update while `internal/orders` (272 s under `-race`) ran in parallel. No data race was reported. The test doesn't touch the changed scripts.
- **Checks:** `CGO_ENABLED=1 go test -race -count=1 ./internal/queue/` gave `ok (48.3 s)`, and `go test -race -count=3 -run TestFreezeRace ./internal/queue/` gave `ok (30.4 s)`.
- **Second run** of `task test:race`: exit 0, `DATA RACE` count 0. Go's test cache reused the first run's passing results for every package except `internal/queue` (same code, same `-race` flags), which re-ran and passed (47.6 s).
- The test and the timeout are unchanged. The load-sensitive 1 s budget in `TestFreezeRace` is listed under Known issues.

### Mutation check
With the `ZREM` in `hold_release.lua` and `hold_consume.lua` disabled, `TestAdmissionRefillsAfterCheckout` fails: `admissions released = 0, want 200`, `300 admissions left, want the 100 shoppers`, `cursor still 300: no new offers after 200 admissions ended`. The scripts were restored afterwards; both files show one `ZREM ... KEYS[7]` line each.

### Before / after (same command: `task rush:run USERS=2000`, 500 seats, seed 42, `--conn pooled`, `--opens-in 30s`, SMOKE mix)
| Metric | Before (`smoke-20261124-081215`) | After round 1 (`smoke-20261124-110912`) | After round 2 (`smoke-20261124-120318`) |
|---|---|---|---|
| **Drain (from T0)** | **399.2 s** | **392.1 s** (missed) | **216.5 s** (target ≤ 300 s: **met**) |
| **Unresolved users** | 0 | **0** | **0** |
| **Checkout p95** | 201.3 ms | **1,840.1 ms** (p50 51.7, p99 2,605.1) | **133.2 ms** (p50 20.1, p99 2,075.6) |
| First sale / sold out | 4.0 s / 385.0 s | 3.7 s / 377.0 s | 3.9 s / 203.0 s |
| Outcomes T / SO / NS / PF / AB | 398 / 1,555 / 0 / 7 / 40 | 386 / 1,581 / 0 / 4 / 29 | 385 / 1,146 / 425 / 7 / 37 (see note) |
| Order p50 / p95 · pay p50 / p95 | 101.8 / 199.0 · 155.6 / 2,080.8 ms | 689.7 / 2,127.9 · 952.3 / 2,306.0 ms | 64.9 / 99.1 · 108.1 / 2,049.0 ms |
| Hold p95 / p99 | 37.1 / 55.9 ms | 36.1 / 428.5 ms | 22.9 / 36.0 ms |
| Errors (5xx, timeouts, transport) | 0, 0, 0 | 0, 0, 0 | 0, 0, 0 |
| Generator lag p99 | 2.2 ms | 1.6 ms (not generator-bound) | 1.2 ms (not generator-bound) |
| Admissions released by checkouts | n/a | 390 (api 384, worker 6) | 385 (api 365, worker 20) |
| `DELETE /queue` calls (swarm `leave` route n) | not recorded | not recorded | 462 |
| Lease renewals (`queue_admission_touches_total`) | n/a | n/a | renewed 1,729, noop 0, error 0 |

Every row comes from that run's `summary.json`, except the Prometheus counters (queried before `task stack:down`; each run starts on a fresh stack). One run per column; no run was repeated or picked.

Note on outcomes: round 2 reclassifies `INSUFFICIENT_SEATS` with 0 available from SOLD_OUT to NO_SEAT (LOADTEST §3.2), so 425 users who were SOLD_OUT before are NO_SEAT now. TICKET and seats sold (500 in all three) are comparable; SOLD_OUT and NO_SEAT are not.

### Why the drain barely moved
From `timeseries.csv`, columns t, cursor, active, available, held, sold:

| t (s) | before: cursor / active / avail / held / sold | after: cursor / active / avail / held / sold |
|---|---|---|
| 11 | 750 / 635 / 0 / 62 / 438 | 750 / 369 / 0 / 46 / 454 |
| 101 | 750 / 718 / 57 / 0 / 443 | 750 / 364 / 45 / 0 / 455 |
| 171 | 750 / 718 / 57 / 0 / 443 | 750 / 364 / 45 / 0 / 455 |
| 186 | 751 / 86 / 57 / 0 / 443 | 817 / 42 / 17 / 28 / 455 |
| 301 | 798 / 40 / 9 / 0 / 491 | 817 / 35 / 2 / 0 / 498 |
| 391 | 811 / 13 / 0 / 0 / 500 | 823 / 5 / 0 / 0 / 500 |

- The fix removed the finished checkouts: stale admissions after the first wave fell from **718 to 364**.
- The remaining 364 are **admitted users who never held**. The swarm answered 405 holds with `INSUFFICIENT_SEATS` (Prometheus `holds_requests_total{result="insufficient"}`). When `available` is 0 because every seat is held, the user ends as SOLD_OUT **without `DELETE /queue`** (`internal/rush/user.go:341-344`; LOADTEST §3.2 makes users leave only on NO_SEAT and ABANDONED).
- No hold or order ever ends for them, so the new rule can't release them. With 45 seats back, the target is max(10, ⌈45 × 1.5⌉) = 68, below the 364 seeking, so nobody is offered anything until those admissions hit `ADMISSION_TTL` (3 m) at t ≈ 181 s.
- The tail repeats the pattern: from t = 201 to 371, 35 idle admissions against a target of 10 with 2 seats free.
- My P5a diagnosis (buyers and failed payers only) was **incomplete**. It explained about half the stale admissions.

### Why checkout p95 went up
The regression sits in MockPay's latency, upstream of the changed code. Prometheus `histogram_quantile(q, sum by (le,op)(increase(psp_request_duration_seconds_bucket{op=~"create|confirm"}[9m])))`, evaluated at the end of each run:

| PSP op | before p50 / p95 | after p50 / p95 |
|---|---|---|
| `create` | 58 / 98 ms | 249 / 876 ms |
| `confirm` | 65 / 2,330 ms | 293 / 858 ms |

- `create` runs at `POST /orders`, before any code this fix touches; the only addition on `/pay` is one `ZREM` in `hold_consume.lua` after commit.
- MockPay's database lives on the same Postgres server as `sluice` (the only container under real load, 539 MiB). The host had 2.7–2.8 GB of RAM free during the run. This run also came straight after two race suites, while the earlier one ran on a warm stack.
- That is a likely explanation, but **one run cannot prove it**. P5b's SMOKE runs should re-measure checkout p95 on a quiet machine.

### Options for the remaining gap (as proposed after round 1; the owner chose 3 plus a server-side idle lease, see Round 2)
1. **Server:** release the admission when a hold attempt answers `INSUFFICIENT_SEATS` / `SOLD_OUT` with 0 available. It uses the same script mechanism. The user keeps their token and can retry, uncounted, bounded by `HOLD_LIMIT` and the quota.
2. **Idle admission window:** an admission that hasn't held within N s of its claim leaves `q:active`. This also covers users who close the tab, but it needs a claim-time index and a sweep.
3. **Client:** the swarm and the P6 UI call `DELETE /queue` when the user gives up after "0 available" (a LOADTEST §3.2 change). It doesn't cover users who close the tab.
4. **Config:** a shorter `ADMISSION_TTL` in `rush`. This is blunt, because real shoppers need the time.

**Recommendation (round 1):** 1 + 3. Option 1 uses a signal the server already sees, and option 3 makes the UI honest. Option 2 is the more general fix if users who close the tab matter at 50k.

### Round 2 (owner-approved): client leaves, server lease, PSP check

#### What changed
1. **Swarm compliance (LOADTEST §3.2).** `internal/rush/user.go` applies the exit rule in one place, `user.end`: NO_SEAT and ABANDONED call `DELETE /queue` with the admission token (direct mode has no queue, so it is a no-op). The scattered `leave` calls were removed. The failed-rejoin NO_SEAT now leaves too. `INSUFFICIENT_SEATS` with 0 available means every seat is held, not sold, so it is NO_SEAT, not SOLD_OUT. LOADTEST §3.2 and DESIGN-BRIEF §5 (rule 9, for the P6 UI) say the same.
2. **Sliding admission lease.** `ADMISSION_IDLE_TTL` (default `2m`, `rush` `60s`, validated `0 < idle ≤ ADMISSION_TTL`). `queue_claim.lua` writes `q:active` = `min(now + idle, cap)` and a new `q:cap` = claim + `ADMISSION_TTL`. The token's `exp` is still the cap. Every request with a valid admission token runs `queue_touch.lua` (`ZADD q:active XX GT CH min(now + idle, exp) uid`) from `Keyring.Require`, via `Keyring.WithAdmissionTouch(queue.Lease.Touch)` wired in `internal/app/api.go`. The renewal is best effort (500 ms timeout, `queue_admission_touches_total{result}`). The tick's existing `ZREMRANGEBYSCORE` removes idle and capped admissions and now prunes `q:cap`. Records: ADR 0010 addendum part 2, ARCHITECTURE §5, DATA-MODEL §4/§5, API §1.1/§3.3, STACK §4.
3. **PSP latency, measured on both sides.** New `mockpay_request_duration_seconds{op}` (MockPay handler time, including the injected `MOCKPAY_LATENCY_MS`) and `psp_conn_wait_seconds{op}` (API/worker client, request start → `GotConn` via `httptrace`). The pool bound is configurable: `PSP_MAX_CONNS` (default 64, `.env.example`, STACK §4).

#### Order of work, as requested
1. `task stack:down`; `docker ps` showed no SLUICE or test containers (only `geniaichatbot-db-1` and `geniaichatbot-redis-1` from another project, left alone; they were no longer running when the session ended, and I did not stop them).
2. `go test -race -count=20 -run TestFreezeRace ./internal/queue/`, then the full race suite (below).
3. `task stack:up` (all healthy), `task rush:run USERS=2000`, Prometheus queries, `task stack:down`.

#### Race gate
- `go test -race -count=20 -run TestFreezeRace ./internal/queue/` with nothing else running: `ok (171.136s)`. I ran it a second time with `-v` to count the passes: 20 of 20 `--- PASS: TestFreezeRace`. That makes 40 of 40.
- `CGO_ENABLED=1 go test -race -count=1 -timeout 30m ./...` (as requested, no `-p`): **exit 1**, `DATA RACE` count 0. Go ran all packages at once (16 CPUs, 6 Postgres + Redis container pairs in the 7.6 GB VM). Five tests failed, every one on a timeout:
  - `TestRaceSingleSeat/redis_pg`: `redisx: hold_seats: i/o timeout`
  - `TestEnsureLoadedOnDemand`: `flushall: context deadline exceeded`
  - `TestCreateOrdersConcurrent`: holds got 25 × 503, then `REQUEST_IN_PROGRESS`
  - `TestFreezeRace`: `transition ON_SALE: context deadline exceeded`
  - `TestConcurrentTouchesNeverResurrect` (new): `hold_best: i/o timeout`
- That last test also printed `86 finished admissions re-created by a touch`, which was **wrong**: it counted users whose hold had timed out as finished checkouts, when they never released anything. I fixed the test to count only checkouts that really ended. A hold error still fails the test. With `XX` removed from `queue_touch.lua` it still fails (`150 finished admissions re-created by a touch`); with `XX` restored it passes.
- `CGO_ENABLED=1 go test -race -count=1 -p 2 -timeout 30m ./...` (uncached; `-p 2` is what `task test:race` uses): **exit 0**, 20 packages `ok`, 0 `--- FAIL`, 0 `DATA RACE` (orders 263.8 s, queue 64.5 s, inventory 55.2 s).
- So the gate is green at the repo's own parallelism, not at unbounded parallelism on this laptop. No test, timeout or assertion was loosened.

#### Mutation check (lease)
Removing `XX` from `queue_touch.lua`: `TestConcurrentTouchesNeverResurrect` fails with `150 finished admissions re-created by a touch`. Restored; `grep -c "'XX', 'GT', 'CH'"` = 1.

#### Why the drain is now 216.5 s
From `loadtest/results/smoke-20261124-120318/timeseries.csv` (t, cursor / active / avail / held / sold):

| t (s) | round 1 | round 2 |
|---|---|---|
| 11 | 750 / 369 / 0 / 46 / 454 | 750 / 14 / 0 / 56 / 444 |
| 31 | n/a | 750 / 7 / 0 / 56 / 444 |
| 71 | n/a | 815 / 28 / 0 / 37 / 463 |
| 101 | 750 / 364 / 45 / 0 / 455 | 832 / 9 / 5 / 11 / 484 |
| 151 | n/a | 845 / 0 / 0 / 1 / 499 |
| 201 | n/a | 855 / 2 / 0 / 1 / 499 |
| 211 | n/a | 855 / 1 / 0 / 0 / 500 |

- Admissions after the first wave fell from **364 to 7** at t = 31 s, mostly from the swarm's 462 `DELETE /queue` calls. The lease renewed 1,729 times. There is no counter for idle expiries, so this run can't split how many slots the lease freed versus the leaves.
- From then on `active` is at most 28. Every seat that comes back is offered within a tick (cursor 750 → 815 → 832 → 845 → 855).
- The remaining drain is **hold and payment TTLs**, not admissions. Seats sit in `held` with `active` near 0 (56 held from t = 11 to 61; 1 held from t = 151 to about 205) until `HOLD_TTL` (60 s) or `PAYMENT_HOLD_TTL` (90 s) returns them. The last seat sold at 203.0 s, and the last user resolved at 216.5 s.

#### PSP p95: server vs client
Queried from the fresh stack's raw cumulative counters right after the run (`histogram_quantile(q, sum by (le)(<metric>_bucket{op=...}))`; script output, one run):

| op | view | n | p50 ms | p95 ms | p99 ms | mean ms |
|---|---|---|---|---|---|---|
| create | MockPay server (`mockpay_request_duration_seconds`) | 408 | 44.2 | 94.2 | 100.0 | 64.6 |
| create | API client attempt (`psp_request_duration_seconds`) | 408 | 44.6 | 94.4 | 100.0 | 65.1 |
| create | API client pool wait (`psp_conn_wait_seconds`) | 408 | 0.3 | 0.5 | 2.1 | 0.1 |
| confirm | MockPay server | 428 | 47.2 | 2,054.5 | 3,610.9 | 155.7 |
| confirm | API client attempt | 428 | 47.6 | 2,216.7 | 3,643.3 | 156.2 |
| confirm | API client pool wait | 428 | 0.3 | 0.5 | 2.0 | 0.1 |

- **No pool queueing:** 398 of 408 creates had a connection within 1 ms, and all 408 within 5 ms. The client's view equals MockPay's own within 0.5 ms at p50/p95. So the 64-connection cap was **not raised**; it is now configurable (`PSP_MAX_CONNS`, default 64), and the metric will show it if that changes at 50k. With no cap change there is no before/after cap comparison to show.
- The confirm tail (p95 ≈ 2 s) is on the **server** side: MockPay's injected `timeout_after_commit` and `fail` faults (2% each in `rush.env`) and `pm_timeout` users. That matches the swarm's pay p95 of 2,049.0 ms.
- Round 1's checkout p95 of 1,840.1 ms didn't recur (133.2 ms now) with the same pool. That fits the earlier hypothesis of machine load rather than the pool, but round 1 had no server-side metric, so it can't be proven after the fact. A first-time Prometheus `increase()` over a 10 m window undercounted (server 417 vs client 263) because the series were created mid-window; the raw counters above don't have that problem.

## 5. Deviations and decisions
| What changed vs the spec | Why | ADR |
|---|---|---|
| New `sluice probe <url>` subcommand | Distroless has no shell or curl for healthchecks | 0010 |
| `rush.env` `TRUSTED_PROXIES` covers every private range; Caddy `trusted_proxies private_ranges`; synthetic IPs from 198.18.0.0/15 | Caddy must be trusted on any Docker subnet, or every swarm user shares one IP and the per-IP limits refuse them | 0010 |
| Postgres `max_connections=200` | 5 pools × `DB_MAX_CONNS` 24 = 120 > 100 | 0010 |
| APIs depend on `seed` (which depends on `migrate`) completing | `up --wait` otherwise reports a one-shot's exit as a failure (seen on the first `stack:up`) | 0010 |
| Mutating calls time out at 5 s (not 2 s); new IK per hold attempt and per `/confirm` poll; third OFFER_EXPIRED ends as NO_SEAT; decline retry pays with `pm_ok` | LOADTEST leaves these open or contradicts itself (a 2 s `/pay` timeout races the API's 2 s PSP timeout) | 0010; LOADTEST §3.3 updated |
| `summary.json` adds `anomalies`, `generator` and `config` extras; `audit` is `null` | So no unexpected answer hides in an outcome count; the auditor is P5b | 0010 |
| `stack:up` supports only `ENV=rush` for now | dev/demo env profiles are later work (demo is P8) | — |
| Admissions are released when the hold or order ends (owner-approved); `hold_release.lua` gains `uid` and `free_admission` args, `hold_consume.lua` gains `uid`; `inventory_repair.lua` and all three take `q:active`; sweeper queries return `user_id` | Finished checkouts held admission slots until `ADMISSION_TTL` | 0010 addendum |
| Admission is a sliding lease: new `ADMISSION_IDLE_TTL`, `q:cap` ZSET, `queue_touch.lua`, `queue_claim.lua` gains `idle_ttl_ms` and returns the cap, `queue_tick.lua` prunes `q:cap`; `Keyring.WithAdmissionTouch` hook in `Require` | Idle admitted users held slots until `ADMISSION_TTL` without client cooperation (owner-approved) | 0010 addendum part 2 |
| Swarm: `INSUFFICIENT_SEATS` with 0 available → NO_SEAT (was SOLD_OUT); leave centralised in `user.end` | LOADTEST §3.2 compliance | 0010 addendum part 2 |
| `PSP_MAX_CONNS` (was the constant `MaxConnsPerHost = 64`); new metrics `mockpay_request_duration_seconds`, `psp_conn_wait_seconds` | Owner asked for the p95 check and a configurable cap | STACK §4 |

Assumptions:
- The rush window used for `mean_rps` / `peak_rps` runs from T0 − 10 s to the last terminal user. Latency histograms include every user request (all fall inside that window with the default arrival profile).
- On Windows dev runs the swarm's CPU number is the Go runtime's estimate; in the container it is `getrusage`.

## 6. Known issues and debt
- **Owner decision: should a lapsed lease end the token?** An idle user whose lease lapsed (or whose checkout ended) still holds a valid admission token until `exp`, and can hold again outside the controller's `seeking` count (bounded by `HOLD_LIMIT` and I4). Rejecting such tokens on the hold path would close this, but it changes the token contract (ADR 0010 addendum part 2).
- **No idle-expiry counter.** The tick removes idle leases with `ZREMRANGEBYSCORE` and doesn't count them separately from capped ones; add `queue_admissions_expired_total` if P5b needs the split.
- **After a Redis flush `q:cap` is not restored**; a re-claim reports the lease expiry as the cap (shortens, never extends). Snapshotting `q:cap` would fix it.
- **Drain now bound by hold/payment TTLs** (60 s / 90 s in `rush`): 216.5 s at 2,000 users. At 50k it depends on how many holds time out; P5b measures it.
- **The race suite needs `-p 2` on this laptop.** Unbounded package parallelism times out Redis/PG calls under `-race` (five tests, 0 races). `TestFreezeRace` is load-sensitive in the same way (1 s `tickTimeout`). Neither was changed. CI runs `task test:race` (`-p 2`).
- `--conn dedicated` was not run at 2,000 users on this laptop (the brief allowed pooled). It is the default and the 50k CI mode, so P5b/P7 must run it.
- The `/confirm` polling path wasn't exercised in this run (§4).
- `web/out` is served as-is; a static export is P6.

## 7. Interview Q&A
- **Q: Why time requests from their intended start?** If the server stalls for 1 s, a closed-loop client sends nothing during the stall and then measures fast replies afterwards. That's coordinated omission, and it hides the stall. Timing from the scheduled moment charges the backlog to the server. `TestIntendedStartCapturesStall` proves it: one 1 s stall among 100 requests puts p99 above 900 ms.
- **Q: Why does the swarm send `X-Forwarded-For`, and why is that safe?** The API rate-limits per IP and trusts XFF only from `TRUSTED_PROXIES`. The swarm's peer is Caddy (private range, trusted), Caddy trusts the swarm and appends its own peer, and the API takes the right-most untrusted entry, which is the synthetic 198.18.x.y address. A public client can't spoof it in demo mode because the demo profile won't trust arbitrary private peers.
- **Q: How do you make a 2,000- or 50,000-user mix reproducible when goroutines interleave randomly?** Every user's choices come from a PCG stream seeded by `(seed, user index)` and are drawn before the run, so scheduling can't change who does what. Only server-driven timing differs between runs.
- **Q: Why did the drain take 6+ minutes when the sale itself took seconds?** The controller counts admitted users without a hold as "seeking". Two groups sat there long after they were done: buyers and failed payers, and users told "0 available" who gave up. The controller saw the shop as full, so seats freed by expired holds waited for those admissions to expire after 3 minutes. Releasing admissions when the hold or order ends removed the first group (718 → 364 stale) but not the second, so the drain only moved from 399.2 s to 392.1 s. Then the clients left on giving up, and admission became a 60 s sliding lease renewed by every request, so idle users drop out on their own: 364 → 7 stale and a 216.5 s drain. A fix you can measure separately is worth more than a fix you assume works.
- **Q: Why `ZADD XX GT` for the lease?** `XX` means a renewal can never bring back an admission that already ended (released, left, expired). Without it, a late request racing a checkout re-created 150 of 150 admissions in the concurrency test. `GT` means an old, slow request can't shorten a newer lease. Capping the score at the token's `exp` keeps `ADMISSION_TTL` a hard limit, and all of it is one atomic command, so there is no read-modify-write race.
- **Q: How do you tell pool queueing from a slow PSP?** Measure both sides. MockPay records handler time, and the client records time-to-connection with `httptrace`. In the 2,000-user run they agreed within 0.5 ms and every create got a connection within 5 ms, so raising the pool cap would have changed nothing.
- **Q: Why distroless, and what did it cost?** It has no shell, package manager or libc, and runs nonroot: a small image (44.7 MB) with little attack surface. The cost is that healthchecks need the binary itself (`sluice probe`).

## 8. Files changed (`git status --short`)
```
 M .env.example
 M Taskfile.yml
 M cmd/sluice/main.go
 M cmd/sluice/main_test.go
 M db/queries/holds.sql
 M deploy/compose.yml
 M deploy/prometheus/prometheus.yml
 M docs/API.md
 M docs/ARCHITECTURE.md
 M docs/DATA-MODEL.md
 M docs/DESIGN-BRIEF.md
 M docs/LOADTEST.md
 M docs/STACK.md
 M go.mod
 M go.sum
 M internal/app/api.go
 M internal/app/orders.go
 M internal/app/queue.go
 M internal/auth/http.go
 M internal/auth/jwt.go
 M internal/config/config.go
 M internal/db/gen/holds.sql.go
 M internal/inventory/hold.go
 M internal/inventory/hold_integration_test.go
 M internal/inventory/service.go
 M internal/inventory/sweep_integration_test.go
 M internal/inventory/sweeper.go
 M internal/mockpay/server.go
 M internal/orders/confirm.go
 M internal/orders/fixture_test.go
 M internal/payments/client.go
 M internal/payments/payments_test.go
 M internal/queue/fixture_test.go
 M internal/queue/queue_test.go
 M internal/queue/service.go
 M internal/redisx/keys/keys.go
 M internal/redisx/queue_scripts_test.go
 M internal/redisx/redisx.go
 M internal/redisx/scripts/hold_consume.lua
 M internal/redisx/scripts/hold_release.lua
 M internal/redisx/scripts/inventory_repair.lua
 M internal/redisx/scripts/queue_claim.lua
 M internal/redisx/scripts/queue_tick.lua
 M internal/redisx/scripts_test.go
?? .dockerignore
?? deploy/caddy/
?? deploy/compose.app.yml
?? deploy/compose.rush.yml
?? deploy/docker/
?? deploy/env/
?? docs/adr/0010-stack-containers-and-swarm-details.md
?? docs/reports/P5a-rush.md
?? internal/auth/touch_test.go
?? internal/orders/admission_integration_test.go
?? internal/queue/admission_lease_test.go
?? internal/queue/admission_release_test.go
?? internal/queue/lease.go
?? internal/redisx/scripts/queue_touch.lua
?? internal/rush/
```

## 9. Next phase readiness
P5b can start: `sluice audit`, the baselines (migration `0003_naive_baseline.sql`), the rush Taskfile tasks and k6. Open owner decision before H runs: whether a lapsed lease should end the admission token (§6).

## 10. Git commands
Printed in chat at the end of the session.
