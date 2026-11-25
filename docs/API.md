# API: SLUICE

Base path `/api/v1`. JSON throughout, UTF-8. Times are RFC 3339 UTC. Money is integer paise.

## 1. Conventions

### 1.1 Auth: stateless JWT (HS256) with the `TOKEN_KEYS` key ring (`kid` header)
| Token | `aud` | Claims | TTL | Obtained from |
|---|---|---|---|---|
| Session | `session` | `sub` = user_id | 24 h | `POST /sessions` |
| Queue | `queue` | `sub`, `eid`, `tkt` (0 = pre-queue) | until event start | `POST /events/{id}/queue`, refreshed by status |
| Admission | `admission` | `sub`, `eid` | `ADMISSION_TTL` (hard cap; the server-side lease also ends after `ADMISSION_IDLE_TTL` without a request, or when the checkout ends; `POST /holds` and `POST /orders` then answer 403 `ADMISSION_EXPIRED`) | queue status, once ADMITTED |

Send it as `Authorization: Bearer <token>`. Every token has `iss=sluice`, `iat`, `exp` and `jti`. The wrong audience or event gives 401 `TOKEN_INVALID`; an expired token gives 401 `TOKEN_EXPIRED`.

### 1.2 Idempotency (Stripe-style) on every mutating endpoint marked **IK**
- Header: `Idempotency-Key: <8–128 chars>`. The client generates it (UUID) once per user intent and reuses it on retries.
- Scope is `(user_id, key)`. The stored request hash is `sha256(method | route | canonical JSON body)`.
- Same key and same hash, already completed → **replay** the stored status and body, plus the header `Idempotent-Replayed: true`. Responses with status < 500 are stored (business 4xx outcomes included). A 5xx deletes the key, so the retry runs again. The replayed body has the same JSON values, but key order and spacing follow Postgres `jsonb` (ADR 0006).
- Canonical JSON means the body decoded and re-encoded with object keys sorted and numbers kept verbatim, so whitespace and key order don't change the hash. Bodies are capped at 64 KiB (every API route, and at Caddy).
- Same key and a **different** hash → `422 IDEMPOTENCY_KEY_REUSED`.
- Same key while the first request is still in flight → `409 REQUEST_IN_PROGRESS` with `Retry-After: 1`.
- A STARTED key past `locked_until` (a crash) becomes re-executable. Handlers must be safe to re-run, which is why hold IDs are deterministic and PSP keys are deterministic.
- Keys are garbage-collected after 24 h. A missing key on an IK endpoint → `400 IDEMPOTENCY_KEY_REQUIRED`.
- With `BASELINE=naive` or `holds_only` (rush/test only), payment endpoints skip this. That's the point of the baseline. The overridden routes (naive: hold, create, pay, new attempt, confirm, get; holds_only: pay) are mounted without the idempotency middleware; the real routes keep it (ADR 0011).

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
| `ADMISSION_EXPIRED` | 403 | `POST /holds` or `POST /orders` with a valid admission token whose server-side lease has ended (idle past `ADMISSION_IDLE_TTL`, checkout ended, left the queue, or past the cap). The client may rejoin (`POST /queue?rejoin=true`, two-rejoin limit). ADR 0010 addendum part 3 |
| `NOT_FOUND` | 404 | |
| `SEATS_TAKEN` | 409 | Some requested seats are unavailable (`taken[]`) |
| `INSUFFICIENT_SEATS` | 409 | Fewer seats left than the quantity asked for (`available`) |
| `SOLD_OUT` | 409 | |
| `HOLD_LIMIT` | 409 | The user already has an active hold for this event |
| `HOLD_EXPIRED` | 409 | The hold expired or was reclaimed |
| `REJOIN_LIMIT` | 409 | `POST /queue?rejoin=true` after the 2 allowed rejoins (ADR 0008) |
| `EVENT_STATE` | 409 | Admin open-sale on an event that sold out, closed, or whose queue already has entries (ADR 0009) |
| `ORDER_STATE` | 409 | Action not allowed in the current order state |
| `REQUEST_IN_PROGRESS` | 409 | Concurrent request with the same IK |
| `QUOTA_EXCEEDED` | 422 | Would exceed `max_per_user` |
| `IDEMPOTENCY_KEY_REUSED` | 422 | |
| `PAYMENT_ATTEMPTS_EXHAUSTED` | 422 | More than 3 attempts |
| `RATE_LIMITED` | 429 | With `Retry-After` |
| `DEPENDENCY_UNAVAILABLE` | 503 | Redis/PG/PSP down; with `Retry-After` |
| `OVERLOADED` | 503 | This API replica is at its in-flight limit (`MAX_IN_FLIGHT`); with `Retry-After: 1`. Retry the same request (same `Idempotency-Key`). Probes and `/metrics` are never shed (ADR 0013) |
| `INTERNAL` | 500 | Unexpected fault only (recovered panic, bug). Never a business outcome; see ADR 0003 |

