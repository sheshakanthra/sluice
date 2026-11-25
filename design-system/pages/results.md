# Results Page Overrides

> **PROJECT:** SLUICE
> **Generated:** 2026-11-24 21:11:08
> **Page Type:** Search Results

> ⚠️ **IMPORTANT:** Rules in this file **override** the Master file (`design-system/MASTER.md`).
> Only deviations from the Master are documented here. For all other rules, refer to the Master.

---

## Page-Specific Rules

### Layout Overrides

- **Max Width:** 1200px (standard)
- **Layout:** Full-width sections, centered content
- **Sections:** 1. Hero (problem state), 2. Transformation slider/comparison, 3. How it works, 4. Results CTA

### Spacing Overrides

- No overrides — use Master spacing

### Typography Overrides

- No overrides — use Master typography

### Color Overrides

- **Strategy:** Contrast: muted/grey (before) vs vibrant/colorful (after). Success green for results.

### Component Overrides

- Avoid: Blank screen or '0 results'
- Avoid: Require full type and enter
- Avoid: Wide tables breaking layout

---

## Page-Specific Components

- No unique components for this page

---

## Recommendations

- Effects: Comparison bar animations (grow to value), delta indicator animations (direction arrows), highlight on compare
- Search: Show 'No results' with suggestions
- Search: Show predictions as user types
- Responsive: Use horizontal scroll or card layout
- CTA Placement: After transformation reveal + Bottom

---

## Direction notes (P6b, override the raw output above)

Taken: 1200 px content width, no blank or zero-result state (every table cell says where it came
from, missing values read "n/a" with the reason), tables that never break the layout (the baseline
matrix scrolls inside its own container on mobile, the page does not), success green for passing
invariants, bars that grow to value (Recharts animation, off under `prefers-reduced-motion`).

Rejected: the "transformation slider" and before/after hero (marketing, not evidence); muted-vs-
vibrant colour contrast between baselines and SLUICE (the matrix uses the same ink for every row and
marks failures with a cross icon plus danger text, so colour is never the only cue); search
predictions (there is nothing to search).

Page concept: the **audit certificate**. The page reads top-down like a lab report: headline stubs
(users, seats sold, oversold, duplicate charges, checkout p95) labelled with the run id and whether
it is the CI headline run or a local placeholder; then methodology in four short numbered stops
(the same route-line motif as the landing page); then the four charts (RPS, p95 per route, queue
depth, seats sold) on one shared time axis with T0 and sold-out marked; then the baseline matrix
(B0 / B1 / B2 / SMOKE, with B2 at 20k and 50k to show where direct mode breaks); then the
environment table and links to the raw JSON. Recharts is loaded lazily (`next/dynamic`) so the
headline paints before the chart bundle arrives.
