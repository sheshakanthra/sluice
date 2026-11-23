# P7: Hardening (observability, chaos, SLO tuning, CI headline run)
**Timebox:** 6 h · **Read first:** LOADTEST.md §2, §4, §7–8 · ARCHITECTURE.md §6 · SPEC.md §7

## Goal
The headline 50k run is green on GitHub Actions with every invariant at 0. The chaos suite proves the invariants survive real failures. Every latency number is tuned honestly.

## Tasks
1. **Observability:**
   - `deploy/prometheus/prometheus.yml` scrapes api1..3, worker, mockpay and Caddy metrics.
   - Grafana dashboard `deploy/grafana/dashboards/sluice.json`, provisioned, with these rows: Traffic (RPS by route) · Latency (p50/p95/p99 by route) · Queue (waiting, cursor, offers, active, admit rate) · Inventory (available/held/sold) · Payments (by status, PSP latency, webhooks) · Correctness (invariant counters, duplicates prevented, orphans fixed) · Runtime (goroutines, GC, pgxpool acquire wait, Redis pool).
   - pgxpool and go-redis pool stats exported as metrics.
2. **Production hygiene audit:**
   - Every outbound call has a timeout.
   - Graceful shutdown drains in-flight requests (Caddy health-check failover proves it).
   - Request body limits are set.
   - Security headers are set at Caddy.
   - `GOMEMLIMIT` and `GOGC` per container.
   - No goroutine leaks: run `goleak` in tests.
3. **Chaos suite** (`task chaos`, `deploy/compose.chaos.yml` with Toxiproxy between api↔redis, api↔postgres and api↔mockpay). Each run is the SMOKE-sized rush plus one fault, followed by the audit:

| ID | Fault | When | Must hold |
|---|---|---|---|
| C1 | `redis-cli FLUSHALL` | T0 + 15 s | I1–I4 = 0; worker rebuilds inventory and restores the queue; unresolved 0; the error spike around the flush is reported (not held to the 0.1% budget) |
| C2 | `docker compose kill api2` | T0 + 10 s | I1–I4 = 0; Caddy fails over; error rate reported |
| C3 | Toxiproxy: MockPay +2 s latency on 30% of calls, plus timeouts | Whole run | Duplicate charges 0, orphan charges 0 after settle |
| C4 | `docker compose pause postgres` for 3 s | T0 + 20 s | I1–I4 = 0; requests recover; no stuck holds after settle |

4. **SLO tuning loop** (LOADTEST §8): profile, fix the top bottleneck, re-run, and log before/after numbers in a table in the report. Respect the "never do" list.
5. **CI headline workflow** `.github/workflows/rush.yml`, per LOADTEST §7:
   - `workflow_dispatch` with `scenario` and `users`
   - host sysctl tuning, build images, stack up with `ENV=rush`, swarm, audit
   - upload the artifact
   - write `report.md` to `$GITHUB_STEP_SUMMARY`
   - fail the job on any invariant or on unresolved users
   - **No commits or pushes from CI.**
6. Prepare a `docs/results/README.md` explaining how to read a run (the fields of `summary.json`).

## Sheshakanth's steps (Claude Code cannot do these; list them in the report)
1. Push and make sure the repo is **public** (Actions minutes are free and unlimited only on public repos).
2. Actions → `rush` → Run workflow with H / 50000. Then B0 / B1 / B2 at 10000 if the P5 laptop runs were reduced.
3. Paste the run URLs back into the next Claude Code session (P8 uses them).

## Acceptance
- [ ] `task chaos` passes C1–C4, with results tables in the report.
- [ ] The Grafana dashboard loads with data during `task rush:smoke`. Add a screenshot description to the report.
- [ ] `rush.yml` passes `actionlint`, and a SMOKE-sized dry run with `act` or a local equivalent succeeds, or the report states that only a CI run can verify it.
- [ ] Tuning table with before/after numbers. The SLOs are met on the local SMOKE run, or the remaining gap is explained.
- [ ] `task lint test` is green.

**Commit hint:** `feat(ops): Grafana dashboards, chaos suite, SLO tuning, CI rush workflow`
