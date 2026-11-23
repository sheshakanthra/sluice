# P6: Frontend (UI UX Pro Max)
**Timebox:** 7 h · **Read first:** DESIGN-BRIEF.md (all) · `design-system/MASTER.md` · API.md §1, §3 · DEPLOY.md §A (replay data)

## Goal
Build an extraordinary-looking, calm, accessible booking flow plus Rush Control and Results. They must work **live** against the stack and as a **replay** on a static host.

## Tasks
1. **Page overrides:** run the five `--page` commands in DESIGN-BRIEF §2 and keep the outputs in `design-system/pages/`. Before building each page, read MASTER.md and that page's override.
2. **Foundation (`web/`):**
   - Tokens are already in `globals.css`, and the fonts chosen by the skill are loaded via `next/font` (see the Direction notes in `design-system/MASTER.md`).
   - shadcn components as needed (button, card, tabs, badge, dialog, tooltip, toast, progress, skeleton, separator).
   - `lib/api.ts`: a typed client, one function per endpoint in API.md. It handles problem+json, generates IKs (`crypto.randomUUID()` per user intent and **reuses** them on retry), and reads base URL `NEXT_PUBLIC_API_BASE_URL` in dev, same-origin otherwise.
   - `lib/mode.ts`: LIVE/REPLAY detection (DESIGN-BRIEF §3). `lib/replay.ts` loads the `results/latest/*` files and drives the timeline.
   - TanStack Query: polling follows `retry_after_ms`, uses ETag for availability, and keeps polling in background tabs at the server interval.
   - Tokens are stored in `localStorage` (refresh-safe; wrap every access in try/catch).
3. **Routes and components:** everything in DESIGN-BRIEF §3–4, covering every state in the states column:
   - **SeatMap:** coach tabs plus an overview minimap, 3+2 layout with aisle, grid semantics, keyboard navigation, colour-independent states, and smooth state flips when the bitmap changes. Memoise cells.
   - **HoldTimer:** driven by `expires_at` against a server-offset-corrected clock (compute the offset from the `Date` response header). Announces at 60, 30 and 10 s.
   - **Checkout:** hosted MockPay redirect (`return_url` = `/order?o=…`) and "Pay with saved test card" (`/pay`). After the redirect, call `/confirm` and poll until terminal.
   - **NotChargedNotice:** shown whenever `charged === false` on a failed or expired order.
   - **Rush Control (LIVE):** `/stats` every 1 s, the start-demo-rush button (only when `/demo/rush` exists, i.e. demo mode) and the correctness panel. **REPLAY:** the timeline scrubber over `timeseries.json` at 1× / 4× / 10×, with a "Replay of CI run <id> · <date> · commit <sha>" label.
   - **Results:** headline numbers, methodology summary, Recharts charts (RPS, p95 per route, queue depth, seats sold), the baseline matrix, environment details, and links to the CI run and raw JSON.
4. **Placeholder replay data:** until P8, ship `web/public/results/latest/*` built from the P5 SMOKE run, clearly labelled with the run_id in the UI. **No invented numbers.**
5. **Playwright** (`web/e2e/`) against `task stack:up` (full stack via Caddy :8000):
   - E1 quiet event: join → admitted → best-available 2 → order → hosted checkout Pay → ticket. **Record video.**
   - E2 decline: Decline → "not charged" → new attempt → ticket.
   - E3 expiry (stack with `HOLD_TTL=10s`): hold → wait → "expired", seats released.
   - E4 sold out: seed a 1-seat event via the admin/test endpoints, a bot takes it → SOLD_OUT screen.
   - E5 REPLAY mode: serve `web/out` with no API → landing, control replay and results render with no errors.
   - axe check on every route (0 serious or critical).
6. **Lighthouse** (mobile) on `/queue`, `/book` and `/control` against the built static output. Record the scores in the report.
7. **Taskfile:** `e2e` (stack up → web build → playwright), `web:serve` (static preview).

## Acceptance
- [ ] E1–E5 pass, with E1's video saved to `web/e2e/artifacts/booking.webm`.
- [ ] axe reports 0 serious or critical issues. Lighthouse meets DESIGN-BRIEF §7, or the miss is reported with its cause.
- [ ] The UI UX Pro Max pre-delivery checklist is ticked per page (copy the checklist into the report).
- [ ] Quality bar in DESIGN-BRIEF met; screenshot critique and fixes recorded in the report.
- [ ] The JS budgets in DESIGN-BRIEF §7 are met (paste the `next build` output sizes).
- [ ] `pnpm lint`, typecheck and `task lint test` are green.

**Commit hint:** `feat(web): waiting room, seat map, checkout, Rush Control, results (live + replay)`
