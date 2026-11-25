# P6b: Rush Control, Results, replay, e2e and budgets: Report

| Field | Value |
|---|---|
| Status | DONE, with one reported miss: Lighthouse mobile **Performance** is 62 / 62 / 71 against the ≥ 90 target (§4.2 has the cause). Every other acceptance item is green |
| Date (IST) | 2026-11-25 |
| Time spent vs timebox | Not tracked precisely. P6 is timeboxed at 7 h across P6a and P6b, and P6b ran over two sessions; the first was killed by a PC restart mid-run |
| Commit base | `4a0a82f` (`git rev-parse --short HEAD`) |

## 1. What was built
- **Step 0, admission lease renewal on `/availability`:** `internal/auth/http.go:Keyring.TouchAdmission` renews the idle admission lease when the seat-map poll carries a valid admission token for the event. It is best effort and never refuses, and `ADMISSION_TTL` stays the hard cap. It is wired in `internal/inventory/handler.go`, with test `internal/queue/admission_lease_test.go:TestAvailabilityPollRenewsLease`. This closes the P6a open item where a user browsing the map for more than 60 s got the rejoin screen. The ADR 0010 addendum (part 4) and API.md are updated.
- **Page overrides:** `design-system/pages/control.md` and `results.md`.
- **Routes:**
  - `/control` LIVE: `/stats` every 1 s.
  - `/control` REPLAY: a timeline scrubber (native range input, 1× / 4× / 10×, labelled stops) over the P5b SMOKE `timeseries`, labelled "Replay of local run p5b-smoke · … · commit 570aefc".
  - `/results`: headline numbers, methodology, lazy Recharts charts, baseline matrix and environment details.
  - `/event`, `/how`, and the landing page `/`.
  - `web/lib/replay.ts` plus `web/scripts/build-replay.mjs` build `web/public/results/latest/*` from the P5b SMOKE run. It is labelled with its run id, with no invented numbers.
- **JS budget rule change (ADR 0012):** the budget is the framework (`rootMainFiles`) plus at most 50 KB gzip of app code per route. `web/scripts/js-budget.mjs` is the check, run as `task web:budget`.
- **e2e (`web/e2e/`):** E1–E5 plus an axe pass on every route.
  - `deploy/env/rush.e3.env` is the short-TTL overlay for E3.
  - `task e2e` runs the build, then `stack:up`, then the live tests, then E3 under the overlay (restored by `defer`), then the replay tests.
  - `web/scripts/serve-static.mjs` is the no-API static host that puts the app in REPLAY mode.
- **Fixed in this session (after the restart):**
  - `e2e/helpers.ts:joinAndGetAdmitted`: the "Pick seats now" shortcut click had no timeout. When the 3-2-1 stub navigated mid-click, Playwright kept retrying against the detached button until the 120 s test timeout. It now uses a 2 s click timeout.
  - E1/E2 `waitForURL(/order…/)`: the MockPay return URL is `/order?intent=…&o=…`, so the regex no longer assumes `o=` comes first.
  - Replay `/control` axe: two serious issues.
    - `scrollable-region-focusable`: the event log is now a focusable, labelled `region` with a focus ring.
    - `target-size` (WCAG 2.5.8): the stop buttons are at least 24×24 px and moved clear of the range input's box. `app/control/page.tsx:replayStops` folds a stop that lies within 8 % of the timeline into the previous one, so "T0" and "First sale" (4 s apart) become "T0, first sale", and "Sold out" and "End" become "Sold out, end".
  - `serve-static.mjs`: gzip for text types (§4.2).
  - `web/.prettierignore`: Playwright's `test-results/` and `playwright-report/`.
  - Critique fixes: see §5.

