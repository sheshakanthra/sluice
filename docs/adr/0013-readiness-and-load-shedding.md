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
