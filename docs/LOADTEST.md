# LOADTEST: The Rush

The claim: **50,000 concurrent users on 500 seats → 0 oversells, 0 duplicate charges, checkout p95 N ms, X req/s peak.** This doc defines exactly what we measure, so the claim holds up in an interview.

## 1. What "50,000 concurrent users" means here
Each simulated user is a goroutine with:
- its own anonymous session (its own `user_id`)
- its own keep-alive TCP connection (`--conn=dedicated`)
- its own Idempotency-Keys
- a complete journey: session → join → wait → claim → hold → order → pay → confirm

All 50,000 arrive within a 20 s window around T0 and stay in the system until they reach a terminal state. The run reports `peak_in_system` (joined minus terminal), which must be ≥ 49,000 for the headline. Ports and connections are real. The 50k connections come from one swarm container: we raise `ip_local_port_range` to `1024 65535` (≈ 64k ports to one destination) and `nofile` to 200k.

**Why a custom swarm:** a k6 VU costs MBs of memory, so 50k stateful VUs don't fit on one 16 GB machine. Goroutines do. k6 still cross-checks the numbers at lower concurrency (§5).

## 2. Environments
| Env | Where | Used for |
|---|---|---|
| **Canonical (headline)** | GitHub Actions `ubuntu-latest` (4 vCPU / 16 GB, free for public repos) running the full stack and the swarm on one VM | Resume numbers, public run summary |
| Local smoke | Laptop (Docker Desktop) | `task rush:smoke`: 5k users, fast iteration |
| Local full (optional) | Laptop with `.wslconfig memory=10GB` | Only if it fits. Never the headline |

Full stack topology for every rush: `caddy` → `api1..3`, plus `worker`, `mockpay`, `postgres`, `redis`, and `prometheus`/`grafana` (optional in CI). The swarm runs as a container on the same compose network and targets `http://caddy:8000`. Each run starts from a fresh stack: `docker compose down -v`, then `up`, `migrate`, `seed`.

Every report must state: runner CPU/RAM (`nproc`, `free -m`), versions, commit SHA, scenario parameters and topology. Because the swarm shares CPUs with the system under test, the numbers are conservative. Say so on the results page.

## 3. The swarm (`sluice rush`)

### 3.1 Flags
```
sluice rush --target http://caddy:8000 --event tatkal-chn-sbc --users 50000 \
  --scenario H --arrival "pre=0.7:-10s..0s,post=0.3:0s..10s" --opens-in 60s \
  --conn dedicated --out loadtest/results/<run_id> --seed 42 --max-duration 15m
```
- `--scenario H|B0|B1|B2|SMOKE` presets the behaviour mix and mode.
- `--seed` makes the behaviour mix reproducible.
- The swarm calls `POST /admin/events/{id}/open-sale` (ADMIN_TOKEN from env) so that T0 = start + `--opens-in`.

### 3.2 User state machine
```
ARRIVE → SESSION → JOIN ─┬→ PREQUEUE ──(T0)──→ WAITING
                         └→ WAITING
WAITING ─poll(retry_after±20%)→ WAITING | PAUSED | ADMITTED | SOLD_OUT* | OFFER_EXPIRED → (rejoin ≤2) JOIN
ADMITTED → think → [availability] → HOLD ─┬→ TAKEN/INSUFFICIENT → retry ≤3 (pick → best → fewer seats) → NO_SEAT*
                                          └→ HELD → think → [abandon 8% → ABANDONED*] → ORDER → PAY
PAY ─→ CONFIRMED → TICKET*
    ─→ FAILED (decline) → new attempt (70%) | PAYMENT_FAILED*
    ─→ PENDING (timeout) → retry same IK / poll /confirm → TICKET* | FAILED(not charged)*
* = terminal. Users leave the queue (DELETE /queue) on NO_SEAT and ABANDONED, enforced in one
place (user.end). INSUFFICIENT with available = 0 (every seat held, none sold) ends the retries
at once as NO_SEAT; only a SOLD_OUT answer is SOLD_OUT.
```

