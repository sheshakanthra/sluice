# P8: Ship
**Timebox:** 4 h · **Read first:** DEPLOY.md (all) · SPEC.md §8–9 · LOADTEST.md §3.5

## Inputs from Sheshakanth
- The CI run URL and artifact for the H 50k headline run, and for the B0/B1/B2 runs if they were re-run on CI. He downloads the artifacts into `loadtest/results/` locally.

## Goal
Publish the replay site, make the live demo one command away, and make the README and interview notes as strong as the system. Every number must trace back to `summary.json`.

## Tasks
1. **Results:**
   - Copy the curated headline and baseline files into `docs/results/<run_id>/` and `web/public/results/latest/`.
   - Convert `timeseries.csv` → `timeseries.json`, build `baselines.json` (the B0/B1/B2/H matrix), and copy the E1 booking video.
   - Write `docs/results/RESULTS.md` with: headline table, baseline matrix, environment, methodology summary, run URLs, commit SHAs, and honest limitations (shared-CPU runner, single machine, MockPay instead of a real PSP).
   - `task verify:results`: a small Go or Node script that fails if any number in the README, RESULTS.md or the web results files differs from `summary.json`.
2. **Live demo:**
   - `deploy/env/demo.env.example`.
   - `task demo` builds the web static, brings the stack up with `ENV=demo`, runs migrate + seed, and prints the URLs.
   - `task tunnel` runs `cloudflared tunnel --url http://localhost:8000`.
   - `POST /demo/rush` with a worker job running the in-process bot engine (reuse `internal/rush`), capped at 3,000 bots, with a 2-minute cooldown and a 10-minute abort. It publishes bot-measured metrics to `sl:{e:<eid>}:demo` for Rush Control.
   - Work through the security checklist in DEPLOY.md.
3. **Vercel readiness:**
   - `web` builds as a static export with REPLAY auto-detected.
   - Add the exact Vercel dashboard settings to the report (root directory `web`, no env vars).
   - Verify REPLAY locally with `task web:serve`.
4. **README.md** (portfolio-grade, scannable in 60 s):
   - Hero line, a badge row (CI, license), and the **headline result table**
   - "How it survives a rush": the 4 mechanisms with the mermaid diagram from ARCHITECTURE.md
   - The baseline matrix (naive → fixed), plus a link to the replay site and how to request a live demo
   - Quickstart (`task up migrate seed dev:api dev:worker dev:mockpay dev:web`) and "Reproduce the headline" (`task rush`, or the CI workflow)
   - Project structure, key decisions (link the ADRs), limitations, what changes at 10× (ARCHITECTURE §8)
   - License
5. **`docs/INTERVIEW.md`:** 15 crisp Q&As grounded in the actual code, with file and function references:
   - why a waiting room, and how admission is bounded
   - the lottery fairness argument
   - why Redis *and* Postgres, and what happens if Redis dies
   - fencing (and why it isn't Redlock)
   - the idempotency key design and its crash-recovery semantics
   - timeout-after-commit at the PSP
   - webhook dup/reorder handling
   - the late-payment vs expiry race
   - how p95 was measured (intended start, coordinated omission)
   - why a custom swarm plus k6
   - what the naive baselines proved
   - CI as the canonical environment and its caveats
   - what breaks first at 10×
   - what you'd change for real payments
   - the biggest bug found during the build
6. **`docs/RESUME.md`:** the final bullets per SPEC §8, with the real numbers:
   - an SDE resume version (2–3 bullets)
   - a one-line portfolio version
   - a LinkedIn "featured" blurb. Sheshakanth posts it himself; never post on his behalf.
7. **Demo video script** (`docs/DEMO-SCRIPT.md`, 90 s): what to show and in what order (replay control → results → live booking → demo rush → Grafana).
8. **Final pass:**
   - Every doc in `docs/` matches the code (update specs and ADRs where needed).
   - `.env.example` is complete.
   - LICENSE is present, and no secrets are anywhere (`gitleaks detect` via Docker, if available).

## Acceptance
- [ ] `task verify:results` passes, and every published number traces to the headline `summary.json`.
- [ ] REPLAY works from `web/out` with no backend. `task demo` + `task tunnel` work end to end (booking on `quiet-tuesday`, plus a demo rush with ≤ 3,000 bots).
- [ ] README, RESULTS, INTERVIEW, RESUME and DEMO-SCRIPT are complete and accurate.
- [ ] Every item in SPEC §9 is ✅.

## Sheshakanth's steps (list them in the report)
1. Commit (using the block the report provides) and push.
2. Vercel: import the repo with root directory `web`, then deploy.
3. Tag the release: `git tag -a v1.0.0 -m "SLUICE v1.0.0"`, then `git push origin v1.0.0`.

**Commit hint:** `docs(release): results, README, interview notes, live demo`
