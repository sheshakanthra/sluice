# P8: H 50k CI rush failure, diagnosis

**Status:** DIAGNOSIS ONLY. No code, config or spec changed. Nothing committed.
**Run:** GitHub Actions run 45781258193, `ci-45781258193-h-50000`, commit `5fed0c9`, 2026-11-26.
**Runner:** 4 vCPU, 15.3 GB, ubuntu-latest (`runner.md`). The swarm and the whole stack share these 4 cores, and no container has a CPU limit (`deploy/compose*.yml` has none).
**Source of every number:** the artifact in `loadtest/results/ci-45781258193-h-50000/`, read with `cat`, `zcat stack.log.gz | grep/awk`, and a `python -I` pass over `summary.json` and `timeseries.csv`. The commands are shown inline. Anything marked *estimate* or *inference* is derived from those numbers and was not measured directly.

---

## 1. Money and seats: all zero

From `audit.json` (`checks[]`), after settling for 0.26 s (2 polls, no timeout, nothing pending):

| check | count | detail |
|---|---|---|
| oversold | **0** | 500 issued, capacity 500, 0 seats sold more than once |
| duplicate_charges | **0** | 419 merchant_refs in the ledger, 0 extra charges |
| orphan_charges | **0** | |
| quota_violations | 0 | max per user 4 |
| stuck_holds | 0 | |
| inventory_drift | 0 | |
| ticket_order_consistency | 0 | |
| admitted_after_soldout | 0 | 521 admissions |
| unresolved_users | **12967** | the only failing check |

The invariants held under heavy overload. Every reconcile pass reported 0 fixes of any kind. The failure is about **liveness/capacity**, not correctness.

## 2. Exit 201, where the 12,967 users were stuck, and the deadline

### Exit 201
- `sluice` itself only exits with 0, 1, 2 or 3 (`cmd/sluice/main.go`). Here the auditor returned 1 (`audit.json` `exit_code: 1`, from `unresolved_users`), and `runRush` passed that through.
- **201 comes from go-task, not from sluice.** go-task exits 201 ("task run error") when a command inside a task fails and `task` runs without `--exit-code`. The rush step in `.github/workflows/rush.yml` (`task "$TASK_NAME" ...; code=$?`) captures that wrapper code. So 201 means "the swarm+audit command exited non-zero", and the real code was 1.
- Doc drift, not fixed here: the comment in `Taskfile.yml` above `rush:scenario` says "The task's exit code is the swarm/auditor's: 0 pass, 1 ..., 2 ...". That's not what happens without `task --exit-code`.

### Where they were stuck
The unresolved users add up exactly to the three "no-response" anomalies (`summary.json` `anomalies`):

| anomaly | users | meaning (`internal/rush/user.go`) |
|---|---|---|
| `status:no-response` | 12,795 | joined the queue, then a `GET /queue/status` used up its whole retry budget |
| `session:no-response` | 143 | never got a session (`POST /sessions` used up its budget) |
| `join:no-response` | 29 | had a session, never joined the queue |
| **total** | **12,967** | = `outcomes.UNRESOLVED` = audit `unresolved_users` |

- **Still queued (waiting), never admitted.** None of them were holding or paying:
  - The run had 521 admissions in total (`records.admissions`). 465 of them are accounted for by TICKET 395 + PAYMENT_FAILED 7 + ABANDONED 54 + NO_SEAT 9.
  - `stuck_holds = 0`, and `pending_at_end` is all zero.
  - From the server's side, these 12,795 users are still entries in the queue. They gave up without calling `DELETE /queue`.
- **What "budget used up" means:** `sendWith` retries one request up to `maxSendAttempts = 30` times on a timeout, transport error, 5xx or 429. Between tries it backs off 250 ms → 500 ms → 1 s → 2 s, then stays at 2 s. The status timeout is 2 s (`client.go`). So a user goes UNRESOLVED after **30 consecutive failed status attempts, roughly 1 to 2 minutes of uninterrupted failure.**
- **Gaps in the stack's own `/stats` timeline:** 99 failed samples (`generator.stats_failed`), and 294 of the 546 timeseries rows have no queue/inventory columns.

