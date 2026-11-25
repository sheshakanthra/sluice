# Control Page Overrides

> **PROJECT:** SLUICE
> **Generated:** 2026-11-24 21:11:08
> **Page Type:** Dashboard / Data View

> ⚠️ **IMPORTANT:** Rules in this file **override** the Master file (`design-system/MASTER.md`).
> Only deviations from the Master are documented here. For all other rules, refer to the Master.

---

## Page-Specific Rules

### Layout Overrides

- **Max Width:** 1400px or full-width
- **Grid:** 12-column grid for data flexibility
- **Sections:** 1. Hero (Topic + Timer + Form), 2. What you'll learn, 3. Speaker Bio, 4. Urgency/Bonuses, 5. Form (again)

### Spacing Overrides

- **Content Density:** High — optimize for information display

### Typography Overrides

- No overrides — use Master typography

### Color Overrides

- **Strategy:** Urgency: Red/Orange. Professional: Blue/Navy. Form: High contrast white.

### Component Overrides

- Avoid: Leave UI frozen with no feedback

---

## Page-Specific Components

- No unique components for this page

---

## Recommendations

- Effects: Real-time chart animations, alert pulse/glow, status indicator blink animation, smooth data stream updates, loading effect
- Animation: Use skeleton screens or spinners
- CTA Placement: Hero (Right side form) + Bottom anchor

---

## Direction notes (P6b, override the raw output above)

Taken: high density, a wide 12-column grid (max 1152 px, `max-w-6xl` like every other page), never
leaving the UI frozen without feedback (every panel says when it last updated, and REPLAY says which
run it is), smooth data-stream updates (numerals tick, the sparkline extends).

Rejected: the webinar sections in the raw layout (topic + timer + form, speaker bio, bonuses: a
template that has nothing to do with an operations board); red/orange "urgency" colour (the board
must lower the heart rate: danger red appears only if an invariant is non-zero); blinking status
indicators and glow (only the signal dot breathes while LIVE, and `prefers-reduced-motion` stops it);
spinners (skeleton tiles with the last-known value instead).

Page concept: the **departures board**. A navy sign band carries the event, the mode badge and the
clock (LIVE: server time IST; REPLAY: `T+mm:ss` of the recorded run). Below it, one ticket stub per
headline counter (waiting, admitted/s, active, seats available/held/sold) with Barlow Condensed
numerals, then the **correctness strip**: three stubs, one per invariant (Oversold · Duplicate
charges · Orphan charges), each showing its count and when it was last checked. A p95 sparkline
(status and checkout) and the event log sit side by side on desktop and stack on mobile. In
REPLAY the timeline scrubber is a platform-style track with the T0, first-sale and sold-out stops
marked on it, plus 1× / 4× / 10× speed and play/pause. The run label ("Replay of local run
p5b-smoke · 24 Nov · commit 570aefc") sits directly under the band, never hidden in a tooltip.
