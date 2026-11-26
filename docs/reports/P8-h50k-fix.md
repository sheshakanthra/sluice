# P8: H 50k fix session (A–G): Report

| Field | Value |
|---|---|
| Status | DONE (code, tests, docs). The H 50k claim is **unverified** until the CI runs in §9 |
| Date (IST) | 2026-11-26 |
| Time spent vs timebox | one session; no phase timebox (approved fix list) |
| Commit base | `5fed0c9` (`git rev-parse --short HEAD`) |
| Input | `docs/reports/P8-h50k-diagnosis.md` (run `45781258193`); option numbers below refer to it |

Not changed, as instructed: the arrival window (`pre=0.7:-10s..0s,post=0.3:0s..10s`), the connection mode for H (`dedicated`), the invariants I1–I8/R1, the CI gates (`loadtest/ci/verdict.py`), every threshold, and the 2 s status / 5 s call timeouts. Diagnosis option #3 (a longer status timeout) was **not** applied. C implements option #2(b) and #2(c); A implements #2(a).

## 1. What was built, and expected impact per fix

The impact figures are **estimates** from the diagnosis's own model (0.55 ms generator CPU per request, about 46k users waiting about 463 s in the failed run). They are not measurements, and only the CI H run can confirm them.

### A. Client backoff (swarm and web)
- **Swarm** (`internal/rush/client.go` `backoff`/`retryable`):
  - A failed attempt (timeout, transport, 5xx, 429) waits `max(floor, U(0, min(30 s, 500 ms × 2^attempt)))`. **The cap is 30 s.**
  - The floor is the server's last `retry_after` for status (`req.floor`, set in `user.status`), or `Retry-After`.
  - `REQUEST_IN_PROGRESS` keeps `max(Retry-After, 500 ms)`.
  - The budget is still 30 attempts.
- **Web** (`web/lib/backoff.ts`, `web/lib/retry.ts`):
  - It uses the same constants.
  - The status poll's error path (`app/queue/page.tsx`) is floored at `max(Retry-After, last retry_after_ms)`.
  - `POST /sessions` (`lib/session.ts`) and `POST /queue` (`lib/queue.ts`) retry transient failures up to 5 times. Join is idempotent, and a repeated rejoin is honoured only after OFFER_EXPIRED (`queue_join.lua`), so a retry can't spend a second rejoin.
  - The default query `retryDelay` (`components/providers.tsx`) uses the same backoff.
- **Expected impact:**
  - It removes the retry storm. In the failed run, a timed-out status poll retried after 0.25–2 s instead of about 15 s.
  - Diagnosis estimate: about 2.19M status attempts against about 1.42M at the healthy cadence (46k × 463 s / 15 s). So **about 0.77M attempts (about 35% of status) came from failures**, and A removes most of them.
  - On its own, A was judged "unlikely" to fit 4 cores (diagnosis §5, about 1.7M requests).

### B. Jittered pre-queue (and fixed intervals)
- **Server** (`internal/queue/service.go` `prequeueRetry`):
  - More than 5 s before T0: `clamp(time_to_T0/2, 0.5 s, 5 s) × U(0.8, 1.2)`.
  - Within 5 s of T0: `time_to_T0 + U(0, min(5 s, OFFER_CLAIM_TTL/4))`, so there is one poll in the last 5 s and it lands at a random moment in [T0, T0 + 5 s].
  - After T0, before the freeze: 0.5–1.5 s.
- **Clients:**
  - A good poll waits `retry_after × U(1, 1.1)`, never sooner. This was ±20%, which could undercut the server.
  - The swarm's `/confirm` poll is ±20%. The web seat map is 1 s ± 20%, and the admitted-user watch is ×U(1, 1.1).
- **Expected impact:**
  - Before, about 35k pre-queue users polled every 0.5 s in the last seconds before T0, about 70k polls/s at the halving floor (estimate), and errors started at T−4 s.
  - Now each pre-queue user sends at most one poll in the last 5 s, and about 35k `queue_resolve` calls are spread over 5 s, about 7k/s, instead of landing in one 0.5 s step.

