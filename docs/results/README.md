# How to read a rush run

Every rush (`task rush*`, `task chaos` or the `rush` GitHub Actions workflow) writes one directory per run, `loadtest/results/<run_id>/`. Curated copies live here, under `docs/results/<phase>/`. Every published number must trace back to one of these files and the command that produced it (CLAUDE.md rule 5).

## Files in a run directory

| File | Written by | What it is |
|---|---|---|
| `summary.json` | `sluice rush` (audit block filled by `--audit` / `sluice audit --results`) | The source of every published number. Schema below. |
| `report.md` | `sluice rush` | Human-readable rendering of `summary.json`. CI copies it into the job summary. |
| `audit.json` | `sluice audit` | The auditor's full verdict: each check with its count, the SQL evidence (sample rows) and the settle loop. |
| `timeseries.csv` | `sluice rush` | One row per second relative to T0 (negative = before the sale opens). Columns are listed below. |
| `runner.md`, `stack.log.gz`, `ps.txt`, `verdict.md` | CI only (`.github/workflows/rush.yml`) | Runner CPU/RAM/versions, the whole stack's logs, container states, and the CI gate's verdict (`loadtest/ci/verdict.py`). |

## `summary.json`, field by field

Metric definitions are in `docs/SPEC.md` §6. This section says where each one lives.

### Identity and environment
| Field | Meaning |
|---|---|
| `run_id`, `scenario` | Run name and scenario (`H`, `SMOKE`, `B0`, `B1`, `B2`, chaos runs use `SMOKE`). See LOADTEST §4. |
| `git_sha` | Commit of the code **in the image**. Laptop runs of an uncommitted tree still show the last commit, so the report must say when that happened. |
| `started_at` | Swarm start, UTC. |
| `env.runner` | `local` (laptop) or `github-actions-ubuntu-latest` (the canonical environment, SPEC §7). |
| `env.cpus`, `env.ram_gb` | What the swarm container sees. It shares them with the whole stack, so every number is conservative. |
| `env.go`, `env.postgres`, `env.redis` | Versions. |

### Configuration
| Field | Meaning |
|---|---|
| `config.users`, `config.seats` | Simulated users and seats on sale. |
| `config.hold_ttl_s`, `config.payment_hold_ttl_s` | Hold TTLs of the rush profile. |
| `config.api_replicas` | API replicas behind Caddy (3). |
| `config.conn` | `dedicated` = one keep-alive TCP connection per user (the "concurrent users" definition), `pooled` = shared transport for small ad-hoc runs. |
| `config.arrival` | Arrival curve: by default 70% of users arrive in the 10 s before T0, 30% in the 10 s after. |
| `config.opens_in_s` | Seconds from swarm start to T0 (the sale opening). |
| `config.seed` | RNG seed. The same seed gives the same arrival schedule and choices, so runs are comparable. |
| `config.direct` | `true` when the waiting room is off (B0, B1, B2). |

### Load
| Field | Meaning |
|---|---|
| `load.peak_in_system` | Peak of (joined − terminal) users. The headline needs ≥ 49,000 for the "50,000 concurrent users" claim. |
| `load.requests` | Every HTTP request the swarm sent. |
| `load.peak_rps`, `load.mean_rps` | Peak over 1 s buckets, and the mean over the run. |

### Outcomes
`outcomes.TICKET`, `SOLD_OUT`, `NO_SEAT`, `PAYMENT_FAILED`, `ABANDONED` are the terminal states (SPEC §6). `outcomes.UNRESOLVED` must be **0**: a user who never got an answer is a hard failure. `outcomes.seats_sold` comes from `/stats` (meaningless in B0, whose seats live in `naive_sales`).

### Timing (seconds relative to T0; null if it never happened)
| Field | Meaning |
|---|---|
| `timing.t0` | Sale opening, UTC. |
| `timing.first_sale_s` | First confirmed ticket. |
| `timing.sold_out_s` | Event sold out. With the rush TTLs this includes abandoned holds expiring and their seats being resold. |
| `timing.drain_s` | Last user reaches a terminal state. SLO ≤ 300 s. |
| `timing.window_from_s` | Start of the measurement window (−10 s: the pre-T0 arrivals count). |

