# P0: Bootstrap and guardrails: Report

| Field | Value |
|---|---|
| Status | DONE |
| Date (IST) | 2026-11-23 |
| Time spent vs timebox | about 2h35m / 1h30m (over the 1.5x mark of 2h15m; causes in §6) |
| Commit base | `36f12da` (`git rev-parse --short HEAD`) |

## 1. What was built
- **Tooling (Step 0/1):** Task 3.54.0, golangci-lint v2.15.0 (official release zips, SHA-256 verified against the published checksum files), govulncheck v1.10.0 (`go install`), UI UX Pro Max via `uipro init --ai claude` (`.claude/skills/ui-ux-pro-max/`, gitignored).
- **Repo hygiene:** `.gitignore`, extended `.gitattributes`, `.editorconfig`, `LICENSE` (MIT, Sheshakanth), `.env.example` (all 45 STACK.md §4 variables), `loadtest/results/.gitkeep`.
- **Go module** `github.com/sheshakanthra/sluice` (`go 1.27.0`, `toolchain go1.27.4`):
  - `cmd/sluice`: subcommands `api | worker | mockpay | migrate | seed | rush | audit | version`; unimplemented ones print `not implemented in P0` and exit 2; SIGINT/SIGTERM via `signal.NotifyContext`.
  - `internal/config`: typed config, fail-fast validation reporting every problem at once; `BASELINE`, `TEST_ENDPOINTS_ENABLED` and `QUEUE_ENFORCED=false` are refused unless `APP_ENV` is `rush` or `test`; the dev token key and admin token are refused when `APP_ENV=demo`; `Keys()` records every variable the loader reads (used by the `.env.example` test).
  - `internal/obs`: slog JSON logger that adds `request_id` / `user_id` / `event_id` from the context, a non-global Prometheus registry, and a pprof server on its own mux (only when `PPROF_ADDR` is set).
  - `internal/httpx`: router on stdlib `ServeMux` with a bounded route label, middleware (request id with trusted-proxy check, recover, timeout, access log + RED metrics, CORS), RFC 9457 problem+json with the API.md §1.3 catalogue (+ `INTERNAL`, ADR 0003), `/healthz`, `/api/v1/healthz`, `/readyz`, and a server with `ReadHeaderTimeout` 5 s, `IdleTimeout` 120 s and graceful shutdown.
  - `internal/app`: wiring for `sluice api` (lazy pgx pool, go-redis client, `/metrics`, optional pprof).
  - `internal/buildinfo`: version/commit via `-ldflags -X`, falling back to the VCS stamp, then `unknown`.
- **Infra** `deploy/compose.yml`: `postgres:18.7` (volume at `/var/lib/postgresql`, `pg_isready` healthcheck, `01-mockpay-db.sql`), `redis:8.10.3` (AOF everysec, `noeviction`), `prom/prometheus:v3.16.0` (scrapes host api/worker/mockpay), `grafana/grafana:13.3.1` (anonymous Viewer, provisioned Prometheus datasource). Host ports 5433, **6381** (ADR 0004), 9090, 3001.
- **Taskfile.yml:** `up`, `down`, `ps`, `build`, `version`, `dev:api`, `gen` (stub), `lint`, `test`, `test:short`, `test:race`, `vuln`, `web:install`, `dev:web`, `web:build`, `web:lint`; `migrate`, `seed`, `dev:worker`, `dev:mockpay`, `stack:*`, `rush*`, `audit`, `e2e`, `chaos`, `demo`, `tunnel` are `TODO(Pn)` stubs that exit 1.
- **Lint:** `.golangci.yml` v2 (standard + bodyclose, contextcheck, errorlint, gocritic, gosec, noctx, revive, rowserrcheck, sqlclosecheck; gofmt + goimports formatters).
- **Web** `web/`: Next.js 16.4.6, React 19.3.0, Tailwind 4.3.3 (Turbopack loader), shadcn (`components.json`, `lib/utils.ts`, `components/ui/button.tsx`), static export (`output: 'export'`, `trailingSlash`, `images.unoptimized`), tokens in `app/globals.css`, Barlow + Barlow Condensed via `next/font`, the signature `components/ticket-stub.tsx`, the landing page, Prettier, and `scripts/screenshots.mjs` (Playwright review).
- **Design system:** `design-system/MASTER.md` (generated with the DESIGN-BRIEF §2 query, plus **Direction notes**), `design-system/contrast.py`.
- **CI** `.github/workflows/ci.yml`: Go job (setup-go from `go.mod`, Task, golangci-lint, `task test:race` incl. testcontainers, govulncheck, stamped build) and web job (pnpm from `packageManager`, lint, `format:check`, typecheck, build, `out/index.html` check).
- **Docs:** STACK.md §1.1 pinned versions; ADR 0001 (stack), 0002 (design-system path), 0003 (`INTERNAL` code), 0004 (Redis port); API.md §1.3, DESIGN-BRIEF §1/§2/§9/§10, P0 and P6 phase files, both KICKOFF.md copies updated.