### Deadline: no
- `--max-duration` defaults to 15 min after T0 (`options.go`), which would be T0 + 900 s. The run drained at 516.3 s and the audit finished at 08:23:48Z, well before T0 + 15 min = 08:30:12Z.
- The audit settle didn't time out either (`settle_timed_out: false`).
- These users exhausted their per-request retry budget. The run did not hit a deadline.

### Shape of the run (`timeseries.csv`)
- **Front door choked first.** In-system (users who had joined) only reached 20k at T+29 s, 30k at T+71 s, 40k at T+151 s and 46k at T+239 s, even though all 50k arrivals were scheduled between T−10 s and T+10 s. The cost shows in the attempt counts: session took 332,852 attempts (6.66 per user) and join took 103,474 (2.07 per user).
- **Admission was wasted.**
  - The server's offer cursor only reached 2,383 by the end, and only 521 of those offers were claimed. That leaves about **1,860 offers that expired unclaimed (inference)**: the user holding the ticket was in a status-timeout loop when their turn came.
  - That is why selling 500 seats took 463 s. Sold count by time: 1 at T+25, 217 at T+100, 413 at T+220, 487 at T+380, 500 at T+463.
- **Most of the sale was failing.** In 238 of the 461 one-second rows between T0 and T+460, at least 90% of requests errored. Throughput swung between 246 and 34,153 rps from one second to the next, which looks like convoys forming and stalling.

## 3. Where the CPU went: both sides, and they made each other worse

**What the artifact can and can't tell us:** `ps.txt` is just `docker compose ps`, with no per-container CPU. Nothing in the artifact samples `docker stats`. So the CPU split between the API replicas, Caddy, Postgres and Redis **can't be measured from this run.** The table below is what the artifact does show.

| component | evidence | reading |
|---|---|---|
| **Generator (swarm)** | `generator.cpu_cores_mean` **2.74** (68.5% of 4 cores). `goroutines_max` 131,785. `lag_p99` **3,196.9 ms**, `lag_max` 11,419.6 ms | **Measured: the biggest consumer on the box.** `cpu_cores_max` 12.92 can't happen on 4 cores; it's a `getrusage` / wall-clock sampling artifact from a late sample, so ignore it. The mean of 1 s samples is the number to use. |
| Everything else together (3× api, Caddy, Redis, Postgres, worker, mockpay, Prometheus, Grafana) | 4 − 2.74 = **at most about 1.26 cores on average** (*estimate*) | Starved. |
| API replicas | Caddy's active health check, `GET /readyz` (served from cached state), **timed out at 2 s on all three replicas** at 08:15:11 to 08:15:17Z (right at T0 = 08:15:12Z) and again at 08:17:33Z. The 122 API log lines are almost all `context canceled` inside `queue_join` / `queue_claim` / `queue_resolve`, meaning the client gave up first. MockPay calls also timed out (`psp unavailable after 4 attempts`). | A cached readyz needing more than 2 s means the processes couldn't get CPU time. It doesn't mean slow work. |
| Caddy | **24,632 `no upstreams available`** 503s, all in minute 08:15 (the T0 window, while all three replicas were marked down). Those 503s were on sessions (15,754), status (7,609) and join (1,872). There were also 449 `server closed idle connection` and 158 `EOF`. | Caddy ejected the starved replicas at T0, which turned the herd into 503s. It recovered at 08:15:19Z. The swarm only counted 6,090 5xx. *Unverified explanation:* the rest arrived after the client's 2 s timeout and were counted as timeouts. |
| Postgres | No errors. One timed checkpoint (`write=133.4 s` is checkpoint spreading, `sync=0.009 s`). The workload is tiny (500 seats, 419 ledger refs). | Not the bottleneck. |
| Redis | No warnings after startup. | No evidence it was the bottleneck. The `context canceled` lines were canceled *callers*, not slow scripts. Unproven without `INFO`/slowlog. |

**Verdict:** the box ran out of CPU. The **generator was the largest consumer (measured 2.74 of 4 cores)**, and the stack couldn't serve the offered load on the roughly 1.3 cores left over. Two feedback loops then made it worse:

