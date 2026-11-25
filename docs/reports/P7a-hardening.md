# P7a: Hardening, tasks 1–3 (Step 0 cascade fix, observability, hygiene, chaos): Report

| Field | Value |
|---|---|
| Status | DONE for this session's scope: Step 0 plus P7 tasks 1–3. Tasks 4–6 (SLO tuning loop, `rush.yml`, `docs/results/README.md`) are left for P7b |
| Date (IST) | 2026-11-25 |
| Time spent vs timebox | About 2 h (16:30–18:20 IST, from command timestamps) of P7's 6 h, for half the phase |
| Commit base | `5138893` (`git rev-parse --short HEAD`). Every run below used the uncommitted working tree, so summaries still say `git_sha 5138893` |

## 1. What was built

### Step 0: the 20k cascade (ADR 0013)
- **Readiness has its own clients.**
  - `internal/app/api.go:RunAPI` opens a 2-connection pgx pool (`healthPoolConns`) and `redisx.NewHealthClient` (2 connections), used only by `/readyz`. The migrations check also uses the health pool.
  - `internal/httpx/health.go:Readiness` caches the result. Once warm, `State` never waits for a probe: it serves the cached state and, after the 1 s TTL, starts one background refresh. It probes synchronously only when cold or more than 10 s stale.
  - `SetDraining` makes `/readyz` answer 503 on SIGTERM.
- **Load shedding.**
  - `internal/httpx/shed.go:Shed` allows at most `MAX_IN_FLIGHT` (96) **write** requests (POST/DELETE/PUT/PATCH) per replica. The next one waits up to `SHED_WAIT` (50 ms), then gets 503 `OVERLOADED` + `Retry-After: 1`.
  - Reads, probes and `/metrics` always pass (see §4.2 for why reads are exempt).
  - The new error code is in `problem.go`, API.md §1.3 and `web/components/notices.tsx`.
- **Caddy tolerates a busy box.**
  - Active check: `health_fails 3` / `health_passes 1`.
  - Passive check: `max_fails 100` in `fail_duration 5s`.
  - `lb_try_duration 1s` retries a failed dial on another replica.
- **Abandoned hold commits are compensated.** `internal/inventory/hold.go:hold` now also undoes the Redis grant when the PG commit fails with `context.Canceled`/`DeadlineExceeded`. Before, the grant blocked its seats until expiry; this caused drift 6 and was found in the first 20k run.
- **Pool sizing.** `DB_MAX_CONNS` stays 24, decided from the pool metrics (§4.1).

### Task 1: observability
- `internal/obs/pools.go:RegisterPoolStats` exports `sluice_pgxpool_*` (acquired, idle, total, max, constructing, acquire/empty/canceled counts, acquire and empty-wait seconds) and `sluice_redis_pool_*` (total, idle, stale, pending, hits, misses, timeouts, waits, wait seconds), labelled by `pool` (main, health). They are wired in the api, worker and mockpay processes.
- `audit_live{event_id,check}` (`internal/orders/reconcile.go`) is the worker's 5 s audit cache as a gauge, for the Correctness row.
- `sluice_http_shed_total{method}`.
- Caddy metrics: global `metrics` option, served on `:9180` in-network only.
- `deploy/prometheus/prometheus.yml` scrapes api1..3, worker, mockpay, `caddy:9180`, plus host dev targets labelled `mode=dev`.
- `deploy/grafana/dashboards/sluice.json` (provisioned, uid `sluice`) has 7 rows and 34 panels:
  - Traffic: RPS by route, status, shed + Caddy upstream health, RPS and in-flight by replica, edge RPS by code.
  - Latency: p50/p95/p99 by route.
  - Queue: waiting/cursor, offers/active, admit rate and joins.
  - Inventory: seats by state, hold results, expired/compensations/drift repairs.
  - Payments: by status, PSP p50/p95/p99, webhooks and deliveries.
  - Correctness: live invariants, duplicates prevented / orphans fixed, orders by status.
  - Runtime: goroutines, heap, GC, pgxpool acquired and wait, Redis pool pending and timeouts.