The client-observable outcomes of a rush (sold out, taken, rate-limited) are **never** 5xx (I8).

### 1.4 Path parameters
`{eventId}` accepts either the event UUID or its slug (for example `tatkal-chn-sbc`).

### 1.5 Other headers
`X-Request-Id` comes back on every response (and is accepted from trusted proxies). `RateLimit-Policy` / `RateLimit` follow the IETF draft (draft-ietf-httpapi-ratelimit-headers) structured fields on limited routes, e.g. `RateLimit-Policy: "join-user";q=6;w=60` and `RateLimit: "join-user";r=5;t=10`; the most constrained rule of the route is reported. A 429 carries `Retry-After` in whole seconds. Availability uses `ETag`; `If-None-Match` returns 304.

## 2. Endpoints

| Method | Path | Auth | IK | Purpose |
|---|---|---|---|---|
| GET | `/healthz` | — | | Liveness (always 200 while the process runs) |
| GET | `/api/v1/healthz` | — | | Same as `/healthz`, reachable through Caddy. Returns `{"status":"ok","mode":"live"}` (`"demo"` once demo mode lands in P7; Rush Control shows "Start a demo rush" only then). The web app uses it to detect LIVE vs REPLAY |
| GET | `/readyz` | — | | Readiness (PG + Redis reachable, migrations applied). Served from a cached state refreshed in the background (≤ 1 s old) over dedicated health connections, so a busy replica still answers fast; 503 while draining on shutdown (ADR 0013) |
| POST | `/api/v1/sessions` | — | | Create an anonymous user + session token |
| GET | `/api/v1/events` | — | | List events (+ status, sale times, counts) |
| GET | `/api/v1/events/{eventId}` | — | | Event details + seat layout |
| GET | `/api/v1/events/{eventId}/availability` | — (optional admission) | | Seat-state bitmap + counts (ETag). An admission token for this event renews the lease (§3.3) |
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
| POST | `/api/v1/admin/events/{eventId}/open-sale` | admin | | `{ "opens_in_s": 0..86400 }` sets T0 = now + N and status SCHEDULED → 200 `{event_id, sale_opens_at, status}`. Only while nobody holds a ticket or lottery entry, else 409 `EVENT_STATE` (ADR 0009) |
| POST | `/api/v1/admin/reconcile` | admin | | Run reconciliation now (the auditor's settle step), synchronously → 200 `{orphans_fixed, duplicates_fixed, converged, captures_requeued, dead_holds_settled, inventory_repaired}`; 503 if a step failed |
| POST | `/api/v1/demo/rush` | — (rate-limited) | | Demo mode only: `{ "bots": 2000, "opens_in_s": 20 }` → 202 |
| POST | `/api/v1/test/admit` | — | | Test/rush only: `{ "event_id", "user_id" }` → admission token |

Per-route rate limits (when `RATE_LIMITS_ENABLED`): sessions 20/min/IP; join 6/min/user and 30/min/IP; status 3/s/user (burst 6, in-process); holds 20/min/user; orders, pay and confirm 10/min/user each (new payment attempts share the pay bucket); demo rush 1 per 2 min globally (P7). Redis GCRA (go-redis/redis_rate), shared by all replicas; burst = the per-minute count. The user is the verified token subject (any audience); a request without a valid token skips user rules and is then rejected by auth. The client IP is the peer, or, when the peer is in `TRUSTED_PROXIES`, the right-most `X-Forwarded-For` entry that is not a trusted proxy. If Redis fails the check, the request passes (fail open; `ratelimit_decisions_total{result="error"}`). The status limiter keeps at most 200,000 users per replica (sharded LRU). `POST /queue` answers 503 `DEPENDENCY_UNAVAILABLE` (Retry-After 1) if the queue counters cannot be restored (ADR 0009).

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
- `admission_token` and `admission_expires_at` are set only on ADMITTED. Both carry the admission's hard cap (`q:cap`: claim + `ADMISSION_TTL`), floored to the second, so repeated polls report the same value.
- The admission is also a sliding lease: every request that presents the admission token renews it to `ADMISSION_IDLE_TTL` from now (never past the cap). An admitted user who makes no request for `ADMISSION_IDLE_TTL` loses the admission: `POST /holds` and `POST /orders` answer 403 `ADMISSION_EXPIRED`, and a later status poll answers `OFFER_EXPIRED` (ADR 0010 addendum). A lapsed lease is never renewed. `GET /availability` is public but also renews the lease when it carries a valid admission token for that event (best effort, never refuses; a missing, invalid or foreign token is ignored). The web client sends it only while the page is visible (Page Visibility API), so a visible seat map keeps the admission alive up to the cap and a hidden or closed tab lets it lapse (ADR 0010 addendum part 4). Clients that end without seats should still call `DELETE /queue` at once.
- An admission ends early when the user's checkout ends: their order reaches CONFIRMED, FAILED or EXPIRED, or their hold is released or expires (ADR 0010 addendum). The admission token still verifies until its `exp`, but it can no longer be spent: `POST /holds` and `POST /orders` check the lease in Redis (the same `queue_touch.lua` call that renews it, so no extra round trip) and answer 403 `ADMISSION_EXPIRED`. A later status poll answers `OFFER_EXPIRED`, because the admission is no longer in `q:active` and the offer was used. A rejoin is then honoured (bounded by the two-rejoin limit and the per-user quota). Other admission-token routes (`DELETE /holds/{id}`, `DELETE /queue`) still accept the token. If the check itself fails (Redis unreachable), the two routes answer 503 `DEPENDENCY_UNAVAILABLE`. If Redis lost the queue state (`q:init` missing, R1), the token alone decides until the queue is restored. Direct mode (`QUEUE_ENFORCED=false`) with a session token is not checked. `POST /test/admit` writes a lease the way a claim does.

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
Errors: `SEATS_TAKEN{taken}`, `INSUFFICIENT_SEATS{available}`, `SOLD_OUT`, `HOLD_LIMIT`, `QUOTA_EXCEEDED` (pre-checked in PG inside the hold transaction, enforced at confirm), `NOT_ADMITTED` (no token while `QUEUE_ENFORCED`), `TOKEN_INVALID` (an admission token for another event, or a session token while the queue is enforced), `VALIDATION` (both or neither of `seat_nos`/`quantity`, more than `MAX_SEATS_PER_ORDER`, an unknown or duplicate seat, unknown fields). With `QUEUE_ENFORCED=false` (rush/test only) a session token is accepted too. `hold_id` is `UUIDv5(NS_HOLD, user_id:Idempotency-Key)` (DATA-MODEL §5), and `expires_at` has millisecond precision.

### 3.5 `POST /orders` (IK) → 201
```json
// request
{ "hold_id": "5f0c..." }
// response
{ "order_id": "0193...", "status": "PENDING_PAYMENT", "amount_paise": 250000, "currency": "INR",
  "seat_nos": [212, 213], "hold_expires_at": "2026-11-22T04:40:12Z",
  "payment": { "attempt": 1, "status": "CREATED", "intent_id": "pi_...", "checkout_url": "http://localhost:8000/pay/checkout/pi_..." },
  "charged": false, "tickets": [], "failure_code": null }
```
Creating the order moves the hold to PAYMENT_PENDING with `PAYMENT_HOLD_TTL`. A retry with the same IK replays, and a second order for the same hold returns the existing order (`hold_id` is unique). `checkout_url` is `MOCKPAY_PUBLIC_URL + "/checkout/" + intent_id` and is present only while the order is PENDING_PAYMENT and the attempt is CREATED. With `QUEUE_ENFORCED=true` this route needs the admission token for the hold's event (another event's token: 401 `TOKEN_INVALID`); otherwise a session token also works.

