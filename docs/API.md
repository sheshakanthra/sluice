# API: SLUICE

Base path `/api/v1`. JSON throughout, UTF-8. Times are RFC 3339 UTC. Money is integer paise.

## 1. Conventions

### 1.1 Auth: stateless JWT (HS256) with the `TOKEN_KEYS` key ring (`kid` header)
| Token | `aud` | Claims | TTL | Obtained from |
|---|---|---|---|---|
| Session | `session` | `sub` = user_id | 24 h | `POST /sessions` |
| Queue | `queue` | `sub`, `eid`, `tkt` (0 = pre-queue) | until event start | `POST /events/{id}/queue`, refreshed by status |
| Admission | `admission` | `sub`, `eid` | `ADMISSION_TTL` | queue status, once ADMITTED |

Send it as `Authorization: Bearer <token>`. Every token has `iss=sluice`, `iat`, `exp` and `jti`. The wrong audience or event gives 401 `TOKEN_INVALID`; an expired token gives 401 `TOKEN_EXPIRED`.

### 1.2 Idempotency (Stripe-style) on every mutating endpoint marked **IK**
- Header: `Idempotency-Key: <8–128 chars>`. The client generates it (UUID) once per user intent and reuses it on retries.
- Scope is `(user_id, key)`. The stored request hash is `sha256(method | route | canonical JSON body)`.
- Same key and same hash, already completed → **replay** the stored status and body, plus the header `Idempotent-Replayed: true`.
- Same key and a **different** hash → `422 IDEMPOTENCY_KEY_REUSED`.
- Same key while the first request is still in flight → `409 REQUEST_IN_PROGRESS` with `Retry-After: 1`.
- A STARTED key past `locked_until` (a crash) becomes re-executable. Handlers must be safe to re-run, which is why hold IDs are deterministic and PSP keys are deterministic.
- Keys are garbage-collected after 24 h. A missing key on an IK endpoint → `400 IDEMPOTENCY_KEY_REQUIRED`.
- With `BASELINE=naive` or `holds_only` (rush/test only), payment endpoints skip this. That's the point of the baseline.

### 1.3 Errors: RFC 9457 `application/problem+json`
```json
{ "type": "https://sluice.dev/errors/seats-taken", "title": "Some seats were just taken",
  "status": 409, "code": "SEATS_TAKEN", "detail": "2 of 3 seats are no longer available",
  "taken": [12, 13], "request_id": "01J..." }
```
| code | HTTP | When |
|---|---|---|
| `VALIDATION` | 400 | Bad body or params |
| `IDEMPOTENCY_KEY_REQUIRED` | 400 | IK endpoint without the header |
| `TOKEN_INVALID` / `TOKEN_EXPIRED` | 401 | |
| `NOT_ADMITTED` | 403 | Inventory endpoint without a valid admission token (when `QUEUE_ENFORCED`) |
| `NOT_FOUND` | 404 | |
| `SEATS_TAKEN` | 409 | Some requested seats are unavailable (`taken[]`) |
| `INSUFFICIENT_SEATS` | 409 | Fewer seats left than the quantity asked for (`available`) |
| `SOLD_OUT` | 409 | |
| `HOLD_LIMIT` | 409 | The user already has an active hold for this event |
| `HOLD_EXPIRED` | 409 | The hold expired or was reclaimed |
| `ORDER_STATE` | 409 | Action not allowed in the current order state |
| `REQUEST_IN_PROGRESS` | 409 | Concurrent request with the same IK |
| `QUOTA_EXCEEDED` | 422 | Would exceed `max_per_user` |
| `IDEMPOTENCY_KEY_REUSED` | 422 | |
| `PAYMENT_ATTEMPTS_EXHAUSTED` | 422 | More than 3 attempts |
| `RATE_LIMITED` | 429 | With `Retry-After` |
| `DEPENDENCY_UNAVAILABLE` | 503 | Redis/PG/PSP down; with `Retry-After` |

The client-observable outcomes of a rush (sold out, taken, rate-limited) are **never** 5xx (I8).

