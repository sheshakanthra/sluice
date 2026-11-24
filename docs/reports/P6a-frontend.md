# P6a: Frontend foundation and rider flow: Report

| Field | Value |
|---|---|
| Status | DONE for the P6a scope (tasks 1–3, limited to the queue, book, checkout and order pages). Rush Control, Results, replay data, Playwright e2e, Lighthouse and JS budgets are P6b |
| Date (IST) | 2026-11-24 |
| Time spent vs timebox | Not tracked precisely. P6 is timeboxed at 7 h across P6a and P6b |
| Commit base | `1e8d159` (`git rev-parse --short HEAD`) |

## 1. What was built
- **Page overrides (task 1, 3 of 5):** `design-system/pages/{queue,book,checkout}.md` came from the DESIGN-BRIEF §2 `--page` commands and were moved up per ADR 0002. Each one ends with a "Direction notes (P6a)" section saying what was taken from the raw output and what was rejected. Most of the rejections are dark themes, marketing sections and blink effects, which conflict with MASTER's light transit-signage direction. `control` and `results` are left for P6b.
- **Foundation (`web/lib`):**
  - `api.ts`: a typed client with one function per endpoint. It turns problem+json into `ApiError` (NETWORK and TIMEOUT included) and corrects for server clock offset from the `Date` header, intersecting the bounds of every response. It reads `NEXT_PUBLIC_API_BASE_URL` in dev and uses same-origin `/api/v1` otherwise.
  - `intent.ts`: one Idempotency-Key per user intent, stored in localStorage. The key is reused after transient failures and dropped once the server has decided.
  - `storage.ts`: try/catch-wrapped localStorage plus a subscribable per-event entry store.
  - Other modules: `session.ts`, `mode.ts` (LIVE/REPLAY via `/api/v1/healthz`, 1.5 s timeout), `queue.ts` (join/rejoin and give-the-slot-back), `seats.ts` (bitmap decode, layout geometry), `format.ts` (IST, ₹ en-IN), `hooks.ts`.
  - TanStack Query (`components/providers.tsx`): polling follows `retry_after_ms` and `Retry-After`, continues in background tabs, and availability uses ETag/304.
- **Components:** `SiteHeader` + `ModeBadge`, `Notice`, `NotChargedNotice`, `ErrorProblem` (code → human copy), `StatePanel`, `ReplayNotice`, `RejoinPanel`, `TickingNumber`, `ProgressRail`, `OutlookBadge`, `SeatCounts`, `SeatMap` (`CoachTabs` with per-coach minimaps, `SeatGrid`, memoised `SeatCell`, `Legend`), `QuantityStepper`, `HoldTimer`, `PriceSummary`, `TicketCard` + `QRCode` (client-side, `uqr`). `ui/button.tsx` was restyled to the MASTER direction: 4 px radius, flat, every size at least 44 px.
- **Routes:**
  - `/queue?e=` covers PREQUEUE, WAITING, PAUSED, ADMITTED (3-2-1, then `/book`), SOLD_OUT, OFFER_EXPIRED (rejoin, rejoins left), CLOSED and LEFT (leave the line: `DELETE /queue`).
  - `/book?e=` covers NOT_ADMITTED, SEATS_TAKEN (flash plus "hold best N instead"), INSUFFICIENT (0 left gives the slot back), SOLD_OUT, HOLD_LIMIT, ADMISSION_EXPIRED (rejoin screen) and Leave (`DELETE /queue`).
  - `/checkout?o=` covers pending, processing (confirm every 6 s), declined (not charged, new attempt up to 3), and failed/expired (not charged).
  - `/order?o=` covers CONFIRMED tickets, PENDING (auto-poll plus confirm after the MockPay redirect), and FAILED/EXPIRED (not charged).
  - In REPLAY mode, each booking route shows an explainer plus "run it yourself".
- **Deps:** `@tanstack/react-query@5.108.4`, `uqr@0.1.3`.

## 2. Acceptance (P6a scope)
- [x] Quiet-tuesday booked end to end through Caddy :8000 using the hosted MockPay checkout, at both widths. Command: `node flow.mjs docs/reports/assets/p6a 375 1440` (Playwright + Edge, script kept in the session scratchpad):
  ```
  == 375px   admitted + on /book after 4740 ms   confirmed after 8898 ms, ticket codes: SLC-BJS3-YS6C, SLC-J2N3-ENMB   console errors: none
  == 1440px  admitted + on /book after 4366 ms   confirmed after 6692 ms, ticket codes: SLC-K5MN-Z68V, SLC-VDP5-SQJA   console errors: none
  ```