### C. Position-aware `retry_after` (option #2(b))
- **Formula:** `min(clamp(eta/4, 1 s, 60 s) × U(0.8, 1.2), safeWait(position))`, with `safeWait = ((ceil(pos/QUEUE_MAX_ADMIT_PER_TICK) − 1) × QUEUE_TICK + OFFER_CLAIM_TTL/2) / 1.1`.
- **Wiring:** `queue.Options` gains `OfferTTL`, `MaxAdmitPerTick` and `Tick`, set from existing env vars (`internal/app/queue.go`). `NewService` refuses zeros.
- **The safety property** "a client that obeys it never misses its offer" is tested over a grid (`TestWaitingRetryNeverSkipsAnOffer`). That test caught a real bug in my first formula, which didn't leave room for the client's +10% at a 2 s TTL; it's fixed with the `/ 1.1`.
- **Rush config (30 s TTL, 200/tick, 100 ms):**
  - position 46,000 → 34.5 s (was 15 s)
  - position 2,000 → 14.5 s
  - front → 1 s
- **Expected impact (A+B+C):**
  - About 46k × 463 s / about 30 s (average, estimate) ≈ **0.7M status**, plus about 0.2M pre-queue and about 0.1M session/join ≈ **1.0M requests**, which is about 1.0 generator core.
  - That's against 2.63M in the failed run, and it's around the diagnosis's fit line (about 1M).
  - If the stack stays healthy, the sale also finishes sooner than 463 s, which lowers it further.
  - The residual risk is still the T0 session+join burst (35k in 10 s), which none of A–G touch beyond backoff.

### D. Caddy: no fleet-wide ejection (`deploy/caddy/Caddyfile`, ADR 0013 addendum)
- **Health check:** `health_interval 3s`, `health_timeout 5s` (was 2 s), `health_fails 5` (was 3, so about 15 s of consecutive failures before ejection).
- **Panic fallback:** when Caddy has no healthy upstream for `lb_try_duration`, `handle_errors 503` on `/api/*` sends the request to api1..3 anyway, tagged `X-Upstream-Fallback: all-unhealthy`.
- **Verified locally** with `caddy:2.11.9` and two stand-in upstreams (scratchpad config, not committed):
  - Health URI always failing: `curl -X POST -d hello-body /api/x` → `HTTP/1.1 200`, `X-Upstream-Fallback: all-unhealthy`, body `upstream POST hello-body`. The body survives the fallback.
  - Healthy upstreams, upstream answers its own 503: `/api/busy` → `HTTP/1.1 503`, body `not ready`, **no** fallback header. Its own `OVERLOADED` passes through.
  - Both upstreams stopped: `503` after `4.022191` s (`curl -w "%{http_code} %{time_total}"`).
  - `caddy validate` on the real Caddyfile: `Valid configuration`. `caddy fmt --diff`: no changes.
- **Expected impact:** in the failed run, 24,632 Caddy-generated 503s came from about 6 s with every replica ejected. Those requests now reach a (slow) replica instead. This doesn't create CPU; it stops slow probes from turning into an outage.

### E. Timeout from send, latency from intended start (`internal/rush/client.go` `sendDeadline`, LOADTEST §3.4)
- The per-call timeout is armed at `httptrace.GotConn`.
- A hard stop at dial (5 s) + timeout bounds a connection that never comes.
- Latency is still recorded from `intended`.
- **Tests:**
  - `TestTimeoutRunsFromSendNotFromDial`: a 600 ms dial with a 300 ms budget → 200 OK, latency p50 ≥ 600 ms. The old `WithTimeout` would have timed out.
  - `TestTimeoutStillFiresOnASlowServer`.
  - `TestSendDeadlineConcurrent` (64 goroutines, `-race`).
- **Expected impact (honest scope):** it removes false timeouts caused by reconnect/dial time after a previous timeout closed the connection. It does **not** remove timeouts caused by the generator not being scheduled to read a response that already arrived (lag p99 was 3.2 s). A and C reduce that lag by cutting requests, and E doesn't. The share can't be quantified from the failed artifact.