### 3.3 Behaviour mix (scenario H)
| Behaviour | Share |
|---|---|
| Quantity 1 / 2 / 4 | 80% / 15% / 5% |
| Best-available / pick seats from the latest bitmap | 70% / 30% |
| Think time after admission / after hold | U(1–4 s) / U(2–6 s) |
| Abandon after hold (lets the TTL reclaim) | 8% |
| Payment method `pm_ok` / `pm_decline` / `pm_timeout` | 95% / 3% / 2% |
| Double-submit `/pay` concurrently with the **same** IK | 5% |
| Client timeout, then retry with the **same** IK (also on 5xx, 429 after `Retry-After`, and `REQUEST_IN_PROGRESS`) | automatic |
| Rogue retry with a **new** IK | 1% |
| Status timeout 2 s, then retry; HTTP timeout 5 s on every other call (a 2 s `/pay` timeout would race the API's 2 s PSP timeout; ADR 0010) | — |
| Each hold attempt (pick → best → fewer) and each `/confirm` poll is a new intent with a **new** IK; a third `OFFER_EXPIRED` ends as NO_SEAT; a declined payment's retry pays with `pm_ok` | ADR 0010 |
| Synthetic client IP (X-Forwarded-For) | ~10 users per IP (NAT-like) |

### 3.4 Measurement rules
- **Client-side latency** with HdrHistogram (1 µs–60 s, 3 significant digits), one histogram per route.
- **Coordinated-omission safe:** record each request against its *intended* start time, for example the scheduled poll time, not the time it actually went out. If the swarm falls behind schedule, that delay counts as latency.
- Errors and timeouts are recorded at their elapsed time and counted separately. Expected 409/422/429 are counted as business outcomes, not errors.
- The rush window runs from T0 − 10 s to the last terminal user.
- Every 1 s the swarm samples `GET /stats/{id}` and records the server-side state for replay.
- **Self-check:** the swarm reports its own CPU and goroutine count plus its schedule lag p99. If lag p99 > 100 ms, the run is flagged as **generator-bound** and the report must say so.

### 3.5 Outputs (`loadtest/results/<run_id>/`)
- `summary.json`: the source of every number we publish:
```json
{ "run_id": "...", "git_sha": "...", "scenario": "H", "started_at": "...",
  "env": { "runner": "github-ubuntu-latest", "cpus": 4, "ram_gb": 16, "go": "...", "postgres": "...", "redis": "..." },
  "config": { "users": 50000, "seats": 500, "hold_ttl_s": 60, "payment_hold_ttl_s": 90, "api_replicas": 3 },
  "load": { "peak_in_system": 49870, "requests": 0, "peak_rps": 0, "mean_rps": 0, "generator_lag_p99_ms": 0 },
  "outcomes": { "TICKET": 0, "SOLD_OUT": 0, "NO_SEAT": 0, "PAYMENT_FAILED": 0, "ABANDONED": 0, "UNRESOLVED": 0, "seats_sold": 500 },
  "timing": { "t0": "...", "first_sale_s": 0, "sold_out_s": 0, "drain_s": 0 },
  "latency_ms": { "status": {"p50":0,"p95":0,"p99":0,"max":0,"n":0}, "join": {}, "hold": {}, "order": {}, "pay": {}, "confirm": {}, "checkout": {} },
  "errors": { "rate": 0.0, "5xx": 0, "timeouts": 0, "transport": 0 },
  "business": { "409": 0, "422": 0, "429": 0 },
  "audit": { "oversold": 0, "duplicate_charges": 0, "orphan_charges": 0, "quota_violations": 0, "stuck_holds": 0, "inventory_drift": 0, "passed": true,
             "mode": "normal", "ticket_order_consistency": 0, "admitted_after_soldout": 0, "unresolved_users": 0, "settle_s": 0, "settle_timed_out": false, "exit_code": 0 },
  "records": { "admitted_after_soldout": 0, "admissions": 0, "sold_out_seen_at": "...", "sold_out_grace_ms": 1000 },
  "headline": { "checkout_p95_ms": 0, "status_p95_ms": 0, "peak_rps": 0 } }
```
- `timeseries.csv` with 1 s rows: `t, rps_total, rps_join, rps_status, rps_hold, rps_order, rps_pay, rps_confirm, p95_status_ms, p95_checkout_ms, errors, in_system, waiting, cursor, offers, active, available, held, sold, sold_out`
- `report.md`: a human summary (the same tables as the job summary)
- `audit.json`: the full auditor output
- The `audit` block is `null` until `sluice audit --results <dir>` (or `sluice rush --audit`) merges its verdict in. A count is `null` when the check doesn't apply (naive mode: `stuck_holds`, `inventory_drift`; direct mode: `admitted_after_soldout`). `records` holds the swarm's own I7 count (ADR 0011).

## 4. Scenarios
| ID | Users | Waiting room | Holds | Payments | Purpose | Expected |
|---|---|---|---|---|---|---|
| **H** (headline) | 50,000 | on | `redis_pg` | idempotent | The claim | All invariants 0, SLOs met or explained |
| SMOKE | 5,000 | on | `redis_pg` | idempotent | Fast local loop | All invariants 0 |
| **B0** naive | 10,000 | **off** (`QUEUE_ENFORCED=false`) | `BASELINE=naive` (check-then-insert into `naive_sales`) | naive (`confirm=true`, random keys, charge on click) | Show the bugs exist | **oversold > 0, duplicate charges > 0** |
| **B1** holds-only | 10,000 | off | correct | naive (`BASELINE=holds_only`) | Show holds fixed but payments still broken | oversold = 0, **duplicate charges > 0** |
| **B2** direct | 10,000 | off | correct | idempotent | Correct without the queue, but slower | 0 / 0, worse latency and error rate than H |
| E1 (optional) | 10,000 | off | `HOLD_PATH=pg_only` | idempotent | Value of the Redis fast path | 0 / 0; compare p95 with B2 |
| C1–C4 | 10,000 | on | correct | idempotent | Chaos (P7) | All invariants 0 |

B0 doubles as the **auditor sensitivity test**: if the auditor reports 0 on B0, the auditor is broken.

Scenario env overlays: `deploy/env/rush.b0.env`, `rush.b1.env`, `rush.b2.env` (selected by `SLUICE_SCENARIO`; `task rush:b0` etc. set it). In B0 the seats live in `naive_sales`, which `/stats` can't see, so B0's `seats_sold`/`sold_out_s` are not meaningful; the auditor's counts are the source. Baseline modes turn off only the reconciler's PSP-intent pass, so B1's duplicate charges aren't refunded before the audit (ADR 0011).

## 5. k6 cross-checks (Docker image `grafana/k6`, no install)
- **K1 `spike-queue.js`:** `ramping-arrival-rate` to 3,000 iterations/s for 30 s. Each iteration is `POST /sessions` + `POST /queue`. Reports p95 for join and session; it should agree with the swarm's join p95 within ±20%.
- **K2 `checkout.js`:** against the `loadtest-big` event (50,000 seats), `constant-arrival-rate` 200/s for 60 s. Each iteration is `POST /test/admit` + hold(1) + order + pay(`pm_ok`). Reports the checkout p95 and the **max sustainable checkouts/s** (raise the rate until p95 > 250 ms or errors > 0.1%).
- Run with `K6_WEB_DASHBOARD=true K6_WEB_DASHBOARD_EXPORT=loadtest/results/<id>/k6.html` for a free HTML report.

## 6. Auditor (`sluice audit --event <slug> [--mode naive]`)
1. **Settle** (≤ 120 s): call `POST /admin/reconcile`, then wait until there are no River jobs in `available/running/retryable` (the periodic kinds `reconcile` and `audit_cache` excluded), no holds past expiry, no undelivered MockPay webhooks that still have attempts left, and no AUTHORIZED/CAPTURED intent younger than the orphan age (35 s). Call reconcile once more and wait for quiet again. The checks run even if settle times out (ADR 0011).
2. **Checks** (each reports a count and examples):

| Check | Source | Pass |
|---|---|---|
| oversold | seats with > 1 **distinct** order in `tickets`, plus distinct (seat, order) pairs − capacity (naive mode: `naive_sales` by `order_ref`; one order's own repeat insert is one buyer) | 0 |
| duplicate_charges | MockPay `/v1/ledger/summary`: merchant_refs with > 1 captured-not-refunded intent | 0 |
| orphan_charges | Captured-not-refunded intents whose order isn't CONFIRMED | 0 |
| quota_violations | Tickets per (event, user) > max_per_user | 0 |
| stuck_holds | Holds ACTIVE/PAYMENT_PENDING with `expires_at < now() − 2 × SWEEPER_INTERVAL` | 0 |
| inventory_drift | Redis avail/sold/held vs PG seat states | 0 |
| ticket_order_consistency | Every ticket's order is CONFIRMED with a CAPTURED (or AUTHORIZED, capture pending) payment | 0 mismatches |
| admitted_after_soldout | From swarm records: ADMITTED answers whose request was sent > 1 s (the `/stats` poll interval) after the swarm first saw sold out; n/a in direct mode | 0 |
| unresolved_users | From swarm records | 0 |

3. Exit code: 0 if everything passes, 1 if any invariant fails, 2 if the settle times out, 3 if the auditor itself couldn't run (bad flags, unreachable datastore).
4. Runs as `sluice audit` (also the `audit` compose service, profile rush; `task audit EVENT=... [RESULTS=<run_id>] [MODE=naive]`) or at the end of `sluice rush --audit`. It reads Postgres (both databases), Redis and MockPay's ledger directly, and calls the admin reconcile route.

## 7. CI: `.github/workflows/rush.yml`
- **Trigger:** `workflow_dispatch` with inputs `scenario` (H \| SMOKE \| B0 \| B1 \| B2) and `users`. `timeout-minutes: 60`.
- **Steps:**
  1. Checkout, set up Go, install Task.
  2. Tune the host:
     `sudo sysctl -w net.core.somaxconn=65535 net.ipv4.tcp_max_syn_backlog=65535 net.ipv4.ip_local_port_range="1024 65535" net.ipv4.tcp_tw_reuse=1 fs.file-max=2000000`
  3. `docker compose build`, then `task stack:up ENV=rush`.
  4. `task rush SCENARIO=... USERS=...`, then `task audit`.
  5. Upload `loadtest/results/<run_id>` as an artifact.
  6. Write `report.md` to `$GITHUB_STEP_SUMMARY` (public proof on the run page).
  7. Fail the job if the audit fails or there are unresolved users.
- CI never commits or pushes anything. Sheshakanth copies the curated results in P8.

## 8. Tuning loop (P7)
Profile with pprof on `PPROF_ADDR`, then fix the top bottleneck and re-run. Usual suspects, in order:
1. Caddy → API connection churn: set `keepalive_idle_conns_per_host 1024`.
2. pgxpool size and acquire waits.
3. JSON encoding on the status path.
4. Lock contention in the snapshot cache.
5. Redis round trips: pipeline them where they're independent.
6. GC: set `GOGC`/`GOMEMLIMIT` per container.

Write down every change with before/after numbers in the P7 report.

**Never** do any of the following to hit a number:
- disable durability
- cache inventory decisions anywhere except Redis/PG
- drop requests silently
- exclude errors from the percentiles
- raise timeouts to hide errors
- report the best of N runs as if it were the typical run (report the median of 3 if you run several)
