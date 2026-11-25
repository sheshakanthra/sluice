# DESIGN BRIEF: SLUICE

The goal is an **extraordinary-looking, calm-under-pressure** booking experience plus a mission-control dashboard that makes a reviewer say "wait, this is real?" within 10 seconds.

## 1. Feel and brand
- **Mood:** calm, certain, fast and honest. People arriving at 10:00:00 are anxious, so the UI lowers their heart rate. Nothing jumps, and nothing spins without information.
- **SLUICE has its own identity.** It is not a reskin of any portfolio or personal palette. **Never use the `#0B0F12` / `#00FFCC` / JetBrains Mono portfolio palette** anywhere in SLUICE.
- **UI UX Pro Max owns the whole visual system:** palette, typefaces, type scale, neutral ramp, semantic colours (success / warning / danger / info), spacing, radius, elevation and motion. The chosen direction and its rationale live in the **Direction notes** section of `design-system/MASTER.md`.
- **Avoid generic SaaS defaults:** neon-on-black, decorative gradients, glassmorphism, emoji as icons, and template hero sections (big centred headline + two buttons + feature grid with no point of view).
- **All text meets WCAG AA** (≥ 4.5:1, or ≥ 3:1 for large text and UI component boundaries) on the surface it sits on.
- Every UI task is held to the **Quality bar in §10**.
- Light or dark is part of the direction the skill's output and the Direction notes decide. A second theme is a P8 stretch.

## 2. Generating the design system with UI UX Pro Max
The P0 install puts the skill at `.claude/skills/ui-ux-pro-max/`. Use `python3`, or `python` on Windows if that's the working one.

```bash
# P0: master design system (persisted to design-system/MASTER.md)
python3 .claude/skills/ui-ux-pro-max/scripts/search.py "ticket booking waiting room seat map high-demand rush" --design-system --persist -p "SLUICE"

# P6: page overrides (persisted to design-system/pages/<page>.md)
python3 .claude/skills/ui-ux-pro-max/scripts/search.py "virtual waiting room queue position countdown progress" --design-system --persist -p "SLUICE" --page "queue"
python3 .claude/skills/ui-ux-pro-max/scripts/search.py "interactive seat map selection legend accessibility" --design-system --persist -p "SLUICE" --page "book"
python3 .claude/skills/ui-ux-pro-max/scripts/search.py "checkout timer payment status trust" --design-system --persist -p "SLUICE" --page "checkout"
python3 .claude/skills/ui-ux-pro-max/scripts/search.py "real-time operations dashboard KPI counters live charts" --design-system --persist -p "SLUICE" --page "control"
python3 .claude/skills/ui-ux-pro-max/scripts/search.py "benchmark results methodology charts comparison table" --design-system --persist -p "SLUICE" --page "results"

# Stack guidance while implementing
python3 .claude/skills/ui-ux-pro-max/scripts/search.py "static export data fetching performance" --stack nextjs
python3 .claude/skills/ui-ux-pro-max/scripts/search.py "charts realtime" --domain chart
python3 .claude/skills/ui-ux-pro-max/scripts/search.py "accessibility forms timers" --domain ux
```
The skill persists to `design-system/sluice/` (it always nests output under the project slug). After every `--persist` run, move the files up to `design-system/MASTER.md` and `design-system/pages/<page>.md` and delete the empty `design-system/sluice/` folder (ADR 0002).

After generating `MASTER.md`, choose **one bold, specific direction** from the skill's output and append a **"Direction notes"** section: the chosen style, palette and type, the **signature element** that carries through every screen, what was deliberately rejected from the raw output and why, and the WCAG contrast of every text colour (from `python design-system/contrast.py`). Then map the tokens into Tailwind v4 `@theme` variables and shadcn CSS variables (one source of truth in `web/app/globals.css`).