Every order route (`POST /orders`, `/pay`, `/payments`, `/confirm`, `GET /orders/{id}`) returns this same order view. `/payments` (new attempt) needs the last attempt FAILED or VOIDED, the hold live, and at most 3 attempts (else 409 `ORDER_STATE` / 409 `HOLD_EXPIRED` / 422 `PAYMENT_ATTEMPTS_EXHAUSTED`). A PSP that is down gives 503 `DEPENDENCY_UNAVAILABLE` with `Retry-After`; the Idempotency-Key is released, so the retry runs again with the same deterministic PSP keys.

### 3.6 `POST /orders/{id}/pay` (IK) → 200
```json
// request
{ "payment_method": "pm_ok" }
// response
{ "order_id": "...", "status": "CONFIRMED", "charged": true, "payment": { "attempt": 1, "status": "AUTHORIZED", "intent_id": "pi_..." },
  "tickets": [ { "seat_no": 212, "label": "C5-13", "code": "SLC-7Q4K-92XM" } ], ... }
```
`payment_method` is one of `pm_ok | pm_decline | pm_timeout | pm_slow`. The status can come back `CONFIRMED`, `PENDING_PAYMENT` (the PSP is still processing, so poll `/confirm`), or `FAILED` with `failure_code` and `charged: false`.