## 2. Acceptance checklist
- [x] **`task up` → all containers healthy.** `task up` →
  ```
  sluice-grafana-1      grafana/grafana:13.3.1    ... Up 58 seconds (healthy)       0.0.0.0:3001->3000/tcp
  sluice-postgres-1     postgres:18.7             ... Up About a minute (healthy)   0.0.0.0:5433->5432/tcp
  sluice-prometheus-1   prom/prometheus:v3.16.0   ... Up About a minute (healthy)   0.0.0.0:9090->9090/tcp
  sluice-redis-1        redis:8.10.3              ... Up About a minute (healthy)   0.0.0.0:6381->6379/tcp
  ```
  Extra checks: `psql ... select datname` → `mockpay`, `sluice`; `show fsync` → `on`; `show synchronous_commit` → `on`; `redis-cli config get appendonly/appendfsync/maxmemory-policy` → `yes` / `everysec` / `noeviction`; Grafana anonymous `GET /api/datasources/uid/prometheus` → `Prometheus http://prometheus:9090`; Prometheus `up` → `sluice-api 1` while `task dev:api` ran.
- [x] **`task dev:api`, `/healthz` 200, `/readyz` 200 with infra up and 503 after `task down`.**
  ```
  curl -s -i localhost:8080/healthz           → HTTP/1.1 200 OK  {"status":"ok"}
  curl localhost:8080/api/v1/healthz          → {"mode":"live","status":"ok"} [200]
  curl localhost:8080/readyz   (infra up)     → {"checks":{"postgres":"ok","redis":"ok"},"status":"ready"} [200]
  task down; curl -i localhost:8080/readyz    → HTTP/1.1 503 Service Unavailable
                                                 Content-Type: application/problem+json, Retry-After: 1
                                                 {"checks":{"postgres":"unavailable","redis":"unavailable"},"code":"DEPENDENCY_UNAVAILABLE",...}
  curl localhost:8080/healthz  (infra down)   → {"status":"ok"} [200]
  ```
- [x] **`task lint test` green, `task test:race` green.** `task lint test:race` → `0 issues.` and `ok` for all 6 packages (`cmd/sluice`, `internal/app`, `internal/buildinfo`, `internal/config`, `internal/httpx`, `internal/obs`). gcc 16.2.0 (MinGW-w64 UCRT) is installed, so **race was verified locally** (not CI-only). The final rerun served 5 of 6 packages from Go's test cache (inputs unchanged since the uncached `task test:race` run earlier in the session, which also passed all 6).
- [x] **`task web:build` produces `web/out/index.html`.** → `✓ Compiled successfully`, routes `○ /`, `○ /_not-found`; `ls -la web/out/index.html` → 32445 bytes.
- [x] **`design-system/MASTER.md` exists and includes Direction notes** (renamed from "Brand overrides" by the Fixes in §11).
- [x] **`go run ./cmd/sluice version` prints version and commit.** → `sluice unknown (commit unknown, go1.27.4)` (dev, unstamped); `task version` → `sluice dev (commit 36f12da, go1.27.4)` (via `-ldflags`).
- [x] **`.env.example` covers every env var the config reads.** `internal/config/config_test.go`: `TestEnvExampleCoversConfig` (every key `Keys()` records is in `.env.example`, nothing stale, and the example values load) and `TestEnvExampleMatchesStackDoc` (`.env.example` ↔ STACK.md §4) both pass.
- [x] **No mutating git command was run during the session.** Only `git status --short`, `git status --short -uall`, `git rev-parse --short HEAD`, `git check-ignore -v` (read-only) and `git rev-parse` inside Taskfile vars.