### F. Per-container CPU on every CI rush, optional swarm pinning
- **Sampling** (`.github/workflows/rush.yml`):
  - A background `loadtest/ci/dockerstats.sh` writes `docker-stats.csv` from just before the rush until an always-run stop step.
  - `loadtest/ci/cpu_summary.py` writes `cpu.md` (mean/p95/max cores per container while the swarm ran), which goes into the job summary and the artifact alongside `docker-stats.csv.gz`.
- **Pinning:**
  - The new input `swarm_cpuset` (validated, blank by default) → `RUSH_CPUSET` → `cpuset` on the swarm container only.
  - It's disclosed in `summary.json` `env.swarm_cpuset`, as a callout in `report.md` (`internal/rush/report.go`), and in `runner.md`.
  - **Not enabled.** There's no measurement yet showing it helps, and the diagnosis estimated that a 1-core swarm couldn't produce the H demand. Decide after the first CI H run's `cpu.md`.
  - Empty cpuset = unrestricted, checked on Docker 28.3.3: `cpuset.cpus.effective` read `0-15` unpinned and `0` with `RUSH_CPUSET=0`.

### G. Taskfile exit-code comment
- The comment above `rush:scenario` and the `audit` description now say that plain `task` exits 201 on any failing command, and that `task -x` passes the real code through.
- Verified with go-task 3.54.0 on a one-line Taskfile (`exit 1`): `plain: 201`, `-x: 1`.
- CI behaviour is unchanged.

## 2. Acceptance checklist (from the session brief)
- [x] Unit tests for backoff/jitter/retry_after: Go `TestBackoff*`, `TestRetryableHonoursServerPacing`, `TestStretchNeverSooner`, `TestWaitingRetry*` (3), `TestPrequeueRetry*` (2), `TestNewServiceRequiresPacing`. Web `lib/backoff.test.mjs` → `ℹ pass 5 ℹ fail 0` (`pnpm --dir web test`, now also in `ci.yml` and `task web:test`).
- [x] `task lint` → `0 issues.`
- [x] Race tests on the touched packages: `CGO_ENABLED=1 go test -race -p 2 -timeout 20m -count=1 ./internal/queue/... ./internal/rush/... ./internal/app/...` → `ok internal/queue 84.496s`, `ok internal/rush 8.050s`, `ok internal/app 12.754s`. The full `task test:race` (every package) was **not** run this session.
- [x] Web: `pnpm lint` clean, `pnpm typecheck` clean, `pnpm format:check` → `All matched files use Prettier code style!`
- [x] Workflows: `rhysd/actionlint:1.7.7 -verbose` → `Found 0 errors in 2 files`.
- [x] Local SMOKE, correctness: `task rush:smoke RUN_ID=p8fix-smoke-2` → `task exit 0`, audit passed, every invariant 0, unresolved 0 (§4).
- [x] Docs + ADR: API.md §3.3, ARCHITECTURE §4/§5, LOADTEST §3.2–3.4/§7, DESIGN-BRIEF §7, results/README, ADR 0017 (new), ADR 0013 addendum, ADR 0015 addendum 2.
- [ ] H 50000 passes on CI: not run here (needs GitHub Actions; §9).

## 3. Tests
| Suite | Count | Result | Notes |
|---|---|---|---|
| unit, new (Go) | 12 test funcs (`internal/queue/retry_internal_test.go` 5, `internal/rush/backoff_test.go` 7) | pass | table-driven |
| unit, new (web) | 5 | pass | `node --test`, Node 24.21.0 type stripping |
| integration (testcontainers) | queue, app packages | pass | inside the race run above |
| race (`-race`) | queue, rush, app | pass | local, gcc available |
| e2e (Playwright) | — | not run | no UI markup changed. Only poll intervals and retries changed, and `task e2e` needs the full stack |

Coverage wasn't measured this session. I stopped the run so it wouldn't compete with the SMOKE for CPU.