### 3.7 `GET /orders/{id}` → 200
```json
{ "order_id": "...", "event_id": "...", "status": "CONFIRMED", "amount_paise": 250000,
  "payment": { "attempt": 1, "status": "CAPTURED" }, "charged": true,
  "tickets": [ ... ], "failure_code": null, "hold_expires_at": "..." }
```
`charged` is true only when a payment is AUTHORIZED or CAPTURED and not voided or refunded. The UI uses it for the "you were not charged" guarantee. `payment` is the live attempt if there is one, else the latest attempt. Another user's order is 404.

### 3.8 `GET /events/{id}/availability` → 200 / 304
```json
{ "version": 18231, "counts": { "available": 120, "held": 310, "sold": 70, "capacity": 500 },
  "states": "<base64: 2 bits per seat_no, 00=available 01=held 10=sold>" }
```
Each API replica serves this from an in-memory cache. It re-checks each event at most every 100 ms, first reading only `ver`, and rebuilds the bitmap only when `ver` changed (one `MULTI` read of `ver`, `avail` and `sold`). Packing: 2 bits per seat, MSB first, so seat *i* is in byte ⌊*i*/4⌋ at bit shift 6 − 2·(*i* mod 4); the last byte is zero-padded. Seats in neither `avail` nor `sold` are held. `ETag` is `"r<ver>"`. With `HOLD_PATH=pg_only`, or while Redis isn't loaded, the source is Postgres and the `ETag` is `"p<sum of seat versions>"` (ADR 0006). `If-None-Match` with the current `ETag` returns 304. `GET /events` and `GET /events/{id}` take their `counts` from the same cache, falling back to Postgres counts on error.

### 3.9 `GET /stats/{id}` → 200
```json
{ "ts": "...", "event_status": "ON_SALE", "sale_opens_at": "...", "sold_out_at": null,
  "queue": { "joined": 50000, "waiting": 48211, "cursor": 1790, "offers": 140, "active": 610, "admit_rate": 182.4 },
  "inventory": { "available": 40, "held": 300, "sold": 160, "capacity": 500 },
  "correctness": { "oversold": 0, "duplicate_charges": 0, "orphan_charges": 0, "checked_at": "..." },
  "demo_rush": { "running": false } }
```
Each replica recomputes the body at most once per second per event (`Cache-Control: public, max-age=1`). `event_status` and `sale_opens_at` come from PG; `queue` from the replica's queue snapshot (≤ 1 s old): `joined` = presize + tail (before the freeze: lottery entries), `waiting` = joined − cursor (users who left are not subtracted); `inventory` from the live seat counts; `correctness` from the worker's `sl:{e}:audit` hash (refreshed every 5 s for ON_SALE events; all four fields `null` when there is none). `sold_out_at` is always `null` for now: no column records it (ADR 0009). `demo_rush` is fixed until demo mode lands (P7).

### 3.10 `GET /events` → 200
```json
{ "events": [ { "id": "0193...", "slug": "tatkal-chn-sbc", "name": "Demo Express, Chennai → Bengaluru (Tatkal drop)",
    "venue": "Chennai → Bengaluru", "status": "SCHEDULED",
    "starts_at": "...", "prequeue_opens_at": "...", "sale_opens_at": "...",
    "capacity": 500, "price_paise": 125000, "currency": "INR", "max_per_user": 4,
    "counts": { "available": 500, "held": 0, "sold": 0, "capacity": 500 } } ] }
```
Ordered by `sale_opens_at`, then slug. In P1 the counts come from Postgres; P2 switches them to live counts. `Cache-Control: no-cache`.

### 3.11 `GET /events/{eventId}` → 200 / 404
The §3.10 object plus `"layout"` (the descriptor in DATA-MODEL §7). `{eventId}` is a UUID or a slug. An unknown or malformed id returns 404 `NOT_FOUND`. If Postgres is unreachable you get 503 `DEPENDENCY_UNAVAILABLE` with `Retry-After: 1`.