## 3. Screens and states
| Route (static export: IDs go in query params) | Purpose | Must show | States |
|---|---|---|---|
| `/` | Landing | One-line pitch, two event cards (Rush / Quiet), "How it survives a rush" in 4 tiles, headline result strip (50k · 0 oversold · 0 duplicate · p95), CTA to Rush Control | LIVE vs REPLAY mode badge |
| `/event?e=<slug>` | Event | Route and time (IST), price, seats left (live), countdown to T0, fairness note ("arriving early doesn't help: positions at opening are randomised") | Not open, pre-queue open, on sale, sold out |
| `/queue?e=<slug>` | **Waiting room** (lightest page) | Ticket number, position and people ahead, ETA range, progress rail, outlook badge, seats left, "refresh-safe" note, last updated | PREQUEUE (countdown, lottery explainer), WAITING, PAUSED ("all seats are on hold, some may come back"), ADMITTED (3-2-1 "It's your turn" → book), SOLD_OUT (empathetic final), OFFER_EXPIRED (rejoin CTA, rejoins left) |
| `/book?e=<slug>` | Seat selection | Coach tabs or overview (C1–C10, 3+2 layout with aisle), live seat states, legend, quantity 1–4, "Best available" action, selection summary, admission time left | SEATS_TAKEN (highlight + alternatives), INSUFFICIENT, SOLD_OUT, HOLD_LIMIT, NOT_ADMITTED |
| `/checkout?o=<orderId>` | Checkout | **Hold timer** (mm:ss; turns amber under 60 s), seats and labels, price summary in ₹, pay options (hosted MockPay checkout, or "Pay with saved test card") | Pending, processing ("confirming with the bank: don't close this tab"), failed (**"You were not charged"**), expired |
| `/order?o=<orderId>` | Ticket | Ticket card(s): seat labels, ticket code, QR (client-side), event summary, "charged ₹X" | CONFIRMED, PENDING (auto-poll), FAILED/EXPIRED (not charged) |
| `/control?e=<slug>` | **Rush Control** | Big counters (waiting, admitted/s, active, available/held/sold), sold-out clock, correctness panel (**Oversold 0 · Duplicate charges 0 · Orphan charges 0**), latency sparkline (p95), event log ticker, mode badge | LIVE (with a "Start a demo rush" button in demo mode) and REPLAY (timeline scrubber, 1× / 4× / 10×) |
| `/results` | Proof | Headline numbers, methodology (§1–4 of LOADTEST, shortened), charts (RPS over time, p95 per route, queue depth, seats sold), **baseline matrix** (B0/B1/B2/H), environment specs, links to the CI run and raw JSON | — |
| `/how` | How it works | Architecture diagram, the 4 mechanisms with small sequence illustrations, the failure-mode table (short) | — |

**Mode detection:** on load, fetch `/api/v1/healthz` with a 1.5 s timeout. It's **LIVE** only if the response is JSON with `status: "ok"`. Anything else means **REPLAY**: a timeout, a 404, or an HTML fallback page on a static host. Replay uses `/results/latest/{summary.json,timeseries.json,baselines.json}` from `web/public`. Booking pages in REPLAY show a short explainer, the recorded e2e video of the booking flow, and "Run it yourself in 2 commands".

## 4. Components
`ModeBadge`, `CountdownClock` (IST, tabular numerals), `QueueTicket`, `ProgressRail` (animated, honest, never moves backwards unless the server says so), `OutlookBadge`, `SeatCounts`, `SeatMap` + `SeatCell` + `CoachTabs` + `Legend`, `QuantityStepper`, `HoldTimer`, `PriceSummary`, `PaymentStatus`, `NotChargedNotice`, `TicketCard` (with QR), `StatTile`, `LiveSparkline`, `TimelineScrubber`, `EventLog`, `ResultTable`, `BaselineMatrix`, `ErrorProblem` (renders problem+json codes as human messages).

Seat states must **not rely on colour alone**:
| State | Fill | Icon / pattern |
|---|---|---|
| Available | Outline, neutral | Empty |
| Selected | Filled with the accent | Check |
| Held by others | Dim | Diagonal hatch + clock |
| Sold | Muted | Cross |
| Yours (held) | Accent outline + glow | Check |

## 5. UX rules for rush moments
1. **Never show a bare spinner.** Every wait shows what's happening and when the next update comes.
2. **Refresh-safe.** Tokens live in `localStorage` (per browser). Refreshing, or closing and reopening the tab, resumes the same place in line, and the copy says so ("Refreshing won't lose your place on this browser").
3. **Honest odds.** Show the outlook (LIKELY / POSSIBLE / UNLIKELY) with plain-language copy. Never promise a seat.
4. **Money clarity.** Every failure path states "You were not charged" when `charged:false`, and "charged ₹X" when true. Never leave the money state ambiguous.
5. **No dark patterns.** No fake scarcity, no fake timers. Every number on screen is real (live) or labelled as a replay.
6. **IST everywhere**, with the date (for example "10:00:00 IST, Sat 22 Nov"). Use ₹ with Indian digit grouping (₹1,25,000).
7. **Polling** follows the server's `retry_after_ms`. Keep polling in background tabs (people switch apps), but use the server-advised interval only, never faster.
8. **Motion is meaningful only:** position ticks, a seat flips state, the timer is urgent. Respect `prefers-reduced-motion`.
9. **Give the slot back.** When an admitted user ends without seats, the UI calls `DELETE /events/{id}/queue` with the admission token, the same exit rule the swarm follows (LOADTEST §3.2). That covers no seats left after the retries, "all seats are on hold" (`INSUFFICIENT_SEATS` with 0 available), and "Leave"/"Cancel" from the seat map or the hold (ABANDONED). The copy says so ("We've given your place to the next person"). The server also frees an idle admission after `ADMISSION_IDLE_TTL` (ADR 0010 addendum), but the explicit call frees it at once and doesn't rely on that.

