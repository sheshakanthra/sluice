# P8: Swarm efficiency (H 50k generator-bound): Report

| Field | Value |
|---|---|
| Status | DONE (code, tests, docs, local runs). Whether H 50000 is still generator-bound on 4 cores is **unverified** until the CI run in §9 |
| Date (IST) | 2026-11-27 |
| Time spent vs timebox | one session; no phase timebox (approved brief) |
| Commit base | `175c887` |
| Input | CI H 50000 run `45930086030` and SMOKE run `45929043006` (both on `175c887`), as summarised in the session brief. I could not download either artifact: `gh run download` is not allowed in this session. The CI facts below are the brief's |

Not changed, as instructed:
- what the server sees: one dedicated keep-alive connection per user, the request pattern and pacing, the arrival curve, the 2 s / 5 s timeouts and the 30-attempt budget;
- the CI gates, every threshold, and invariants I1–I8/R1.

The only server change is the admission-token fix in §1.E (ADR 0018), which the brief asked for.

## 1. What was built

### A. Swarm profiles on every rush (brief item 1)
- **`--pprof`** (`internal/rush/profile.go`, `options.go`) writes the swarm's own profiles into its result directory:
  - `cpu.pprof` covers the whole run;
  - `heap.pprof` is rewritten each time the live heap passes its previous peak by 10%.
- **Taskfile:**
  - `rush:scenario` now always passes `--pprof`, so every `task rush*` gets it, and CI runs those tasks.
  - New task `task rush:profile USERS=15000`: the H mix on a fresh stack with dedicated connections, then the audit, then `go tool pprof -top` of the swarm.
- **CI** (`.github/workflows/rush.yml`):
  - The profiles are already inside the uploaded `loadtest/results/<run_id>` artifact.
  - The collect step writes `pprof-top.txt`, and the job summary shows it in a collapsed block.
  - `rhysd/actionlint:1.7.7 -verbose` → `Found 0 errors in 2 files`.
- **`summary.json` `generator`** gains the whole-run cost, from the first user to the last:
  - `cpu_seconds` (getrusage), `cpu_cores_run_mean`, `cpu_ms_per_1k_requests`;
  - `gc_cycles`, `gc_cpu_seconds` (runtime/metrics), `heap_live_peak_mb`, `pprof`, `cpu_samples_invalid`.
  - `report.md` gets a second self-check table.

### B. Dedicated connections without net/http's per-connection machinery (brief item 2, ADR 0019)
- **Where the CPU went:** the before-profile (§4.1) put about 36% of samples under net/http's `persistConn.readLoop`/`writeLoop`, 14% under dialling and 17% in the scheduler. GC used 59.17 CPU-s, the heap was mostly per-connection `bufio` buffers, and there were about 3 goroutines per waiting user.
- **New: `internal/rush/conn.go` `userConn`**, a minimal HTTP/1.1 client over the user's one keep-alive TCP connection, driven by the user's own goroutine:
  - It writes the request bytes and reads the response itself: Content-Length, chunked with trailers, `Connection: close`, HTTP/1.0, 1xx, 204/304.
  - The timeout is a socket deadline set once the connection is dialled or reused, so it still runs from the send (LOADTEST §3.4).
  - The 4 KiB read buffer and the request buffer come from pools.
  - `context.AfterFunc` closes the connection when the run ends, which unblocks any request in flight.
  - It retries a dead reused keep-alive connection once, under net/http's own rule (GET/HEAD/OPTIONS/TRACE or an Idempotency-Key).
- **`client.go`:** dedicated mode uses `userConn`. Pooled mode keeps net/http (`httpDo`, unchanged logic).
- **`run.go`:** `clientFor`/`twinClient` take the run context, and the user's connection is closed when the user is done, as `CloseIdleConnections` did before.

The remaining suspects from the brief, checked against the after-profile (§4.1):
- **Per-user timers:** gone from the request path. The socket deadline is a netpoll timer, and `sleepUntil` timers are cheap since Go 1.23.
- **JSON decoding:** `encoding/json/v2.makeStructArshaler` is 2.6% cumulative.
- **Histogram locking:** below the profile's node cutoff. Not changed.
- **What's left** is kernel time for socket write/read/connect/epoll (`Syscall6` 47.8% flat). It scales with requests and connections, which the brief fixes. See §6.