- `web/scripts/grafana-shot.mjs` takes a Playwright screenshot of the dashboard.

### Task 2: production hygiene
- **Timeouts.** Every outbound call already had one: the PSP client (`payments.Options.Timeout`), Redis (2 s dial/read/write), pgx connect 3 s, request context 10 s, MockPay webhook context. Added:
  - per-round timeouts on the sweeper (`sweepTimeout` 30 s) and the idempotency GC (30 s);
  - a 30 s `http.Client.Timeout` on MockPay's default webhook client.
- **Graceful shutdown.** On SIGTERM, `/readyz` turns 503 for `drainDelay` = 3 s, then `Shutdown` drains for up to 15 s. `stop_grace_period` is now 25 s. Verified in §2.
- **Body limits.** `httpx.BodyLimit(64 KiB)` covers every API route, and Caddy has `request_body max_size 64KB`.
- **Security headers at Caddy.** `X-Content-Type-Options`, `X-Frame-Options DENY`, `Referrer-Policy`, `Permissions-Policy`, `Cross-Origin-Opener-Policy` and CSP (`'unsafe-inline'` scripts, because the Next static export inlines its bootstrap), with `-Server`. There's no HSTS on plain-HTTP :8000; it belongs at the TLS edge.
- **`GOMEMLIMIT` and `GOGC` per container** (`deploy/compose.app.yml`): api 512MiB, worker and mockpay 256MiB, `GOGC=100`.
- **goleak.**
  - `goleak.VerifyTestMain` runs in auth, buildinfo, config, fsm, httpx, jobs, obs, payments and rush.
  - `internal/testutil/leakcheck.Check` runs after the containers close in the inventory and orders TestMains (the hold and payment concurrency suites). It ignores only Docker-client/testcontainers goroutines.

### Task 3: chaos suite (ADR 0014)
- `task chaos` runs `loadtest/chaos/run.sh` with `loadtest/chaos/verdict.py`, plus `deploy/compose.chaos.yml` and `deploy/toxiproxy/toxiproxy.json`.
- Toxiproxy 2.12.0 sits between api1..3 and Postgres, Redis and MockPay.
- Each case: fresh stack → 5k SMOKE rush → fault timed from the swarm's `T0 +Ns` line → audit → one row per case.

## 2. Acceptance checklist (tasks 1–3 only)
- [x] **`task chaos` passes C1–C4, with results tables in the report.** `task chaos` printed the following (copy: `docs/results/p7a/chaos-suite.md`):

| case | fault | oversold | dup charges | orphans | quota | stuck holds | drift | unresolved | TICKET | error rate (run) | errors T−2..T+10 s / requests | peak err/s | exit | verdict |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| C1 | `redis-cli FLUSHALL` at T0+15 s (`OK` in the log) | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 379 | 1.7907% | 0 / 7184 | 947 | 0 | PASS |
| C2 | `docker compose kill api2` at T0+10 s, left down | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 377 | 0.8328% | 202 / 7829 | 425 | 0 | PASS |
| C3 | MockPay +2,000 ms on 30% of connections + timeout(3,000 ms) on 5%, whole run | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 390 | 2.0798% | n/a (whole run) | 908 | 0 | PASS |
| C4 | `docker compose pause postgres` for 3 s at T0+20 s | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 392 | 1.1100% | 9 / 7164 | 437 | 0 | PASS |

  - **C1.** The worker rebuilt inventory: the swarm's `available/held/sold` series runs on unbroken across T0+15 s (`timeseries.csv`, t=12…22: sold 123 → 451, no gap). Unresolved is 0, so every queued user still got an answer after the queue keys vanished. The flush window itself had 0 errors.
    - Caveat: `grep -ciE 'rebuil|restor'` on the worker log found 0 lines, because the rush profile logs at `warn`. The evidence for the rebuild is the data, not a log line.
  - **C2.**
    - Caddy failed over: `no upstreams available` = 0, and 98 health-check lines name api2.
    - 202 errors in T−2…T+10 s.
  - **C3.** Duplicate charges 0 and orphans 0 after settle.
  - **C4.**
    - Stuck holds 0 and drift 0.
    - Requests recovered: 9 errors in T−2…T+10 s.
  - **Error timing.** In all four runs most errors fall in the sale-open burst at T0+1…5 s (C1 t=1: 947, C3 t=1: 908, C4 t=1: 437 errors/s), not at the fault. Every API dependency call goes through the extra Toxiproxy hop; this is reported, not held to the budget.