1. **The generator's lag is bigger than its own timeout.** Lag p99 3,197 ms is more than the 2,000 ms status timeout. Some of the "timeouts" are self-inflicted: the goroutine wasn't scheduled to read the response in time. The artifact can't split how many.
2. **Timeouts speed up polling (retry storm).**
   - On the healthy path, a waiting user polls at the server's `retry_after_ms`, which is clamp(ETA/4, 1 s, 15 s) with ±20% jitter (`internal/queue/service.go` `waitingRetry`). With 46k users ahead and an admission rate in the single digits, the ETA is huge, so that's 15 s for almost everyone.
   - A *timed-out* poll ignores that and retries after 250 ms to 2 s (`retryable` in `client.go`). So the more overloaded the system is, the more often each user polls.
   - *Inference from Go net/http behaviour, not measured:* each timeout on an HTTP/1.1 `dedicated` connection also closes that TCP connection, and Caddy cancels its upstream request. The retry has to reconnect both hops.
3. **The T0 herd.**
   - Before T0, the retry delay is `prequeueRetry` = clamp(time_to_T0 / 2, 0.5 s, 5 s), with no server-side jitter. 70% of users (35k) arrive between T−10 s and T0, so their polls bunch up at a 0.5 s cadence right at T0.
   - At T0 each of them runs a `queue_resolve` Lua call.
   - Errors begin at T−4 s (149), and by T0 they are 9,772 of 10,239 rps. That is when `/readyz` failed everywhere.

So this is **neither a pure server failure nor pure generator starvation.** It's a co-located capacity failure: the generator takes most of the CPU, and the timeout-driven retries multiply the load on whatever CPU is left. The server never produced a wrong answer.

## 4. Request mix

`python -I` over `summary.json` `latency_ms.*.n` (each attempt, retries included, is recorded):

| route | attempts | share of 2,631,617 | per user |
|---|---|---|---|
| **status** | **2,192,563** | **83.3%** | **43.85** |
| session | 332,852 | 12.6% | 6.66 |
| join | 103,474 | 3.9% | 2.07 |
| hold + order + pay (`checkout`) | 2,314 | 0.09% | 0.05 |
| all others | 414 | 0.02% | |