## 2. Acceptance checklist
- [x] **E1–E5 pass; E1's video saved to `web/e2e/artifacts/booking.webm`.** These are separate runs against `task stack:up` (Caddy :8000). `task e2e` runs the same steps; they were run one at a time to fix failures between steps.
  - `pnpm --dir web exec playwright test --project live --grep-invert @expiry`:
    ```
    ✓ 1–6 [live] axe-live.spec.ts › axe live / , /event/?e=quiet-tuesday, /event/?e=tatkal-chn-sbc, /control/, /results/, /how/
    ✓  7 [live] e2-decline.spec.ts › E2 decline: not charged, new attempt, ticket (7.7s)
    ✓  8 [live] e4-soldout.spec.ts › E4 sold out: a bot takes the only seat, the person sees SOLD_OUT (7.1s)
    E1 ticket codes: SLC-2K2C-DGTB, SLC-D4WU-JKY4
    ✓  9 [live] e1-booking.spec.ts › E1 quiet event: ... ticket @booking (17.9s)
    9 passed (59.7s)
    ```
  - E3: API replicas and worker recreated with `SLUICE_SCENARIO=e3` (`docker inspect` showed `HOLD_TTL=10s`, `PAYMENT_HOLD_TTL=10s`), then `--grep @expiry`:
    ```
    axe checkout (expired): 0 violations, 0 serious/critical
    ✓  1 [live] e3-expiry.spec.ts › E3 hold expiry: expired, not charged, seats released @expiry (16.4s)
    ```
    Restored afterwards: `HOLD_TTL=60s`, `PAYMENT_HOLD_TTL=90s`.
  - E5: `pnpm --dir web exec playwright test --project replay`, which reported `9 passed (24.8s)`. Its first run failed on the two replay `/control` axe issues above, and this pass came after the fix.
  - Video: `web/e2e/artifacts/booking.webm` (2076 KB), copied to `web/public/results/latest/booking.webm`.
- [x] **axe: 0 serious or critical.** Every live and replay route logged `0 violations, 0 serious/critical` (output above). The live axe routes were rerun after the dashboard edits: `6 passed (23.2s)`, all at 0.
- [ ] **Lighthouse meets DESIGN-BRIEF §7, or the miss is reported with its cause.** This is a reported miss. Accessibility is 100 and Best Practices 96 on all three routes, which meets the target. Performance is 62 / 62 / 71 against ≥ 90. §4.2 has the numbers and the cause.
- [x] **UI UX Pro Max pre-delivery checklist per page:** §6.
- [x] **Quality bar; screenshot critique and fixes recorded:** §5, with screenshots in `docs/reports/assets/p6b/`.
- [x] **JS budgets met (framework + ≤ 50 KB app code, ADR 0012):** §4.1. All 9 routes are within budget.
- [x] **`pnpm lint`, typecheck, `task lint test` green:**
  - `pnpm lint` printed `$ eslint` with no findings.
  - `pnpm typecheck` printed `✓ Types generated successfully` and tsc was clean.
  - `pnpm format:check` printed `All matched files use Prettier code style!`.
  - `task lint` printed `golangci-lint run ./...` and `0 issues.`
  - `task test:race` (gcc present, so the race gate ran locally) gave `ok` for every package with tests and no `FAIL` or `DATA RACE` lines. The slowest were `internal/orders 356.027s`, `internal/queue 102.143s` and `internal/inventory 71.289s`.

## 3. Tests
| Suite | Count | Result | Notes |
|---|---|---|---|
| Go unit + integration (testcontainers) under `-race` | all packages | ✅ | `task test:race`, run locally. No testcontainers were left behind afterwards (`docker ps -aq --filter label=org.testcontainers=true` returned 0) |
| e2e live (E1, E2, E4, axe ×6) | 9 | ✅ | 59.7 s |
| e2e live E3 (short-TTL overlay) | 1 | ✅ | 16.4 s |
| e2e replay (E5 ×9 routes + axe) | 9 | ✅ | 24.8 s |

## 4. Numbers
### 4.1 JS budgets: `task web:budget` (after `task web:build`)
```
framework (rootMainFiles, 5 files): 128.7 KB gzip
route        first-load   framework   app code   budget (app)
/              163.9 KB    128.7 KB    35.2 KB   <= 50 KB ok
/event         169.7 KB    128.7 KB    41.0 KB   <= 50 KB ok
/queue         173.9 KB    128.7 KB    45.2 KB   <= 50 KB ok
/book          176.9 KB    128.7 KB    48.2 KB   <= 50 KB ok
/checkout      176.4 KB    128.7 KB    47.7 KB   <= 50 KB ok
/order         174.3 KB    128.7 KB    45.6 KB   <= 50 KB ok
/control       175.6 KB    128.7 KB    46.9 KB   <= 50 KB ok
/results       176.0 KB    128.7 KB    47.3 KB   <= 50 KB ok
/how           156.7 KB    128.7 KB    28.0 KB   <= 50 KB ok

lazy chunks (not in any page's HTML), gzip:
    129.3 KB  _next/static/chunks/42oedz33c1gi_.js
     38.6 KB  _next/static/chunks/0cz1d0mv5g_q7.js
```
`/book` has the least headroom: 1.8 KB.