Extra gates: `govulncheck ./...` → `No vulnerabilities found.`; `task web:lint` → ESLint clean, `All matched files use Prettier code style!`, `✓ Types generated successfully` + `tsc --noEmit` clean.

## 3. Tests
| Suite | Count | Result | Notes |
|---|---|---|---|
| unit | 26 top-level tests (104 runs incl. table subtests, `go test -short -v ./... \| grep -c "^=== RUN"`) | pass | config, httpx, obs, buildinfo, cmd |
| integration (testcontainers) | 1 (`TestReadyzAgainstRealDependencies`) | pass (16.81 s) | real `postgres:18.7` + `redis:8.10.3`; 200 → stop Redis → 503 |
| race (`-race`) | all 6 packages | pass | local (gcc present) and in CI |
| property (rapid) | 0 | n/a | starts in P2 |
| e2e (Playwright) | 0 | n/a | P6; P0 uses Playwright only for the screenshot review |

Top-level PASS count: `go test -v ./... | grep -E "^--- (PASS|FAIL|SKIP)"` → `27 PASS` (26 unit + 1 integration).

Coverage (`go test -cover ./...`): cmd/sluice 60.7%, internal/app 43.2%, internal/buildinfo 63.6%, internal/config 97.6%, internal/httpx 92.7%, internal/obs 90.0%. (Core packages inventory/orders/queue don't exist yet.)

## 4. Numbers
None: P0 produces no performance numbers. Contrast ratios are design data, produced by `python design-system/contrast.py` and recorded in MASTER.md's Direction notes.

### Pinned versions
| Component | Version |
|---|---|
| Go | go1.27.4 (`go 1.27.0` + `toolchain go1.27.4`) |
| Node / pnpm | v24.21.0 / 11.9.0 |
| Next / React / Tailwind | 16.4.6 / 19.3.0 / 4.3.3 |
| Postgres / Redis | `postgres:18.7` / `redis:8.10.3` |
| Prometheus / Grafana | `v3.16.0` / `13.3.1` |
| pgx / go-redis / client_golang / uuid / testcontainers-go | v5.13.0 / v9.23.0 / v1.25.0 / v1.6.0 / v0.45.0 |
| golangci-lint / govulncheck / Task | v2.15.0 / v1.10.0 / 3.54.0 |
| Prettier / Playwright | 3.10.1 / 1.65.1 |

### Placeholder page (screenshot description)
Files: `docs/reports/assets/p0/landing-{375,1440}.png` (+ `-reduced-motion` variants). Light pale-blue paper page under a navy platform-sign header ("SLUICE" wordmark in spaced Barlow Condensed with a breathing orange signal dot; "PLATFORM P0 · FOUNDATIONS"). Left: an orange "TATKAL OPENS AT" sign label above a huge navy `10:00:00 IST` set in Barlow Condensed with tabular figures, the headline "Fifty thousand people. Five hundred seats. One honest queue.", a short paragraph, an accent-strong "See how it holds up" button and an outlined "Source on GitHub" button. Right (below on mobile): the signature ticket stub, a white card ("RUSH TICKET · DEMO", Chennai → Bengaluru, "Train 40500 · Sluice Demo Special · 500 seats") split by a dashed tear line with punched notches, then `50,000` people in the rush, `0` oversold seats and `0` double charges, and a sunk footer band with the `50,000 · 0 · 0` strip and "Targets until a public CI run proves them". Below: "The route", a navy railway line with four numbered stops (Waiting room / Seat hold / Payment / Audit), each with a guarantee chip, ending at an orange destination stop.

## 5. Deviations and decisions
| What changed vs the spec | Why | ADR |
|---|---|---|
| Design system flattened from `design-system/sluice/` to `design-system/` | The skill always nests under the project slug; every doc references `design-system/MASTER.md` | 0002 |
| Added `INTERNAL` (500) problem code | Recover middleware needs a catalogued code; business outcomes still never 5xx (tested) | 0003 |
| Redis host port 6380 → **6381** | Another local project's container holds 6380; Sheshakanth chose to move SLUICE | 0004 |
| Brand palette replaced by a skill-owned direction ("Swiss transit signage, light") | Sheshakanth's P0 fix request (see §11) | Direction notes in MASTER.md; DESIGN-BRIEF §1/§2/§10 |
| `QUEUE_ENFORCED=false` refused outside rush/test; dev token key and admin token refused when `APP_ENV=demo`; `PAYMENT_HOLD_TTL >= HOLD_TTL` | Fail-fast safety in the spirit of STACK.md §4 ("rush/test may disable"); the demo is public | none (stricter validation, no behaviour the specs rely on) |
| `/readyz` checks Postgres + Redis only | API.md §2 also mentions "migrations applied"; migrations arrive in P1, which must add that check | none (P1 task) |
| Task and golangci-lint installed from release binaries, not `go install` | `go install` of Task stalled downloading the 51 MB `u-root` module at ~156 KB/s; the release zips were SHA-256 verified | none |
| `typecheck` script runs `next typegen` first | `LayoutProps` is a Next-generated global; CI typechecks before building | none |
| Screenshot review uses installed Microsoft Edge (`channel: "msedge"`) on Windows | Avoids a ~150 MB Chromium download on a slow link; CI/Linux uses bundled Chromium | none |

## 6. Known issues and debt
- **Timebox overrun:** about 2h35m against 1h30m. Causes: slow network (~156 KB/s; a `go install` stalled for 20+ minutes and a `pnpm install` hung idle until restarted with default timeouts), two session interruptions under memory pressure (about 2 GB free, with other projects' containers running), and the design-direction rework requested before closing P0.
- `/readyz` doesn't yet check "migrations applied" (P1).
- Grafana has no dashboards yet (the provisioning folder is ready; P5 adds them).
- `web/components/ui/button.tsx` (from `shadcn init`) isn't used by the landing page yet; P6 adopts it with the brand tokens.
- `web/pnpm-workspace.yaml` (from create-next-app) sets `allowBuilds: sharp: false, unrs-resolver: false`. Neither is needed for a static export with unoptimized images.
- CI hasn't run yet (needs a push).

## 7. Understand this before moving on (Sheshakanth's interview prep)
1. **Q:** Why are `/healthz` and `/readyz` different, and why does the API start even when Postgres is down?
   **A:** `/healthz` is liveness: it only proves the process serves HTTP, so an orchestrator never kills a healthy process because a dependency blipped (`internal/httpx/health.go:RegisterHealth`). `/readyz` pings Postgres and Redis concurrently with a 2 s budget each and returns 503 `DEPENDENCY_UNAVAILABLE` with `Retry-After` so a load balancer stops routing to it (`runChecks`). The pgx pool is created lazily (`internal/app/api.go:NewPGPool`), so a restart during an outage doesn't crash-loop; it reports not-ready until the database returns.
2. **Q:** Your metrics are labelled by route. Why not by URL path?
   **A:** Paths contain IDs (`/orders/0193...`), so labelling by path creates one time series per order: unbounded cardinality that can take Prometheus down during a rush. `internal/httpx/router.go:Router.Handle` records the registered pattern (`GET /api/v1/orders/{orderId}`) into a `routeHolder` pointer placed in the context by `Observe`, so the label set is bounded and unmatched requests collapse into `unmatched` (`TestMetricsUseRoutePattern`).
3. **Q:** How do you stop someone accidentally running the naive baseline on the public demo?
   **A:** Config is validated fail-fast at startup (`internal/config/config.go:Config.validate`): `BASELINE != off`, `TEST_ENDPOINTS_ENABLED=true` and `QUEUE_ENFORCED=false` are refused unless `APP_ENV` is `rush` or `test`, and with `APP_ENV=demo` the dev signing key and admin token are refused. `sluice api` exits 1 with every problem listed (`cmd/sluice/main.go:runService`, `TestRunAPIFailsFastOnBadConfig`). Because `config.Keys()` records each variable as it is read, `.env.example` can't drift from the code.

## 8. Files changed
`git status --short` (`web/node_modules`, `web/out`, `web/.next`, `.env` and the skill folder are ignored):
```
 M .gitattributes
 M KICKOFF.md
 M docs/API.md
 M docs/DESIGN-BRIEF.md
 M docs/KICKOFF.md
 M docs/STACK.md
 M docs/phases/P0-bootstrap.md
 M docs/phases/P6-frontend.md
?? .editorconfig
?? .env.example
?? .github/
?? .gitignore
?? .golangci.yml
?? LICENSE
?? Taskfile.yml
?? cmd/
?? deploy/
?? design-system/
?? docs/adr/0001-stack.md
?? docs/adr/0002-design-system-path.md
?? docs/adr/0003-internal-error-code.md
?? docs/adr/0004-redis-host-port.md
?? docs/reports/assets/
?? go.mod
?? go.sum
?? internal/
?? loadtest/
?? web/
```
(plus this report, `docs/reports/P0-bootstrap.md`). By area: Go (`cmd/`, `internal/`, `go.mod`, `go.sum`), infra (`deploy/`, `Taskfile.yml`), quality (`.golangci.yml`, `.github/workflows/ci.yml`), web (`web/`), design (`design-system/`, `docs/reports/assets/p0/`), docs (`docs/`, `KICKOFF.md`), hygiene (`.gitignore`, `.gitattributes`, `.editorconfig`, `.env.example`, `LICENSE`, `loadtest/results/.gitkeep`).

## 9. Next phase readiness
- P1 (schema) can start: Postgres 18.7 runs on 5433 with `sluice` and `mockpay` databases; testcontainers works locally; `task gen` is the sqlc hook.
- P1 must add the "migrations applied" check to `/readyz`.
- Sheshakanth: commit and push, then confirm both CI jobs go green on GitHub Actions (first CI run).
- Local note: SLUICE Redis is on **6381**; your other project keeps 6380.

## 10. Git commands (Sheshakanth runs these)
```bash
git add -A
git status --short
git commit -m "chore(bootstrap): repo skeleton, infra, CI, design system" -m "- Go module with sluice CLI, fail-fast config, slog, metrics, problem+json, health and readiness" -m "- Compose infra: Postgres 18.7, Redis 8.10.3 on 6381, Prometheus, Grafana; Taskfile and golangci-lint v2" -m "- CI for lint, race tests with testcontainers, govulncheck and the web build" -m "- Next.js static export with Swiss transit signage direction and ticket stub signature" -m "- Design system regenerated, direction notes, quality bar, ADRs 0001 to 0004" -m "Phase P0 - report: docs/reports/P0-bootstrap.md"
```

## 11. Fixes (design ownership and quality bar, applied before closing P0)
Requested by Sheshakanth before P0 closed. What changed:

1. **DESIGN-BRIEF §1:** removed the "Personal brand tokens" bullet and the "mission-control HUD" direction. Added that SLUICE has its own identity; UI UX Pro Max owns the whole visual system; the `#0B0F12` / `#00FFCC` / JetBrains Mono portfolio palette must never be used; no generic SaaS defaults (neon-on-black, gradients, glassmorphism, emoji icons, template heroes); all text meets WCAG AA. "Dark ships first" became "light or dark is part of the chosen direction".
2. **DESIGN-BRIEF §10 Quality bar** added (binding for P0 and P6), with pointers from §1 and §9. It went in as §10 so the §2/§3/§4/§7 references in other docs stay valid.
3. **DESIGN-BRIEF §2:** the P0 query is now `"ticket booking waiting room seat map high-demand rush"`. "Brand overrides" → "Direction notes" in DESIGN-BRIEF, P0-bootstrap.md, both KICKOFF.md copies and ADR 0002. JetBrains / `#0B0F12` mentions removed from P0-bootstrap.md and P6-frontend.md. P6 acceptance gained "Quality bar in DESIGN-BRIEF met; screenshot critique and fixes recorded in the report".
4. **MASTER.md regenerated:** `rm design-system/MASTER.md`, then the new query with `--persist`, flattened per ADR 0002. The raw output was Hotel/Hospitality: navy `#1E3A8A` + booking orange `#F97316` on `#EFF6FF`, Cormorant/Montserrat, "Liquid Glass", and an Event/Conference landing pattern. Two extra skill queries (`--domain style`, `--domain typography`) supplied the rest. **Chosen direction: "Swiss transit signage, light":** the generated navy + booking-orange palette, the skill's Minimalism & Swiss style, and its Barlow Condensed / Barlow pairing (drawn from transit signage, tabular figures). **Signature element: the ticket stub**, a card split by perforated tear lines that always carries the one number that matters right now (queue position, hold timer, seat, headline result). Rejected: Liquid Glass (banned glassmorphism, poor legibility), the luxury serif, the event-landing template, shadows, and `#3B82F6` as text (3.38:1 on paper). Recorded with a full contrast table in MASTER.md → Direction notes.
5. **Tokens and landing page rebuilt:** `web/app/globals.css` now maps the Direction-notes palette onto Tailwind `@theme` and the shadcn slots, with the perforation and signal primitives and a reduced-motion override. Fonts are Barlow / Barlow Condensed via `next/font`. `web/app/page.tsx` and `web/components/ticket-stub.tsx` are rebuilt to the quality bar.
6. **Grep check:** `grep -rniE "0B0F12|00FFCC|JetBrains" web design-system --exclude-dir=node_modules --exclude-dir=.next --exclude-dir=out` → no matches (exit 1). The same pattern over the build output `web/out web/.next` → no files listed.

### Screenshot critique (Playwright, 375 px and 1440 px)
Command: `cd web && pnpm build && pnpm screenshots` (Edge via Playwright 1.65.1; full-page captures plus `reducedMotion: "reduce"` passes; machine checks for horizontal overflow, touch targets < 44 px, and interactive heights off the 4 px half-grid).

**Round 1, findings:**
| # | Width | Finding (design review) | Category |
|---|---|---|---|
| 1 | both | **Bug:** the vertical tear line's top punch was drawn *inside* the stub, biting into the last "0" of 50,000; its bottom punch floated over the sunk band | correctness / signature element |
| 2 | both | The tear-line edge punches read as faint pale circles, not holes; the signature element was too timid | signature / contrast |
| 3 | 375 | The headline broke badly ("50,000 people. 500 / seats. One honest / queue."), splitting a number from its noun and orphaning "queue." | hierarchy / typography |
| 4 | both | "50,000" appeared three times in one viewport (headline, stub, strip), which reads as repetition rather than emphasis | hierarchy / "does this look like a template?" |
| 5 | 375 | "SEATS SOLD TWICE" wrapped to two lines while "DOUBLE CHARGES" didn't, so the two 0s sat at different heights | alignment / consistency |
| 6 | 1440 | A 12 px "IST" floated detached at the baseline of a 128 px clock | hierarchy |
| 7 | both | The wordmark link was a 24 px touch target (machine check: `a "SLUICE" 107x24`) | accessibility |
| 8 | 1440 | The route section's left column leaves empty space below its copy | spacing (accepted: it's a sticky column that travels with the route on scroll) |

**Fixes applied:**
1. Internal (vertical) tear lines no longer draw punches; holes appear only where a real ticket has them, at the card edge.
2. Edge punches got a 1 px `--rule` border, so the visible half reads as a cut-out arc.
3 and 4. The headline became "Fifty thousand people. Five hundred seats. One honest queue." with `text-balance`, which sets as three clean lines at both widths; the stub now owns the numerals, which is exactly the signature element's job.
5. The label became "Oversold seats" (same length as "Double charges"), so both columns set on one line and the 0s align.
6. "IST" is now set in the display face at 24/36 px and reads as part of the clock.
7. The wordmark link is `min-h-11` (44 px) with an `aria-label`.
- **Round 2 nits:** orphans ("twice.", "27.") fixed with `text-pretty` on body paragraphs and a non-breaking space in "seat 27".

**Final pass** (`pnpm screenshots`, after the fixes): horizontal overflow `0px` at 375 and 1440; touch targets < 44 px: `none`; interactive heights off the 4 px half-grid: `none`; fonts in use: `Barlow | "Barlow Condensed"` only.

### UI UX Pro Max pre-delivery checklist (landing page)
- [x] No emojis used as icons (lucide-react only)
- [x] All icons from one consistent set (Lucide)
- [x] `cursor-pointer` on all clickable elements
- [x] Hover states with smooth transitions (150 ms colour/border; arrow nudge on the primary CTA)
- [x] Light mode: text contrast ≥ 4.5:1 (every text token on paper/card measured by `contrast.py`; lowest is `--ink-subtle` 5.49 on paper)
- [x] Focus states visible for keyboard navigation (2 px navy outline, 4 px offset; white on the navy header)
- [x] `prefers-reduced-motion` respected (global override; the signal dot stops breathing; captured as `*-reduced-motion.png`)
- [x] Responsive at 375 and 1440 px (screenshots); 768 and 1024 px use the same breakpoints (`sm`, `lg`) and weren't separately captured in P0
- [x] No content hidden behind fixed navbars (the header isn't fixed)
- [x] No horizontal scroll on mobile (overflow `0px`)