### C. Server-facing behaviour unchanged (brief item 3)
- `TestUserConnWireFormat` sends every request shape the swarm makes (13 cases, including `?rejoin=true`, bodies and the IK) through the old net/http client and through `userConn`. It captures the raw bytes at a TCP listener and requires them to be **byte-identical** (Host apart), with exactly 1 connection each.
- SMOKE before/after (§4.3): `loadtest/ci/shape_compare.py` (new) compares requests per route per user, outcomes and the rps shape.

### D. CPU self-check bug (brief item 4)
- **Cause:** `cpuMeter.read` called `processCPU()` and `time.Now()` one after the other. A goroutine preempted between the two (seconds in a starved run) pairs a CPU reading with a much later wall time. The next interval then counts the CPU of the stall against a short wall span, which is how 12.20 "cores" showed on 4 cores. The earlier H run `45781258193` (on disk) shows the same: `cpu_cores_max` 12.92.
- **Fix** (`profile.go` `cpuMeter`):
  - Each read brackets the CPU read between two wall-clock reads and retries a bracket wider than 20 ms up to 5 times.
  - The CPU value is stamped at the bracket midpoint.
  - A sample that can't be bracketed is invalid and left out of max and mean (`cpu_samples_invalid`).
- **Test:** `TestCPUMeterIgnoresDeschedulingBetweenReads` reproduces the bug with an injected 2.5 s stall: the old logic reads **> 10 cores** for a 4-core process, and the new meter stays within ±2% of 4.0. `TestCPUMeterMarksAnUnbracketableReadInvalid` covers the invalid case.
- The whole-run `cpu_cores_run_mean` doesn't depend on 1 s samples at all.

### E. The 14 × 401 TOKEN_EXPIRED (brief item 5, ADR 0018): a real bug, fixed
- **Yes, a token TTL was shorter than the worst legitimate wait.** The admission token's `exp` is the hard cap (claim + `ADMISSION_TTL`, 3 min in rush). A visible seat map keeps the lease up to the cap, and a hold taken just before the cap lives `HOLD_TTL` (1 min).
  - The order for that hold, and release or leave, all carry the admission token, so they got 401 after the cap.
  - The web client then said "Nothing was held or charged" while the seats stayed held.
  - It also contradicted API.md, which said past-cap `POST /holds`/`POST /orders` give 403 `ADMISSION_EXPIRED`.
  - The earlier H run on disk shows the same pattern: `hold:401 TOKEN_EXPIRED 3`, `leave:… 8`, `order:… 5`, `release:… 1` (`loadtest/results/ci-45781258193-h-50000/summary.json` `anomalies`). The swarm's starvation (lag p99 seconds, 30-attempt retries) is what stretched those journeys past the cap; a slow network does the same to a person.
- **Fix:** a spend grace equal to `HOLD_TTL`.
  - `auth.Keyring.WithSpendGrace`, wired in `internal/app/api.go`; `auth/http.go` holds `RequireHoldBacked`, `AdmissionPastCap` and the grace path.
  - Past the cap, `POST /orders` goes on only for the caller's own ACTIVE, unexpired hold of that event (`orders/handler.go` `create`), else 403 `ADMISSION_EXPIRED`.
  - `DELETE /holds/{id}` and `DELETE /queue` accept the token.
  - `POST /holds` answers 403 `ADMISSION_EXPIRED` (rejoin) instead of 401.
  - A past-cap token never renews the lease.
  - Beyond cap + grace it stays 401.
- **A bug the new test caught in my first version:** on DELETE /queue (`queue`, `admission` audiences) the queue audience is tried first. JWT checks the lifetime before the audience, so it reported "expired" and the loop stopped before the admission grace was tried. Fixed with `slices.Contains(auds, AudAdmission)`.

### F. 50k estimate (brief item 6): see §4.5.

## 2. Acceptance checklist (from the session brief)
- [x] **CPU and heap pprof in every CI rush artifact, plus a local task:**
  - `--pprof` in `rush:scenario`, `pprof-top.txt` and the summary block in `rush.yml`, and `task rush:profile`.
  - Local runs wrote `cpu.pprof` and `heap.pprof` (`loadtest/results/p8eff-{before,after}-15k/`).
