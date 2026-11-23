# Design System Master File

> **LOGIC:** When building a specific page, first check `design-system/pages/[page-name].md`.
> If that file exists, its rules **override** this Master file.
> If not, strictly follow the rules below.

---

**Project:** SLUICE
**Generated:** 2026-11-23 13:23:33
**Category:** Hotel/Hospitality

---

## Global Rules

### Color Palette

| Role | Hex | CSS Variable |
|------|-----|--------------|
| Primary | `#1E3A8A` | `--color-primary` |
| Secondary | `#3B82F6` | `--color-secondary` |
| CTA/Accent | `#F97316` | `--color-cta` |
| Background | `#EFF6FF` | `--color-background` |
| Text | `#1E40AF` | `--color-text` |

**Color Notes:** Sky blue + booking orange

### Typography

- **Heading Font:** Cormorant
- **Body Font:** Montserrat
- **Mood:** luxury, high-end, fashion, elegant, refined, premium
- **Google Fonts:** [Cormorant + Montserrat](https://fonts.google.com/share?selection.family=Cormorant:wght@400;500;600;700|Montserrat:wght@300;400;500;600;700)

**CSS Import:**
```css
@import url('https://fonts.googleapis.com/css2?family=Cormorant:wght@400;500;600;700&family=Montserrat:wght@300;400;500;600;700&display=swap');
```

### Spacing Variables

| Token | Value | Usage |
|-------|-------|-------|
| `--space-xs` | `4px` / `0.25rem` | Tight gaps |
| `--space-sm` | `8px` / `0.5rem` | Icon gaps, inline spacing |
| `--space-md` | `16px` / `1rem` | Standard padding |
| `--space-lg` | `24px` / `1.5rem` | Section padding |
| `--space-xl` | `32px` / `2rem` | Large gaps |
| `--space-2xl` | `48px` / `3rem` | Section margins |
| `--space-3xl` | `64px` / `4rem` | Hero padding |

### Shadow Depths

| Level | Value | Usage |
|-------|-------|-------|
| `--shadow-sm` | `0 1px 2px rgba(0,0,0,0.05)` | Subtle lift |
| `--shadow-md` | `0 4px 6px rgba(0,0,0,0.1)` | Cards, buttons |
| `--shadow-lg` | `0 10px 15px rgba(0,0,0,0.1)` | Modals, dropdowns |
| `--shadow-xl` | `0 20px 25px rgba(0,0,0,0.15)` | Hero images, featured cards |

---

## Component Specs

### Buttons

```css
/* Primary Button */
.btn-primary {
  background: #F97316;
  color: white;
  padding: 12px 24px;
  border-radius: 8px;
  font-weight: 600;
  transition: all 200ms ease;
  cursor: pointer;
}

.btn-primary:hover {
  opacity: 0.9;
  transform: translateY(-1px);
}

/* Secondary Button */
.btn-secondary {
  background: transparent;
  color: #1E3A8A;
  border: 2px solid #1E3A8A;
  padding: 12px 24px;
  border-radius: 8px;
  font-weight: 600;
  transition: all 200ms ease;
  cursor: pointer;
}
```

### Cards

```css
.card {
  background: #EFF6FF;
  border-radius: 12px;
  padding: 24px;
  box-shadow: var(--shadow-md);
  transition: all 200ms ease;
  cursor: pointer;
}

.card:hover {
  box-shadow: var(--shadow-lg);
  transform: translateY(-2px);
}
```

### Inputs

```css
.input {
  padding: 12px 16px;
  border: 1px solid #E2E8F0;
  border-radius: 8px;
  font-size: 16px;
  transition: border-color 200ms ease;
}

.input:focus {
  border-color: #1E3A8A;
  outline: none;
  box-shadow: 0 0 0 3px #1E3A8A20;
}
```

### Modals

```css
.modal-overlay {
  background: rgba(0, 0, 0, 0.5);
  backdrop-filter: blur(4px);
}

.modal {
  background: white;
  border-radius: 16px;
  padding: 32px;
  box-shadow: var(--shadow-xl);
  max-width: 500px;
  width: 90%;
}
```

---

## Style Guidelines

**Style:** Liquid Glass

**Keywords:** Flowing glass, morphing, smooth transitions, fluid effects, translucent, animated blur, iridescent, chromatic aberration

**Best For:** Premium SaaS, high-end e-commerce, creative platforms, branding experiences, luxury portfolios

**Key Effects:** Morphing elements (SVG/CSS), fluid animations (400-600ms curves), dynamic blur (backdrop-filter), color transitions

### Page Pattern

**Pattern Name:** Event/Conference Landing

- **Conversion Strategy:** Early bird pricing with deadline. Social proof (past attendees). Speaker credibility. Multi-ticket discounts.
- **CTA Placement:** Register CTA sticky + After speakers + Bottom
- **Section Order:** 1. Hero (date/location/countdown), 2. Speakers grid, 3. Agenda/schedule, 4. Sponsors, 5. Register CTA

---

## Anti-Patterns (Do NOT Use)

- ❌ Poor photos
- ❌ Complex booking

### Additional Forbidden Patterns

- ❌ **Emojis as icons** — Use SVG icons (Heroicons, Lucide, Simple Icons)
- ❌ **Missing cursor:pointer** — All clickable elements must have cursor:pointer
- ❌ **Layout-shifting hovers** — Avoid scale transforms that shift layout
- ❌ **Low contrast text** — Maintain 4.5:1 minimum contrast ratio
- ❌ **Instant state changes** — Always use transitions (150-300ms)
- ❌ **Invisible focus states** — Focus states must be visible for a11y

---

## Pre-Delivery Checklist

Before delivering any UI code, verify:

- [ ] No emojis used as icons (use SVG instead)
- [ ] All icons from consistent icon set (Heroicons/Lucide)
- [ ] `cursor-pointer` on all clickable elements
- [ ] Hover states with smooth transitions (150-300ms)
- [ ] Light mode: text contrast 4.5:1 minimum
- [ ] Focus states visible for keyboard navigation
- [ ] `prefers-reduced-motion` respected
- [ ] Responsive: 375px, 768px, 1024px, 1440px
- [ ] No content hidden behind fixed navbars
- [ ] No horizontal scroll on mobile

---

## Direction notes

> Written in P0 per `docs/DESIGN-BRIEF.md` §1, §2 and §10. The generated output above is the foundation;
> **these notes override it wherever they conflict.** The single source of truth in code is
> `web/app/globals.css` (`:root` tokens + `@theme inline`). Contrast numbers come from
> `python design-system/contrast.py`.

### The direction: Swiss transit signage, light
SLUICE is the calm station concourse at 09:59:59. It borrows the language of railway platform signage, the
environment a Tatkal booker already associates with "the train is real and the number is final": enamel navy
sign bands, one safety-orange signal colour, big condensed numerals, a strict grid, and no decoration that
doesn't carry information.

| Decision | Choice | Source in the skill's output |
|---|---|---|
| Palette | Navy `#1E3A8A` + booking orange `#F97316` on pale-blue paper `#EFF6FF` | Master palette above ("Sky blue + booking orange") |
| Style | Minimalism & Swiss Style: grid-first, flat, single accent, no shadows | `--domain style` result 2 ("transit signage wayfinding bold functional high contrast") |
| Type | **Barlow Condensed** (display + all numerals) / **Barlow** (body) | `--domain typography` result 1. Barlow is drawn from transit and highway signage, has tabular figures, and its condensed cut packs a 6-digit queue number into a 375 px screen |
| Theme | Light | Master palette (background `#EFF6FF`). Daylight-legible on a phone on a station platform, and far from neon-on-black |

### Signature element: the ticket stub
One component carries the concept through every screen: a **ticket stub**, a card split by a **perforated tear
line** (a dashed rule with two semicircle notches cut out of the card edges). The stub always holds **the one
number that matters right now**, set huge in Barlow Condensed with tabular figures; the body holds the context.

| Screen | The stub's number |
|---|---|
| Landing | 50,000 · 0 · 0 (the targets, labelled as targets until the CI run proves them) |
| Waiting room | Your place in line (`#41,234`), ticking down |
| Booking | Seats selected / seats left |
| Checkout | The hold timer (`04:59`), turning warning-amber under 60 s |
| Ticket | Coach and seat (`C3 · 27`) with the QR in the body |
| Results / Rush Control | Headline counters, one stub per invariant |

The orange accent appears on a stub only as the **signal dot** (live / your turn) or the active notch. That is
what makes the accent mean something.

### Resolved palette

| Role | CSS variable | Hex | Contrast paper / card / sunk | Use |
|---|---|---|---|---|
| Paper | `--paper` | `#EFF6FF` | n/a | Page background |
| Card | `--card` | `#FFFFFF` | n/a | Stubs, panels |
| Sunk | `--sunk` | `#DBEAFE` | n/a | Recessed bands; **only ink, ink-muted, navy, danger and info may sit on it as text** |
| Ink | `--ink` | `#0F1F47` | 14.76 / 16.06 / 13.17 | Body text, numerals |
| Ink muted | `--ink-muted` | `#3B4A6B` | 8.12 / 8.83 / 7.24 | Secondary text |
| Ink subtle | `--ink-subtle` | `#55647F` | 5.49 / 5.97 / 4.90 | Captions, timestamps |
| Navy (primary) | `--navy` | `#1E3A8A` | 9.52 / 10.36 / 8.49 | Sign bands, headings, links, focus ring |
| Accent strong | `--accent-strong` | `#C2410C` | 4.76 / 5.18 / 4.24 | Primary button fill (white text 5.18:1), accent text on paper/card |
| Accent strong hover | `--accent-strong-hover` | `#9A3412` | n/a (fill) | Primary button hover/pressed (white text, see pairs) |
| Accent (signal) | `--accent` | `#F97316` | 2.58 / 2.80 / 2.30 | **Never text on light surfaces.** Fills under ink text (5.73:1), signal dot, active notch; on ink 5.73:1 |
| Rule | `--rule` | `#C7D5EC` | decorative | Grid lines, dividers, perforation |
| Rule strong | `--rule-strong` | `#7D8DAA` | 3.08 / 3.35 / 2.75 | Input and seat outlines on paper/card (WCAG 1.4.11) |
| Success | `--success` | `#15803D` | 4.61 / 5.02 / 4.11 | Confirmed, ticket issued |
| Warning | `--warning` | `#B45309` | 4.61 / 5.02 / 4.12 | Hold timer < 60 s, PAUSED |
| Danger | `--danger` | `#B91C1C` | 5.95 / 6.47 / 5.30 | Failures (always with the "you were not charged" copy) |
| Info | `--info` | `#1D4ED8` | 6.16 / 6.70 / 5.49 | Pre-queue, neutral notices |

Text-on-fill pairs: white on navy 10.36, white on accent-strong 5.18, white on accent-strong-hover 7.31, ink on accent 5.73, white on ink 16.06,
sky `#BFDBFE` on navy 7.29, accent on navy 3.70 (large text / non-text only).

### Type scale and rhythm (8 px grid)
- Spacing: multiples of 8 px (4 px allowed only inside dense controls). Section gaps 64 / 96 px; card padding 24 / 32 px.
- Type scale (px / line-height): 12/16 caption · 14/20 small · 16/24 body · 20/28 lead · 24/32 h3 · 32/40 h2 · 48/56 h1 · stub numerals 64–144 (Barlow Condensed 600, `tnum`, tracking -0.01em).
- Labels: Barlow 600, 12 px, uppercase, tracking 0.12em (platform-sign labels).
- Radius: 4 px controls, 12 px stubs. Shadows: none. Depth comes from paper → card → ink inversion.

### Motion
- 150–200 ms colour/border transitions on interactive elements; pressed state = 1 px inset translate, no scale.
- Meaningful motion only: numerals tick (per-digit roll), seats flip, timer pulses under 10 s, signal dot breathes while live.
- `prefers-reduced-motion: reduce` turns every animation into an instant state change.

### Rejected from the generated output (and why)
- **Liquid Glass style:** glassmorphism is banned by the brief, and blur and translucency hurt legibility and low-end phone performance at exactly the wrong moment.
- **Cormorant + Montserrat:** a luxury/hospitality mood; anxious people at 10:00:00 need numerals that read instantly, not elegance.
- **Event/Conference landing pattern:** speakers, sponsors and early-bird pricing don't exist here; it's a template hero.
- **Shadow scale and card hover lift:** replaced by flat layering; lifts shift layout.
- **`#3B82F6` secondary as text:** it fails AA as text (3.38:1 on paper, 3.68:1 on card); `--info #1D4ED8` replaces it.