### 4.2 Lighthouse mobile (Lighthouse 13.6.0 via `npx lighthouse@13.6.0`, Playwright's Chromium, default simulated throttling at 4× CPU, built static output on `node web/scripts/serve-static.mjs 4173`)
Command per route: `npx -y lighthouse@13.6.0 http://localhost:4173<route> --form-factor=mobile --only-categories=performance,accessibility,best-practices --output=json --output-path=docs/results/p6b/lighthouse/<name>[.idle].json`. Both runs are reported below; neither is the best of several attempts.

| Run | Route | Perf | A11y | BP | LCP | TBT | CLS | FCP | benchmarkIndex |
|---|---|---|---|---|---|---|---|---|---|
| 1: stack running, no gzip | `/queue/?e=tatkal-chn-sbc` | 50 | 100 | 96 | 6.4 s | 1,830 ms | 0 | 1.1 s | n/r |
| 1 | `/book/?e=tatkal-chn-sbc` | 47 | 100 | 96 | 6.7 s | 3,830 ms | 0 | 1.1 s | 665.5 |
| 1 | `/control/` | 58 | 100 | 96 | 7.1 s | 740 ms | 0 | 1.1 s | n/r |
| **2: stack stopped, gzip on** | `/queue/?e=tatkal-chn-sbc` | **62** | 100 | 96 | 3,884 ms | 1,503 ms | 0 | 810 ms | 1272.5 |
| **2** | `/book/?e=tatkal-chn-sbc` | **62** | 100 | 96 | 3,652 ms | 2,031 ms | 0 | 810 ms | 1241.5 |
| **2** | `/control/` | **71** | 100 | 96 | 4,110 ms | 611 ms | 0.00075 | 810 ms | 1219 |

"n/r": benchmarkIndex was only read for `/book` in run 1. Run 2 is the representative one. Run 1 ran on a CPU shared with the 11-container stack (benchmarkIndex 665 against about 1,250 when idle), and `serve-static` sent everything uncompressed, which Vercel does not do.

**Why Performance misses 90 (from the run-2 JSON audits):**
1. **TBT is the main cost.** On `/book`, about 0.9–1.0 s of script evaluation is `1gvjyx-swlkct.js`. That file is one of Next's `rootMainFiles` (React DOM plus the Next client runtime), so app code can't shrink it. Another 1.4–2.0 s is "Unattributable": main-thread hydration, style and layout. `/book` has one 1,481 ms long task, which is the seat map's coaches, minimaps and grid rendering on first mount.
2. **LCP is render-delayed, not network-bound.** The LCP breakdown shows TTFB of about 20 ms against an element render delay of 1.07 s (`/queue`), 1.19 s (`/book`) and 0.73 s (`/control`) observed. These routes are keyed by query string (`?e=`, `?o=`), so the static export can only prerender a shell, and the content that becomes LCP appears after the framework hydrates and the mode probe returns. Under simulated 4× CPU, that turns into an LCP of 3.6–4.1 s.
3. **Best Practices is 96, not 100**, because of console 404s:
   - `GET /api/v1/healthz` is the LIVE/REPLAY mode probe. It 404s by design on a static host (DESIGN-BRIEF §3).
   - On `/control`, three `__next.<route>.__PAGE__.txt` prefetches 404. The static export never writes these per-segment prefetch files (`web/out/control/` contains only `index.html`, `index.txt`, `__next._full.txt` and `__next._tree.txt`), so they would 404 on Vercel too.

**Proposed fixes (scope decision for Sheshakanth, not done here):**
- (a) Prerender per-event pages (`/event/[slug]` style paths via `generateStaticParams`) instead of `?e=`, so LCP text is in the HTML.
- (b) Defer the seat-grid mount behind the coach tabs (render the active coach first, the minimaps in idle time).
- (c) Ship a static `mode.json` in the replay build so the probe never 404s.

(a) changes URLs that API.md and DEPLOY.md reference, so it needs your call.

