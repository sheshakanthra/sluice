# Checkout Page Overrides

> **PROJECT:** SLUICE
> **Generated:** 2026-11-24 19:41:48
> **Page Type:** Checkout / Payment

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

- Avoid: No feedback after submit
- Avoid: No feedback during loading

---

## Page-Specific Components

- No unique components for this page

---

## Recommendations

- Effects: Real-time chart animations, alert pulse/glow, status indicator blink animation, smooth data stream updates, loading effect
- Forms: Show loading then success/error state
- Feedback: Show spinner/skeleton for operations > 300ms
- CTA Placement: Hero (Right side form) + Bottom anchor

---

## Direction notes (P6a, override the raw output above)

Taken: navy for trust, warning colour for urgency (only under 60 s), loading then success/error
feedback on every action, high-contrast white form surface.

Rejected: webinar sections (topic, speaker bio, bonuses), "status blink" and alert glow (the brief
asks for calm: the timer pulses only under 10 s), repeating the form twice.

Page concept: the **boarding pass**. One stub: the number is the hold timer mm:ss (amber under
60 s), the body lists coach-seat labels and the price summary in rupees with Indian grouping.
Two pay options as full-width rows: hosted MockPay checkout (primary, accent-strong) and "Pay with
saved test card" (secondary). Processing replaces the pay rows with a step list ("Confirming with
the bank: don't close this tab", next check in Ns). Failed and expired always lead with the
NotChargedNotice. The order page reuses the stub as the ticket: number is coach and seat, QR in
the body.