- [x] Decline path: `node flow.mjs ... 375 --decline`. Decline on MockPay → "You were not charged" → attempt 2 → saved card → `confirmed after 7303 ms ... console errors: none`.
- [x] 403 ADMISSION_EXPIRED → rejoin screen, plus leaving the queue on give-up. Command: `node states.mjs ...`:
  ```
  DELETE /api/v1/events/quiet-tuesday/queue -> 204         (Leave, give my turn away)
  POST /api/v1/events/quiet-tuesday/holds -> 403           (idle 65 s > ADMISSION_IDLE_TTL=60s)
  rejoin screen after 65216 ms idle / rejoined and admitted again
  ```
- [x] Screenshot critique at 375 and 1440 done and fixed (§3).
- [x] `pnpm lint` → `$ eslint` (no findings). `pnpm typecheck` → `✓ Types generated successfully`, tsc clean. `pnpm format:check` → `All matched files use Prettier code style!`. `pnpm build` → 8/8 static pages.
- [x] `task lint` → `golangci-lint run ./...  0 issues.`
- [ ] `task test:race` was not run. This session changed no Go code, and P5b recorded host memory pressure on the local race gate. The Go race gate is unchanged since P5b.
- [ ] E1–E5, axe, Lighthouse, JS budgets: P6b.

