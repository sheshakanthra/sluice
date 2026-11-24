# Queue Page Overrides

> **PROJECT:** SLUICE
> **Generated:** 2026-11-24 19:41:48
> **Page Type:** General

> ⚠️ **IMPORTANT:** Rules in this file **override** the Master file (`design-system/MASTER.md`).
> Only deviations from the Master are documented here. For all other rules, refer to the Master.

---

## Page-Specific Rules

### Layout Overrides

- **Max Width:** 800px (narrow, focused)
- **Layout:** Single column, centered
- **Sections:** 1. Hero with countdown, 2. Product teaser/preview, 3. Email capture form, 4. Social proof (waitlist count)

### Spacing Overrides

- **Content Density:** Low — focus on clarity

### Typography Overrides

- No overrides — use Master typography

### Color Overrides

- **Strategy:** Anticipation: Dark + accent highlights. Countdown in brand color. Urgency indicators.

### Component Overrides

- Avoid: No indication of progress
- Avoid: Show loading spinner for 10s+

---

## Page-Specific Components

- No unique components for this page

---

## Recommendations

- Effects: Hover states on CTA (color shift, slight scale), form field focus animations, loading spinner, success feedback
- Feedback: Step indicators or progress bar
- AI Interaction: Stream text response token by token
- CTA Placement: Email form prominent (above fold) + Sticky form on scroll

---

## Direction notes (P6a, override the raw output above)

Taken: single centred column, max width 800 px (we use 640 px: one stub plus context), low density,
progress always visible, never a bare spinner.

Rejected: "dark + accent" (MASTER Direction is light transit signage), email capture form and
waitlist social proof (there is no marketing funnel here: the user is already in line), hover
scale on CTAs (layout shift; MASTER uses a 1 px pressed translate).

Page concept: the **platform departure board**. The stub's number is the user's place in line in
Barlow Condensed 96-144 px, ticking down per digit. Under the tear line: people ahead, ETA range,
the honest progress rail (a track with station ticks that only moves forward), outlook badge and
seats left. A navy sign band at the top carries the event and the live signal dot. Every state
(PREQUEUE, WAITING, PAUSED, ADMITTED, SOLD_OUT, OFFER_EXPIRED, CLOSED) swaps the stub's number and
the band copy, never the layout, so nothing jumps. "Last updated Ns ago" and the next poll time
replace any spinner. This is the lightest page: no charts, no images, one JSON poll.