### Latency (`latency_ms.<route>` = `p50`, `p95`, `p99`, `max`, `n`)
Client-side, measured from each request's **intended** start time (so generator delays count against the system, never for it), errors included. Routes: `session`, `join`, `status`, `availability`, `hold`, `order`, `pay`, `confirm`, `release`, `leave`, `payments`, plus the composite:
- `checkout` = `hold` + `order` + `pay` + `confirm` pooled. Its `p95` is **the** p95 of the resume claim (SLO ≤ 250 ms).
- `status` p95: SLO ≤ 50 ms. `join` p95: SLO ≤ 100 ms.

A route with `n: 0` was never called in that run (for example `confirm`, which the swarm polls only when a payment answers "pending").

### Errors and business answers
| Field | Meaning |
|---|---|
| `errors.rate` | (5xx + transport errors + timeouts) / all requests. SLO < 0.1%. 503 `OVERLOADED` (shedding) counts as an error. |
| `errors.5xx`, `errors.timeouts`, `errors.transport` | The three parts. |
| `business.409`, `422`, `429` | Expected business answers (seat taken, validation, rate limit). Not errors. |
| `anomalies` | Unexpected answers by step, for example `hold:401 TOKEN_INVALID`. Should be empty; anything here needs explaining. |

### Generator self-check
| Field | Meaning |
|---|---|
| `generator.lag_p99_ms`, `lag_max_ms` | How late the swarm fired requests compared with their schedule. |
| `generator.generator_bound` | `true` when lag p99 > 100 ms (LOADTEST §3.4): the load generator itself was starved, so the latency numbers measure the laptop, not SLUICE. Never publish SLO numbers from such a run. |
| `generator.cpu_cores_max/mean`, `goroutines_max` | The swarm's own footprint. |
| `generator.stats_samples`, `stats_failed` | `/stats` polls used for the inventory series. |

### Records and audit
| Field | Meaning |
|---|---|
| `records.admissions`, `admitted_after_soldout`, `sold_out_seen_at`, `sold_out_grace_ms` | Swarm-side facts the auditor checks (I8: nobody admitted after sold out, beyond a 1 s grace). Null in direct mode. |
| `audit.mode` | `normal`, or `naive` for B0/B1. |
| `audit.oversold`, `duplicate_charges`, `orphan_charges`, `quota_violations`, `stuck_holds`, `inventory_drift`, `ticket_order_consistency`, `admitted_after_soldout`, `unresolved_users` | Invariant counts (I1–I8, R1; SPEC §6). All must be **0** for H, SMOKE and B2. Null = the check does not apply in that mode. B0 must show oversold > 0 and duplicate charges > 0, B1 duplicate charges > 0: if they don't, the auditor is blind. |
| `audit.settle_s`, `settle_timed_out` | How long the auditor waited for the system to settle (reconciler, sweeper) before judging. |
| `audit.passed`, `exit_code` | `0` pass, `1` violations, `2` settle timeout, `3` error. |
| `headline` | `checkout_p95_ms`, `status_p95_ms`, `peak_rps`: the claim's three numbers, copied from above. |

## `timeseries.csv` columns
`t` (s from T0), `rps_total`, `rps_join`, `rps_status`, `rps_hold`, `rps_order`, `rps_pay`, `rps_confirm`, `p95_status_ms`, `p95_checkout_ms` (per second), `errors`, `in_system`, then the queue (`waiting`, `cursor`, `offers`, `active`) and inventory (`available`, `held`, `sold`, `sold_out`) as polled from `/stats`.

## Reading a run in five checks
1. **Valid?** `generator.generator_bound` is `false`, and `env.runner` says where it ran. Laptop numbers are never the headline (LOADTEST §2).
2. **Correct?** Every `audit.*` count is 0 (or the expected non-zero for B0/B1) and `outcomes.UNRESOLVED` is 0.
3. **Concurrent?** `load.peak_in_system` (≥ 49,000 for H).
4. **Fast?** `latency_ms.checkout.p95`, `status.p95`, `join.p95`, `errors.rate`, `timing.drain_s` against SPEC §7.
5. **Typical?** One run is one sample; on a laptop runs vary by about 2×. Compare medians of 3 runs and state the spread (LOADTEST §8).

## Running the headline in CI
GitHub → Actions → `rush` → Run workflow → `scenario` and `users` (blank = the scenario's default). The job fails on any invariant violation or unresolved user (B0/B1: when the expected bugs are *not* detected). Latency SLOs are reported in the job summary but never fail the job. The run's directory is the artifact `rush-ci-<run id>-<scenario>-<users>`. CI never commits; curated copies go into `docs/results/` by hand (P8).