- **Status polls per user: 43.85 here, against 27.47 in SMOKE 5k on the same runner** (137,336 / 5,000, from run 45779952735 as you reported it; I couldn't download that artifact in this session). That's 1.6× per user at 10× the users, so about 16× the status volume.
- Timeouts: 2,192,874 = 83.3% of all attempts, and the error rate was 83.69%. The recorder doesn't split timeouts by route. Status p50 is 2,228 ms, above the 2 s timeout, so **more than half of all status attempts went past their timeout budget, counted from their intended start.**
- **Share of CPU (*estimate*):**
  - Generator CPU per attempt in this run: 2.74 cores × 526 s / 2.63M ≈ **0.55 ms CPU/request**. Local SMOKE runs on a 16-core machine give 0.63 and 0.82 ms (`p7b-head-3`, `p7b-head-1`), so the figure is consistent.
  - Generator cost is roughly the same per request, so status accounts for about **83% of the generator's CPU (≈ 2.3 cores)**.
  - On the server, a status poll for a waiting user is answered from the snapshot without Redis, so it costs less than a checkout. Every poll still passes through Caddy, though. Status's share of server CPU can't be measured from this artifact.

## 5. Options, ranked by honesty then effort

What "fits" means:
- The SUT is CPU-starved from about 5k attempts/s on average with the generator at 2.74 cores.
- Generator cost is about 0.55 ms CPU per request.
- To leave the stack about 2.5 to 3 cores, the generator has to stay around 1 to 1.5 cores. That's about **2k rps on average over about 500 s, or roughly 1M requests in total**, with a T0 burst the stack can absorb.

These are back-of-envelope figures. Each option needs a CI run to confirm.

| # | Option | Honesty | Effort | Request estimate at 50k | Fits on 4 cores? |
|---|---|---|---|---|---|
| **1** | **Claim a lower N honestly.** Step N on the 4-core runner (for example 10k, 20k, 30k) and report the largest clean N on CI. Report this 50k run as it is: "0 oversold / 0 duplicate / 0 orphan under overload; 12,967 (25.9%) unresolved". | Highest | Lowest (CI time only) | n/a | 5k passes today (fact). Larger N not yet measured. |
| **2** | **Server-paced, position-aware polling (a real production pattern).** (a) The swarm, and the web client if it behaves the same way, treats the last `retry_after_ms` as the *floor* for retrying a failed or timed-out poll, with jittered exponential backoff on top. No retry at 2 s or faster. (b) The server lengthens the interval by outlook, for example UNLIKELY users (far past remaining stock × overadmit) at 30 to 60 s instead of 15 s. (c) The server adds jitter to pre-queue answers near T0, and spreads `queue_resolve` over a few seconds instead of a 0.5 s lockstep. | High: it changes how the system behaves for real users, not how it's measured. It needs an ADR, an update to API.md §3.3, and owner approval (API contract plus architecture). | Medium | (a) alone: about 46k × 463 s / 15 s ≈ 1.4M status + about 0.2M pre-queue + about 0.1M session/join ≈ **1.7M** (generator ≈ 1.8 cores). (a+b+c): with a 60 s cap, about 46k × 463 / 60 ≈ 0.36M + 0.2M + 0.1M ≈ **0.65M** (generator ≈ 0.7 cores). A healthy server also sells faster, which shrinks these further. | (a) alone: **unlikely.** (a+b+c): **plausible.** The remaining risk is the T0 join+session burst (35k in 10 s). |
| **3** | **Raise the status timeout from 2 s to 5 s,** matching the other calls. Latency is still recorded from the intended start (coordinated-omission safe), so nothing gets hidden. | High if written up in LOADTEST §3.3 + an ADR | Low | It removes the false timeouts caused by generator lag (lag p99 3.2 s > 2 s) and the reconnects that follow. It doesn't reduce baseline demand. | **No on its own.** It helps alongside #2. |
| **4** | **Partition the CPU and measure per container.** Pin the swarm to 1 core with `cpuset` or `cpus` and give the stack 3. Sample `docker stats` every second into the artifact. | High (it makes the generator-bound condition explicit, and it would have answered Q3 here) | Low | No change by itself | **No on its own.** A 1-core generator can't produce today's demand. Combined with #2 it gives clean attribution. Worth doing whatever else is chosen. |
| 5 | **Cheaper swarm per user:** `--conn pooled`, which avoids 50k sockets and per-timeout reconnect churn and roughly halves goroutines (*inference*). Plus smaller JSON decodes. | Medium: H is specified as `dedicated` (one connection per user, like a browser tab). Pooled is less realistic and must be disclosed (ADR + LOADTEST change). | Low | Same request count, lower CPU per request by an unknown amount (must be measured) | Unknown. Not enough on its own at 2.6M requests. |
| 6 | **Timing changes to the scenario:** widen the arrival window beyond −10 s..+10 s, lower the pre-queue fraction, or open later. | Low: it weakens what "rush" means. Acceptable only as a separately named scenario, never as "H 50k". | Low | Spreads the T0 burst; total requests mostly unchanged | Might pass, but it wouldn't be the H claim. **Not recommended for H.** |

**Recommendation:**
- Do **#4** (measurement) and **#1** (an honest current claim) now. Both are cheap and neither depends on anything else.
- Then propose **#2 + #3** to Sheshakanth as an architecture/API change with an ADR. That's the only path where 50k on 4 cores looks plausible without diluting the test.
- Avoid #6 for the headline.

## Assumptions and open items
- **Exit 201:** the go-task meaning comes from go-task's documented exit codes. It isn't repeated anywhere in this repo.
- **Generator CPU mean:** treated as representative even though 99 of the /stats samples failed. CPU is read in the same sampling loop, so some seconds may be missing.
- **Caddy 503s vs swarm 5xx:** Caddy logged 24,632 503s; the swarm counted 6,090 5xx. The difference is unexplained. The most likely cause (not verified) is that the 503s arrived after the client's timeout.
- **"About 1,860 offers expired unclaimed":** cursor 2,383 − 521 admissions. That assumes the cursor counts offers made.
- **Web client:** not checked here for the same timeout retry behaviour as the swarm. Check it before #2(a).
- **SMOKE artifact:** run 45779952735 was not available locally. Its numbers above are as reported by Sheshakanth.