## 4. Numbers: local SMOKE (laptop, correctness only)
**These are laptop numbers, not a claim.** Docker Desktop, 16 logical CPUs, 9.7 GB for the VM (`env` in `summary.json`), with the docker-stats sampler running alongside. The run is generator-bound (lag p99 472.6 ms > 100 ms), so its latencies and error rate measure this laptop, not SLUICE.

Command: `task rush:smoke RUN_ID=p8fix-smoke-2` → `task exit 0`. Values below are from `loadtest/results/p8fix-smoke-2/summary.json` (read with `python -I`):

| field | value |
|---|---|
| audit | oversold 0, duplicate_charges 0, orphan_charges 0, quota_violations 0, stuck_holds 0, inventory_drift 0, ticket_order_consistency 0, admitted_after_soldout 0, **unresolved_users 0**, settle 23.1 s, passed true, exit_code 0 |
| outcomes | TICKET 377, SOLD_OUT 4184, NO_SEAT 395, PAYMENT_FAILED 4, ABANDONED 40, UNRESOLVED 0, seats_sold 500 |
| requests | 98,053; status n 70,733 (**14.15 per user**); session n 16,889; join n 8,031 |
| errors | rate 0.1988, 5xx 2,029, timeouts 17,461 |
| timing | first sale 25.5 s, sold out 162 s, drain 173.8 s |
| generator | cpu_cores_mean 0.65, goroutines_max 14,438, lag p99 472.6 ms, generator-bound true |

The first attempt (`p8fix-smoke-1`) never started the swarm: `up --wait` failed with `container sluice-grafana-1 is unhealthy`. Grafana was still running its first-boot migrations while a coverage test run I had started was competing for CPU. I stopped that run and re-ran; no code changed in between.

**Open item: the 19.9% error rate.**
- Earlier laptop SMOKEs at commit `9a8cadb` had error rates of 0.0050 (`p7b-head-1`) and 0 (`p7b-head-3`), with status at 22.77 and 20.28 per user.
- Here status per user is lower (14.15, as A–C intend), but the errors are much higher. They are concentrated between T−4 s and T+25 s (`timeseries.csv`); after T+26 s the error column is 0.
- In that window, the API logged `rate limit check failed open: context deadline exceeded` (Redis calls missing deadlines) while Redis averaged 0.16 cores. `cpu.md` from the local sampler shows Caddy as the top container (mean 1.16, p95 5.24, max 10.29 cores) and a sample total above 16 cores. The max/total figures are `docker stats` sampling artifacts on Docker Desktop. That points to a host/VM-wide stall, not to one component.
- **I could not attribute it.** A same-day baseline of `5fed0c9` on this laptop wasn't run. Two hypotheses are untested: the cost of `docker stats --no-stream` on Docker Desktop, and the Caddy changes (D).
- **The CI SMOKE (§9) decides it:** compare its error rate and `cpu.md` against SMOKE run `45779952735`. If the CI SMOKE error rate is clearly worse than that run, stop and investigate before running H.

## 5. Deviations and decisions
| What changed vs the spec | Why | ADR |
|---|---|---|
| WAITING `retry_after` cap 15 s → position-aware, ≤ 60 s, offer-safe bound | C | 0017 |
| PREQUEUE `retry_after` jittered and spread over [T0, T0+5 s] | B | 0017 |
| Client poll jitter ±20% → ×U(1, 1.1) (never sooner than the server asked) | A/B: the server's bound needs to know the client's maximum stretch | 0017 |
| Failure backoff 250 ms→2 s fixed → full jitter, cap 30 s, floored at server pacing | A | 0017 |
| Swarm timeout clock starts at `GotConn` | E | 0017 |
| Caddy health 2 s/3 fails → 5 s/5 fails at 3 s interval; panic fallback | D | 0013 addendum |
| CI samples docker stats; optional `swarm_cpuset` | F | 0015 addendum 2 |

