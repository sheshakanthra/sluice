# ADR 0013: Readiness independent of the request pool, load shedding, tolerant health checks

- **Status:** accepted
- **Date:** 2026-11-25
- **Phase:** P7 (session P7a)

## Context
P5b found a cascade in direct mode at 20k users (`docs/reports/P5b-rush.md` §4.4):

1. Each API replica has `DB_MAX_CONNS=24`. Under the rush every connection was checked out and the hold path's queries queued for one until their deadlines.
2. `/readyz` pinged Postgres **through the same pool**, so it queued too and missed Caddy's 2 s `health_timeout`.
3. Caddy marked all three replicas unhealthy; 49,105 requests got 503 `no upstreams available`.

The waiting room exists to keep that load away from the replicas, but a replica that is merely busy must never look dead. P7a reproduced and fixed it in three steps, each one measured with `task rush:b2 USERS=20000` (B2 = the correct system without the waiting room, the scenario that produced the cascade).

## Decision
1. **Readiness has its own clients and a cached state** (`internal/httpx/health.go`, `internal/app/api.go`).
   - The API opens a 2-connection pgx pool and a 2-connection Redis client used only by `/readyz` (and the migrations check).
   - `Readiness.State` serves the cached result; once it is older than 1 s it starts **one** background refresh and still answers at once. Only a cold or >10 s stale state probes synchronously.
   - On SIGTERM `/readyz` turns 503 (`draining`) for 3 s before `Shutdown` drains in-flight requests (`stop_grace_period: 25s`).
2. **Each replica sheds its own overload** (`internal/httpx/shed.go`): at most `MAX_IN_FLIGHT` (96) **write** requests (POST/DELETE/PUT/PATCH) at once; the next one waits up to `SHED_WAIT` (50 ms), then gets 503 `OVERLOADED` with `Retry-After: 1`. Reads, probes and `/metrics` are never shed. Reads are Redis-only or cached, and the queue status poll is the waiting room's heartbeat: the first SMOKE run with all methods counted shed status polls at T0 and reached a 0.5369% error rate; with writes only it was 0.0154% (P7a report §4.2).
3. **Caddy health checks tolerate a busy box** (`deploy/caddy/Caddyfile`): active `health_fails 3` / `health_passes 1`; passive `max_fails 100` within `fail_duration 5s`; `lb_try_duration 1s` retries a failed dial on another replica.
4. **A hold whose commit is abandoned undoes its Redis grant** (`internal/inventory/hold.go`). When the PG commit fails with `context.Canceled`/`DeadlineExceeded` (the client gave up while it waited for a connection), the grant is compensated at once instead of blocking its seats until expiry. PG stays the judge: if the commit landed after all, `repairSeats` takes the seats back out of `avail`.
5. **Pool sizes stay at `DB_MAX_CONNS=24` per process**; see the measurements below.

## Measurements (all `task rush:b2 USERS=20000`, laptop, same stack)
See `docs/reports/P7a-hardening.md` §3 for the full tables and the Prometheus queries.

| run | change | Caddy `no upstreams available` | `/readyz` p99 | error rate | unresolved | drift | audit |
|---|---|---|---|---|---|---|---|
| p5b-b2-20k (P5b) | before | 49,105 | n/a (not exported) | 57.28% | 4 | 0 | fail |
| p7a-b2-20k | 1 + 2 (TTL cache, probe in the request path), `MAX_IN_FLIGHT=256` | 40,052 | 0.85 s (handler) | 88.95% | 133 | 6 | fail |
| p7a-b2-20k-v2 | + background refresh, `MAX_IN_FLIGHT=96`, `health_fails 3`, step 4 | 65,747 | 0.010 s | 83.84% | 0 | 0 | **pass** |
| p7a-b2-20k-v3 | + passive `max_fails 100` | **0** | 0.047 s | 66.30% | 7 | 0 | fail (unresolved 7) |

In v3, at most one replica was marked down at a time (api1, for about 20 s of the run), so Caddy always had an upstream. Caddy's active probe still timed out 35 times in total, while the `/readyz` handler's p99 was 47 ms; the delay is in connection setup on a CPU-saturated box, which `health_fails 3` absorbs. All three 20k runs and P5b's are generator-bound (lag p99 > 100 ms, LOADTEST §3.4): v1 3,326 ms, v2 1,008 ms, v3 203.5 ms, P5b 137.3 ms. They show the cascade is gone, not SLO numbers.

