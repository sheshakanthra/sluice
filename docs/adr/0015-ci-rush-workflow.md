# ADR 0015: CI rush workflow details

- **Status:** accepted
- **Date:** 2026-11-25
- **Phase:** P7 (session P7b)

## Context
LOADTEST §7 describes `.github/workflows/rush.yml` in seven steps. Writing it surfaced details the spec leaves open:

1. Step 3 says "`docker compose build`, then `task stack:up ENV=rush`", and step 4 "`task rush`, then `task audit`". But `task rush*` already starts from a fresh stack (`down -v`, `up --wait`, seed) and runs the auditor itself (`sluice rush --audit`). Running `stack:up` first would be torn down at once, and a separate `task audit` would audit twice.
2. Step 7 says "fail the job if the audit fails". For B0 and B1 a failed audit is the **expected** result (LOADTEST §4: they exist to show the bugs, and B0 is the auditor's sensitivity test).
3. The application image is distroless `nonroot` (uid 65532) and writes results into the `loadtest/results` bind mount, which on a Linux runner is owned by the runner user.

## Decision
- `rush.yml` builds the images once (`docker compose ... build`, so the build is its own timed step), then runs the scenario's task (`rush`, `rush:smoke`, `rush:b0`, `rush:b1`, `rush:b2`) with `ENV=rush USERS=<n> RUN_ID=ci-<run id>-<scenario>-<users>`. That task covers stack up, seed, swarm and audit. There is no separate `stack:up` or `task audit` step.
- The pass/fail gate is `loadtest/ci/verdict.py`, which reads `summary.json`:
  - **H, SMOKE, B2:** every audit count is 0, the audit settled and passed, `sluice rush` exited 0, and unresolved users are 0. A null count fails, except `admitted_after_soldout` in direct mode, where that check does not apply.
  - **B0:** oversold > 0 **and** duplicate charges > 0. **B1:** oversold = 0 **and** duplicate charges > 0. Both also need unresolved users = 0 and a settled audit. A baseline showing no bug fails the job, because then the auditor is blind.
  - Latency SLOs are printed in the job summary with target / measured / met, and never fail the job (SPEC §7: correctness is hard, latency is a goal).
- ~~`loadtest/results` is made world-writable before the run (`chmod 0777`)~~ Superseded by the addendum below. `web/out` (gitignored, Caddy's static mount) is created empty.
- The job summary is `runner.md` (nproc, `free -m`, kernel, Docker versions, commit, run id) + the run's `report.md` + the verdict. The artifact `rush-<run_id>` holds the run directory plus `runner.md`, `stack.log.gz`, `ps.txt` and `verdict.md`, kept 90 days.
- `permissions: contents: read`, `persist-credentials: false` on checkout, no step writes to git.
- `users` is validated as an integer in 1..60,000; blank means the scenario's default.

## Consequences
- One workflow serves every scenario, and the baselines are self-checking: a green B0/B1 job means "the auditor caught the bugs".
- The CI gate and the local `sluice rush --audit` exit code agree for H/SMOKE/B2; for B0/B1 only the CI gate encodes the expected signature.
- Prometheus and Grafana still start in CI (they are in `deploy/compose.yml`) and cost a little CPU on the runner; their data is not uploaded.
- The workflow can only be fully verified on GitHub: `actionlint` validates it locally, and the gate and the task it calls are the ones exercised by the local SMOKE runs.
- Docs updated: `docs/LOADTEST.md` §7, `docs/results/README.md`.

## Addendum (2026-11-26): results ownership
The first CI run (`45766424648`, SMOKE 5000) could not publish its results: the containers (uid 65532) wrote them `0600` in `0750` directories, which the runner user couldn't read (`P7b-hardening.md` §11). Now:
- `rush` and `audit` run as the runner's uid/gid (`user: "${SLUICE_UID:-65532}:${SLUICE_GID:-65532}"` in `deploy/compose.rush.yml`; CI exports both).
- Result files are `0644` and directories `0755`.
- An `if: always()` step chowns `loadtest/results` to the runner and makes it `a+rX` before the summary and the upload.
- The summary step and `verdict.py` name a missing or unreadable file, still write what they can, and exit 1.

The gate rules and thresholds are unchanged.