## 6. Known issues and debt
- The §4 laptop error rate is unexplained (see above).
- The swarm's 30-attempt budget now spans minutes. A far-back user whose status keeps failing (floor about 34 s) can outlast `--max-duration` and end UNRESOLVED by deadline rather than by budget. That's intentional, documented in ADR 0017.
- The web client and the swarm duplicate the backoff constants (Go and TS). ADR 0017 says to change both together.
- `cpu.md` totals from `docker stats` can exceed the core count because of sampling skew. Read the means, not the maxes.
- The T0 session+join burst (35k arrivals in T−10..0 s) is unchanged by design. If H still fails, look at that next; the cheapest knob is session/join cost, not the arrival window.

## 7. Understand this before moving on
1. **Q:** Why can a server tell a user to wait 34 s without risking their offer?
   **A:** `internal/queue/service.go:safeWait`. The cursor advances at most `QUEUE_MAX_ADMIT_PER_TICK` per `QUEUE_TICK`, so position p can't be offered sooner than `ceil(p/perTick) − 1` ticks from now, and the offer then lives `OFFER_CLAIM_TTL`. Keeping half the TTL as margin and dividing by the client's maximum stretch (1.1) guarantees the next poll lands inside the window. `TestWaitingRetryNeverSkipsAnOffer` checks this over a grid.
2. **Q:** Why floor backoff at `retry_after` instead of just exponential?
   **A:** `internal/rush/client.go:retryable`. Without the floor, a failure makes a client poll *faster* than when healthy (that was the retry storm in the diagnosis). With it, overload can only slow each client down. Full jitter stops failed clients from re-synchronising.
3. **Q:** Doesn't starting the timeout at `GotConn` hide slowness?
   **A:** No. `client.do` still records latency from `q.intended`, so dial and scheduling delay still count (coordinated-omission safe). Only the "timed out?" decision moved, so the swarm doesn't blame the server for its own reconnects.

## 8. Files changed (`git status --short`)
- **Server:** `internal/queue/service.go`, `internal/app/queue.go`, `internal/queue/fixture_test.go`, `internal/queue/retry_internal_test.go` (new)
- **Swarm:** `internal/rush/client.go`, `user.go`, `scenario.go`, `options.go`, `report.go`, `rush_test.go`, `backoff_test.go` (new)
- **Web:** `web/lib/backoff.ts` (new), `web/lib/retry.ts` (new), `web/lib/backoff.test.mjs` (new), `web/lib/session.ts`, `web/lib/queue.ts`, `web/components/providers.tsx`, `web/app/queue/page.tsx`, `web/app/book/page.tsx`, `web/package.json`
- **Deploy/CI:** `deploy/caddy/Caddyfile`, `deploy/compose.rush.yml`, `.github/workflows/rush.yml`, `.github/workflows/ci.yml`, `Taskfile.yml`, `loadtest/ci/dockerstats.sh` (new), `loadtest/ci/cpu_summary.py` (new)
- **Docs:** `docs/API.md`, `docs/ARCHITECTURE.md`, `docs/LOADTEST.md`, `docs/DESIGN-BRIEF.md`, `docs/results/README.md`, `docs/adr/0013-...md`, `docs/adr/0015-...md`, `docs/adr/0017-poll-pacing-and-client-backoff.md` (new), `docs/reports/P8-h50k-diagnosis.md` (new, from the previous session), `docs/reports/P8-h50k-fix.md` (this file)

## 9. CI steps (Sheshakanth, after pushing)
1. **Actions → rush → Run workflow:** scenario `SMOKE`, users blank, `swarm_cpuset` blank.
   - Pass = the verdict is green.
   - Compare error rate and status-per-user with run `45779952735` (27.47 status/user).
   - Check that `cpu.md` is in the summary.
   - If the error rate is clearly worse than `45779952735`, stop here and send me the artifact.
2. **Then:** scenario `H`, users blank (50000), `swarm_cpuset` blank.
   - Pass = the verdict is green (all invariants 0, unresolved 0).
   - Whatever the result, the artifact's `cpu.md` now answers the swarm-vs-stack CPU split that the diagnosis couldn't.
3. **Only if H fails:** decide pinning from step 2's `cpu.md` (for example `swarm_cpuset=0-1` if the swarm is still above about 2 cores). Every pinned run is disclosed in `report.md`.

## 10. Git commands
See the chat message for this session (PowerShell-safe block).
