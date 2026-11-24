# P5: The Rush harness (swarm, auditor, baselines)
**Timebox:** 6 h · **Read first:** LOADTEST.md (all) · SPEC.md §6–8 · ARCHITECTURE.md §3

## Goal
Build the machine that produces the claim. It needs a 50k-user swarm with honest measurement and an auditor that **provably** catches oversells and duplicate charges, since the naive baselines must fail. This phase also covers the first real numbers.

## Tasks
1. **Containerise:**
   - `deploy/docker/Dockerfile`: multi-stage, `CGO_ENABLED=0`, output `gcr.io/distroless/static-debian12:nonroot`, with version and commit via ldflags.
   - `deploy/compose.app.yml`: `api1..3`, `worker`, `mockpay`, `caddy`, each with a healthcheck.
   - `deploy/caddy/Caddyfile` with three routes:
     - `/api/*` → api1..3 (`lb_policy least_conn`, active health check on `/readyz`, `keepalive_idle_conns_per_host 1024`)
     - `/pay/*` → mockpay (strip `/pay`)
     - static `web/out` with an SPA fallback
   - `deploy/env/rush.env` per STACK.md §4.
2. **`sluice rush`** (`internal/rush`), everything in LOADTEST §3:
   - flags, presets, the user state machine and the behaviour mix
   - arrival profile and dedicated connections (one `http.Transport` per user with `MaxConnsPerHost=1`)
   - synthetic XFF, the IK discipline (same key on retry; a new key only for the 1% rogue retry)
   - HdrHistogram per route with **intended-start** timing
   - the 1 s `/stats` sampler and the generator self-check (CPU, goroutines, schedule lag)
   - outputs `summary.json`, `timeseries.csv` and `report.md`
   - a progress line every 5 s
3. **`deploy/compose.rush.yml`:** the swarm container on the compose network, with `sysctls: net.ipv4.ip_local_port_range: "1024 65535"`, `ulimits: nofile: 200000` and the results volume mounted.
4. **`sluice audit`** (`internal/audit`), per LOADTEST §6: settle, then every check, `audit.json`, and the exit codes. It's also called at the end of `sluice rush --audit`.
5. **Baselines** (only in rush/test). These are the code a hurried team would write. Keep them small and realistic:
   - **`BASELINE=naive`:**
     - *Hold* is a read-only availability check against `naive_sales` (migration `0003_naive_baseline.sql`; 0002 is the queue snapshot state, ADR 0009). Nothing is reserved, and the response is a pseudo-hold carrying an `order_ref`. Best-available picks the lowest unsold seat numbers it just read.
     - *Pay* does a check-then-insert into `naive_sales` with no lock and no constraint. It then creates a MockPay intent with `confirm=true`, a **random** PSP key and `merchant_ref = order_ref`, and captures immediately.
     - No idempotency middleware.
   - **`BASELINE=holds_only`:** the correct holds, orders and ConfirmOrder (no oversell possible). But `/pay` uses the naive payment path: every call creates a new intent with a random key and captures it, without consulting the order's live payment.
   - Put naive code in `internal/inventory/naive` and `internal/payments/naive` under a loud `// BASELINE ONLY` banner, and wire it in only when config allows (startup refuses outside rush/test).
   - **Direct mode:** with `QUEUE_ENFORCED=false`, a session token is enough for holds and orders.
   - Scenario env overlays: `deploy/env/rush.b0.env`, `rush.b1.env` and `rush.b2.env` sit on top of `rush.env`.
6. **Taskfile:**
   - `stack:up` / `stack:down` (`ENV=dev|rush|demo`)
   - `rush:smoke` (5k), `rush` (50k, H), `rush:b0`, `rush:b1`, `rush:b2` (10k each)
   - `audit`, `k6:spike`, `k6:checkout`
   - every rush task: fresh stack → migrate → seed → swarm → audit → print the summary table
7. **k6 scripts** `loadtest/k6/spike-queue.js` and `loadtest/k6/checkout.js` (LOADTEST §5), run through `docker run grafana/k6` on the compose network.
8. **Self-tests** for the harness:
   - a unit test of the HDR and intended-start logic: a fake server that stalls 1 s must show up in p99
   - the percentile maths against a known distribution
   - summary JSON schema validation

## Runs to perform and record (locally; the 50k headline comes in P7 on CI)
| Run | Command | Must show |
|---|---|---|
| SMOKE (5k) | `task rush:smoke` | All invariants 0, unresolved 0 |
| B0 (10k, or the largest the laptop can manage) | `task rush:b0` | **oversold > 0 and duplicate charges > 0.** This proves the auditor works |
| B1 | `task rush:b1` | oversold 0, **duplicates > 0** |
| B2 | `task rush:b2` | 0 / 0, higher p95 / error rate than SMOKE |
| K1, K2 | `task k6:spike`, `task k6:checkout` | Agree with the swarm within ±20% on join p95; record max checkouts/s |

If the laptop can't sustain 10k, use the largest stable number, record it, and mark the run for re-running on CI in P7.

## Acceptance
- [ ] Each run in the table matches its "Must show" column, with the numbers pasted from `summary.json` into the report.
- [ ] B0 fails the audit with a non-zero exit, H/SMOKE pass with exit 0, and the auditor's sensitivity is shown.
- [ ] `peak_in_system` and `generator_lag_p99_ms` are reported for every run.
- [ ] `task lint test` is green.

## Report extras
The first bottleneck you noticed (with pprof evidence if you have it), the laptop specs, and the baseline matrix table filled in with real numbers.

**Commit hint:** `feat(rush): 50k-user swarm, invariant auditor, naive baselines, k6 cross-checks`