### 3.12 Sessions, release, test admission, cache rebuild (P2)
- `POST /sessions` → 201 (§3.1). The token is `aud=session` for 24 h; `sub` is a fresh UUIDv7. Tokens are HS256 via `golang-jwt/jwt/v5`; `exp` and `iat` get 5 s of clock-skew leeway.
- `DELETE /holds/{holdId}` → 204. Releasing a hold that is already RELEASED or EXPIRED is a 204 no-op. Someone else's hold, or an unknown id, gives 404 `NOT_FOUND`. A hold with an order (PAYMENT_PENDING) or a sold one gives 409 `ORDER_STATE`.
- `POST /test/admit` (mounted only when `TEST_ENDPOINTS_ENABLED=true`, which config allows only for `APP_ENV` rush/test): `{ "event_id": "<uuid or slug>", "user_id": "<uuid>" }` → 200 `{ "event_id", "user_id", "admission_token", "expires_at" }`, with an admission TTL of `ADMISSION_TTL`.
- `POST /admin/events/{eventId}/rebuild-cache` with `Authorization: Bearer <ADMIN_TOKEN>` → 200 `{ "event_id", "available", "held", "sold", "holds", "reclaimed", "fence_floor" }`. A wrong or missing admin token gives 401 `TOKEN_INVALID`.

## 4. Webhooks (MockPay → SLUICE)
`POST /api/v1/webhooks/mockpay` with headers:
- `MockPay-Signature: t=<unix>,v1=<hex(hmac_sha256(MOCKPAY_WEBHOOK_SECRET, t + "." + raw_body))>`
- `MockPay-Event-Id: evt_...`

Verification uses a constant-time compare and a tolerance of |now − t| ≤ 300 s; several `v1=` values are accepted (secret rotation). A bad signature gets 400 `VALIDATION` and is counted as `webhooks_total{type="unknown",result="bad_signature"}`. A body over 64 KiB, a body that is not a MockPay event, or a `MockPay-Event-Id` that differs from the body's `id` also gets 400. Then, in one tx: insert into `webhook_events` (a conflict means a duplicate: return 200 `{"received":true}`, count `result="duplicate"`, do nothing) and enqueue `process_webhook` (unique per inbox event). Respond 200 fast.

`process_webhook` results (`webhooks_total{type,result}`): `processed`; `regression_ignored` (the event ranks below the payment's current status, e.g. `authorized` after `captured`); `unknown_intent` (no payment has this intent: the reconciler handles any money).

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
| GET | `/v1/ledger/summary?metadata.event_id=&since=` | For the auditor: `{"refs": [{merchant_ref, intents, authorized, captured, voided, refunded}]}`, counts of intents with each ledger entry kind |
| GET | `/checkout/{id}` | Hosted page with a **TEST MODE** banner. Buttons: Pay (pm_ok), Decline (pm_decline), Simulate timeout (pm_timeout) |
| POST | `/checkout/{id}/pay` | Form or JSON `{payment_method}` → authorize and 303 to `return_url` with `order=<merchant_ref>&intent=<id>&status=<intent status>` added (SLUICE's own `return_url` already carries `o=<order_id>` for the web page). Internal key `checkout:<id>:<method>`, so a double click replays |
| GET | `/metrics`, `/healthz` | |

MockPay semantics:
- The same IK with the same body replays the stored response; the same IK with a different body gets 422.
- `pm_timeout` and `MOCKPAY_TIMEOUT_AFTER_COMMIT_RATE` **commit first, then respond after `MOCKPAY_HANG_MS`**. That's the classic double-charge trap.
- Webhooks are delivered at least once with retries (0, 1, 2, 4, 8, 16 s; 6 attempts), with optional duplicates, reordering and delay according to the fault env vars.
- In test mode, the header `X-MockPay-Fault: timeout_after_commit|fail|dup_webhook|reorder_webhook` forces a fault deterministically. It is honoured only when `APP_ENV` is `rush` or `test`. `fail` answers 503 before anything commits (the key is not stored, so a retry runs).
- State-level no-ops (ADR 0007): confirming an authorized/captured intent, voiding a canceled/failed one, or refunding a refunded one returns the intent unchanged. Voiding a `requires_payment` intent cancels it with no ledger entry. Other invalid transitions are 409 `intent_state` with the current intent in the body.
- Intents carry `authorized: true|false` (the ledger has an `AUTH` entry), so a canceled intent tells whether money was ever held.
- The ledger is append-only in the database (a trigger rejects UPDATE, DELETE and TRUNCATE).

The SLUICE PSP client uses a **2 s** timeout per call, retries ≤ 3 with jittered backoff, at most **64 connections per PSP host** per process (calls beyond that wait for a free connection within their timeout; ADR 0009), and always the same deterministic key: `order:<order_id>:attempt:<n>` to create, `confirm:<intent_id>`, `capture:<intent_id>`, `void:<intent_id>`, `refund:<intent_id>`.
