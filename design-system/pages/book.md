# Book Page Overrides

> **PROJECT:** SLUICE
> **Generated:** 2026-11-24 19:41:48
> **Page Type:** Dashboard / Data View

> ⚠️ **IMPORTANT:** Rules in this file **override** the Master file (`design-system/MASTER.md`).
> Only deviations from the Master are documented here. For all other rules, refer to the Master.

---

## Page-Specific Rules

### Layout Overrides

- **Max Width:** 1400px or full-width
- **Grid:** 12-column grid for data flexibility
- **Sections:** 1. Full-screen interactive element, 2. Guided product tour, 3. Key benefits revealed, 4. CTA after completion

### Spacing Overrides

- **Content Density:** High — optimize for information display

### Typography Overrides

- No overrides — use Master typography

### Color Overrides

- **Strategy:** Immersive experience colors. Dark background for focus. Highlight interactive elements.

### Component Overrides

- Avoid: Icon buttons without labels
- Avoid: No hover feedback on clickable elements
- Avoid: Inputs that look like plain text

---

## Page-Specific Components

- No unique components for this page

---

## Recommendations

- Effects: Color gradient transitions on data change, cell highlighting on hover, tooltip reveal on click, smooth color animation
- Accessibility: Add aria-label for icon-only buttons
- Interaction: Change cursor and add subtle visual change
- Forms: Use distinct input styling
- CTA Placement: After interaction complete + Skip option for impatient users

---

## Direction notes (P6a, override the raw output above)

Taken: wide layout (max 1200 px), high density, distinct input styling, hover feedback on every
seat, labelled controls (no icon-only buttons), CTA after the interaction (the hold bar).

Rejected: dark immersive background (light signage direction), guided product tour and "benefits"
sections (the user has minutes, not curiosity), colour-gradient transitions on data change (seat
states flip with a 150 ms fill/border change plus an icon, never colour alone).

Page concept: the **coach diagram**. Coach tabs C1-C10 double as a minimap: each tab shows its
free-seat count and a 10-cell bar. The active coach is drawn like a carriage plan: 3 + aisle + 2,
window seats marked, rows numbered. Seat states follow DESIGN-BRIEF §4 (outline / accent fill +
check / hatch + clock / cross / accent outline). On mobile the selection summary and the hold
action live in a sticky bottom stub (the stub's number is seats selected); on desktop they sit in
a right-hand stub. The admission time left sits in the navy band.
