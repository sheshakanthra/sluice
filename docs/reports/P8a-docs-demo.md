# P8a: Ship, docs and live demo half (no CI numbers): Report

| Field | Value |
|---|---|
| Status | DONE for the P8a scope: README skeleton, INTERVIEW.md, DEMO-SCRIPT.md, a verified live demo path (`task demo` + `task tunnel`) and the Vercel steps. **Not in P8a, by design:** every CI number, `docs/results/RESULTS.md`, `task verify:results`, `docs/RESUME.md` and the replay data swap (P8b, which needs the CI run). **The demo rush is deferred to P8b** (Sheshakanth's scope call this session, ADR 0016) |
| Date (IST) | 2026-11-26 |
| Time spent vs timebox | Not timed precisely. The command logs span about 12:50–13:20 IST for the demo, tunnel and gate runs, and the docs work came before and after that. P8's 4 h timebox covers P8a + P8b |
| Commit base | `2af2f4f` (`git rev-parse --short HEAD`) |

## 1. What was built

### Live demo path (it was stubbed; now it works end to end)
- **`task demo`** (`Taskfile.yml`) runs `node deploy/scripts/demo-env.mjs`, then `task web:build`, then `task stack:up ENV=demo` (build, migrate, seed, 3 API replicas, worker, MockPay, Caddy :8000, Prometheus, Grafana), then prints the URLs.
- **`task tunnel`** runs `cloudflare/cloudflared:2026.11.2` on the compose network with `tunnel --no-autoupdate --url http://caddy:8000`. A precondition checks that `sluice-caddy-1` is running. Nothing is installed on the host, no account is needed, and only the front door can be exposed.
- **`deploy/env/demo.env.example`** is the demo profile:
  - `APP_ENV=demo`, test endpoints off, queue enforced, baseline off, rate limits on, no pprof;
  - production TTLs;
  - same-origin `MOCKPAY_PUBLIC_URL=/pay` and `PUBLIC_APP_URL=/`;
  - no random MockPay declines or late answers; duplicate and reordered webhooks on;
  - secrets as the fail-closed placeholder `generate-with-task-demo`.
- **`deploy/scripts/demo-env.mjs`** writes the gitignored `deploy/env/demo.env` once, with 32 random bytes for each of TOKEN_KEYS, ADMIN_TOKEN, MOCKPAY_API_KEY and MOCKPAY_WEBHOOK_SECRET. Later runs keep it. It prints no secret.
- **Fixes found by running the flow:**
  1. **Same-origin public URLs.** `internal/config/config.go:publicURL` now accepts an http(s) URL with a host **or** a same-origin absolute path, and refuses `//host` and `/\host`. `internal/mockpay/checkout.go:returnURL` accepts the same set. Before this, the spec'd `MOCKPAY_PUBLIC_URL=/pay` failed config, and MockPay dropped any non-absolute return URL. Behind a tunnel whose host is random, absolute URLs would have sent visitors back to `localhost`.
  2. **`/pay/metrics` was public.** Caddy's `handle_path /pay/*` proxied MockPay's Prometheus metrics to anyone on :8000, and so through the tunnel. `deploy/caddy/Caddyfile` now answers 404 for `/pay/metrics*`. Prometheus still scrapes `mockpay:8090` in-network.
  3. **Generator idempotence bug** (found on the second run): the "placeholder left in" check matched the header comment and would have refused every re-run. It now checks `KEY=value` lines only.

### Docs
- **`README.md`:**
  - a 3-line hero and the problem;
  - the four mechanisms with the ARCHITECTURE mermaid diagram, and the I1–I8 + R1 table;
  - run locally (`task demo`, and the dev loop `task up`, `migrate`, `seed`, `dev:*`), the CI rush and the laptop rush, chaos, and the live demo;
  - structure, ADR table, limitations and 10× scaling;
  - a **Results section with explicit "filled in P8b from CI run `<URL>`" placeholders**;
  - one laptop-labelled table (the P5b baseline matrix) linked to its report, with numbers re-read from `summary.json` (§4).
- **`docs/INTERVIEW.md`:**
  - the 60-second pitch and the design story;
  - 15 hard questions grounded in code (file and function): Postgres vs Redis and R1, holds/TTL/fencing vs Redlock, idempotency and crash recovery, PSP timeout-after-commit, webhook duplicates and reordering, late payment vs expiry, lottery fairness, the admission idle lease, shedding and readiness, coordinated omission, swarm + k6, the auditor, CI caveats, 10× and real payments;
  - the honest misses, each with what was done and its ADR or report: Lighthouse, laptop latency noise, the admission leak, the 20k cascade, plus smaller finds.
- **`docs/DEMO-SCRIPT.md`:** a 3-minute walkthrough (replay Rush Control → Results → a live booking through the tunnel → Grafana → close) using the real on-screen labels, with a recovery table. The demo rush step is left out and the script says why.
- **`docs/adr/0016-live-demo-path.md`:** the decisions above.
- **Specs kept true:**
  - `docs/DEPLOY.md`: the B commands, cloudflared in Docker, demo profile details, the demo rush marked P8b, the security checklist notes, Vercel settings and what the replay site shows now;
  - `docs/STACK.md`: the cloudflared pin, layout, and the `MOCKPAY_PUBLIC_URL`, `PUBLIC_APP_URL` and `DEMO_MODE` rows;
  - `docs/API.md`: healthz mode, `/demo/rush` "not built yet", `checkout_url` and `return_url` rules;
  - `docs/ARCHITECTURE.md`: "demo bots: P8b" in the diagram.

## 2. Acceptance checklist
P8a items from Sheshakanth's brief, then the P8 phase-file items this half touches.

- [x] **README skeleton with no invented numbers.** The Results table has only "_filled in P8b_" cells. The one numeric table is labelled laptop, linked to `docs/reports/P5b-rush.md` §4.1, and re-derived from `docs/results/p5b/*/summary.json` (§4).
- [x] **docs/INTERVIEW.md** has the pitch, the design story, the requested hard questions and the honest misses with their ADRs and reports. Every number in it is quoted from a report that shows its command.
- [x] **docs/DEMO-SCRIPT.md** has 3 minutes of real UI labels (checked against `web/app/*`, `web/components/*`, `internal/mockpay/checkout.go`) and real commands.
- [x] **`task demo` works on this machine.** `task demo` → exit 0, and every container healthy (`sluice-caddy-1 ... Up 29 seconds (healthy) 0.0.0.0:8000->8000/tcp`, plus api1–3, worker, mockpay, postgres, redis, prometheus and grafana healthy; migrate and seed exited 0). `docker inspect sluice-api1-1` env: `APP_ENV=demo`, `TEST_ENDPOINTS_ENABLED=false`, `MOCKPAY_PUBLIC_URL=/pay`, `PUBLIC_APP_URL=/`, `PPROF_ADDR=`. A second `node deploy/scripts/demo-env.mjs` → `deploy/env/demo.env exists: keeping its secrets`. `git check-ignore -v deploy/env/demo.env` → `.gitignore:5:deploy/env/*.env`.
- [x] **Booking works locally on the demo profile.** `E2E_BASE_URL=http://localhost:8000 pnpm exec playwright test --project=live e2e/e1-booking.spec.ts e2e/e2-decline.spec.ts` → `2 passed (24.3s)`. E1 was hosted MockPay checkout → ticket (`SLC-Z2QK-2Z88, SLC-3CYZ-CK2M`). E2 was decline → new attempt → ticket. axe found 0 serious or critical issues on every page.
- [x] **Cloudflare Quick Tunnel works end to end.** `task tunnel` → `Your quick Tunnel has been created!` and `Registered tunnel connection ... location=maa01`, URL `https://recruiting-bass-film-directive.trycloudflare.com` (dead now; the URL changes every run).
  - Through the public URL: `/api/v1/healthz` → `{"mode":"live","status":"ok"}`, `/` 200, `/pay/readyz` 200, `/api/v1/events/quiet-tuesday` 200, `POST /api/v1/test/admit` 404.
  - CSP and `X-Frame-Options: DENY` are present.
  - `E2E_BASE_URL=<tunnel URL> pnpm exec playwright test --project=live e2e/e1-booking.spec.ts e2e/e2-decline.spec.ts` → `2 passed (40.9s)`, E1 ticket codes `SLC-9Z5R-ZX96, SLC-5233-Q338`.
- [x] **Nothing internal is behind the tunnel.**
  - Before the fix, `/pay/metrics` → 200 with `# HELP go_gc_duration_seconds`. After the Caddy fix it's 404.
  - `/grafana/` and `/metrics` return the SPA shell (`<title>SLUICE · Rush-proof ticket booking</title>`, 0 `go_goroutines` lines).
  - `/pay/debug/pprof/`, `/api/metrics` and `/api/debug/pprof/` → 404; `/pay/v1/intents` → 401.
  - Prometheus targets after the fix: `sluice-mockpay http://mockpay:8090/metrics up`.
  - `caddy validate` → exit 0.
- [x] **Per-IP limits see the real visitor.** After one `POST /api/v1/sessions` through the tunnel and one locally, the Redis limiter keys were `rate:sl:rl:sessions:ip:14.96.<redacted>` (the public IP, through Cloudflare's `X-Forwarded-For`) and `...:ip:172.27.<redacted>` (the Docker gateway, local). IPs are redacted here on purpose.
- [x] **REPLAY works from `web/out` with no backend.** `pnpm exec playwright test --project=replay` → `9 passed (19.7s)` (E5 on every route, axe 0 serious or critical).
- [x] **Vercel steps listed, nothing deployed, no login** (§5).
- [x] **`task lint`** → `0 issues.`
- [x] **`task test:race`** (gcc present, so race was verified locally) → exit 0, 21 packages `ok`, no `DATA RACE` (§3).
- [x] **ADR for every decision:** ADR 0016.
- [ ] **P8 phase file, P8b items (not attempted, by design):** `task verify:results`, `docs/results/RESULTS.md`, replay data from the CI run, `docs/RESUME.md`, a demo rush with ≤ 3,000 bots, and SPEC §9 ✅. They all need the CI headline run or the deferred demo rush.

## 3. Tests
| Suite | Count | Result | Notes |
|---|---|---|---|
| unit + integration (`task test:race`) | 21 packages | all `ok`, exit 0 | testcontainers against Postgres 18 / Redis 8; `internal/orders` 416.4 s |
| race (`-race`) | same run | no data races | local (gcc available) |
| new unit tests | `TestPublicURLsAcceptSameOriginPaths`, `TestDemoEnvExample`, 5 new `TestLoadRejects` cases, `TestReturnURL` (9 cases) | pass | |
| e2e live (Playwright) | E1 + E2, run twice (localhost:8000 and the tunnel URL) | 2/2 each | against `ENV=demo` |
| e2e replay (Playwright) | 9 | 9/9 | `web/out` via `serve-static.mjs` |

Coverage for the packages touched this phase (`go test -short -cover`): config 96.5%; mockpay 5.8% with `-short` (its integration tests are skipped in short mode, but they ran and passed in the race run above). inventory, orders and queue weren't touched.

E1 records a video and copies it over the committed `web/public/results/latest/booking.webm`. I backed the file up before each E1 run and copied it back afterwards. `sha256sum` was `39b2b472...c4166b` before and after both runs, and `git status` doesn't list the file.

## 4. Numbers
P8a produced no new performance numbers. The README and INTERVIEW quote numbers only from earlier reports. The README's laptop table was re-read from the committed summaries with this command (the field list inside `join` is abbreviated here; it is the column list below):
```
for r in p5b-smoke p5b-b0 p5b-b1 p5b-b2; do node -e "const s=require('./docs/results/p5b/$r/summary.json'),a=s.audit; console.log([...].join(' | '))"; done
p5b-smoke | SMOKE | 5000 | local | 570aefc | 0 | 0 | 0 | 0 | 0 | 329.7
p5b-b0 | B0 | 10000 | local | 570aefc | 17 | 19 | 0 | 0 | 1 | 57
p5b-b1 | B1 | 10000 | local | 570aefc | 0 | 27 | 0 | 0 | 1 | 322
p5b-b2 | B2 | 10000 | local | 570aefc | 0 | 0 | 0 | 0 | 0 | 266.8
```
(columns: run, scenario, users, runner, sha, oversold, duplicate charges, orphan charges, unresolved, audit exit, checkout p95 ms). These match `docs/reports/P5b-rush.md` §4.1. The README doesn't show their latencies.

## 5. Vercel: the clicks (Sheshakanth does these after pushing; nothing was deployed or logged into)
1. https://vercel.com → **Log in with GitHub** (Hobby plan; no card).
2. **Add New… → Project** → **Import** `sheshakanthra/sluice` (grant the Vercel GitHub app access to that repo only).
3. **Configure Project:**
   - **Project Name:** `sluice` (gives `https://sluice.vercel.app`, or the closest free name).
   - **Framework Preset:** Next.js (auto-detected).
   - **Root Directory:** click *Edit* → choose `web` → *Continue*.
   - **Build and Output Settings:** leave every override off. Install comes from `web/pnpm-lock.yaml` and `packageManager: pnpm@11.9.0`, the build is `next build`, and `output: 'export'` makes Vercel serve `out/`.
   - **Node.js version** (Project → Settings → General after creation): 24.x, matching `engines.node >=24`.
   - **Environment Variables:** **none.** Don't add `NEXT_PUBLIC_API_BASE_URL`: with it unset, the app probes same-origin `/api/v1/healthz`, which 404s on Vercel, so it switches to REPLAY.
4. **Deploy.** Later pushes to `main` redeploy automatically.
5. **Check:** `/` shows the landing page; `/control/` shows "Replay of local run p5b-smoke · … · commit 570aefc" with the 1× / 4× / 10× timeline; `/results/` shows the runner as "Local laptop (Docker Desktop)"; `/queue/?e=quiet-tuesday` shows "This is the replay site, so … isn't live here". Also verify the booking video seeks to `#t=1`. P6b couldn't verify that locally because `serve-static` doesn't support Range requests; Vercel does.

**What the replay site shows before P8b:** the P5b **laptop** SMOKE run (`p5b-smoke`, 5,000 users, `source.kind: "local"`, note "Local SMOKE run (P5b), a placeholder until the CI headline run replaces it in P8") and the P5b baseline matrix. Every number on it comes from those `summary.json` files, and the UI labels it local. P8b swaps in the CI headline run.

## 6. Deviations and decisions
| What changed vs the spec | Why | ADR |
|---|---|---|
| `MOCKPAY_PUBLIC_URL` / `PUBLIC_APP_URL` accept same-origin paths; MockPay `return_url` too | The spec'd `/pay` failed config; the tunnel host is random | 0016 |
| cloudflared runs from `cloudflare/cloudflared:2026.11.2` on the compose network instead of a `winget` install | Not installed here; Docker-only fits rule 2; it can only reach `caddy:8000` | 0016 |
| Caddy 404s `/pay/metrics*` | MockPay metrics were public through the front door | 0016 |
| Demo profile: random MockPay faults off, webhook dup/reorder on | Predictable visitor booking; faults are on-demand buttons | 0016 |
| Demo rush and `/healthz` `mode: "demo"` deferred to P8b; `DEMO_MODE` is set but nothing reads it | Sheshakanth's scope call this session | 0016 |
| `task demo` uses the full stack (3 replicas, Prometheus, Grafana) as DEPLOY.md says | No change; recorded because "keep the machine light" was asked. It was up only for the verification, then `task stack:down ENV=demo` | — |

## 7. Known issues and debt
- **Demo rush not built** (P8b): `POST /api/v1/demo/rush`, the worker job reusing `internal/rush` (≤ 3,000 bots, 2-min cooldown, 10-min abort), `sl:{e:<eid>}:demo` metrics, `/healthz` `mode: "demo"`, and the concurrency tests.
- **Infra ports are published on all host interfaces** (Postgres 5433 with dev credentials, Redis 6381, Prometheus 9090, Grafana 3001 with admin/admin). They're never behind the tunnel, but anyone on the same LAN can reach them. This is pre-existing. A fix, binding them to `127.0.0.1:` in `deploy/compose.yml`, is a one-line-per-port change for Sheshakanth to approve; it would affect anything that reaches them from another host.
- **The demo database accumulates bookings** on `quiet-tuesday` across runs (this session ran E1 and E2 twice, and each booking takes seats). `task stack:down ENV=demo -- -v` resets it.
- Not run this session: `gitleaks` (P8b final pass) and Lighthouse (unchanged since P6b; no web code changed).

## 8. Understand this before moving on (Sheshakanth's interview prep)
1. **Q:** Your hosted checkout redirects back to the app. How does that work behind a tunnel whose hostname you don't know until it starts?
   **A:** Both public URLs are same-origin paths in the demo profile (`/pay`, `/`). `internal/config/config.go:publicURL` accepts a path that starts with exactly one `/`, `orders.Service.returnURL` builds `/order?o=<id>`, and `internal/mockpay/checkout.go:returnURL` keeps it relative, so the 303 `Location` is a path the browser resolves against whatever host it's on. `//evil.test` is refused in both places, because browsers treat it as another origin: an open redirect.
2. **Q:** How do you know the tunnel doesn't leak Grafana or Postgres?
   **A:** cloudflared runs in a container on the compose network and is told one URL, `http://caddy:8000`. Everything it serves is whatever Caddy routes. I probed the internal paths through the public URL: `/pay/metrics` was a real leak (MockPay's Prometheus output), now a 404 in the Caddyfile; `/grafana/` and `/metrics` hit the SPA fallback; pprof doesn't exist on the routed ports.
3. **Q:** Rate limits are per IP. Through Cloudflare and Caddy, whose IP do you limit?
   **A:** The visitor's. Cloudflare's edge puts the client IP in `X-Forwarded-For`; cloudflared and Caddy are private-range peers in `TRUSTED_PROXIES`; the API takes the right-most entry that isn't a trusted proxy. I verified it from the limiter's Redis keys: tunnel traffic keyed on the public IP, local traffic on the Docker gateway.

## 9. Files changed (`git status --short`)
```
Code / config
 M Taskfile.yml
 M deploy/caddy/Caddyfile
?? deploy/env/demo.env.example
?? deploy/scripts/
 M internal/config/config.go
 M internal/config/config_test.go
 M internal/mockpay/checkout.go
 M internal/mockpay/format_test.go
Docs
 M README.md
 M docs/API.md
 M docs/ARCHITECTURE.md
 M docs/DEPLOY.md
 M docs/STACK.md
?? docs/DEMO-SCRIPT.md
?? docs/INTERVIEW.md
?? docs/adr/0016-live-demo-path.md
?? docs/reports/P8a-docs-demo.md
```
`deploy/env/demo.env` (real secrets) is gitignored and not listed.

## 10. Next phase readiness (P8b)
1. Sheshakanth: commit and push (block below), then wait for the in-progress CI `rush` run (H, 50,000) to go green.
2. Download artifact `rush-<run_id>` into `loadtest/results/<run_id>/` and send the run URL. P8b then does RESULTS.md, `task verify:results`, the replay data swap, the README numbers, RESUME.md, the demo rush and the final pass (gitleaks, SPEC §9).
3. Vercel import (§5) can happen right after this push. It shows the laptop replay until P8b.

## 11. Git commands (Sheshakanth runs these; PowerShell- and bash-safe)
```bash
git add -A
git status --short
git commit -m "docs(release): README, interview notes, demo script, live demo path" -m "- task demo and task tunnel: demo profile with generated secrets, cloudflared in Docker" -m "- Same-origin MOCKPAY_PUBLIC_URL and PUBLIC_APP_URL so checkout works behind a tunnel" -m "- Caddy no longer serves MockPay metrics on the front door" -m "- README skeleton with P8b result placeholders, INTERVIEW.md, DEMO-SCRIPT.md, ADR 0016" -m "Phase P8a - report: docs/reports/P8a-docs-demo.md"
```