## 5. Screenshot critique (`docs/reports/assets/p6b/*.png`, REPLAY mode at 390×844 and 1280×800)
| Page / width | Finding | Fix |
|---|---|---|
| control / 1280 | The "T0" stop button was drawn inside the "First sale" button (T+0:00 vs T+0:04), and "Sold out" overlapped "End". axe flagged all four as `target-size` | `replayStops` merges stops closer than 8 % of the timeline ("T0, first sale", "Sold out, end"). Buttons are at least 24×24 px and moved below the range input |
| control / 390 | The tile label "ADMITTED PER SEC…" was truncated with an ellipsis | Labels wrap (`text-balance`), and on phones a two-line label height is reserved so the dashed rules in a row stay aligned (re-shot `control-m.png`) |
| control / both | The event log scrolled but couldn't be reached by keyboard | Focusable `region` labelled "Event log", with a navy focus ring |
| queue, book, checkout, order / 390 (replay notice) | The E1 video showed as a black box until played, because the recorder's first frame is blank | `src=…booking.webm#t=1` so the still shows `/queue`. **Not verified locally:** the seek needs HTTP Range support, which `serve-static` lacks (re-shot `book-m.png` still showed 0:00). Vercel serves ranges. Verify on the P8 deploy |
| replay notice / 390 | "localhost:8000/queue?e=quiet-tuesday" wraps after `queue?` | Accepted: it's a code-styled URL, and wrapping at `?` keeps it readable |
| control / both | The stat numbers render two-tone in the screenshot ("3,518" with "518" lighter) | Not a bug: the `TickingNumber` count-up was mid-tween at capture time |
| results, landing, how / both | Hierarchy, spacing and replay labelling are correct; no overflow | None needed |

