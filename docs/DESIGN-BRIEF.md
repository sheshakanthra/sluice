# DESIGN BRIEF: SLUICE

The goal is an **extraordinary-looking, calm-under-pressure** booking experience plus a mission-control dashboard that makes a reviewer say "wait, this is real?" within 10 seconds.

## 1. Feel and brand
- **Mood:** calm, certain, fast and honest. People arriving at 10:00:00 are anxious, so the UI lowers their heart rate. Nothing jumps, and nothing spins without information.
- **Personal brand tokens** (Sheshakanth's identity, applied across his projects). These **override** the generated palette where they conflict:
  - Background `#0B0F12` (dark-first)
  - Accent `#00FFCC`, used sparingly: primary actions, your position, live indicators
  - **JetBrains Mono** for numbers, codes, timers, ticket codes and data tables (`font-variant-numeric: tabular-nums`)
- UI UX Pro Max chooses the body and heading typeface, the neutral ramp, the semantic colours (success / warning / danger / info), spacing, radius, elevation and motion. Its choices must reach WCAG AA contrast on `#0B0F12`.
- Aesthetic direction: a "mission-control HUD". Thin 1 px grid lines, subtle glow **only** on live elements, depth from layered surfaces rather than drop shadows, generous whitespace. Avoid generic SaaS gradients, glassmorphism soup, and emoji as icons.
- A light theme is optional (P8 stretch). Dark ships first.

## 2. Generating the design system with UI UX Pro Max
The P0 install puts the skill at `.claude/skills/ui-ux-pro-max/`. Use `python3`, or `python` on Windows if that's the working one.

```bash
# P0: master design system (persisted to design-system/MASTER.md)
python3 .claude/skills/ui-ux-pro-max/scripts/search.py "real-time ticket booking waiting room seat map mission control dashboard dark fintech-grade trust" --design-system --persist -p "SLUICE"

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
After generating `MASTER.md`, append a **"Brand overrides"** section that lists the three tokens above and where they apply. Then map the tokens into Tailwind v4 `@theme` variables and shadcn CSS variables (one source of truth in `web/app/globals.css`).

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

## 6. Accessibility (WCAG 2.2 AA)
- The seat map is a `grid` with roving tabindex and arrow-key navigation. Each seat's aria-label reads like "Coach 3, seat 27, window, available". Enter or Space toggles a seat, and a live region announces changes to the selection.
- Hold timer and queue position updates go to `aria-live="polite"`, throttled to at most one announcement per 15 s plus announcements at 60, 30 and 10 s for the timer.
- Visible focus rings (accent), focus never lost on state changes, 44 px minimum touch targets, colour-independent states, contrast ≥ 4.5:1 for text.
- Playwright runs `@axe-core/playwright` on every route, with 0 serious or critical violations allowed.

## 7. Performance budgets
| Route | JS (gzipped) | Other |
|---|---|---|
| `/queue` | ≤ 120 KB | LCP ≤ 2.0 s on mid-range Android over 4G throttle; CLS < 0.05; one tiny JSON poll |
| `/book` | ≤ 200 KB | Seat map renders 500 seats in < 16 ms per frame on update (memoised cells, keyed diffs) |
| `/control`, `/results` | ≤ 300 KB | Charts lazy-loaded |
| All | — | Lighthouse mobile: Performance ≥ 90, Accessibility ≥ 95, Best Practices ≥ 95 |

## 8. Copy deck (key lines)
- Pre-queue: "You're in the pre-queue. At 10:00:00 IST everyone here gets a random place in line, so arriving earlier doesn't help, but don't be late."
- Waiting: "You're #41,234 in line · 40,889 people ahead · about 3–4 min." Outlook UNLIKELY: "Seats are running out faster than the line is moving. We'll tell you the moment it's decided."
- Paused: "All remaining seats are on hold right now. Some usually come back. Stay here."
- Admitted: "It's your turn. You have 10:00 to pick seats."
- Hold: "Seats held for 04:59. They're yours until the timer ends."
- Not charged: "Payment didn't go through, and **you were not charged**. Your seats are released."
- Sold out: "Sold out. Every seat found an owner, and nobody got the same seat twice."

## 9. Done means
- Every route in §3 is implemented in LIVE and REPLAY modes, following `design-system/MASTER.md` plus the page overrides.
- The UI UX Pro Max pre-delivery checklist passes for each page (record it in the P6 report).
- axe shows 0 serious or critical issues, the Lighthouse targets in §7 are met, and the e2e flows pass with the video recorded.
