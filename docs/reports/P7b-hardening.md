# P7b: Hardening, tasks 4–6 (Step 0 latency investigation, SLO tuning, CI rush workflow, results guide): Report

| Field | Value |
|---|---|
| Status | DONE for this session's scope, with one stated gap: the latency SLOs are **not** met on the laptop and are left to the CI run (see §4). The pre-P7a control has 2 runs, not 3 (§4.1). |
| Date (IST) | 2026-11-25 |
| Time spent vs timebox | About 2 h of measurement (20:40–22:45 IST, from command timestamps), well inside the remaining P7 budget |
| Commit base | `9a8cadb` (`git rev-parse --short HEAD`). The working tree changed only docs, CI, scripts and `deploy/compose.yml`'s Postgres healthcheck, so the "HEAD" runs below test the committed Go code |

## 1. What was built

### Step 0: SMOKE latency regression (investigation, not a code fix)
P7a measured checkout p95 at 823.8–1925.1 ms on SMOKE, against 329.7 ms in P5b, on every route. This session investigated in the order instructed:
1. **GC first.** `loadtest/manual/p7b-gcq.sh` reads per-process GC and CPU figures from Prometheus right after each run.
2. **Bisect.** Instead of reverting the P7a changes one at a time, I first ran the **whole pre-P7a tree** (`5138893`, extracted with read-only `git archive` into the scratchpad) as a control. If reverting everything at once doesn't make the code faster, no single revert can, so the one-by-one bisect wasn't needed (§4.1).
3. **Medians of 3, interleaved.** HEAD and control runs alternated so that drift in laptop state hits both trees equally. Comparison script: `loadtest/manual/p7b-cmp.py`.

**Finding:** the pre-P7a code is no faster, GC is not the cause, and the laptop is too noisy for latency work. Caddy at 2.3–4.4 cores is the open lead for CI (§4.1, §6).

### Task 4: SLO tuning loop
No code was tuned this session. The usual suspects in LOADTEST §8 were checked against the data:

| # | Suspect | Status |
|---|---|---|
| 1 | Caddy → API keep-alive | Already set (`keepalive 2m`, `keepalive_idle_conns_per_host 1024`). Caddy is still the largest CPU consumer: **open lead** |
| 2 | pgxpool size / acquire waits | Sized in P7a (ADR 0013); unchanged |
| 3–5 | JSON on status, snapshot-cache locks, Redis round trips | Not profiled. The control shows the gap is not in the code that changed, and on this laptop a profile would mostly show contention |
| 6 | GC (`GOGC`/`GOMEMLIMIT`) | **Ruled out** (§4.1) |

No change was made just to hit a number, and nothing on the LOADTEST §8 "never do" list was touched.

