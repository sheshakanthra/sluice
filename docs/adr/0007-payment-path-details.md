# ADR 0007: Payment path details: reclaim settlement, late authorizations, MockPay additions

- **Status:** accepted
- **Date:** 2026-11-23
- **Phase:** P3

## Context
P3 builds the payment path from ARCHITECTURE §4.3–4.5, DATA-MODEL §2–3 and API §3.5–3.7/§4/§5. Building it surfaced places where the specs disagree with each other or leave something open:
1. **What the sweeper does to an order.** ARCHITECTURE §4.4 said the sweeper "expires its order ... and enqueues `void_payment` if a payment was authorized". The P3 test table says an order whose hold was reclaimed before its authorization landed ends **FAILED (`HOLD_LOST`)** with the intent VOIDED. Both can't be right: an EXPIRED order with voided money and a FAILED one describe the same situation.
2. **Lock order.** ConfirmOrder locks order → hold. The sweeper locks hold → (order). Run naively, the two deadlock.
3. **A canceled intent that was authorized.** When a CREATED payment's intent is voided after the PSP authorized it but before SLUICE heard about it, the PSP says `canceled`. The payment state machine had no CREATED → VOIDED edge, and a plain `canceled` status doesn't say whether money was ever held.
4. **Authorizations that land after the order is terminal** (FAILED/EXPIRED). §4.3 only covered "another attempt is live".
5. **Hosted checkout return.** API §5 has MockPay 303 to `return_url?order=...`, but the intent schema had no `return_url` column. DESIGN-BRIEF/P6 have the web page read `/order?o=<id>`.
6. **Webhook job uniqueness.** "Unique jobs" was unspecified. Unique per intent would drop the second of two different events for one intent while the first is queued.
7. **The reconciler and in-flight orders.** §4.5 voided any authorized intent "whose order isn't CONFIRMED" after 30 s, which would void a PENDING order whose webhook was merely lost.
8. **Append-only ledger.** DATA-MODEL §2 called the ledger append-only, but only by comment.

## Decision
- **Reclaim settles the order through the money.** The sweeper and the takeover path call an orders `ReclaimHook` inside the reclaim transaction. It locks the order with `FOR UPDATE SKIP LOCKED` (if ConfirmOrder holds the lock, it is mid-flight and will itself find the hold gone and settle). Settling enqueues `void_payment` for every attempt that may hold money (CREATED with an intent, AUTHORIZED) and `refund_payment` for CAPTURED ones, and leaves the order PENDING_PAYMENT while any is outstanding. Once nothing is pending, the order becomes **FAILED `HOLD_LOST`** if money was ever authorized (a payment is VOIDED/REFUNDED, or the PSP intent says `authorized: true`), else **EXPIRED**. An order with no intent expires at once. The void/refund jobs, ConfirmOrder and the reconciler all call the same settle step. This keeps both spec statements true in their own cases: never-paid orders EXPIRE; paid-then-lost orders FAIL with `HOLD_LOST` and `charged:false`.
- **New payment edge CREATED → VOIDED** (an intent canceled after a PSP authorization SLUICE never recorded). CREATED → FAILED covers a cancel with no authorization.
- **MockPay derives `authorized`** from the ledger (an `AUTH` entry exists) and returns it on every intent.
- **Late authorizations** for a terminal order with no live attempt are recorded AUTHORIZED and voided in the same transaction; a second authorization while another attempt is live is voided and counted in `duplicate_charges_prevented_total` (the partial unique index stays the backstop).
- **`intents.return_url`** column (`NOT NULL DEFAULT ''`). SLUICE sends `PUBLIC_APP_URL + "/order?o=<order_id>"` (new env var `PUBLIC_APP_URL`); MockPay adds `order`, `intent` and `status`. The web page reads `o`; the duplicate `order` param is MockPay's generic contract and harmless. `checkout_url` is `MOCKPAY_PUBLIC_URL + "/checkout/<intent>"`. The checkout form uses the internal key `checkout:<intent>:<method>`, so a double click replays.
- **`process_webhook` is unique per inbox row** (`provider`, `event_id`). All River unique jobs use `ByArgs` with every non-completed state, so a finished job never blocks a later legitimate one (for example a second `capture_payment` re-enqueued by the reconciler).
- **The reconciler converges before it voids.** An authorized/captured intent older than `OrphanAge` whose order is still PENDING_PAYMENT is attached (if a crash lost the id) and pushed through ConfirmOrder; only money with no confirmed owner is voided or refunded. A CONFIRMED order paid by a different attempt counts as a prevented duplicate.
- **MockPay specifics:** it migrates its own database at start-up (goose, embedded) and is never touched by `sluice migrate`. Voiding a `requires_payment` intent cancels it with no ledger entry. State-level no-ops (confirm an authorized intent, void a canceled one, refund a refunded one) return the intent unchanged, so retries with a fresh key are safe. `X-MockPay-Fault` is honoured only in `rush`/`test`. `fail` answers 503 before the transaction, so nothing (not even the idempotency row) is stored.
- **Ledger append-only in the database:** a statement trigger rejects UPDATE, DELETE and TRUNCATE on `ledger_entries` (SQLSTATE 42501).
- **The hosted checkout page uses MockPay's own fictional brand**, not SLUICE's design system, because it stands in for a third-party PSP page. It still meets the MASTER.md accessibility checklist (labels, focus states, contrast, a yellow TEST MODE banner, no JS).
- **The live audit cache** (`sl:{e:<eid>}:audit`, 30 s TTL) is written by an `audit_cache` periodic job every 5 s; P4's `/stats` reads it.

## Consequences
- An order whose hold is reclaimed with money outstanding stays PENDING_PAYMENT for one job round trip (the void) before it settles. The UI must keep polling PENDING orders (P6 already does).
- Every path that touches money ends in the same settle step, so the outcome no longer depends on whether the sweeper, the webhook or the user's `/confirm` runs first. `TestConfirmSweeperRace`, `TestHoldReclaimedBeforeAuthorization` (both job orders) and the rapid property test check this.
- `PUBLIC_APP_URL` must be set to the web origin in the stack (`http://localhost:8000`) and in live demo mode.
- Docs updated in this phase: ARCHITECTURE §4.3–4.5, DATA-MODEL §2 (now the migration verbatim) and §3, API §2 (admin reconcile), §3.5–3.7, §4, §5, STACK §4 (`PUBLIC_APP_URL`) and the repo layout, `.env.example`.