## 6. Accessibility (WCAG 2.2 AA)
- The seat map is a `grid` with roving tabindex and arrow-key navigation. Each seat's aria-label reads like "Coach 3, seat 27, window, available". Enter or Space toggles a seat, and a live region announces changes to the selection.
- Hold timer and queue position updates go to `aria-live="polite"`, throttled to at most one announcement per 15 s plus announcements at 60, 30 and 10 s for the timer.
- Visible focus rings (accent), focus never lost on state changes, 44 px minimum touch targets, colour-independent states, contrast ≥ 4.5:1 for text.
- Playwright runs `@axe-core/playwright` on every route, with 0 serious or critical violations allowed.

## 7. Performance budgets
**JS rule (every route): framework + at most 50 KB (gzip) of app code** (ADR 0012). "Framework" is Next's `rootMainFiles` in `.next/build-manifest.json` (React DOM, the Next client runtime, the Turbopack loader: 128.7 KB gzip on Next 16.4 + React 19.3). App code is every other script the route's exported HTML loads, minus `noModule` polyfills. Lazy chunks (`next/dynamic`) don't count toward first load. Check: `node web/scripts/js-budget.mjs` after `pnpm build` (exit 1 when a route is over).

| Route | JS (gzipped) | Other |
|---|---|---|
| `/queue` | framework + ≤ 50 KB; imports nothing heavy (no charts, no motion library) | LCP ≤ 2.0 s on mid-range Android over 4G throttle; CLS < 0.05; one tiny JSON poll |
| `/book` | framework + ≤ 50 KB | Seat map renders 500 seats in < 16 ms per frame on update (memoised cells, keyed diffs) |
| `/control`, `/results` | framework + ≤ 50 KB first load | Charts lazy-loaded |
| All | framework + ≤ 50 KB | Lighthouse mobile: Performance ≥ 90, Accessibility ≥ 95, Best Practices ≥ 95 |

## 8. Copy deck (key lines)
- Pre-queue: "You're in the pre-queue. At 10:00:00 IST everyone here gets a random place in line, so arriving earlier doesn't help, but don't be late."
- Waiting: "You're #41,234 in line · 40,889 people ahead · about 3–4 min." Outlook UNLIKELY: "Seats are running out faster than the line is moving. We'll tell you the moment it's decided."
- Paused: "All remaining seats are on hold right now. Some usually come back. Stay here."
- Admitted: "It's your turn. You have 10:00 to pick seats."
- Hold: "Seats held for 04:59. They're yours until the timer ends."
- Not charged: "Payment didn't go through, and **you were not charged**. Your seats are released."
- Sold out: "Sold out. Every seat found an owner, and nobody got the same seat twice."

## 9. Done means
- The Quality bar in §10 is met, with the screenshot critique and fixes recorded in the phase report.
- Every route in §3 is implemented in LIVE and REPLAY modes, following `design-system/MASTER.md` plus the page overrides.
- The UI UX Pro Max pre-delivery checklist passes for each page (record it in the P6 report).
- axe shows 0 serious or critical issues, the Lighthouse targets in §7 are met, and the e2e flows pass with the video recorded.

## 10. Quality bar (binding for every UI task: P0 and P6)
1. **Design like a senior product designer at a top consumer app.** The skill's output is the foundation, not the ceiling: push past its defaults to something memorable and specific to a Tatkal-style rush.
2. **One clear concept that carries through every screen:** a signature element, a consistent type scale and spacing rhythm, and a deliberate colour hierarchy (one accent, used with intent).
3. **Craft details:** hover, focus and pressed states; skeleton loading; empty and error states that are designed rather than default; meaningful motion (position ticking, seats flipping, timer urgency) that respects `prefers-reduced-motion`; crisp alignment on an **8 px grid**; real-looking content, never lorem ipsum.
4. **Mobile-first:** flawless at 375 px, then excellent at desktop widths.
5. **Screenshot review before calling any UI done:** capture each page with Playwright at **375 px and 1440 px**, critique it like a design review (hierarchy, spacing, contrast, consistency, "does this look like a template?"), fix what you find, and list the critique and the fixes in the phase report.