### 1.4 Path parameters
`{eventId}` accepts either the event UUID or its slug (for example `tatkal-chn-sbc`).

### 1.5 Other headers
`X-Request-Id` comes back on every response (and is accepted from trusted proxies). `RateLimit-Policy` / `RateLimit` follow the IETF draft fields on limited routes. Availability uses `ETag`; `If-None-Match` returns 304.

## 2. Endpoints

| Method | Path | Auth | IK | Purpose |
|---|---|---|---|---|
| GET | `/healthz` | — | | Liveness (always 200 while the process runs) |
| GET | `/api/v1/healthz` | — | | Same as `/healthz`, reachable through Caddy. Returns `{"status":"ok","mode":"live"}`. The web app uses it to detect LIVE vs REPLAY |
| GET | `/readyz` | — | | Readiness (PG + Redis reachable, migrations applied) |
| POST | `/api/v1/sessions` | — | | Create an anonymous user + session token |
| GET | `/api/v1/events` | — | | List events (+ status, sale times, counts) |
| GET | `/api/v1/events/{eventId}` | — | | Event details + seat layout |
| GET | `/api/v1/events/{eventId}/availability` | — | | Seat-state bitmap + counts (ETag) |
| POST | `/api/v1/events/{eventId}/queue` | session | | Join (idempotent per user); `?rejoin=true` after OFFER_EXPIRED |
| GET | `/api/v1/events/{eventId}/queue/status` | queue | | Position / admission (hot path; no Redis for waiting users) |
| DELETE | `/api/v1/events/{eventId}/queue` | queue or admission | | Leave (frees an admission slot) |
| POST | `/api/v1/events/{eventId}/holds` | admission | ✔ | Hold specific seats or best-available |
| DELETE | `/api/v1/holds/{holdId}` | admission | | Release your hold |
| POST | `/api/v1/orders` | admission | ✔ | Create an order for a hold → PSP intent |
| POST | `/api/v1/orders/{orderId}/pay` | session | ✔ | Pay with a saved test method (server-side confirm) |
| POST | `/api/v1/orders/{orderId}/payments` | session | ✔ | New payment attempt after a failure (≤ 3) |
| POST | `/api/v1/orders/{orderId}/confirm` | session | ✔ | Sync with the PSP and run ConfirmOrder (after redirect / polling) |
| GET | `/api/v1/orders/{orderId}` | session | | Order + tickets + payment summary (owner only) |
| POST | `/api/v1/webhooks/mockpay` | signature | | MockPay webhook inbox |
| GET | `/api/v1/stats/{eventId}` | — | | Live stats for Rush Control (cached ≤ 1 s) |
| POST | `/api/v1/admin/events/{eventId}/reset` | admin | | Reset the event (demo/test) |
| POST | `/api/v1/admin/events/{eventId}/rebuild-cache` | admin | | Rebuild Redis from PG (R1) |
| POST | `/api/v1/admin/events/{eventId}/open-sale` | admin | | `{ "opens_in_s": 30 }` sets T0 = now + N |
| POST | `/api/v1/admin/reconcile` | admin | | Run reconciliation now (the auditor's settle step) |
| POST | `/api/v1/demo/rush` | — (rate-limited) | | Demo mode only: `{ "bots": 2000, "opens_in_s": 20 }` → 202 |
| POST | `/api/v1/test/admit` | — | | Test/rush only: `{ "event_id", "user_id" }` → admission token |

Per-route rate limits (when `RATE_LIMITS_ENABLED`): sessions 20/min/IP; join 6/min/user and 30/min/IP; status 3/s/user (burst 6, in-process); holds 20/min/user; orders, pay and confirm 10/min/user; demo rush 1 per 2 min globally.

## 3. Key shapes

### 3.1 `POST /sessions` → 201
```json
{ "user_id": "0193...", "session_token": "eyJ...", "expires_at": "2026-11-22T10:00:00Z" }
```

### 3.2 `POST /events/{id}/queue` → 200
```json
{ "state": "PREQUEUE", "queue_token": "eyJ...", "ticket_no": null,
  "opens_at": "2026-11-22T04:30:00Z", "retry_after_ms": 2000 }
```
After T0 it returns `"state": "WAITING", "ticket_no": 41234`. Already sold out → `"state": "SOLD_OUT"`.

### 3.3 `GET /events/{id}/queue/status` → 200 (always 200 for valid tokens; the state carries the outcome)
```json
{
  "state": "WAITING",
  "ticket_no": 41234, "position": 40890, "ahead": 40889,
  "eta_s": 210, "outlook": "UNLIKELY",
  "counts": { "available": 120, "held": 310, "sold": 70, "capacity": 500 },
  "retry_after_ms": 14200,
  "queue_token": null,
  "admission_token": null,
  "opens_at": "2026-11-22T04:30:00Z"
}
```
`state` is one of `PREQUEUE | WAITING | PAUSED | ADMITTED | SOLD_OUT | OFFER_EXPIRED | CLOSED`.
- `queue_token` is set when the server re-issues it: PREQUEUE → WAITING carries the resolved ticket. The client must replace its stored token.
- `admission_token` and `admission_expires_at` are set only on ADMITTED.

### 3.4 `POST /events/{id}/holds` (IK) → 201
Request, one of:
```json
{ "seat_nos": [212, 213] }
{ "quantity": 2 }
```
Response:
```json
{ "hold_id": "5f0c...", "status": "ACTIVE", "seat_nos": [212, 213],
  "labels": ["C5-13", "C5-14"], "fence": 9182, "expires_at": "2026-11-22T04:35:12Z" }
```
Errors: `SEATS_TAKEN{taken}`, `INSUFFICIENT_SEATS{available}`, `SOLD_OUT`, `HOLD_LIMIT`, `QUOTA_EXCEEDED` (pre-checked in PG, enforced at confirm), `NOT_ADMITTED`.

### 3.5 `POST /orders` (IK) → 201
```json
// request
{ "hold_id": "5f0c..." }
// response
{ "order_id": "0193...", "status": "PENDING_PAYMENT", "amount_paise": 250000, "currency": "INR",
  "seat_nos": [212, 213], "hold_expires_at": "2026-11-22T04:40:12Z",
  "payment": { "attempt": 1, "intent_id": "pi_...", "checkout_url": "http://localhost:8000/pay/checkout/pi_..." } }
```
Creating the order moves the hold to PAYMENT_PENDING with `PAYMENT_HOLD_TTL`. A retry with the same IK replays, and a second order for the same hold returns the existing order (`hold_id` is unique).

### 3.6 `POST /orders/{id}/pay` (IK) → 200
```json
// request
{ "payment_method": "pm_ok" }
// response
{ "order_id": "...", "status": "CONFIRMED", "tickets": [ { "seat_no": 212, "label": "C5-13", "code": "SLC-7Q4K-92XM" } ] }
```
`payment_method` is one of `pm_ok | pm_decline | pm_timeout | pm_slow`. The status can come back `CONFIRMED`, `PENDING_PAYMENT` (the PSP is still processing, so poll `/confirm`), or `FAILED` with `failure_code` and `charged: false`.

### 3.7 `GET /orders/{id}` → 200
```json
{ "order_id": "...", "event_id": "...", "status": "CONFIRMED", "amount_paise": 250000,
  "payment": { "attempt": 1, "status": "CAPTURED" }, "charged": true,
  "tickets": [ ... ], "failure_code": null, "hold_expires_at": "..." }
```
`charged` is true only when a payment is AUTHORIZED or CAPTURED and not voided or refunded. The UI uses it for the "you were not charged" guarantee.

### 3.8 `GET /events/{id}/availability` → 200 / 304
```json
{ "version": 18231, "counts": { "available": 120, "held": 310, "sold": 70, "capacity": 500 },
  "states": "<base64: 2 bits per seat_no, 00=available 01=held 10=sold>" }
```
Each API replica serves this from an in-memory cache refreshed when `ver` changes.

### 3.9 `GET /stats/{id}` → 200
```json
{ "ts": "...", "event_status": "ON_SALE", "sale_opens_at": "...", "sold_out_at": null,
  "queue": { "joined": 50000, "waiting": 48211, "cursor": 1790, "offers": 140, "active": 610, "admit_rate": 182.4 },
  "inventory": { "available": 40, "held": 300, "sold": 160, "capacity": 500 },
  "correctness": { "oversold": 0, "duplicate_charges": 0, "orphan_charges": 0, "checked_at": "..." },
  "demo_rush": { "running": false } }
```

## 4. Webhooks (MockPay → SLUICE)
`POST /api/v1/webhooks/mockpay` with headers:
- `MockPay-Signature: t=<unix>,v1=<hex(hmac_sha256(MOCKPAY_WEBHOOK_SECRET, t + "." + raw_body))>`
- `MockPay-Event-Id: evt_...`

Verification uses a constant-time compare and a tolerance of |now − t| ≤ 300 s. A bad signature gets 400 and is counted. Then, in one tx: insert into `webhook_events` (a conflict means a duplicate: return 200 and do nothing) and enqueue `process_webhook`. Respond 200 fast.

Body:
```json
{ "id": "evt_...", "type": "intent.authorized", "created": 1763712000,
  "data": { "intent": { "id": "pi_...", "status": "authorized", "merchant_ref": "<order_id>", "amount_paise": 250000, "metadata": {} } } }
```
Types: `intent.authorized`, `intent.failed`, `intent.captured`, `intent.canceled`, `intent.refunded`.

## 5. MockPay API (internal `http://mockpay:8090`; browsers use `/pay/*` via Caddy)
| Method | Path | Notes |
|---|---|---|
| POST | `/v1/intents` | Bearer `MOCKPAY_API_KEY`, **IK required**. `{amount_paise, currency, merchant_ref, metadata, return_url, confirm?, payment_method?}` → 201 `{id, status, checkout_url}`. `confirm=true` with a method authorizes immediately (naive baseline uses this with random keys) |
| GET | `/v1/intents/{id}` | |
| GET | `/v1/intents?merchant_ref=&status=&updated_since=` | Reconciler |
| POST | `/v1/intents/{id}/confirm` | IK. `{payment_method}` → authorized \| failed (+ webhook) |
| POST | `/v1/intents/{id}/capture` | IK. Idempotent per intent (`UNIQUE(intent_id, kind)`) |
| POST | `/v1/intents/{id}/void` | IK |
| POST | `/v1/intents/{id}/refund` | IK |
| GET | `/v1/ledger/summary?metadata.event_id=&since=` | For the auditor: per merchant_ref `{intents, authorized, captured, refunded}` |
| GET | `/checkout/{id}` | Hosted page with a **TEST MODE** banner. Buttons: Pay (pm_ok), Decline (pm_decline), Simulate timeout (pm_timeout) |
| POST | `/checkout/{id}/pay` | Form or JSON `{payment_method}` → authorize and 303 to `return_url?order=...` |
| GET | `/metrics`, `/healthz` | |

MockPay semantics:
- The same IK with the same body replays the stored response; the same IK with a different body gets 422.
- `pm_timeout` and `MOCKPAY_TIMEOUT_AFTER_COMMIT_RATE` **commit first, then respond after `MOCKPAY_HANG_MS`**. That's the classic double-charge trap.
- Webhooks are delivered at least once with retries (0, 1, 2, 4, 8, 16 s; 6 attempts), with optional duplicates, reordering and delay according to the fault env vars.
- In test mode, the header `X-MockPay-Fault: timeout_after_commit|fail|dup_webhook|reorder_webhook` forces a fault deterministically.

The SLUICE PSP client uses a **2 s** timeout per call, retries ≤ 3 with jittered backoff, and always the same deterministic key: `order:<order_id>:attempt:<n>` to create, `confirm:<intent_id>`, `capture:<intent_id>`, `void:<intent_id>`, `refund:<intent_id>`.
