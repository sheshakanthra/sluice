# P3: Orders, MockPay, idempotent payments
**Timebox:** 6 h · **Read first:** ARCHITECTURE.md §3 (I2–I6), §4.3–4.5, §6 · DATA-MODEL.md §1–3 · API.md §3.5–3.7, §4, §5

## Goal
Money is always correct. No path ever charges twice, and no path keeps money without issuing a ticket. Every failure ends with an honest "you were not charged."

## Tasks
1. **MockPay** (`sluice mockpay`, `internal/mockpay`, its own DB `mockpay`, with its own goose migrations in a separate embed dir):
   - The full API in API.md §5, the IK semantics, and the append-only ledger.
   - Hosted checkout page `/checkout/{id}`: plain server-rendered HTML with a big **TEST MODE** banner, a fictional "MockPay" brand, and Pay / Decline / Simulate-timeout buttons → 303 to `return_url`.
   - Webhook dispatcher: a goroutine pool over `webhook_deliveries` with `FOR UPDATE SKIP LOCKED`, HMAC signing, retries (0, 1, 2, 4, 8, 16 s), and the faults from env plus the `X-MockPay-Fault` test header.
   - `pm_timeout` and the timeout-after-commit faults **commit first, then hang** for `MOCKPAY_HANG_MS`.
   - `/v1/ledger/summary` for the auditor. `/metrics`.
2. **PSP client** (`internal/payments`): 2 s timeout, ≤ 3 jittered retries, deterministic keys (API.md §5), typed errors. **Webhook verification** with constant-time compare and the 300 s tolerance.
3. **Orders** (`internal/orders`):
   - `POST /orders`: tx (order PENDING_PAYMENT; hold → PAYMENT_PENDING with `PAYMENT_HOLD_TTL`; payment attempt 1 CREATED), then `hold_extend` in Redis, then the PSP intent (outside the tx), then store `provider_intent_id`. Retrying after a PSP failure re-runs the create with the **same** PSP key.
   - `POST /orders/{id}/pay`: confirm the intent at MockPay with a saved method (key `confirm:<intent>`), then ConfirmOrder.
   - `POST /orders/{id}/payments`: a new attempt (≤ 3), only when the previous attempt is FAILED or VOIDED.
   - `POST /orders/{id}/confirm`: pull from the PSP, then ConfirmOrder. `GET /orders/{id}` (owner only), returning `charged`.
   - **ConfirmOrder** exactly as in ARCHITECTURE §4.3. It's the single convergence point and it's idempotent.
4. **Webhook inbox:** `POST /webhooks/mockpay` → verify → insert `webhook_events` + River `InsertTx(process_webhook)` → 200. Duplicates → 200 no-op.
5. **River workers** in `sluice worker`:
   - `process_webhook`, `capture_payment`, `void_payment`, `refund_payment`: unique per (kind, intent), with retries and backoff.
   - Periodic `reconcile` (every `RECONCILE_INTERVAL`), as in ARCHITECTURE §4.5. The sweeper now also expires PENDING_PAYMENT orders and voids any authorization.
   - `POST /admin/reconcile` runs it synchronously.
6. **Redis follow-ups:** after confirm, `hold_consume`; after failure or expiry, `hold_release`. Both best effort, and repaired by the reconciler.
7. **Metrics:**
   - `orders_total{status}`, `payments_total{status}`, `psp_requests_total{op,result}`, `psp_request_duration_seconds{op}`
   - `webhooks_total{type,result}` (processed / duplicate / bad_signature / regression_ignored)
   - `duplicate_charges_prevented_total`, `orphans_fixed_total`, `idempotency_replays_total`
8. **Live audit cache:** a worker job every 5 s computes oversold, duplicate and orphan counts for each ON_SALE event into `sl:{e:<eid>}:audit` (used by `/stats` in P4).

## Required tests (integration: PG + Redis + an in-process MockPay server)
| Case | Expected |
|---|---|
| Happy path: hold → order → pay `pm_ok` | CONFIRMED, tickets issued, capture job → CAPTURED, ledger has 1 capture |
| Double-click `/pay`, same IK, concurrent | 1 authorization, 1 capture, both responses consistent |
| Retry `/pay` with a **new** IK | No second intent and no second charge; returns CONFIRMED |
| `pm_timeout` (commit, then hang past the client timeout) + retry with the same IK | 1 intent, 1 charge |
| Webhook duplicated 3× | Processed once; inbox has 1 row |
| Webhook `captured` arrives before `authorized` | Final state CAPTURED; the regression is ignored and counted |
| Webhook never arrives; client calls `/confirm` | CONFIRMED via the pull |
| Hold expires and is reclaimed **before** the authorization lands | Order FAILED (HOLD_LOST), intent VOIDED, `charged:false`, seats with the new holder |
| Authorization lands **just before** the sweeper | CONFIRMED; the sweeper no-ops |
| Quota exceeded at confirm (forced: bump the user's quota row to 3 after the hold of 2 was placed, bypassing the hold-time pre-check) | FAILED (QUOTA_EXCEEDED), voided, not charged |
| Capture fails 2× (PSP 5xx), then succeeds | CAPTURED after retries, exactly 1 ledger capture |
| An orphan created artificially (authorized intent, no order) | Reconciler voids it within one cycle |
| Two authorized attempts for the same order (forced) | The second is voided; `duplicate_charges_prevented_total` += 1 |
| **Property test** (`rapid`): random interleavings of pay / confirm / webhooks (dup, reorder) / sweeper / reconcile / capture failures over 1–3 orders sharing seats | After settle: I1–I4 hold, ledger captures per order ≤ 1, and every captured intent has a CONFIRMED order or a refund |

## Acceptance
- [ ] Every test above passes under race.
- [ ] Manual check with MockPay running: book on `quiet-tuesday` through the hosted checkout in a browser (Pay, Decline, Timeout). Every outcome shows the correct `charged` state.
- [ ] Run `task lint test` and both come back green.

**Commit hint:** `feat(payments): MockPay PSP, idempotent orders, confirm with fencing, reconciler`
