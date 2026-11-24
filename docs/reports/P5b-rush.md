# P5b: The Rush (auditor, baselines, k6, recorded runs): Report

| Field | Value |
|---|---|
| Status | DONE with open items: two latency comparisons missed (§2, the K1 ±20% check and B2's "higher p95"), and the local race gate was cut short by host memory pressure (§3) |
| Date (IST) | 2026-11-24 |
| Time spent vs timebox | Not tracked precisely. P5 is timeboxed at 6 h across P5a and P5b |
| Commit base | `570aefc` (`git rev-parse --short HEAD`) |

Laptop: 12th Gen Intel Core i5-12500H (12 cores, 16 logical), 15.7 GB RAM, Windows 11. Docker Desktop 28.3.3 sees 16 CPUs and 10,427,236,352 bytes (9.7 GiB). Toolchain is go1.27.4, Postgres 18.7, Redis 8.10.3 and grafana/k6 1.4.0.
- Hardware: `Get-CimInstance Win32_Processor / Win32_ComputerSystem`
- Docker: `docker info --format '{{.NCPU}} CPUs, {{.MemTotal}} bytes, {{.ServerVersion}}'`

The swarm, k6 and the whole stack all share this one machine.

## 1. What was built

**Step 0: a lapsed admission lease ends the admission** (the owner's contract fix)
- `internal/redisx/scripts/queue_touch.lua` now answers `LIVE` / `LAPSED` / `UNKNOWN` in the same call that renews the lease, so there is no extra round trip. A lapsed lease is never revived. When `q:init` is missing (R1), the answer is `UNKNOWN` and the token alone decides.
- `internal/auth`:
  - `RequireLive` refuses LAPSED with **403 `ADMISSION_EXPIRED`** (new code in `internal/httpx/problem.go`). If the check itself errors, it fails closed with 503 + `Retry-After: 1`.
  - `POST /events/{id}/holds` and `POST /orders` use `RequireLive`. The other admission routes keep best-effort `Require`.
- `internal/queue/lease.go`: `Touch`/`Renew` return the state. `Grant` lets `POST /test/admit` write a lease the way a real claim does.
- Swarm (`internal/rush/user.go`): on `ADMISSION_EXPIRED` the user releases their hold if they have one, then rejoins. Rejoins share the two-rejoin budget.
- Docs: API.md §1.1/§3.3 and the error table, ARCHITECTURE §5, DATA-MODEL (`queue_touch`), and ADR 0010 addendum part 3.

**Task 4: the auditor, `sluice audit`** (`internal/audit`)
- **Settle:** reconcile, wait for quiet, reconcile again, wait again. The default timeout is 120 s.
- **Checks:** oversold, duplicate_charges, orphan_charges, quota, stuck_holds, inventory_drift, ticket_order_consistency, admitted_after_soldout and unresolved_users.
- **Outputs:**
  - `audit.json`;
  - a markdown table;
  - with `--results`, the verdict merged into `summary.json` and `report.md` (`rush.ApplyAudit`).
- **Exit codes:** 0 pass, 1 invariant failed, 2 settle timed out, 3 the auditor couldn't run.
- **Ways to run it:**
  - `sluice rush --audit` runs it at the end of a swarm.
  - Compose service `audit` (profile rush).
  - `task audit EVENT=... [RESULTS=...] [MODE=naive]`.

**Task 5: the naive baselines**
- Migration `db/migrations/0003_naive_baseline.sql` adds `naive_orders` and `naive_sales` with no unique constraint. Queries are in `db/queries/naive.sql`.
- **B0, `BASELINE=naive`:**
  - `internal/inventory/naive`: check-then-insert holds.
  - `internal/payments/naive.Orders`: seats checked then inserted in `naive_sales`, and a random PSP key with `confirm=true` on every click.
- **B1, `BASELINE=holds_only`:** `internal/payments/naive.HoldsOnly` uses the real holds and orders, with only `/pay` replaced by the naive version.
- **Wiring** (`internal/app/baseline.go`): route overrides, plus a second lock that refuses baselines outside rush/test (`baseline_test.go`). In baseline modes only the reconciler's PSP-intent pass is skipped (`orders.Options.NoPSPReconcile`).
- **Scenario env overlays:** `deploy/env/rush.b0.env`, `rush.b1.env`, `rush.b2.env`, selected by `SLUICE_SCENARIO`.

**Task 6: k6**
- `loadtest/k6/spike-queue.js` (K1) and `loadtest/k6/checkout.js` (K2, stepped arrival rates).
- They run as `grafana/k6:1.4.0` on the compose network via `task k6:spike` / `task k6:checkout`.
- Each run writes `k1.json`/`k2.json`, `k6-summary.json` and the HTML dashboard.

**Task 7: Taskfile**
- `rush:smoke`, `rush`, `rush:b0`, `rush:b1` and `rush:b2` each do: fresh stack (`down -v`), build, migrate, seed, swarm `--audit`.
- `audit`, `k6:spike` and `k6:checkout`. The P5 TODO stubs are gone.

**Task 8: harness self-tests**
- P5a already had the stall-in-p99 test, the known-distribution percentile test and the schema test.
- `internal/rush/p5b_test.go` adds:
  - `TestAdmissionExpiredRejoins`;
  - `TestRecordsAdmittedAfterSoldout`;
  - `TestApplyAuditSchema`.
- `internal/audit/audit_integration_test.go` runs against real Postgres and Redis:
  - **sensitivity:** an oversell, a duplicate and an orphan are each counted and the audit exits 1;
  - **clean event:** everything 0 and exit 0, then one Redis seat moved shows as drift 1;
  - **settle timeout:** exit 2.

**Curated results:** `docs/results/p5b/<run>/` holds `summary.json`, `audit.json`, `report.md` and `k1.json`/`k2.json`. The raw `loadtest/results/` stays gitignored. `loadtest/matrix.py` prints the matrix below from the `summary.json` files.

## 2. Acceptance checklist

Owner's brief:
- [x] **Step 0.** A lapsed user can't hold, and an active user can:
  - `go test ./internal/queue/ -run TestLapsedAdmissionCannotHold` → ok
  - `go test ./internal/orders/ -run TestOrderCreateRefusesLapsedAdmission` → ok
  - Mutation check (gate disabled): both fail with a 201 for the lapsed user. Restored afterwards.
- [x] **B0** oversold > 0 and duplicate charges > 0, with a non-zero auditor exit.
  - `task rush:b0 RUN_ID=p5b-b0` → oversold **17**, duplicate charges **19**, audit `exit_code` **1**.
  - The swarm exited 1; Task wraps that as 201.
- [x] **B1** oversold 0 and duplicates > 0.
  - `task rush:b1 RUN_ID=p5b-b1` → oversold **0**, duplicate charges **27**, audit exit **1**.
- [x] **B2 and SMOKE** have every invariant at 0.
  - `task rush:b2 RUN_ID=p5b-b2` → all 0, audit exit **0**.
  - `task rush:smoke RUN_ID=p5b-smoke` → all 0, unresolved 0, audit exit **0**.
- [x] **Largest size the laptop sustains.** 10k is the largest direct-mode size this laptop sustains. B2 at 20k and at 50k both failed the run validity checks (§4.3), so B0/B1/B2 are recorded at 10k.
- [x] `task lint` → `0 issues.`
- [ ] **`task test:race`: partial.** The run was stopped by the host when memory ran critically low. 10 of 21 packages passed under `-race` before that. The rest passed without `-race` this session (§3). Sheshakanth should run `task test:race`, or let CI do it.

Phase file (`docs/phases/P5-rush.md`):
- [ ] **Each run matches its "Must show".** SMOKE, B0 and B1 match. Two misses:
  - **B2: ❌ on "higher p95 / error rate than SMOKE" at 10k.** B2's invariants are 0/0, but its checkout p95 is 266.8 ms against SMOKE's 329.7 ms, and both error rates are 0.
    - Why: 9,625 of the 10k B2 users end as NO_SEAT after a fast hold attempt, so B2's checkout p95 is mostly hold requests (n = 10,175). SMOKE's is mostly order and pay.
    - At 20k, B2 does degrade sharply: checkout p95 5,103.6 ms, error rate 57.28%.
    - The comparison should be made per route, at comparable admitted load, in P7.
  - **K1: ❌ on ±20%.**
    - K1 at 350/s, matched to the swarm's peak join rate: join p95 **5.07 ms**, against SMOKE's swarm join p95 **13.3 ms**.
    - K1 at the defined 3,000/s: **544.8 ms**.
    - The two measure different things. The swarm times joins from intended start while 136k status polls and checkouts run alongside. k6 times isolated joins from send.
- [x] **B0 fails, SMOKE passes, sensitivity shown.**
  - B0 audit exit 1 vs SMOKE exit 0.
  - `TestAuditCatchesNaiveOversellAndDuplicate` plants an oversell, a duplicate and an orphan; each is counted and the exit is 1.
  - `TestAuditPassesCleanEvent` plants one Redis seat moved and gets drift 1.
- [x] **`peak_in_system` and `generator_lag_p99_ms` for every run.** See the matrix in §4.1.
- [x] **K2 max checkouts/s recorded.** **50/s** is sustainable (p95 200.3 ms, 0 errors, 0 dropped); 100/s isn't (p95 709.4 ms).
- [x] Report extras: the first bottleneck (§4.4), laptop specs (header) and the baseline matrix (§4.1).

## 3. Tests
| Suite | Count | Result | Notes |
|---|---|---|---|
| unit + integration (all packages) | 195 `func Test*` in the repo (`grep -rhoE "^func Test[A-Za-z0-9_]+" --include=*_test.go . \| wc -l`) | `go test ./... -count=1`: all packages ok, apart from `internal/db` `TestMigrationsUpDownUp`, which expected 2 migrations. Fixed to expect 3 plus the naive tables; `go test ./internal/db/` ok | New: `internal/audit` (3 testcontainers tests), `internal/rush/p5b_test.go` (3), `internal/app/baseline_test.go` (1), audit cases in `cmd/sluice/main_test.go`, `ADMISSION_EXPIRED` in the httpx catalogue test, and the Step 0 tests |
| integration (testcontainers) | 38 tests in files that use pgtest/redistest | ok (same run) | Real Postgres 18.7 and Redis 8.10.3 |
| race (`-race`) | | **partial, local** | `task lint test:race` was stopped by the host for low memory. Before that, these passed under `-race`: `cmd/sluice`, `internal/app`, `internal/audit`, `internal/auth`, `internal/buildinfo`, `internal/config`, `internal/db`, `internal/events`, `internal/fsm`, `internal/httpx`. Not reached under `-race`: inventory, jobs, mockpay, obs, orders, payments, queue, ratelimit, redisx, rush, tcretry. Those passed without `-race` this session. I did not restart it, per the host's instruction |
| lint | | `task lint` → `0 issues.` | After fixing errorlint (audit `redis.Nil`), gocritic (evalOrder) and gosec G115 (naive capacity compare) |
| property / e2e | | n/a this phase | |

Coverage: not measured this session. I skipped it to avoid another heavy run under memory pressure.

## 4. Numbers

Every run is a fresh stack (`docker compose ... down -v`, then build, migrate, seed), with 500 seats, `--conn dedicated`, a 30 s pre-open and the rush TTLs (hold 60 s, payment hold 90 s), and 3 API replicas. The stack was stopped between runs. Each number below comes from `docs/results/p5b/<run>/summary.json`, which is the copy of `loadtest/results/<run>/summary.json` written by the command shown.

### 4.1 Baseline matrix (`python loadtest/matrix.py p5b-smoke p5b-b0 p5b-b1 p5b-b2 p5b-b2-20k p5b-b2-50k`)
| run | scenario | users | TICKET | UNRESOLVED | oversold | duplicate charges | orphan charges | quota | stuck holds | drift | ticket/order | admitted after sold out | audit exit | checkout p95 ms | error rate | peak in system | gen lag p99 ms |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| p5b-smoke | SMOKE | 5000 | 376 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 329.7 | 0.0000% | 4253 | 1.5 |
| p5b-b0 | B0 | 10000 | 450 | 0 | 17 | 19 | 0 | 0 | n/a | n/a | 0 | n/a | 1 | 57.0 | 0.0421% | 8007 | 18.4 |
| p5b-b1 | B1 | 10000 | 344 | 0 | 0 | 27 | 0 | 0 | 0 | 0 | 0 | n/a | 1 | 322.0 | 0.0542% | 7449 | 25.9 |
| p5b-b2 | B2 | 10000 | 341 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | n/a | 0 | 266.8 | 0.0000% | 7425 | 16.9 |
| p5b-b2-20k | B2 | 20000 | 370 | 4 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | n/a | 1 | 5103.6 | 57.2789% | 14984 | 137.3 |
| p5b-b2-50k | B2 | 50000 | 375 | 4 | 0 | 0 | 0 | 0 | 0 | 0 | 0 | n/a | 1 | 21446.7 | 57.3510% | 33510 | 8650.8 |

Commands:
- `task rush:smoke RUN_ID=p5b-smoke`
- `task rush:b0 RUN_ID=p5b-b0`
- `task rush:b1 RUN_ID=p5b-b1`
- `task rush:b2 RUN_ID=p5b-b2`
- `task rush:b2 USERS=20000 RUN_ID=p5b-b2-20k`
- `task rush:b2 USERS=50000 RUN_ID=p5b-b2-50k`

`n/a` means the check doesn't apply: B0 has no holds and no Redis inventory, and direct mode has no admissions (ADR 0011).

Reading the matrix:
- **B0 (from `audit.json`):**
  - oversold 17 = 8 seats sold to more than one order, plus 9 sales over capacity (509 distinct seat/order sales for 500 seats). That's the LOADTEST §6 formula.
  - duplicate charges 19, with `extra_charges` 19 over 453 refs in the ledger.
  - The 18 pay 503s were all `POST /orders/{id}/pay`, grouped from the api1..3 JSON logs. These are MockPay's injected timeout-after-commit faults. The naive client gives up, the swarm retries, and the retry uses a fresh random key, which charges the customer a second time. The real system's deterministic PSP key prevents exactly this.
  - B0's `seats_sold` (0) and `sold_out_s` (n/a) come from `/stats`, which can't see `naive_sales` (ADR 0011). For B0, the audit's counts are the truth.
- **B1:** the real holds keep oversold at 0, but the naive `/pay` leaves 27 duplicate charges. B1 and B2 sold 447 and 451 seats, not 500. In direct mode almost everyone ends NO_SEAT within about 14 s (drain 13.9 s), before abandoned holds hit their 60 s TTL. Nobody is left to buy the seats those holds release.
- **SMOKE vs SLOs (SPEC §6):**

  | Metric | Measured | SLO | Met? |
  |---|---|---|---|
  | checkout p95 | 329.7 ms | ≤ 250 ms | ❌ missed (bottleneck in §4.4) |
  | status p95 | 3.7 ms | ≤ 50 ms | ✅ |
  | join p95 | 13.3 ms | ≤ 100 ms | ✅ |
  | error rate | 0.0000% | < 0.1% | ✅ |
  | drain | 246.9 s | ≤ 5 min | ✅ |

  Source: `docs/results/p5b/p5b-smoke/report.md`.
- **Audit settle times** (each run's `report.md`): SMOKE 19.5 s, B0 30.2 s, B1 32.2 s, B2 32.6 s. None timed out.

### 4.2 k6 cross-checks
| Run | Command | Result (from `k1.json` / `k2.json`) |
|---|---|---|
| K1 (defined) | `task k6:spike RUN_ID=p5b-k1` | Target 3,000/s; achieved 2,080.7 iterations/s over the 45 s run; 18,839 dropped iterations (1,500-VU cap); 93,660 joins, error rate 0. Join p50 / p95 / p99: 361.9 / **544.8** / 724.7 ms. Session p95: 221.6 ms |
| K1 at swarm rate | `task k6:spike RATE=350 RUN_ID=p5b-k1-350` | 13,124 joins, 0 dropped, error rate 0. Join p50 / p95 / p99: 2.0 / **5.07** / 7.5 ms. Session p95: 3.5 ms |
| K2 | `task k6:checkout STEPS=50,100,200,300 STEP_S=60 RUN_ID=p5b-k2` | See the table below |

| K2 step | iterations | checkouts ok | dropped | checkout p50 / p95 / p99 ms | error rate | sustainable |
|---|---|---|---|---|---|---|
| 50/s | 3000 | 3000 | 0 | 94.2 / **200.3** / 2086.5 | 0 | ✅ |
| 100/s | 5990 | 5990 | 10 | 212.9 / 709.4 / 2206.3 | 0 | ❌ |
| 200/s (the defined K2) | 6774 | 6774 | 5227 | 3058.6 / 5795.9 / 7086.1 | 0 | ❌ |
| 300/s | 6464 | 5935 | 11536 | 2810.6 / 7938.2 / 10669.0 | 8.18% | ❌ |

- **Max sustainable checkouts/s: 50.** The criteria are p95 ≤ 250 ms, errors ≤ 0.1% and no dropped iterations. The true ceiling lies somewhere between the 50 and 100 steps.
- The steps ran back to back on one 50k-seat event, so each step inherits the previous step's backlog.
- p99 sits around 2.1 s even at 50/s. That's MockPay's 2% timeout-after-commit fault (`MOCKPAY_TIMEOUT_AFTER_COMMIT_RATE=0.02`, `MOCKPAY_HANG_MS=3000`) running into the PSP client timeout. It's by design.

**Swarm vs k6 on join p95: not within ±20%.**
- SMOKE swarm join p95 is 13.3 ms. K1 at 350/s gives 5.07 ms; K1 at 3,000/s gives 544.8 ms.
- The swarm records joins from intended start (coordinated-omission safe) on a stack that is also serving status polls (136,375 in SMOKE) and checkouts.
- k6 `http_req_duration` starts at send and measures joins in isolation.
- Proposal for P7: compare like with like, either with a join-only swarm mode or with a K1 that also polls status. I did not tune either side to make them agree.

### 4.3 Largest size this laptop sustains
- **Direct mode (B0/B1/B2): 10k.**
  - 10k ran clean: generator lag p99 16.9–25.9 ms, error rate ≤ 0.0542%, 0 unresolved.
  - 20k is generator-bound by the LOADTEST §3.4 rule (lag p99 137.3 ms > 100 ms), with a 57.28% error rate and 4 unresolved.
  - 50k: lag p99 8,650.8 ms, 151,840 timeouts and 4 unresolved.
  - Even under that overload, oversold, duplicate charges, orphans, drift and stuck holds all stayed 0 at both 20k and 50k.
  - Memory during 50k (sum of `docker stats` samples every 10 s, peak): about 4.9 GiB in total. The swarm took 2.0 GiB and Caddy 1.7 GiB.
- **The waiting-room scenarios (SMOKE/H)** weren't pushed past 5k in this session. The 50k H headline is P7's CI run, per the phase file.

### 4.4 First bottleneck
1. **Direct mode, ≥ 20k users: Postgres-bound replicas, then Caddy ejects every replica.**
   - Evidence from the 20k run, grouping the api1..3 JSON logs:
     - 4,863 × `POST /holds` 503;
     - 4,702 × `idempotency claim: insert key: context canceled`;
     - 238 × `get event: context canceled`.
   - Caddy's log: 49,105 × 503 with `"msg":"no upstreams available"`.
   - Mechanism:
     1. Each replica has `DB_MAX_CONNS=24`.
     2. The hold path's idempotency claim and hold insert queue for connections until requests hit their deadlines.
     3. `/readyz` then misses Caddy's 2 s `health_timeout`, so Caddy marks all three replicas unhealthy.
     4. Every request answers 503 until a replica recovers.
   - This is exactly what the waiting room exists to prevent. Candidates for P7: pool size, a cheaper readiness check under load, and passive vs active health settings.
2. **Checkout under the waiting room (SMOKE): order and pay dominate.**
   - SMOKE p50 / p95 by route: hold 21.2 / 40.4 ms, order 150.7 / 316.9 ms, pay 215.6 / 462.6 ms.
   - Order calls `CreateIntent` and pay calls `Confirm` on MockPay, which adds an injected 20–60 ms each (`MOCKPAY_LATENCY_MS=20-60`). The remainder is Postgres work in those transactions.
   - K2's ceiling of 50/s sustainable (between 50 and 100/s) is consistent with that.
   - No pprof was taken this session. Profiling the order and pay transactions is the first P7 tuning step.

## 5. Deviations and decisions
| What changed vs the spec | Why | ADR |
|---|---|---|
| A lapsed admission lease ends the admission: `ADMISSION_EXPIRED` (403) on holds and orders, checked inside `queue_touch.lua`, fails closed | Owner decision (Step 0) | 0010 addendum part 3 |
| B0 writes to its own `naive_orders`/`naive_sales` (migration 0003), not to `seats`/`tickets` | B0 must be able to oversell without weakening I1 constraints | 0011 |
| Baseline modes skip only the reconciler's PSP-intent pass | Otherwise B1's duplicate charges would be refunded before the audit | 0011 |
| Oversold counts distinct orders per seat; one order's repeated insert is one buyer | A retry by the same order isn't an oversell | 0011 |
| Settle ignores periodic River kinds and dead webhooks, and waits out young intents (orphan age 35 s) | Otherwise settle never ends, or audits before the reconciler may act | 0011 |
| `admitted_after_soldout` is judged from request send time, with a 1 s grace; n/a in direct mode | `/stats` is polled at 1 s | 0011 |
| Auditor exit 3 = the auditor couldn't run | Keeps 2 meaning "settle timed out" only | 0011 |
| `summary.json` gains the audit extras plus a `records` block; null means not applicable | Shows the checks LOADTEST §6 lists beyond §3.5's six | 0011; LOADTEST §3.5 updated |
| K2 runs stepped rates (`STEPS`, `STEP_S`) in one k6 run | To find the max sustainable rate in one fresh stack | — (LOADTEST §5 already says "raise the rate") |

Docs updated:
- API.md: §1.1, §1.2 (baseline routes skip idempotency), §3.3 and the error table
- ARCHITECTURE §5
- DATA-MODEL: `queue_touch`, and 0003 as built
- LOADTEST §3.5, §4 (overlays, B0 `/stats` caveat) and §6
- STACK (repo layout)
- ADR 0010 (addendum part 3) and ADR 0011

## 6. Known issues and debt
- **The local race gate is incomplete.** 11 packages weren't reached under `-race` (§3). They need `task test:race` on a machine with more free memory, or CI.
- **Checkout p95 SLO missed in SMOKE:** 329.7 ms against ≤ 250 ms. K2's max is 50/s sustainable. Profiling order and pay comes first in P7.
- **Direct mode collapses at ≥ 20k** through Caddy's health ejection (§4.4), with 4 unresolved users at 20k and at 50k. Correctness held; availability didn't.
- **The K1 vs swarm ±20% comparison isn't apples to apples** (§4.2). P7 needs a like-for-like method.
- **B2 "higher p95 than SMOKE"** doesn't hold at 10k when checkout p95 is aggregated (§2). It needs per-route comparison at matched load.
- **B0's `/stats` is blind to `naive_sales`,** so its `seats_sold`/`sold_out_s` aren't meaningful.
- **Accepted gap (ADR 0010 part 3):** the admission check and the hold script are two Redis calls. A lease can lapse between them.

## 7. Understand this before moving on
1. **Q:** How do you know the auditor would catch an oversell if one happened?
   **A:** It's shown two ways:
   - B0 ran the naive check-then-insert code under real contention and the auditor counted 17, with exit 1.
   - `internal/audit/audit_integration_test.go:TestAuditCatchesNaiveOversellAndDuplicate` plants one seat sold to two orders, a double capture and an orphan; each is counted.

   The counting is in `internal/audit/checks.go`: `oversold` counts distinct orders per seat, and `money` uses `captured − refunded > 1` per merchant_ref from MockPay's ledger.
2. **Q:** Why did B1 double-charge when its holds were correct?
   **A:** `internal/payments/naive/naive.go:charge` creates a new intent with a random idempotency key and `confirm=true` on every click.
   - When MockPay commits and then times out, the client sees an error and the user retries. That second click is a second charge.
   - The real path uses the deterministic key `order:<id>:attempt:<n>` (`payments.CreateKey`), so a retry replays the same intent.
   - The reconciler, which would refund the extra charge, is off in baseline mode (`orders.Options.NoPSPReconcile`) so the bug stays visible.
3. **Q:** What happens to a user whose admission lease lapses mid-checkout?
   **A:** `queue_touch.lua` answers `LAPSED` and doesn't renew, and `auth.RequireLive` turns that into 403 `ADMISSION_EXPIRED` on `POST /holds` and `POST /orders`. The swarm then releases its hold and rejoins, within the two-rejoin budget (`internal/rush/user.go:run`). If Redis lost the queue (`q:init` missing), the answer is `UNKNOWN` and the signed token decides until the queue is restored (R1).

## 8. Files changed (`git status --short`)
```
Step 0 (admission liveness)
 M internal/redisx/scripts/queue_touch.lua
 M internal/redisx/keys/keys.go
 M internal/redisx/queue_scripts_test.go
 M internal/auth/http.go
 M internal/auth/jwt.go
 M internal/auth/touch_test.go
 M internal/httpx/problem.go
 M internal/httpx/httpx_test.go
 M internal/queue/lease.go
 M internal/queue/admission_lease_test.go
 M internal/inventory/handler.go
 M internal/inventory/handler_integration_test.go
 M internal/orders/handler.go
?? internal/orders/admission_gate_test.go

Auditor and CLI
?? internal/audit/
 M cmd/sluice/main.go
 M cmd/sluice/main_test.go

Baselines
?? db/migrations/0003_naive_baseline.sql
?? db/queries/naive.sql
?? internal/db/gen/naive.sql.go
 M internal/db/gen/models.go
 M internal/db/migrate_test.go
?? internal/inventory/naive/
?? internal/payments/naive/
?? internal/app/baseline.go
?? internal/app/baseline_test.go
 M internal/app/api.go
 M internal/app/orders.go
 M internal/orders/service.go
 M internal/orders/reconcile.go

Swarm
 M internal/rush/metrics.go
 M internal/rush/options.go
 M internal/rush/report.go
 M internal/rush/run.go
 M internal/rush/user.go
?? internal/rush/p5b_test.go

Stack, tasks, k6
 M Taskfile.yml
 M deploy/compose.app.yml
 M deploy/compose.rush.yml
?? loadtest/k6/
?? loadtest/matrix.py

Docs and results
 M docs/API.md
 M docs/ARCHITECTURE.md
 M docs/DATA-MODEL.md
 M docs/LOADTEST.md
 M docs/STACK.md
 M docs/adr/0010-stack-containers-and-swarm-details.md
?? docs/adr/0011-baselines-and-auditor-details.md
?? docs/reports/P5b-rush.md
?? docs/results/
```
Also:
```
 M .gitignore
?? deploy/env/rush.b0.env
?? deploy/env/rush.b1.env
?? deploy/env/rush.b2.env
```
The three overlays matched `.gitignore`'s `deploy/env/*.env` rule. I added the exception `!deploy/env/rush.b[0-2].env`; they contain only `QUEUE_ENFORCED`/`BASELINE`.

## 9. Next phase readiness
- **Run `task test:race`** on a machine with free memory, or let CI do it, before relying on the race gate for the 11 packages not reached.
- **The scenario overlays are now tracked.** `.gitignore` gained `!deploy/env/rush.b[0-2].env`. Without the files, `task rush:b0` and `rush:b1` would silently run the correct system, because the overlay is optional in compose.
- **P7 starts from §4.4:**
  - profile order and pay;
  - the direct-mode pool and Caddy health behaviour;
  - a like-for-like K1 comparison;
  - the 50k H headline on CI.

## 10. Git commands (Sheshakanth runs these)
```bash
git add -A
git status --short
git commit -m "feat(rush): 50k-user swarm, invariant auditor, naive baselines, k6 cross-checks" -m "- Lapsed admission lease ends the admission: 403 ADMISSION_EXPIRED on holds and orders" -m "- sluice audit: settle, nine invariant checks, exit 0/1/2/3, verdict merged into summary.json" -m "- BASELINE naive and holds_only with migration 0003; B0 oversold 17 dup 19, B1 dup 27" -m "- k6 spike and checkout scripts, rush and k6 Taskfile tasks, harness self-tests" -m "- SMOKE and B2 audit clean; ADR 0011; curated results in docs/results/p5b" -m "Phase P5b - report: docs/reports/P5b-rush.md"
```