- [x] **Profiled locally at 15k, fixed the top costs, before/after CPU per 1k requests and GC** (§4.1, §4.2):
  - CPU per 1k requests: 961.04 → 599.95 ms;
  - GC CPU: 59.17 → 7.71 s, GC cycles 22 → 11, live heap peak 255.93 → 81.29 MB;
  - lag p99: 785.9 → 25.3 ms.
- [x] **Server-facing behaviour unchanged:**
  - The wire bytes are identical (`TestUserConnWireFormat`).
  - In SMOKE over the common window t ≤ 165 s, total requests +0.2%, status +0.2%, join +0.0%, session and join exactly 1.000 per user (§4.3).
- [x] **Self-check CPU sampling bug fixed:** `TestCPUMeter*` pass (§1.D).
- [x] **14 × TOKEN_EXPIRED explained and fixed, with an ADR:** ADR 0018, `TestAdmissionSpendGrace`, `TestOrderCreatePastAdmissionCap` (§1.E).
- [x] **Estimated swarm cores at 50k** (§4.5).
- [x] **`task lint`** → `0 issues.`
- [x] **Unit tests and race on touched packages:** `CGO_ENABLED=1 task test:race` → `TASK_EXIT=0`, every package `ok` (`internal/orders 166.435s`; cached results are keyed to the current code). See §3 for the one environmental failure on the way.
- [ ] **H 50000 not generator-bound on the 4-core runner:** not run here. It needs GitHub Actions (§9).

## 3. Tests
| Suite | Count | Result | Notes |
|---|---|---|---|
| unit, new (rush) | `conn_test.go` 6 test funcs (13 wire cases, 7 response cases, 3 retry cases), `cpumeter_test.go` 2 | pass | table-driven; raw TCP fixture server |
| unit, new (auth) | `grace_test.go` 2 test funcs (11 cases + 64-goroutine) | pass | |
| integration, new (orders) | `TestOrderCreatePastAdmissionCap` | pass | testcontainers Postgres + Redis |
| race (`-race`), full `task test:race` | all packages | pass | local, gcc available. Second attempt, see the note below |
| benchmark | `BenchmarkStatusPoll` (§4.4) | ran | |
| e2e (Playwright) | — | not run | no UI change |