Pool sizing, from the v2 run's `sluice_pgxpool_*` metrics: all 3 × 24 request connections were checked out for the whole rush; 79.8% of acquires had to wait, 0.133 s on average; the three replicas completed about 73,700 acquires over the 6 min query window, while Postgres used ~2.6 cores of a laptop shared with the 20k-user generator and Caddy. Postgres CPU, not the pool, is the limit, so more connections would add contention without adding throughput. `MAX_IN_FLIGHT=96` (4 × the pool) bounds the queue in front of the pool so a request that is admitted gets a connection well inside the swarm's 5 s client timeout; at 256 most admitted requests timed out client-side (v1: 344,557 timeouts).

## Consequences
- A saturated replica answers fast: `/readyz` in milliseconds, overflow with 503 `OVERLOADED` + `Retry-After`. Clients retry with the same `Idempotency-Key` (the swarm and the web app already do).
- 503 `OVERLOADED` is a new code (API.md §1.3, `web/components/notices.tsx`). It counts as an error in the rush error rate: shedding is honest overload, not success.
- The readiness state can lag reality by up to ~1 s plus one probe (≤ 2 s). A dependency outage is still reported within about 3 s.
- Each API process opens 2 more Postgres connections: 3 × 26 + worker 24 + MockPay 24 = 126 of `max_connections=200`.
- Direct mode at 20k is still far outside the SLOs on this laptop (the generator is CPU-starved too); the waiting room is the design answer. P7 task 4 (tuning) and the CI headline run own the numbers.
- Docs updated: `docs/API.md` (§1.3 `OVERLOADED`, `/readyz`, body cap), `docs/STACK.md` §4 (`MAX_IN_FLIGHT`, `SHED_WAIT`, health pool), `docs/ARCHITECTURE.md` §6 (two failure-mode rows), `.env.example`, `deploy/env/rush.env`.

## Addendum (2026-11-26): no fleet-wide ejection, panic fallback
**Context.** The H 50k CI run `45781258193` (4 vCPU shared by the swarm and the stack) shows `health_fails 3` was not enough. At T0, Caddy's active `/readyz` probe timed out at 2 s on **all three** replicas, and they were all marked down from 08:15:11 to 08:15:17Z. In that window Caddy itself answered 24,632 requests with 503 `no upstreams available` (`P8-h50k-diagnosis.md` §3). The replicas were starved of CPU, not dead: `/readyz` is served from cached state.

**Decision** (`deploy/caddy/Caddyfile`):
1. **Slower, more tolerant active check:** `health_interval 3s`, `health_timeout 5s`, `health_fails 5`. A replica now has to fail about 15 s of consecutive probes, each allowed 5 s, before it is ejected. `health_passes 1` still brings it back on the first pass. The passive check (`max_fails 100` in `fail_duration 5s`) is unchanged and still catches a killed replica within a second at rush rates (chaos C2).
2. **Panic fallback when every replica looks unhealthy:**
   - When the proxy finds no healthy upstream for `lb_try_duration` (1 s), it raises its own 503. A `handle_errors 503` block for `/api/*` then sends the request to `api1..3` anyway, ignoring health, with `lb_try_duration 1s`.
   - These answers carry `X-Upstream-Fallback: all-unhealthy`.
   - A replica's own responses, including 503 `OVERLOADED` + `Retry-After`, are responses, not proxy errors, so they pass straight through and never trigger the fallback.
   - If the replicas really are gone, the fallback's dials fail and the client gets a 502/503 about 1 s later.
   - Verified locally with `caddy:2.11.9` and two stand-in upstreams whose health URI always failed. With both marked unhealthy, a `POST` with a body was served through the fallback (200, body intact, fallback header set). A replica's own 503 passed through without the header. With both upstreams stopped, the client got 503 after 4.0 s. The commands and output are in `docs/reports/P8-h50k-fix.md` §D.

**Consequences.**
- A starved fleet keeps serving, and each replica's own shedding (`MAX_IN_FLIGHT`, 503 `OVERLOADED`) stays the overload valve. Caddy no longer turns slow probes into a fleet-wide outage.
- A truly dead replica that still accepts TCP but never answers `/readyz` stays in rotation up to about 15 s longer than before. Requests to it time out client-side and are retried with backoff (ADR 0017). The passive check still ejects replicas whose dials fail.
- While all replicas are marked unhealthy, every request first waits `lb_try_duration` (1 s) before the fallback runs. That delay shows up in client latency, measured from the intended start.