### Task 5: `.github/workflows/rush.yml` (ADR 0015)
- **Trigger:** `workflow_dispatch` with `scenario` (choice: H, SMOKE, B0, B1, B2) and `users` (blank means the scenario's default; validated as an integer from 1 to 60,000). Runs on `ubuntu-latest` with `timeout-minutes: 60`.
- **Steps:**
  1. Checkout with `persist-credentials: false`, then setup-go and Task 3.54.0.
  2. Run the LOADTEST §7 sysctls.
  3. Make the bind mounts writable for the distroless `nonroot` image.
  4. Write `runner.md` (nproc, `free -m`, kernel, Docker versions, SHA).
  5. `docker compose build`.
  6. Run the scenario's task (`task rush` / `rush:smoke` / `rush:b0|b1|b2`), which brings up a fresh stack, seeds, runs the swarm and audits.
  7. Collect logs, `ps` and runner info.
  8. Write `runner.md` + `report.md` + the verdict to `$GITHUB_STEP_SUMMARY`.
  9. Upload `rush-<run_id>` (90 days), then `down -v`.
- **Gate:** `loadtest/ci/verdict.py`.
  - H, SMOKE and B2 fail on any audit count ≠ 0, a failed or unsettled audit, unresolved users, or a non-zero exit from `sluice rush`.
  - B0 and B1 fail when the expected bugs are **not** detected (auditor sensitivity) or when users are unresolved.
  - Latency SLOs appear in a target / measured / met table and never gate.
- `permissions: contents: read`. No step commits, pushes or writes to git.

### Task 6: `docs/results/README.md`
Covers every file in a run directory, `summary.json` field by field (identity, env, config, load, outcomes, timing, latency, errors, generator, records, audit, headline), the `timeseries.csv` columns, a five-check reading guide (valid → correct → concurrent → fast → typical), and how to trigger and download the CI run.

### Fixes found along the way
- **Postgres healthcheck race** (`deploy/compose.yml`). Run `p7b-base-3` failed at startup: migrate got `dial tcp 172.27.0.4:5432: connect: connection refused` even though postgres was reported `Healthy`. The cause: `pg_isready` over the Unix socket answers from the entrypoint's temporary initdb server, which doesn't listen on TCP. The check is now `pg_isready -h 127.0.0.1 ...`, so it turns healthy only once the real server is listening. This matters for CI, where every run starts from a fresh volume. All five later fresh-stack runs started cleanly.
- `ci.yml`: quoted the two version variables that actionlint/shellcheck flagged (SC2086).

## 2. Acceptance checklist (remaining P7 items)
- [x] **`rush.yml` passes `actionlint`:** `docker run --rm -v "<repo>:/repo" -w /repo rhysd/actionlint:1.7.7 -no-color -verbose` → `Found 0 errors in 2 files` (rush.yml and ci.yml).
  - **Dry run:** neither `act` nor a local Linux runner was available, so **only a CI run can verify the workflow end to end.**
  - What was verified locally:
    - the task each scenario calls (`task rush:smoke`: 5 clean runs this session, every audit passed);
    - the gate against past runs, all with the expected exit codes: `verdict.py` on `p7a-smoke-v2` SMOKE → 0, `p5b-b0` B0 → 0, `p5b-b1` B1 → 0, `p5b-b2` B2 → 0, `p7a-b2-20k-v3` B2 (7 unresolved) → 1, and `p5b-b2` judged as B0 → 1.
- [x] **Tuning table with before/after numbers** (§4.1). **The SLOs are not met on the local SMOKE run.** Explanation:
  - Checkout p95 median is 1232.9 ms against a 250 ms target, and status, join and error rate miss on some runs too.
  - The same gap appears with the pre-P7a code.
  - The laptop's run-to-run spread is wider than any effect being measured.
  - The canonical number is the CI run (SPEC §7).
- [x] **`task lint test` green.**
  - `task lint` → `0 issues.`
  - `task test:race` → exit 0 with the stack down and nothing else running. `internal/inventory` (60.899 s) and `internal/orders` (270.352 s) ran fresh; the other 19 packages reported `(cached)`. No Go file changed this session, so the cached passes hold for this tree. Log: `loadtest/results/p7b-test-race.log` (local).
  - `python -m py_compile loadtest/ci/verdict.py` passes.
- [x] (P7a, unchanged) `task chaos` C1–C4 and the Grafana dashboard: see `docs/reports/P7a-hardening.md` §2.

## 3. Tests
| Suite | Count | Result | Notes |
|---|---|---|---|
| unit + integration, `-race` | 21 packages | pass | 19 cached (no Go changes), inventory and orders re-run |
| `loadtest/ci/verdict.py` against stored runs | 6 cases | pass | expected 0/1 exit codes (§2) |
| actionlint 1.7.7 (with shellcheck) | 2 workflows | 0 errors | |
| SMOKE rush + audit | 5 clean runs | all audits pass, every invariant 0, unresolved 0 | §4.1 |

## 4. Numbers

### 4.1 Step 0: HEAD vs pre-P7a control, SMOKE (5k users, waiting room on, seed 42)
Commands: `task rush:smoke RUN_ID=<id>` in the repo (HEAD) and in the extracted `5138893` tree (control), interleaved head-1, ctl-1, head-2, ctl-2, head-3; then `bash loadtest/manual/p7b-gcq.sh 6m` after each HEAD run, and `python loadtest/manual/p7b-cmp.py <ids>`. Copies: `docs/results/p7b/`.

| run | code | checkout p50 / **p95** ms | hold p95 | order p95 | pay p95 | status p95 | join p95 | error rate | peak rps | drain s | gen lag p99 ms | audit |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| p5b-smoke (P5b) | 570aefc | 35.1 / **329.7** | 40.4 | 316.9 | 462.6 | 3.7 | 13.3 | 0.0000% | 7001 | 246.9 | 1.5 | pass |
| p7b-head-1 | 9a8cadb | 165.5 / **1232.9** | 275.5 | 965.6 | 1516.5 | 170 | 588.3 | 0.5030% | 3417 | 215.4 | 20.1 | pass |
| p7b-head-2 | 9a8cadb | 99.1 / **1056.8** | 140.4 | 956.9 | 1222.7 | 17.6 | 548.4 | 0.0624% | 3392 | 276.0 | 8.3 | pass |
| p7b-head-3 | 9a8cadb | 347.6 / **1709.1** | 531.5 | 1467.4 | 2261 | 231.2 | 417.8 | 0.0000% | 3409 | 185.2 | 13.1 | pass |
| p7b-ctl-1 | 5138893 (pre-P7a) | 130.2 / **842.8** | 217 | 764.9 | 971.8 | 426.5 | 605.7 | 0.0025% | 4022 | 120.6 | 12.1 | pass |
| p7b-ctl-2 | 5138893 (pre-P7a) | 106.4 / **1300.5** | 145.4 | 1264.6 | 1727.5 | 178.6 | 332.3 | 0.0000% | 3585 | 188.8 | 11.4 | pass |

- **HEAD (3 runs):** checkout p95 median **1232.9 ms**, spread 1056.8–1709.1 ms (1.6×).
- **Control (2 runs, not 3):** 842.8 and 1300.5 ms (1.5×). The third control run (`ctl-3`) was killed partway when the laptop ran critically low on memory, and was not repeated (Sheshakanth's decision). With two runs there is no median: the midpoint, 1071.7 ms, is shown only for orientation.
- **Verdict: the pre-P7a code is no faster.** The two trees overlap inside the laptop's spread, and both are 3–5× P5b's 329.7 ms. P5b-era SMOKE runs of a single commit already ranged 133.2–1840.1 ms (`smoke-20261124-*`, commit c9b4d55). So the P7a "regression" was not caused by P7a's code: the one-by-one bisect was unnecessary, and no P7a change was reverted.
- **Earlier runs this session, excluded from the comparison:**
  - 5 earlier runs (p7b-base-1/2, p7b-pre-1/2/3) overlapped with an unrelated Remotion video render on the host (12 headless Chrome processes + ffmpeg). Host CPU was 60–66% with the stack idle, 0.6 GB of 15.7 GB RAM was free, and the host paged at 1.5k–6.2k pages/s.
  - Two of them were generator-bound (base-1 lag p99 261.2 ms, base-2 415 ms > 100 ms, LOADTEST §3.4).
  - Their checkout p95 values (1668.1, 1838.1; control 2385.9, 3133.4, 2439.2 ms) pointed the same way, with the control no faster, but they are not used as evidence.

**GC (HEAD runs, Prometheus, 6 min window):**

| run | peak GC cycles/s per api replica | GC pause total s (api) | max heap MiB (api) | max CPU cores api1/2/3 | **max CPU cores Caddy** | Caddy heap MiB |
|---|---|---|---|---|---|---|
| p7b-head-1 | 1.96 / 2.08 / 1.88 | 0.31 / 0.30 / 0.26 | 46.4 / 48.3 / 44.3 | 0.73 / 0.73 / 0.72 | **1.86** | 147.1 |
| p7b-head-2 | 2.12 / 2.16 / 2.32 | 0.24 / 0.16 / 0.20 | 48.8 / 44.2 / 51.4 | 0.70 / 0.70 / 0.74 | **1.84** | 226.2 |
| p7b-head-3 | 1.77 / 1.80 / 1.80 | 0.24 / 0.33 / 0.46 | 52.0 / 46.5 / 46.9 | 0.73 / 0.76 / 0.73 | **1.88** | 154.2 |
| p7b-base-1 (contaminated) | 1.80 / 1.72 / 1.72 | 0.55 / 0.44 / 0.48 | 45.1 / 42.1 / 41.8 | 0.82 / 0.80 / 0.80 | **2.32** | 195.0 |
| p7b-base-2 (contaminated) | 1.80 / 1.88 / 2.00 | 0.72 / 0.63 / 0.40 | 48.5 / 48.9 / 47.0 | 0.72 / 0.72 / 0.70 | **4.40** | 301.9 |

- **GC is not the cause.**
  - About 2 cycles/s at peak and under 0.5 s of total pause per replica over the run.
  - Peak heap of about 52 MiB against `GOMEMLIMIT=512MiB`, so collection is paced by `GOGC=100`, not by the limit. Raising the limit would change nothing.
  - "Max CPU cores" is `max_over_time(rate(process_cpu_seconds_total[30s])[6m:5s])`.
- **Caddy is the largest single CPU consumer:**
  - 1.84–1.88 cores in the clean runs (2.5× any API replica);
  - 2.32–4.40 cores in the contaminated runs (the "2.3–4.4 cores" lead).
  - The API replicas never exceeded 0.76 cores, so they are waiting, not computing.
  - The extra Caddy work in P7a (global `metrics` instrumentation, header rewriting) was not isolated, since the control without it was no faster. This is the first thing to look at on the CI runner (§6).
- **Correctness held on every run:** all 5 clean runs and the 5 contaminated ones passed the audit with every invariant 0 and unresolved 0.

### 4.2 Tuning table (LOADTEST §8)
| change | before | after | kept? |
|---|---|---|---|
| none to code. Step 0 control: P7a's runtime changes vs the pre-P7a tree | checkout p95 842.8 / 1300.5 ms (pre-P7a, 2 runs) | 1232.9 ms median, 1056.8–1709.1 (HEAD, 3 runs) | P7a changes kept (no measurable cost) |
| GC: raise `GOMEMLIMIT` | — | not made: the limit is never reached (heap ≤ 52 MiB of 512 MiB) | n/a |
| Postgres healthcheck over TCP (reliability, not latency) | 1 of 3 fresh starts failed (`p7b-base-3`) | 5 of 5 fresh starts ok | kept |

**SLO status on the laptop (HEAD medians of 3):** checkout p95 1232.9 ms (target ≤ 250, **missed**); status p95 170 ms (≤ 50, **missed**); join p95 548.4 ms (≤ 100, **missed**); error rate 0.0624% (< 0.1%, met by the median, missed by head-1 at 0.5030%); drain 215.4 s (≤ 300, met). The bottleneck on this laptop is CPU contention: the swarm, Caddy, 3 API replicas, Postgres, Redis, Prometheus and Grafana share one Docker VM capped at 10 GB on a 15.7 GB host that also runs the IDE and other workloads. Caddy is the largest consumer.

## 5. Deviations and decisions
| What changed vs the spec | Why | ADR |
|---|---|---|
| `rush.yml` runs the scenario task (which brings up the stack and audits) instead of separate `stack:up` + `task audit` steps; the gate is `verdict.py`, with expected-bug rules for B0/B1 | `task rush*` already does both; B0/B1 must fail the audit by design | 0015 |
| Step 0 bisect replaced by a whole-tree control | Reverting everything at once was no faster, so single reverts could not be | (report only) |
| Postgres healthcheck probes TCP | initdb's temporary server answered the socket probe | (report only, comment in `deploy/compose.yml`) |
| Pre-P7a control: 2 runs, not 3 | Third run killed by host memory pressure; Sheshakanth chose not to repeat it | (report only) |

Docs updated: `docs/LOADTEST.md` §7, `docs/results/README.md` (new), `docs/adr/0015-ci-rush-workflow.md` (new).

## 6. Known issues and debt
- **Latency SLOs unmet locally.** The canonical answer is the CI run. If CI also misses, start with Caddy: compare a run with the global `metrics` option off, then the header block, on the 4-vCPU runner (median of 3).
- **Laptop noise.** A 1.5–1.6× spread between identical runs, plus whatever else runs on the host (a video render this session, which also drove the memory-pressure kills). Local runs are fine for correctness, not for latency.
- **`rush.yml` is unverified end to end** until its first GitHub run. A 50k H run must also show `peak_in_system ≥ 49,000` and `generator_bound: false`, or it can't be the headline.
- **Unresolved users in direct mode at 20k** (P7a, B2) are still open. B2 in CI is at 10k.

## 7. Understand this before moving on (Sheshakanth's interview prep)
1. **Q:** Your SMOKE p95 got 3× worse after a hardening change. How did you find out whether the change caused it?
   **A:**
   - Ran the code from before the change as a control, on the same machine, interleaved with the new code, three runs each, and compared medians and spreads.
   - The old code was no faster (842.8 / 1300.5 vs a median of 1232.9 ms), so the change wasn't the cause, and bisecting it would have measured noise.
   - Separately, I checked GC from the runtime's own metrics before touching any knob.
2. **Q:** Why not just take the best of three runs?
   **A:** On a noisy box the best run measures luck: the spread here was 1.6×. Report the median and the spread. Any run that is generator-bound (the load generator itself lagging > 100 ms) or that overlaps another workload is excluded and the exclusion is stated. That's what happened to five runs this session.
3. **Q:** Why does the CI gate treat B0 failing the audit as a pass?
   **A:** B0 is the naive system: it must oversell and double-charge. If the auditor reports 0 on B0, the auditor is blind and every "0 oversold" claim is worthless. So the gate asserts the expected bug signature for the baselines and zero violations for the real system.

## 8. Files changed (`git status --short`)
- `M .github/workflows/ci.yml`: SC2086 quoting
- `?? .github/workflows/rush.yml`: CI headline workflow
- `M deploy/compose.yml`: Postgres TCP healthcheck
- `M docs/LOADTEST.md`: §7 matches the workflow
- `?? docs/adr/0015-ci-rush-workflow.md`
- `?? docs/results/README.md`, `?? docs/results/p7b/` (5 run summaries, GC stats, comparison)
- `?? docs/reports/P7b-hardening.md`
- `?? loadtest/ci/verdict.py`
- `?? loadtest/manual/p7b-gcq.sh`, `?? loadtest/manual/p7b-smoke3.sh`, `?? loadtest/manual/p7b-cmp.py`

## 9. Next phase readiness, and Sheshakanth's GitHub steps
P8 needs the CI run URLs. Steps on GitHub, in order:
1. Commit and push (§10). Under **Settings → General → Danger Zone**, make sure the repository is **Public**, because Actions minutes are free and unlimited only for public repos. Under **Settings → Actions → General**, make sure Actions are allowed.
2. **Actions** tab → workflow **`rush`** (left sidebar) → **Run workflow** → branch `main`, scenario **`SMOKE`**, users blank (5000) → **Run workflow**. This is the CI baseline: wait for it to go green (about 10–15 min), then open the run and check the job summary (verdict PASS, generator-bound false) and the `rush-ci-…-smoke-5000` artifact.
3. **Run workflow** again: scenario **`H`**, users **`50000`**. This is the headline. In the summary, check verdict PASS, `peak_in_system ≥ 49,000`, generator-bound false, and the SLO table.
4. Then three more runs, one at a time: **`B0`** with **`10000`**, **`B1`** with **`10000`**, **`B2`** with **`10000`**. B0 and B1 are green when the auditor *detects* their bugs; B2 is green with every invariant 0.
5. Copy the five run URLs (`https://github.com/<you>/sluice/actions/runs/<id>`) and paste them into the P8 session. If any run fails, paste its URL as well; the artifact holds `stack.log.gz` for diagnosis.

## 10. Git commands (Sheshakanth runs these)
```bash
git add -A
git status --short
git commit -m "ci(rush): headline rush workflow, CI verdict gate, results guide" -m "- rush.yml: workflow_dispatch scenario and users, sysctl tuning, artifact, job summary" -m "- verdict.py: invariants gate H SMOKE B2, expected-bug gate for B0 and B1" -m "- docs/results/README.md explains every summary.json field" -m "- Postgres healthcheck over TCP to avoid the initdb startup race" -m "- Step 0: pre-P7a control no faster, GC ruled out, laptop too noisy" -m "Phase P7b - report: docs/reports/P7b-hardening.md"
```