How the race gate went, in order (nothing was skipped or loosened):
1. **Touched packages** (`go test -race -p 2 ./internal/{rush,auth,orders,app,inventory,queue}/...`): 5 ok, **auth FAILED**. That was `TestAdmissionSpendGrace/past_cap:_release/leave_accepted`, a real bug in my gate (§1.E), now fixed. After the fix `go test -race ./internal/auth/... ./internal/rush/...` → both ok.
2. **First full `task test:race`:** **orders FAILED**. `TestCreateOrdersConcurrent` got `still 503 after 40 tries`, because the in-test MockPay timed out under load (a known Windows overload: the test's own comment and the P5a report §race gate). The rush stack from the SMOKE runs was still up. A rerun of the order tests alone after `task stack:down` passed (`TestCreateOrdersConcurrent (3.44s)`).
3. **Second full `task test:race`**, stack down: `TASK_EXIT=0`.

Coverage wasn't measured this session.

## 4. Numbers
**These are laptop numbers** (Docker Desktop, 16 logical CPUs, 10.4 GB for the VM, i5-12500H). They compare the swarm with itself. They are not a claim about SLUICE or about CI. Each figure is one run; I didn't take the best of several.

### 4.1 Profile, 15,000 users, H mix, dedicated connections
Commands:
- `task -x rush:profile USERS=15000 RUN_ID=p8eff-before-15k`: the swarm at `175c887` plus this session's instrumentation only. `go tool pprof -top cpu.pprof | grep -c userConn` → `0`.
- `task -x rush:profile USERS=15000 RUN_ID=p8eff-after-15k`: with `userConn`.
- Both audits passed with every count 0.

Top cumulative swarm frames (`go tool pprof -top -cum cpu.pprof`):

| before (321.65 s samples / 298.75 s) | cum | after (137.11 s samples / 216.55 s) | cum |
|---|---|---|---|
| `net/http.(*persistConn).writeLoop` | 18.64% | `rush.(*userConn).roundTrip` | 53.35% |
| `net/http.(*persistConn).readLoop` | 17.52% | `net.(*conn).Write` (syscall) | 21.58% |
| `runtime.schedule` | 17.19% | `net.(*Dialer).DialContext` | 19.04% |
| `net/http.(*Transport).dialConn` | 13.99% | `runtime.findRunnable` | 21.42% |
| `runtime.findRunnable` | 13.34% | `rush.readResponse` | 8.58% |
| `runtime.mallocgc` | 8.03% | `encoding/json/v2.makeStructArshaler.func3` | 2.63% |
| flat `linux.Syscall6` | 29.62% | flat `linux.Syscall6` | 47.79% |

Top in-use heap (`go tool pprof -sample_index=inuse_space -top heap.pprof`):
- **before:** 180.43 MB total; `bufio.NewReaderSize` 32.13 MB, `bufio.NewWriterSize` 30.12 MB, `rush.newTransport` 11.50 MB.
- **after:** 34.99 MB total; `rush.newUserConn` 3.50 MB cumulative.

### 4.2 Before/after cost (`summary.json` `generator`, `load`, `errors`; `python -I` over both files)
| field | before-15k | after-15k |
|---|---|---|
| cpu_seconds | 385.07 | 168.86 |
| **cpu_ms_per_1k_requests** | **961.04** | **599.95** |
| cpu_cores_run_mean | 1.29 | 0.78 |
| gc_cycles | 22 | 11 |
| **gc_cpu_seconds** | **59.17** | **7.71** |
| heap_live_peak_mb | 255.93 | 81.29 |
| goroutines_max | 43981 | 15011 |
| lag_p99_ms / lag_max_ms | 785.9 / 3700.7 (**generator-bound**) | 25.3 / 181.4 (not bound) |
| cpu_cores_max / mean (1 s samples) | 4.3 / 1.17 | 3.88 / 0.72 |
| cpu_samples_invalid | 1 | 1 |
| requests | 400680 | 281460 |
| timeouts / 5xx | 62525 / 8049 | 27071 / 447 |
| session / join requests | 43079 / 28094 | 19512 / 15650 |
| peak_in_system | 14419 | 14913 |
| stats_failed | 19 | 10 |

How to read the per-1k figure: the before run was generator-bound, so its request mix carries more retries and re-dials (43,079 session calls for 15,000 users). The 961 → 600 ms change therefore mixes two effects: cheaper requests, and fewer expensive retries because the generator stopped starving. The controlled per-request comparison is §4.4. The after run's 27,071 timeouts happened with lag p99 at 25.3 ms, so they are the laptop stack's, not the swarm's.

### 4.3 SMOKE before/after: what the server saw
Commands:
- **before:** HEAD's swarm in a scratch copy of the tree (the six changed files restored with `git show HEAD:<file>`, the four new files removed), then `task -x rush:smoke RUN_ID=p8eff-smoke-before`. Its summary has `git_sha` `unknown` because the copy has no `.git`.
- **after:** `task -x rush:smoke RUN_ID=p8eff-smoke-after` (`git_sha` `175c887` plus the working tree). Server code was identical to HEAD at that point: grace wasn't wired yet, and with grace 0 the auth gates behave as before.
- Both audits passed with every count 0, and both runs had unresolved 0.
- Compared with `python -I loadtest/ci/shape_compare.py loadtest/results/p8eff-smoke-before loadtest/results/p8eff-smoke-after` and a column sum over `timeseries.csv`.

Over the common window t ≤ 165 s after T0:

| column | before | after | diff |
|---|---|---|---|
| rps_total (sum) | 95692 | 95862 | +0.2% |
| rps_join | 5000 | 5001 | +0.0% |
| rps_status | 83234 | 83383 | +0.2% |
| rps_hold | 861 | 884 | +2.7% |
| rps_order | 395 | 405 | +2.5% |
| rps_pay | 474 | 486 | +2.5% |

- **Per user:** session 1.000 / 1.000, join 1.000 / 1.000.
- **rps per 5 s bucket** from T0 − 10 s to T0 + 65 s: 726/726, 1048/1048, 1306/1292, 1157/1125, 467/495, 458/460, 568/547, 501/512, 569/567, 530/520, 540/544, 554/542, 536/543, 541/537, 533/543, 543/510.
- **Outcomes:** TICKET 393 / 404, SOLD_OUT 4142 / 4150, NO_SEAT 424 / 403, seats_sold 500 / 500.
- **Whole-run totals differ:** 165,677 vs 102,839 requests, and Pearson r of the full rps curves 0.636. That's because the before run's last seats took longer to sell: sold out at 293 s vs 173 s. In `timeseries.csv` sold was 498 from t = 150 to 210 s and 499 until 285 s, while the last abandoned holds cycled. Every still-waiting user keeps polling until sold out (about 525 rps of status). This is one run each, and the tail depends on which re-offered users abandon (8%) or pay late.
- **Latency differed** (status p95 9.4 vs 27.3 ms, hold p95 406.8 vs 902.1 ms). The busiest after-seconds were T0 + 3 s and + 6 s, at 2,122 and 2,027 rps with status p95 565.2 and 585.7 ms. Laptop SMOKE latency swings run to run: `p8fix-smoke-2` had status p95 2047 ms. I don't read the difference as a swarm effect, but it is a single pair of runs.

### 4.4 Controlled per-request benchmark (same in-process server for both clients)
Command: `go test -run '^$' -bench BenchmarkStatusPoll -benchmem -benchtime 3s -count 3 ./internal/rush/` (Windows, i5-12500H, 64 users, one connection each):

```
BenchmarkStatusPoll/nethttp-16    188683  34134 ns/op  8042 B/op  90 allocs/op
BenchmarkStatusPoll/nethttp-16     97383  45063 ns/op  8074 B/op  90 allocs/op
BenchmarkStatusPoll/nethttp-16     99009  38344 ns/op  8069 B/op  90 allocs/op
BenchmarkStatusPoll/userconn-16   116724  28685 ns/op  2917 B/op  24 allocs/op
BenchmarkStatusPoll/userconn-16   128751  30226 ns/op  2908 B/op  24 allocs/op
BenchmarkStatusPoll/userconn-16   150645  40103 ns/op  2907 B/op  24 allocs/op
```

- Allocations per poll fall 90 → 24, and bytes 8.0 → 2.9 KB. That is the GC drop in §4.2.
- ns/op includes the in-process server and Windows loopback and is noisy: it overlaps (34.1–45.1 µs vs 28.7–40.1 µs). The goroutine-per-connection saving shows mainly at 15k users (scheduler and GC), not in this 64-connection benchmark.

### 4.5 Estimate: swarm cores at 50k on the CI runner after the fix (an estimate, not a measurement)
Inputs:
- **CI before** (brief): 2.43 cores mean, 1,236,366 requests at 2,064 mean rps, so about 599 s and about 1,456 CPU-s, or **about 1.18 ms CPU per request** (starved mix).
- **Laptop before:** 0.961 ms per request (starved mix too). That gives a CI-to-laptop factor of about 1.23. It's rough, because the two mixes differ.
- **Laptop after:** 0.600 ms per request, which is about **0.74 ms per request on CI**.

Load at 50k, if the generator keeps up:
- After-15k sent 18.8 requests per user (281,460 / 15,000). At 50k, positions are further back, so `retry_after` is longer (up to 34.5 s at position 46,000, ADR 0017). I assume 15–25 requests per user, so **0.75–1.25 M requests** and **about 0.55–0.93 k CPU-s**.
- **Steady waiting room:** about 46k waiting / about 30 s mean interval is about 1.5 k rps, which is **about 1.1 cores** (range 0.8–1.5).
- **T0 burst:** about 35k users create a session and join within T0 ± 10 s. Each is a new connection (dial plus 2 requests), about 5 k rps of the most expensive requests, so about 3.5–5 cores for 10–20 s. The stack needs cores at the same moment.

**Verdict:**
- The steady phase should fit easily, at about 1.1 swarm cores against 2.43 before.
- The T0 burst probably still saturates the 4-core box for several seconds. Those seconds hold more than 1% of the run's scheduled actions, so lag p99 may still exceed 100 ms and flag the run generator-bound, even though the lag is confined to the burst.
- The CI run's `timeseries.csv`, `cpu.md` and the new `cpu.pprof` will show which one it is. If it's the burst, the next lever is the arrival window or swarm pinning (ADR 0015 addendum 2). Both change what the run measures and are Sheshakanth's call.

## 5. Deviations and decisions
| What changed vs the spec | Why | ADR |
|---|---|---|
| Dedicated mode no longer uses `http.Transport`; the timeout is a socket deadline, not `httptrace.GotConn` | net/http's per-connection goroutines and per-request timers dominated the swarm's CPU; wire bytes and connection behaviour are unchanged | 0019 |
| Admission tokens accepted up to `HOLD_TTL` past `exp` to order the user's own live hold, release, leave; past-cap `POST /holds` is 403 instead of 401 | the token's TTL was shorter than the worst legitimate wait | 0018 |
| `summary.json` `generator` gains run-cost fields; 1 s CPU samples can be invalid | self-check bug; profiling | 0019 |

Docs updated:
- LOADTEST §1, §3.3, §3.4 and §7;
- API.md §1.1 (token table, TOKEN_EXPIRED, ADMISSION_EXPIRED);
- ARCHITECTURE §4 (admission lease);
- `docs/results/README.md` (generator fields);
- ADR 0018 and ADR 0019.

## 6. Known issues and debt
- **Not proven on CI.** H 50000 is still unverified, and the T0 burst may keep it generator-bound (§4.5).
- **Remaining swarm CPU is kernel networking:**
  - `Syscall6` 47.79% flat after the fix;
  - `net.(*conn).Write` 21.58% cumulative (on loopback and the Docker bridge, the receiving side's TCP work runs in the sender's syscall);
  - dial 19.04% cumulative (one connect per user plus a re-dial after every timeout).
  - None of it can drop without fewer connections or requests, which would change what the server sees.
- **`userConn` scope:** `http://` only, HTTP/1.1 only. That covers everything the swarm targets (Caddy on the compose network).
- **One run per variant.** Neither SMOKE nor the 15k runs were repeated, so run-to-run variance (§4.3 latency) isn't quantified.
- **Web copy:** the web client's TOKEN_EXPIRED notice still says "Nothing was held or charged". After ADR 0018 that answer only comes past cap + `HOLD_TTL`, when any hold has already expired, so it is true now. No UI change was made.
- **Kept on purpose:** the before-SMOKE scratch tree at `C:\Users\shesh\AppData\Local\Temp\claude\f7f5e03a1bdcd196\7211a3ff-6874-496c-8b97-d8fe2c8eb480\scratchpad\head-tree` isn't part of the repo. Its results were copied to `loadtest/results/p8eff-smoke-before`.
- **Not committed:** this session's local run outputs (`loadtest/results/p8eff-*`, including the `.pprof` files). `loadtest/results/*` is gitignored, so they stay on this laptop. Copy any you want to keep into `docs/results/`.

## 7. Understand this before moving on
1. **Q:** How do you know the new swarm client doesn't change what the server sees?
   **A:** `internal/rush/conn_test.go` `TestUserConnWireFormat` runs every request shape the swarm sends through both the old net/http path (`client.httpDo`) and `userConn.roundTrip`. It captures the raw bytes at a TCP listener and compares them byte for byte, plus the connection count. `TestUserConnStaleKeepAlive` checks the net/http retry rule. At run level, SMOKE over the shared window differs by +0.2% in total requests, and session and join stay at exactly 1.000 per user (§4.3).
2. **Q:** Why did the generator report 12 cores on a 4-core machine?
   **A:** The old `cpuMeter.read` took CPU time and the wall clock in two calls. A goroutine descheduled between them gets a late wall stamp, and the next interval charges the stalled seconds' CPU to a short wall span. `profile.go` `cpuMeter.sample` now brackets the CPU read with two clock reads, uses the midpoint, and discards an unbracketable sample. `cpumeter_test.go` reproduces the >10-core reading with the old logic.
3. **Q:** Why accept an expired admission token at all?
   **A:** Because a hold taken just before the cap lives `HOLD_TTL` past it. The grace (`auth/http.go` `require`, `orders/handler.go` `create`) only lets the token turn the user's own ACTIVE, unexpired hold into an order, or give things back. `POST /holds` past the cap is refused (403, rejoin), and the lease is never renewed, so no new spend happens after the cap. That's why I7 and the fencing invariants are untouched (`TestOrderCreatePastAdmissionCap`).

## 8. Files changed
`git status --short`:

**Admission spend grace (ADR 0018)**
```
 M internal/app/api.go
 M internal/auth/http.go
 M internal/auth/jwt.go
?? internal/auth/grace_test.go
 M internal/orders/handler.go
 M internal/orders/admission_gate_test.go
 M docs/API.md
 M docs/ARCHITECTURE.md
?? docs/adr/0018-admission-spend-grace.md
```
**Swarm (ADR 0019)**
```
 M internal/rush/client.go
 M internal/rush/options.go
 M internal/rush/report.go
 M internal/rush/run.go
 M internal/rush/user.go
?? internal/rush/conn.go
?? internal/rush/conn_bench_test.go
?? internal/rush/conn_test.go
?? internal/rush/cpumeter_test.go
?? internal/rush/profile.go
```
**Tooling, CI, docs**
```
 M .github/workflows/rush.yml
 M Taskfile.yml
?? loadtest/ci/shape_compare.py
 M docs/LOADTEST.md
 M docs/results/README.md
?? docs/adr/0019-swarm-dedicated-client.md
?? docs/reports/P8-swarm-efficiency.md
```

## 9. Next phase readiness: CI steps for Sheshakanth
After committing and pushing:
1. **H 50000:** `gh workflow run rush.yml -f scenario=H -f users=50000` (or Actions → rush → Run workflow; leave `swarm_cpuset` blank).
2. **SMOKE 5000 on the same commit**, for the server-facing comparison with run `45929043006`: `gh workflow run rush.yml -f scenario=SMOKE -f users=5000`.
3. **Read the H run:**
   - **Generator check:** `report.md` "Generator self-check", including the new run-cost row and `cpu_samples_invalid`. `cpu_cores_max` should now be ≤ 4.
   - **Per-container CPU:** `cpu.md`, swarm mean and p95, against 2.43 / 3.50 before.
   - **The swarm's profile:** the summary's "Swarm CPU profile" block and `cpu.pprof`.
   - **Lag around T0:** `timeseries.csv` around T0 shows whether any remaining lag is confined to the burst (§4.5).
   - **Anomalies:** `anomalies` should show no `TOKEN_EXPIRED` on hold, order, leave or release within the grace.
4. **Shape comparison:** run `python -I loadtest/ci/shape_compare.py <old SMOKE dir> <new SMOKE dir>` on the two SMOKE artifacts.

## 10. Git commands (Sheshakanth runs these; PowerShell-safe)
Two commits. Plain ASCII, no backticks, dollar signs or double quotes inside the messages, one -m per paragraph; it works as is in PowerShell, Git Bash and zsh.

```powershell
git add internal/app/api.go internal/auth/http.go internal/auth/jwt.go internal/auth/grace_test.go internal/orders/handler.go internal/orders/admission_gate_test.go docs/API.md docs/ARCHITECTURE.md docs/adr/0018-admission-spend-grace.md
git commit -m "fix(auth): let admission tokens finish a live hold past the cap" -m "- Spend grace of HOLD_TTL past the admission token exp" -m "- POST /orders past the cap only for the callers own live hold" -m "- Past-cap POST /holds is 403 ADMISSION_EXPIRED, not 401" -m "- Release and leave accept the token inside the grace" -m "ADR 0018 - report: docs/reports/P8-swarm-efficiency.md"
git add -A
git status --short
git commit -m "perf(rush): drive dedicated connections from the user goroutine" -m "- userConn: byte-identical HTTP/1.1 on one keep-alive conn per user" -m "- 15k local: CPU per 1k requests 961 to 600 ms, GC CPU 59.2 to 7.7 s" -m "- Fix CPU self-check: bracket the CPU read with two clock reads" -m "- Swarm cpu.pprof and heap.pprof in every rush and CI artifact" -m "- task rush:profile and loadtest/ci/shape_compare.py" -m "ADR 0019 - report: docs/reports/P8-swarm-efficiency.md"
```