## 3. Screenshot critique (`docs/reports/assets/p6a/*.png`)
| Page / width | Finding | Fix |
|---|---|---|
| all / 375 + 1440 | The h1 already ends with the route ("Quiet Tuesday, Chennai → Bengaluru") and the subline repeated it. At 375 a dangling "·" wrapped onto its own line | `EventHeading` drops the route line when the name contains the venue |
| queue / 375 | "Estimated wait: under a minute" wrapped to 2 lines of 36 px display type, breaking the 2-column rhythm | Compact ETA `< 1 min` / `3–4 min` |
| book / 375 | The "Leave" button floated centred under the timer: no alignment anchor | Left-aligned on mobile, right on desktop |
| book / both | The column caption ("Window · Middle · Aisle \| Aisle · Window") was 12 px loose text that didn't line up with the seats | Aligned W / M / A header row on the seat grid's own column template, an "aisle" marker, and a "Front of coach C1" sign rule |
| book / 375 | The sticky hold bar was 95 % translucent over seats | Solid card with a 2 px navy top rule and a safe-area inset |
| checkout / 1440 | `max-w-5xl` left edge (240 px) didn't match the header and book grid (176 px) | `max-w-6xl` like the header |
| order / both | The ticket had no departure name, only the route | Event name added to `TicketCard` |
| book / 1440 | Last-clicked seat looked paler than the other pick | Not a bug: hover state (`hover:` is `@media (hover:hover)` in Tailwind v4, so touch screens don't get it) |
| checkout-declined | Timer read 01:30 after a decline, which looked like a reset | Not a bug: logged `hold_expires_at` vs now showed 01:29 left, because the decline round trip takes about 1 s |

After the fixes: no horizontal overflow at either width (the flow script checks `scrollWidth`, and no OVERFLOW lines were printed).

## 4. UI UX Pro Max pre-delivery checklist (queue / book / checkout / order)
| Check | Q | B | C | O |
|---|---|---|---|---|
| No emoji icons (Lucide SVG only; the "✓" glyph in the processing steps was replaced) | ✅ | ✅ | ✅ | ✅ |
| `cursor-pointer` on clickables, `not-allowed` on held/sold seats | ✅ | ✅ | ✅ | ✅ |
| Hover transitions 150–300 ms, no layout-shifting scale | ✅ | ✅ | ✅ | ✅ |
| Text contrast ≥ 4.5:1 (MASTER tokens only; signal orange never used as text) | ✅ | ✅ | ✅ | ✅ |
| Visible focus (navy outline), roving tabindex on the seat grid and coach tabs | ✅ | ✅ | ✅ | ✅ |
| `prefers-reduced-motion` (global rule in `globals.css`; spinner `motion-reduce`) | ✅ | ✅ | ✅ | ✅ |
| Responsive 375 / 1440 checked by screenshot (768 / 1024 not captured in P6a) | ✅ | ✅ | ✅ | ✅ |
| No horizontal scroll on mobile | ✅ | ✅ | ✅ | ✅ |
| Colour never the only cue (seat icons, outlook label + icon, counts as text) | ✅ | ✅ | ✅ | ✅ |
| Touch targets ≥ 44 px (seats 44/48 px, all buttons ≥ 44 px) | ✅ | ✅ | ✅ | ✅ |

## 5. Deviations and decisions
| What | Why | ADR |
|---|---|---|
| The order is created right after the hold (book → hold → order → `/checkout?o=`) | The checkout route is keyed by order id (DESIGN-BRIEF §3). There's no cancel endpoint for an order, so the checkout offers no "cancel": the copy says the seats return when the timer ends and you're not charged | none (no spec change) |
| The book page polls `queue/status` in the background | Only hold, order and release requests renew the admission lease, so polling is the only way to see OFFER_EXPIRED before a 403 | none |
| Rejoins left is counted client-side (2 minus the rejoins this browser made); `REJOIN_LIMIT` from the server overrides it | The status response carries no rejoin count | none |

## 6. Known issues and open items
- **For Sheshakanth (scope/architecture):** in the rush profile, `ADMISSION_IDLE_TTL=60s` ends an admitted user's turn after 60 s without a hold or order request, and nothing a person does while browsing the map renews it. A real user picking seats for more than a minute gets the rejoin screen (verified above). The band now says "at most … An idle turn ends sooner". Options: a longer idle TTL for the demo/dev profiles, or let the admission-token availability poll renew the lease.
- In dev, cross-origin (`next dev` :3000 → API), `Date` and `ETag` aren't CORS-exposed. The offset falls back to 0 and the 304 path is skipped. Same-origin via Caddy is unaffected.
- P6b: the replay video on the booking routes, Rush Control, Results, replay data, e2e E1–E5 + axe, Lighthouse, JS budgets and the `control`/`results` overrides.

## 7. Understand this before moving on
1. **Q:** How does the UI stop a double tap on "Pay" from charging twice? **A:** `lib/intent.ts:withIntent` stores one key per intent (`pay:<order>:<attempt>`) and reuses it until the server answers. The server replays the stored response for the same key (API §1.2), and the PSP key is deterministic as well.
2. **Q:** Why is the hold timer right even when the phone's clock is wrong? **A:** `lib/api.ts:noteServerDate` turns each response's `Date` header into bounds on the offset [date − recv, date + 1000 − sent] and intersects them. `HoldTimer` counts down to `hold_expires_at` on `serverNow()`.
3. **Q:** What happens when an admitted user gives up? **A:** `lib/queue.ts:giveSlotBack` calls `DELETE /events/{id}/queue` with the admission token (204 above), which frees the slot at once instead of waiting for the idle TTL.

## 8. Files changed
```
 M web/app/globals.css  web/app/layout.tsx  web/components/ui/button.tsx  web/package.json  web/pnpm-lock.yaml
?? design-system/pages/{book,checkout,queue}.md
?? docs/reports/P6a-frontend.md  docs/reports/assets/p6a/
?? web/app/{book,checkout,order,queue}/
?? web/components/{checkout-parts,event-heading,notices,providers,queue-parts,rejoin-panel,seat-map,site-header}.tsx
?? web/lib/{api,format,hooks,intent,mode,queue,seats,session,storage}.ts
```

## 9. Next phase readiness
P6b can start: the foundation (`lib/api.ts` already has `getStats`, `mode.ts`) and the components are in place. The stack was stopped with `task stack:down` (volumes kept).

## 10. Git commands (PowerShell-safe)
```powershell
git add -A
git status --short
git commit -m "feat(web): waiting room, seat map, checkout and ticket pages" -m "- Typed API client with problem+json, server clock offset and per-intent idempotency keys" -m "- Queue, book, checkout and order routes covering every state incl. rejoin and leave" -m "- Seat map with coach minimaps, roving tabindex grid and colour-independent states" -m "- Page overrides for queue, book and checkout; screenshot critique at 375 and 1440" -m "Phase P6a - report: docs/reports/P6a-frontend.md"
```