- [x] **The Grafana dashboard loads with data during `task rush:smoke`.**
  - Screenshot: `docs/reports/assets/p7a/grafana-smoke.png`, taken with `node web/scripts/grafana-shot.mjs ../docs/reports/assets/p7a/grafana-smoke.png now-5m` at T0+40 s of `task rush:smoke RUN_ID=p7a-smoke`.
  - What it shows:
    - every row has data, with the sale-open spike at about 17:27;
    - Traffic: RPS by route peaks around 1.2k req/s, edge 200s peak at 2.72k req/s;
    - Queue: waiting climbs to about 4.2k, offers spike to 379, admitted peaks at 43/s;
    - Inventory: available 500 → 0, sold 476 when the shot was taken;
    - Payments: AUTHORIZED/CAPTURED about 22/s, PSP p95 939 ms;
    - Correctness: `oversold`/`duplicates`/`orphans` flat at 0;
    - Runtime: goroutines about 800 per replica, heap ≤ 46.8 MiB, pgxpool main acquired peaks at 24 (api1), health pool at 0.
  - The first attempt rendered empty panels because Grafana lazy-loads panels outside a 1000 px viewport. The script now uses a 3200 px viewport.
- [x] **pgxpool and go-redis pool stats exported:** see the `max acquired conns (main/health)`, `acquire wait` and `redis main pool timeouts` queries in §4.1, all read from these metrics.
- [x] **Hygiene items** (§1, task 2). Graceful shutdown was proved with Caddy health-check failover: a 2k SMOKE run on the stack, with `docker compose stop api2` at T0+10 s (`bash loadtest/manual/p7a-drain.sh`; result `docs/results/p7a/p7a-drain-115018.md`):
  - error rate 0.0591%: **5xx 0**, timeouts 23, transport errors 0;
  - audit passed, unresolved 0;
  - Caddy logged 4 `unexpected status code` (api2's draining `/readyz` 503s) and then health-check failures once api2 was gone. There was 1 `no upstreams available` line in that window.
- [x] **`task lint test:race` green.**
  - `task lint` → `0 issues.` (rerun after the last edit).
  - `task test:race` (gcc 16.2.0 WinLibs present, so race ran locally): every package passed. The first run had 2 failures, both in the new leak check, not in tests: `internal/inventory` and `internal/orders` flagged one goroutine, `github.com/Microsoft/go-winio.ioCompletionProcessor` (the Docker client's Windows named-pipe loop).
  - After adding that to the ignore list: `CGO_ENABLED=1 go test -race -p 2 -count=1 ./internal/inventory ./internal/orders` → `ok inventory 98.828s`, `ok orders 268.032s`.
  - Web: `pnpm lint` clean, `pnpm typecheck` clean, Prettier clean on the two touched files.

## 3. Tests
| Suite | Count | Result | Notes |
|---|---|---|---|
| unit + integration (testcontainers), `-race` | 19 packages with tests, all `ok` | pass | `task test:race`, plus the inventory/orders rerun above |
| new: `httpx.TestShed`, `TestShedDisabled`, `TestBodyLimit`, `TestReadinessCache` | 4 | pass | `TestReadinessCache`: 50 concurrent cold callers run exactly 1 probe |
| new: `app.TestReadyzAgainstRealDependencies/saturated_request_pool` | 1 | pass | Request pool fully checked out. **Before** (readiness on the shared pool): `/readyz = 503 in 2.001s`. **After** (health pool): 200 in 12 ms, 0 s, 1 ms, 0 s, 1 ms. The blocked requests finish with 200 once the pool is freed |
| new: `inventory.TestCompensationOnAbandonedCommit` | 1 (20 goroutines, `-race`) | pass | Fails without the fix (checked by temporarily removing the two `context` conditions: FAIL), passes with it |
| goleak | 9 unit packages + inventory, orders | pass | |

Coverage was not measured this session.

## 4. Numbers

### 4.1 Step 0: before and after, `task rush:b2 USERS=20000`
The Caddy count is `docker logs sluice-caddy-1 | grep -c "no upstreams available"`. Pool and readiness rows come from `bash loadtest/manual/p7a-promq.sh 6m` right after each run (Prometheus `increase`/`max_over_time` over the run window).

| run | change | Caddy `no upstreams available` | `/readyz` p99 | error rate | timeouts | unresolved | drift | gen lag p99 | audit |
|---|---|---|---|---|---|---|---|---|---|
| p5b-b2-20k (P5b report) | before | 49,105 | not exported | 57.28% | n/a | 4 | 0 | 137.3 ms | exit 1 |
| p7a-b2-20k | TTL cache with the probe in the request path, `MAX_IN_FLIGHT=256` (all methods) | 40,052 | 0.785 / 0.844 / 0.874 s (api1/3/2, handler) | 88.95% | 344,557 | 133 | **6** | 3,326.0 ms | exit 1 |
| p7a-b2-20k-v2 | + background refresh, `MAX_IN_FLIGHT=96`, `health_fails 3`, abandoned-commit compensation | 65,747 | 0.010 s | 83.84% | 161,084 | 0 | 0 | 1,007.6 ms | **pass** |
| p7a-b2-20k-v3 | + passive `max_fails 100` | **0** | 0.047 s | 66.30% | 72,317 | 7 | 0 | 203.5 ms | exit 1 (unresolved 7) |

- **v2 → v3:** v2 had `/readyz` healthy, but Caddy's log showed `dial tcp: lookup api2: i/o timeout` (300×, plus 45 for api1 and 40 for api3) and `dial tcp 172.27.0.9:8080: i/o timeout` (235×). At the old passive default of 1 failure, each of these marked a replica down for 5 s.
- **v3:** at most one replica was ever down (api1, `count_over_time(... == 0)` = 4 samples of 5 s), so Caddy always had an upstream.
- **Pool sizing (v2 metrics):**
  - All 3 × 24 main connections were checked out, while the health pools peaked at 2.
  - 79.8% of acquires waited, 0.133 s on average.
  - The api processes completed about 73,700 acquires over the 6 min query window (`increase`). The helper first printed this as "307 acquires/s" by dividing by a hard-coded 240 s; that is corrected here and in `loadtest/manual/p7a-promq.sh`. The window is longer than the busy phase, so no per-acquire hold time is claimed.
  - Postgres was at about 263% CPU (`docker stats`) on a box shared with the swarm and Caddy.
  - More connections would add contention, not throughput, so `DB_MAX_CONNS` stays 24. 96 in-flight writes = 4 × the pool.
- **Validity:** all 20k direct runs are generator-bound (lag p99 > 100 ms, LOADTEST §3.4). They show the cascade is gone, not SLO numbers. B2 at 20k still ends 4–7 users unresolved, as in P5b.

### 4.2 SMOKE (`task rush:smoke`, 5k users, waiting room on)
Per-route numbers come from `python -c` over each `summary.json`.

| run | shedding | error rate | 5xx / timeouts | checkout p50 / p95 ms | hold p95 | order p95 | pay p95 | status p95 | join p95 | audit |
|---|---|---|---|---|---|---|---|---|---|---|
| p5b-smoke (P5b, commit 570aefc) | none | 0.0000% | — | n/a / 329.7 | 40.4 | 316.9 | 462.6 | 3.7 | 13.3 | pass |
| p7a-smoke | all methods, 96 | 0.5369% | 376 / 501 | 190.1 / 1925.1 | 322.8 | 1688.6 | 2177 | 83.8 | 427.5 | pass |
| p7a-smoke-v2 | **writes only**, 96 | **0.0154%** | 14 / 1 | 220.8 / 823.8 | 350 | 667.6 | 1162.2 | 259.8 | 378.1 | pass |
| p7a-smoke-oldcaddy (v2 code, Caddyfile from HEAD) | writes only, 96 | 0.2188% | — | 109.2 / 1872.9 | 281.3 | 2033.7 | 2428.9 | 97.2 | 600.6 | pass |

- **What the first SMOKE showed.** Counting reads in the limit shed queue status polls in the T0 burst: the errors were all at t=0…2 s, and `sluice_http_shed_total` read ~447 over the 12 min window. So only writes count now.
- **v2 is inside the 0.1% error budget**, with 14 POST requests shed (`sluice_http_shed_total{method="POST"} 14`).
- **Latency is worse than P5b on every route, including status and join, which are never shed.** The A/B with HEAD's Caddyfile did no better, so the Caddy changes are not the cause. Three SMOKE runs of the same code also scattered widely (checkout p95 823.8–1925.1 ms).
- **This is an open regression or variance question for P7b's tuning loop (task 4); it was not resolved here.** SLOs aren't claimed from these runs.

## 5. Deviations and decisions
| What changed vs the spec | Why | ADR |
|---|---|---|
| `/readyz` uses dedicated health clients and a cached, background-refreshed state; draining 503 on SIGTERM | P5b cascade | 0013 |
| New 503 `OVERLOADED` code; shedding writes beyond `MAX_IN_FLIGHT` (new env vars `MAX_IN_FLIGHT`, `SHED_WAIT`) | Bounded queueing in front of the PG pool | 0013 |
| Caddy health: `health_fails 3`, `max_fails 100`, `lb_try_duration 1s` | One slow probe or one failed dial ejected healthy replicas | 0013 |
| Hold commit abandoned by cancellation → Redis grant compensated (previously left to expire) | Drift 6 after the first 20k run | 0013 |
| C1–C4 at 5,000 users (LOADTEST said 10,000); only api1..3 go through Toxiproxy; the pass rules | Laptop, as instructed; the judge must not be blinded | 0014 |

Docs updated:
- `docs/API.md` (§1.3, `/readyz`, body cap)
- `docs/STACK.md` §4 env vars, image versions and layout
- `docs/ARCHITECTURE.md` §6 (two failure-mode rows)
- `docs/LOADTEST.md` §4 (C1–C4 row)
- `.env.example`, `deploy/env/rush.env`

## 6. Known issues and debt
- **SMOKE latency vs P5b** (§4.2): unexplained; first item for the P7b tuning loop. Profile with pprof (`PPROF_ADDR=:6060` in rush) at the sale-open burst.
- **Unresolved users in direct mode at 20k** (7 in v3, 4 in P5b), generator-bound. Not an I1–I4 issue; it is I8 under an overload the waiting room is meant to prevent.
- **Caddy active probes still time out under extreme CPU starvation** (35 in v3) even though the handler p99 is 47 ms. The cost is connection setup, which `health_fails 3` absorbs.
- **Chaos error spikes sit at T0 through the Toxiproxy hop.** With chaos and SMOKE both at 5k, the T0 burst is the hardest moment.
- **The `/readyz` state can be ~1 s plus one probe (≤ 2 s) old,** so a dependency outage is reported within about 3 s.
- **`MAX_IN_FLIGHT=96` was sized on B2 at 20k with all methods counted.** It was re-checked on SMOKE with writes only (14 shed). It deserves one more look in P7b at the H 50k shape.

## 7. Understand this before moving on (Sheshakanth's interview prep)
1. **Q:** Why did a *busy* replica get ejected, and why isn't "make the health check cheaper" enough?
   **A:**
   - The probe shared the request pool, so it queued behind requests that were themselves waiting on Postgres.
   - Fixing the probe (`httpx.Readiness`, dedicated pools) still left Caddy's **passive** check ejecting replicas on single dial timeouts.
   - Ejecting one replica moves its load to the others, which then fail too. The general rule: health should mean "can serve", and overload should be signalled per request (`httpx.Shed` → 503 `OVERLOADED` + `Retry-After`), never by looking dead.
2. **Q:** Why shed only writes?
   **A:**
   - The limit protects the 24-connection Postgres pool.
   - Queue status polls and availability reads are Redis-only or cached, and they are the waiting room's heartbeat.
   - Counting them shed status polls at T0 and pushed SMOKE from 0% to 0.54% errors (§4.2).
3. **Q:** A hold's PG commit fails because the client gave up. Why is releasing the Redis grant safe, when the commit might have landed?
   **A:**
   - PG is the judge (I1). `inventory.Service.compensate` first releases the grant, then runs `repairSeats`, which copies PG's view of those seats back into Redis.
   - If the commit landed, the seats show HELD in PG and are taken back out of `avail`.
   - If a later grant slips through first, its PG commit is refused (`ErrSeatsTaken`) and compensated in turn.
   - The worst case is a refused request, never an oversell. `TestCompensationOnAbandonedCommit` covers the common case under `-race`.

## 8. Files changed (`git status --short`)
- **Go: readiness, shedding, metrics.** `M internal/httpx/health.go`, `M internal/httpx/problem.go`, `M internal/httpx/httpx_test.go`, `?? internal/httpx/shed.go`, `?? internal/httpx/shed_test.go`, `?? internal/obs/pools.go`, `M internal/redisx/redisx.go`, `M internal/app/api.go`, `M internal/app/worker.go`, `M internal/app/mockpay.go`, `M internal/mockpay/server.go`, `M internal/config/config.go`, `M internal/orders/service.go`, `M internal/orders/reconcile.go`
- **Go: holds.** `M internal/inventory/hold.go`, `M internal/inventory/sweeper.go`, `M internal/inventory/hold_integration_test.go`, `M internal/inventory/fixture_test.go`
- **Go: tests and leaks.**
  - `M internal/app/api_integration_test.go`, `?? internal/app/readyz_saturation_test.go`, `M internal/orders/fixture_test.go`, `?? internal/testutil/leakcheck/`
  - `?? internal/{auth,buildinfo,config,fsm,httpx,jobs,obs,payments,rush}/leak_test.go`
  - `M go.mod` (goleak v1.3.0 direct)
- **Deploy.** `M deploy/caddy/Caddyfile`, `M deploy/compose.app.yml`, `M deploy/env/rush.env`, `M deploy/prometheus/prometheus.yml`, `?? deploy/compose.chaos.yml`, `?? deploy/toxiproxy/`, `?? deploy/grafana/dashboards/sluice.json`
- **Load test.** `?? loadtest/chaos/` (run.sh, verdict.py), `?? loadtest/manual/p7a-promq.sh`, `?? loadtest/manual/p7a-drain.sh`, `M Taskfile.yml` (`task chaos`)
- **Web.** `M web/components/notices.tsx`, `?? web/scripts/grafana-shot.mjs`
- **Docs.** `M .env.example`, `M docs/API.md`, `M docs/ARCHITECTURE.md`, `M docs/STACK.md`, `M docs/LOADTEST.md`, `?? docs/adr/0013-readiness-and-load-shedding.md`, `?? docs/adr/0014-chaos-suite-details.md`, `?? docs/results/p7a/`, `?? docs/reports/assets/p7a/`, `?? docs/reports/P7a-hardening.md`

## 9. Next phase readiness (P7b: tasks 4–6)
- Start the tuning loop with the SMOKE latency gap in §4.2: pprof at T0, per-route traces of order and pay, and the status p95.
- Write `rush.yml` (actionlint plus a dry run) and `docs/results/README.md`.
- Sheshakanth: nothing is needed before P7b beyond committing this session.

## 10. Git commands (Sheshakanth runs these)
```bash
git add -A
git status --short
git commit -m "feat(ops): readiness pools, load shedding, dashboards, chaos suite" -m "- Readyz on dedicated health pools with cached state; drain on SIGTERM" -m "- Shed writes past MAX_IN_FLIGHT with 503 OVERLOADED; tolerant Caddy health" -m "- Compensate Redis hold grants when the PG commit is abandoned" -m "- pgxpool and redis pool metrics, Caddy metrics, 7-row Grafana dashboard" -m "- Body limits, security headers, GOMEMLIMIT, timeouts, goleak" -m "- task chaos: C1-C4 with Toxiproxy, all pass at 5k users" -m "Phase P7a - report: docs/reports/P7a-hardening.md"
```