## 6. UI UX Pro Max pre-delivery checklist (P6b pages: control / results / landing / event / how)
| Check | Ctl | Res | Land | Evt | How |
|---|---|---|---|---|---|
| No emoji icons (Lucide SVG only) | ✅ | ✅ | ✅ | ✅ | ✅ |
| `cursor-pointer` on clickables (scrubber, stop buttons, speed toggle, links) | ✅ | ✅ | ✅ | ✅ | ✅ |
| Hover transitions 150–300 ms, no layout-shifting scale | ✅ | ✅ | ✅ | ✅ | ✅ |
| Text contrast ≥ 4.5:1 (MASTER tokens; axe colour-contrast clean on every route) | ✅ | ✅ | ✅ | ✅ | ✅ |
| Visible focus (navy outline), including the event log region and stop buttons | ✅ | ✅ | ✅ | ✅ | ✅ |
| `prefers-reduced-motion` respected (global rule in `globals.css`) | ✅ | ✅ | ✅ | ✅ | ✅ |
| Responsive 390 / 1280 checked by screenshot | ✅ | ✅ | ✅ | ✅ | ✅ |
| No horizontal scroll on mobile | ✅ | ✅ | ✅ | ✅ | ✅ |
| Colour never the only cue (charts carry text values; Recharts `accessibilityLayer={false}` so charts aren't fake-interactive, data is in tables and tiles) | ✅ | ✅ | ✅ | ✅ | ✅ |
| Touch targets: ≥ 44 px for primary controls; the scrubber's stop shortcuts are ≥ 24 px (WCAG 2.5.8 AA) and hidden on phones except Start / End | ✅ | ✅ | ✅ | ✅ | ✅ |
| Replay mode always labelled with the run id; no invented numbers | ✅ | ✅ | ✅ | ✅ | ✅ |

## 7. Deviations and decisions
| What changed vs the spec | Why | ADR |
|---|---|---|
| The JS budget is now "framework + ≤ 50 KB app code" per route | Next 16 + React 19's framework alone is 128.7 KB gzip, so the old absolute budget couldn't be met by any app | ADR 0012 (DESIGN-BRIEF §7 updated) |
| `/availability` renews the admission lease | P6a found that browsing the map for more than 60 s ended the turn | ADR 0010 addendum part 4, API.md |
| `serve-static.mjs` gzips text responses | It claims to serve `web/out` "the way a static host does", and static hosts compress. Without gzip, Lighthouse measured a transfer cost the real deploy doesn't have | none (dev tool only) |
| Replay timeline stops closer than 8 % are merged | Overlapping buttons failed WCAG 2.5.8; the event log still lists every moment separately | none |

## 8. Known issues and debt
- **For Sheshakanth (scope):** the Lighthouse Performance miss (§4.2), with fixes (a)–(c). (a) changes public URLs.
- The `#t=1` video still frame is unverified locally (no Range support in `serve-static`).
- `__next.<route>.__PAGE__.txt` prefetch 404s on static export: link prefetch falls back to a normal navigation. Check again on Vercel in P8.
- The local Lighthouse runs use simulated throttling on a desktop CPU, not a real mid-range Android over 4G. DESIGN-BRIEF §7's LCP ≤ 2.0 s target is therefore not measured on the device it names.
- The final `task e2e` was not run as a single command. Its steps were run individually (§2), with the dashboard fixes landing between the live run and the replay run. The live axe routes were rerun after those fixes.

## 9. Understand this before moving on
1. **Q:** How can the frontend know whether it's talking to a real stack? **A:** `web/lib/mode.ts` probes `/api/v1/healthz` with a 1.5 s timeout: a 200 means LIVE, and anything else means REPLAY. `serve-static.mjs` 404s every `/api/*`, so E5 exercises exactly the path a Vercel visitor takes.
2. **Q:** Why does E3 need a separate stack configuration, and how is it undone? **A:** Hold expiry at the real 60 s/90 s TTLs would make the test slow. `task e2e` recreates `api1–3` and `worker` with `SLUICE_SCENARIO=e3` (compose layers `deploy/env/rush.e3.env`) and `defer`s the recreate with `none`, so a failed E3 still restores normal TTLs.
3. **Q:** Why doesn't polling `/availability` let a user hold their turn forever? **A:** `auth.Keyring.TouchAdmission` only renews the idle lease and never passes `ADMISSION_TTL`, the hard cap (`TestActiveAdmissionLivesToHardCap`, `TestAvailabilityPollRenewsLease`). A missing or foreign token is ignored, never refused.

## 10. Files changed (`git status --short`, grouped)
```
Go / API:   M internal/auth/http.go  M internal/inventory/handler.go  M internal/queue/admission_lease_test.go
Docs:       M docs/API.md  M docs/DESIGN-BRIEF.md  M docs/adr/0010-stack-containers-and-swarm-details.md
            ?? docs/adr/0012-js-budget-framework-plus-app.md  ?? docs/reports/P6b-frontend.md  ?? docs/reports/assets/p6b/
            ?? docs/results/p5b/p5b-smoke/timeseries.csv  ?? docs/results/p6b/
Design:     ?? design-system/pages/control.md  ?? design-system/pages/results.md
Infra:      M .gitignore  M Taskfile.yml  ?? deploy/env/rush.e3.env
Web:        M web/app/{book/page.tsx,globals.css,page.tsx}  M web/components/{notices,seat-map,site-header}.tsx
            M web/components/ui/button.tsx  M web/lib/api.ts  M web/package.json  M web/pnpm-lock.yaml  M web/.prettierignore
            ?? web/app/{control,event,how,results}/  ?? web/components/{dashboard,landing-parts,results-charts}.tsx
            ?? web/lib/replay.ts  ?? web/playwright.config.ts  ?? web/e2e/  ?? web/public/results/
            ?? web/scripts/{build-replay,js-budget,serve-static}.mjs
```

## 11. Next phase readiness
P7 can start. The stack was stopped with `task stack:down` (volumes kept), and no test containers are left. Before P8, decide on the Lighthouse fixes in §4.2 (a)–(c).

## 12. Git commands (PowerShell-safe)
```powershell
git add -A
git status --short
git commit -m "feat(web): Rush Control, results, replay mode and e2e suite" -m "- Rush Control live and replay scrubber, results page, landing, event and how pages" -m "- Replay data from the P5b smoke run, labelled with its run id" -m "- Playwright E1-E5 with axe on every route, E1 video, short-TTL overlay for E3" -m "- Availability poll renews the admission lease, capped by ADMISSION_TTL" -m "- JS budget is framework plus 50 KB app code per route (ADR 0012)" -m "- Lighthouse mobile: a11y 100, best practices 96, performance 62-71 (miss reported)" -m "Phase P6b - report: docs/reports/P6b-frontend.md"
```
