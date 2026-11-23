# P3: Payments: Report

| Field | Value |
|---|---|
| Status | DONE |
| Date (IST) | 2026-11-23 |
| Time spent vs timebox | Not measured precisely. The work ran across several context resets and a usage-limit pause in one session; it went past the 6 h timebox, but well short of 1.5× of active work. |
| Commit base | `7e18326` (`git rev-parse --short HEAD`) |

## 1. What was built
- **MockPay** (`sluice mockpay`, `internal/mockpay`, `internal/app/mockpay.go`):
  - It has its own database and goose migrations (`internal/mockpay/migrations`, embedded), applied at start-up. Queries are in `internal/mockpay/queries` and the sqlc output is in `internal/mockpay/mpdb`.
  - It implements the API in API §5. Idempotency uses an advisory lock per key, and the stored response is written in the same transaction as the state change. The intent state machine lives in `intents.go:apply`.
  - The ledger is append-only and enforced by a DB trigger. `UNIQUE(intent_id, kind)` means an intent can never be captured twice.
  - Faults come from env plus the `X-MockPay-Fault` header (rush/test only). `pm_timeout` and `timeout_after_commit` commit first, then hang.
  - Hosted checkout (`checkout.go`): server-rendered with a TEST MODE banner, the fictional MockPay brand, ₹ amounts with Indian digit grouping, a CSP header, and Pay / Decline / Simulate-timeout buttons that 303 to `return_url`.
  - Webhook dispatcher (`webhooks.go`): claims deliveries with `FOR UPDATE SKIP LOCKED` and a lease, signs them with HMAC, and retries at 0/1/2/4/8/16 s before marking them dead. Duplicate and reorder faults are supported.
  - `/v1/ledger/summary` and `/metrics`.
- **PSP client** (`internal/payments`):
  - 2 s timeout and ≤ 3 full-jitter retries on timeouts, network errors, 5xx and 429.
  - Deterministic keys (`CreateKey`, `ConfirmKey`, `CaptureKey`, `VoidKey`, `RefundKey`).
  - Typed errors: `ErrUnavailable`, `ErrNotFound`, `ErrIntentState`/`*StateError`, `ErrKeyReused`, `ErrRejected`.
  - `psp_requests_total` and `psp_request_duration_seconds`.
  - `VerifySignature` does a constant-time `hmac.Equal` over every `v1` value, with a 300 s tolerance.
- **Orders** (`internal/orders`):
  - `create.go`:
    - `POST /orders`: one tx (order, hold → PAYMENT_PENDING, attempt 1), then Redis `hold_extend`, then the PSP intent outside the tx.
    - A retry re-runs with the same PSP key.
    - Also `NewAttempt`, `Pay` and `Confirm`.
  - `confirm.go`:
    - `ConfirmOrder` is the single convergence point (pull, sync, then transaction A or the late/duplicate branches, then transaction B), followed by `hold_consume` / `hold_release`.
    - The settle step (`settle`, `settleDeadHoldTx`) and the `OnReclaim` hook.
  - `process.go`: the webhook inbox (`ReceiveWebhook`), `ProcessWebhook` (with regression detection), and the `Capture`, `Void` and `Refund` jobs.
  - `reconcile.go`: the reconciler (orphans, duplicates, convergence, stale captures, dead holds, Redis drift) and the live audit cache.
  - `handler.go`: the HTTP routes, including `POST /webhooks/mockpay` and `POST /admin/reconcile`.
  - Metrics: `orders_total`, `payments_total`, `webhooks_total{type,result}`, `duplicate_charges_prevented_total`, `orphans_fixed_total`.
- **River** (`internal/jobs`):
  - An insert-only client for the API and a worker client.
  - Workers: `process_webhook`, `capture_payment`, `void_payment`, `refund_payment`. Periodic jobs: `reconcile` (`RECONCILE_INTERVAL`) and `audit_cache` (5 s).
  - A capped full-jitter backoff of 1 s to 60 s.
  - `Dispatch` runs one job by kind, which lets tests drain jobs in a chosen order.
- **Inventory follow-ups**:
  - The sweeper and the takeover path call the reclaim hook inside the reclaim tx.
  - Split out `ExtendInTx`.
  - Added `MirrorExtend` / `MirrorRelease`.
  - Added `RepairDrift` (`drift.go`) and `inventory_drift_repairs_total`.
- **Plumbing**:
  - `idempotency_replays_total` (`httpx.NewReplayCounter`).
  - `PUBLIC_APP_URL` (config, `.env.example`, STACK §4).
  - `keys.Audit`.
  - Wiring in `internal/app/{api,worker,orders,mockpay}.go`.
  - The `sluice mockpay` command and the `task dev:mockpay` task.

## 2. Acceptance checklist
- [x] **Every test above passes under race.** Final gate: `CGO_ENABLED=1 go test -race -count=1 -cover ./...` → see §3. Every row of the phase's test table maps to a test:

  | Required case | Test |
  |---|---|
  | Happy path | `TestHappyPath` (real River worker plus the MockPay dispatcher) |
  | Double-click `/pay`, same IK | `TestDoubleClickPaySameKey`: 8 concurrent clicks → `clicks=8 ok=1 in_progress=7`, 1 AUTH, 1 CAPTURE, the replay carries `Idempotent-Replayed: true` |
  | Retry `/pay`, new IK | `TestRetryPayNewKey` |
  | `pm_timeout` + same-IK retry | `TestPayTimeoutAfterCommit`: `pay with pm_timeout took 416ms (PSP timeout 300ms, hang 900ms)`, 1 intent, 1 capture |
  | Webhook ×3 | `TestWebhookDuplicated3x`: 1 inbox row, 2 counted as `duplicate`, 1 `processed` |
  | `captured` before `authorized` | `TestWebhookCapturedBeforeAuthorized` (manual order, plus the dispatcher `reorder_webhook` fault) |
  | Webhook never arrives → `/confirm` | `TestWebhookNeverArrivesConfirmPull` |
  | Reclaimed before the authorization | `TestHoldReclaimedBeforeAuthorization` (both job orders): FAILED `HOLD_LOST`, VOIDED, `charged:false`, seats HELD by the new holder |
  | Authorization just before the sweeper | `TestAuthorizationJustBeforeSweeper`, plus `TestConfirmSweeperRace` (16 orders run concurrently against 3 sweepers; 6 runs gave mixes between 8/8 and 15/1 confirmed/voided) |
  | Quota exceeded at confirm | `TestQuotaExceededAtConfirm` |
  | Capture fails 2×, then succeeds | `TestCaptureRetries`: client retries (2 × 5xx) and job retries (5 × 5xx, i.e. a whole job attempt fails), exactly 1 CAPTURE each |
  | Artificial orphan | `TestOrphanReconciled`: authorized orphan → VOID, captured orphan → REFUND, `orphans_fixed_total` 2, the second cycle finds 0 |
  | Two authorized attempts | `TestTwoAuthorizedAttempts`: attempt 2 VOIDED, `duplicate_charges_prevented_total` 1 |
  | Property test | `TestPaymentsProperty`: `[rapid] OK, passed 100 tests (3m14.67s)`. Outcomes over all cases: `ledger_AUTH:61 ledger_CAPTURE:52 ledger_VOID:9 order_CONFIRMED:52 order_EXPIRED:77 order_FAILED:9 payment_CAPTURED:52 payment_FAILED:97 payment_VOIDED:9` |

- [x] **Manual check, booking `quiet-tuesday` through the hosted checkout (Pay, Decline, Timeout), each with the correct `charged` state.** I did this with `curl`, posting the same form a browser posts. I did **not** click through in a real browser; Sheshakanth should do that once (see §9). The script is `manual_p3.sh` (scratchpad). Setup: built `sluice.exe`, compose infra, `.env.example` values with `APP_ENV=test TEST_ENDPOINTS_ENABLED=true`, MockPay on :8090, worker on :9091, API on :8080. Output:
  ```
  ===== button: pm_ok
  order   -> {'order_id': '01a20909-5102-…', 'status': 'PENDING_PAYMENT', 'amount_paise': 250000, 'charged': False} checkout_url=http://localhost:8090/checkout/pi_01a20909513f…
  page    -> TEST MODE banner: 1 line(s); amount shown: ₹2,500.00
  checkout POST -> HTTP/1.1 303 See Other; Location: http://localhost:3000/order?intent=pi_01a2…&o=01a20909-5102-…&order=01a20909-5102-…&status=authorized
  GET order after 3 s -> {'status': 'CONFIRMED', 'charged': True, 'payment': 'CAPTURED', 'tickets': 2, 'failure_code': None}
  ===== button: pm_decline
  checkout POST -> HTTP/1.1 303 See Other; Location: …&status=failed
  GET order after 3 s -> {'status': 'PENDING_PAYMENT', 'charged': False, 'payment': 'FAILED', 'tickets': 0, 'failure_code': None}
  POST confirm -> {'status': 'PENDING_PAYMENT', 'charged': False, 'payment': 'FAILED'}
  ===== button: pm_timeout
  checkout POST (1 s client timeout) -> HTTP 000 (curl exit 28)
  GET order after 3 s -> {'status': 'CONFIRMED', 'charged': True, 'payment': 'CAPTURED', 'tickets': 2, 'failure_code': None}
  ===== ledger summary (MockPay)
  {'merchant_ref': '01a20909-5102-…', 'intents': 1, 'authorized': 1, 'captured': 1, 'voided': 0, 'refunded': 0}
  {'merchant_ref': '01a20909-66e7-…', 'intents': 1, 'authorized': 0, 'captured': 0, 'voided': 0, 'refunded': 0}
  {'merchant_ref': '01a20909-7d84-…', 'intents': 1, 'authorized': 1, 'captured': 1, 'voided': 0, 'refunded': 0}
  ===== metrics (worker)
  payments_total{status="CAPTURED"} 2 · psp_requests_total{op="capture",result="ok"} 2
  webhooks_total{result="processed",type="intent.authorized"} 2 · …{type="intent.captured"} 2 · …{type="intent.failed"} 1
  ===== errors in logs   (none)
  ```
  After a decline the order stays PENDING_PAYMENT with `charged:false`, so the user can start attempt 2 while the hold lives (API §3.6). For Timeout, the browser gave up after 1 s, but the authorization had committed, so the webhook confirmed it and there was exactly one capture.
- [x] **`task lint test` green.** `task lint` → `0 issues.` The tests ran as `go test -race -count=1 -cover ./...` (a superset of `task test`). Every package was `ok` except one flaky P2 test, which I fixed and reran green (§3).

## 3. Tests
Final gate: `task lint` → `0 issues.`, then `CGO_ENABLED=1 go test -race -count=1 -cover ./...`:
```
ok  cmd/sluice           1.901s  coverage: 84.0%
ok  internal/app        22.924s  coverage: 19.2%
ok  internal/auth        2.463s  coverage: 32.5%
ok  internal/buildinfo   1.877s  coverage: 63.6%
ok  internal/config      1.977s  coverage: 97.7%
ok  internal/db         23.932s  coverage: 82.2%
ok  internal/events     19.581s  coverage: 88.6%
ok  internal/fsm         1.938s  coverage: 94.7%
ok  internal/httpx       2.923s  coverage: 65.7%
--- FAIL: TestIdempotentHold/redis_pg  (see below; fixed and rerun)
ok  internal/jobs        2.780s  coverage: 5.5%
ok  internal/mockpay    23.496s  coverage: 72.3%
ok  internal/obs         2.424s  coverage: 90.0%
ok  internal/orders    194.641s  coverage: 71.3%
ok  internal/payments    4.383s  coverage: 73.8%
ok  internal/redisx     10.436s  coverage: 56.5%
```
After the fix: `CGO_ENABLED=1 go test -race -count=1 -cover ./internal/inventory/ ./internal/jobs/` → `ok internal/inventory 34.832s coverage: 80.6%`, `ok internal/jobs 3.713s coverage: 5.5%`; `go test -race -count=25 -run 'TestIdempotentHold$' ./internal/inventory/` → `ok 25.141s`; `golangci-lint run ./...` → `0 issues.`

| Suite | Count | Result | Notes |
|---|---|---|---|
| unit (new in P3) | payments 6, mockpay `TestFormatINR` 1, jobs `TestBackoff` 1, orders state machines 4 (updated) | pass | signature vector computed independently in Python |
| integration (testcontainers PG 18 + Redis 8) | orders 15 scenario tests, mockpay 8, inventory 3 new (reclaim hook ×2, RepairDrift) | pass | in-process MockPay with its own DB; real River worker in the worker-mode tests |
| race (`-race`) | whole repo | pass | local (gcc present) |
| property (rapid) | `TestPaymentsProperty`, 100 cases | pass | 3m14.67s under race |
| e2e (Playwright) | none | n/a | P6 |

Coverage for touched core packages (`go test -cover`, same run): inventory 80.6%, orders 71.3%, payments 73.8%, mockpay 72.3%. queue: n/a (P4). `internal/jobs` shows 5.5% because its workers run from the orders tests (another package); only `TestBackoff` counts there.

**Flaky P2 test found and fixed (`TestIdempotentHold`).** In the final gate, the redis_pg case failed with `concurrent same-key hold: ... seats taken: [11]`. Cause: the test first places a best-available hold of 2 (Redis `SPOP`, so random seats out of 50), then races 50 same-key holds on fixed seats 10 and 11. When the first hold drew 10 or 11, Redis correctly answered TAKEN; that happens with probability 1 − C(48,2)/C(50,2) ≈ 7.9% per run. This is a test bug, not a hold-path bug: same-key requests did replay correctly. The fix picks the two raced seats from those the first hold didn't get, and every assertion is unchanged. It then passed 25/25 (`-count=25`).

**Race:** verified locally (gcc present, `CGO_ENABLED=1`).

**First full `task test:race` run:** every package passed except `internal/db`. That package didn't run its tests: `pgtest: start postgres: ... get provider: rootless Docker is not supported on Windows, failed to create Docker provider`. This is a transient testcontainers Docker-provider detection error at container start-up, in a package P3 didn't change. It passed on the rerun (`ok internal/db 9.835s`) and in the final uncached gate.

**Weak first draft, fixed:** the first version of `TestConfirmSweeperRace` ended `0 confirmed, 16 voided` in every run, so only one side of the race was being exercised. Every confirm does a PSP round trip, and the first sweep reclaims all expired holds in one batch. The test now starts confirms 10 ms apart and releases the sweepers the moment confirm n/2 returns. Six runs gave 8/8, 10/6, 11/5, 12/4, 15/1 and 13/3 (confirmed/voided). A run where only one side happens is logged, not failed, and the invariants are checked in every run.

## 4. Numbers
None for this phase beyond the test outputs above (no benchmarks or rush runs in P3).

## 5. Deviations and decisions
| What changed vs the spec | Why | ADR |
|---|---|---|
| A reclaimed hold with money outstanding: void/refund first, then FAILED `HOLD_LOST`. With no money, EXPIRED. (Previously §4.4 said "expire its order".) | ARCHITECTURE §4.4 and the P3 test table disagreed. Settling through the money makes the outcome independent of ordering. | 0007 |
| The reclaim hook locks the order with `SKIP LOCKED` | Avoids an order↔hold lock-order deadlock with ConfirmOrder | 0007 |
| Payment edge CREATED → VOIDED; MockPay intents carry `authorized` | An intent canceled after an authorization SLUICE never recorded | 0007 |
| Late authorization on a terminal order → recorded, then voided | §4.3 only covered "another attempt is live" | 0007 |
| `intents.return_url` column; `PUBLIC_APP_URL` env var | API §5 redirects to `return_url`, which had no column | 0007 |
| `process_webhook` is unique per inbox event, not per intent | Unique-per-intent would drop a second, different event for the same intent | 0007 |
| The reconciler converges PENDING orders through ConfirmOrder before voiding | §4.5 would have voided money for an order whose webhook was only lost | 0007 |
| Ledger append-only enforced by a trigger | It was a comment only | 0007 |
| MockPay state-level no-ops; void of a `requires_payment` intent → canceled with no ledger entry | Safe retries with a fresh key | 0007 |
| The checkout page uses the MockPay brand, not SLUICE's design system | It stands in for a third-party PSP page | 0007 |
| P2 `TestIdempotentHold` races on seats the first hold didn't draw (previously fixed seats 10 and 11) | The fixed seats collided with a random best-available hold in about 8% of runs; assertions unchanged | n/a (test fix) |

Docs updated: ARCHITECTURE §4.3–4.5; DATA-MODEL §2 (now the migration verbatim) and §3; API §2, §3.5–3.7, §4, §5; STACK §4 and the layout; `.env.example`; Taskfile (`dev:mockpay`, `dev:worker` description).

**UI pre-delivery checklist** (MASTER.md) for the checkout page:
- No emoji or icons.
- `cursor:pointer` on buttons.
- 150 ms hover transitions.
- Visible `:focus-visible` (3 px, 6.70:1 against the card).
- `prefers-reduced-motion` turns transitions off.
- Max width 28 rem with 16 px gutters and `overflow-wrap:anywhere` for the ids, so no horizontal scroll at 375 px.
- No fixed bars.
- 48 px buttons.

Contrast, computed with WCAG relative luminance in Python, lowest first: Pay button 5.47:1, Decline hover 5.91:1, Decline 6.47:1, muted text 7.58:1, TEST MODE banner 13.27:1, body 17.85:1.

## 6. Known issues and debt
- **No real-browser click-through yet** (§2). The curl run exercised the same form posts and redirects.
- **The redirect carries both `o=` and `order=`.** `o` comes from SLUICE's `return_url` (the web page contract) and `order` is MockPay's generic parameter (API §5). The duplication is harmless and documented.
- **The property test takes ~3m15s under race** (100 cases), which puts the orders package at ~3.9 min. That's within the default 10-minute per-package timeout and the 20-minute CI job. Lower it with `-args -rapid.checks=N` if CI gets tight.
- **The property run never reached a captured-then-refunded path**: the tally has no REFUND, because only the reconciler refunds and the generator rarely creates a captured orphan. That path is covered directly by `TestOrphanReconciled` and by `markCaptured`'s refund branch.
- **`govulncheck`:** "Your code is affected by 0 vulnerabilities". It also reports 1 vulnerability in a required module with no call path from our code.
- Other P2 tests may combine a random best-available hold with fixed seat numbers in the same way. Only `TestIdempotentHold` was seen failing; the others weren't audited for this pattern in P3.

## 7. Understand this before moving on
1. **Q:** A user pays at the exact moment their hold expires. How do you guarantee they are either ticketed or not charged, and never charged without a ticket?
   **A:** Both paths are PG transactions on the same rows. `orders/confirm.go:confirmAuthorized` locks the order, then the hold, and sells the seats with `UPDATE ... WHERE state='HELD' AND hold_id=$h`; the row count must equal n (I6 fencing). The sweeper (`inventory/sweeper.go:reclaimExpired`) flips the hold to EXPIRED and calls `orders.OnReclaim` in the same tx. Whoever commits first wins. If the sweeper wins, ConfirmOrder finds the hold gone, runs `failAuthorized`, voids the intent and the order ends FAILED `HOLD_LOST`. If confirm wins, the hold is CONSUMED and the sweeper's conditional update changes 0 rows. The reconciler is the last line of defence: any authorized/captured intent with no confirmed owner gets voided or refunded (`reconcile.go:reconcileIntent`).
2. **Q:** Why can't a double click or a retry charge twice?
   **A:** There are three layers.
   - The API Idempotency-Key replays the stored response (`httpx/idempotency.go`).
   - Every PSP call uses a deterministic key (`payments.CreateKey`, `ConfirmKey`, ...), so even a new API key hits the same intent. MockPay serializes each key with an advisory lock and stores the response in the same tx as the state change (`mockpay/intents.go:idempotent`).
   - In Postgres, the partial unique index `payments_one_live_per_order` allows only one live payment per order. A second authorization is voided and counted (`confirmAuthorized`, duplicate branch). On top of that, MockPay's `UNIQUE(intent_id, kind)` makes a double capture impossible.
3. **Q:** `pm_timeout` commits the authorization and then hangs. What does the user see, and what does the system do?
   **A:** The PSP client times out at 2 s and retries with the same `confirm:<intent>` key. MockPay replays the stored answer immediately (replays never hang), so `/pay` returns CONFIRMED (`TestPayTimeoutAfterCommit`: 416 ms with a 300 ms test timeout). With the hosted checkout, the browser may give up, but the `intent.authorized` webhook (or the user's `/confirm`, or the reconciler's convergence) drives ConfirmOrder anyway. The manual run shows CONFIRMED, CAPTURED and exactly one ledger capture.

## 8. Files changed
```
# MockPay (new service)
?? internal/mockpay/                 (server, intents, webhooks, checkout, migrations/, queries/, mpdb/, tests)
?? internal/app/mockpay.go
 M cmd/sluice/main.go
 M cmd/sluice/main_test.go           (mockpay is no longer "not implemented")
 M db/sqlc.yaml                      (second sqlc target: mockpay)
# PSP client
?? internal/payments/                (wire, client, signature, tests)
# Orders, webhooks, jobs
?? internal/orders/confirm.go create.go handler.go jobs.go process.go reconcile.go service.go
?? internal/orders/fixture_test.go payments_integration_test.go property_test.go
 M internal/orders/state.go state_test.go   (CREATED -> VOIDED, PaymentStatus.Rank)
?? internal/jobs/                    (River clients, workers, backoff, test)
?? internal/app/orders.go
 M internal/app/api.go worker.go inventory.go
 M db/queries/orders.sql payments.sql webhooks.sql
 M internal/db/gen/orders.sql.go payments.sql.go webhooks.sql.go
# Inventory follow-ups
 M internal/inventory/hold.go service.go sweeper.go
?? internal/inventory/drift.go reclaim_test.go
 M internal/inventory/fixture_test.go
 M internal/inventory/hold_integration_test.go   (flaky seat pick fixed)
# Plumbing
 M internal/httpx/idempotency.go     (idempotency_replays_total)
 M internal/config/config.go .env.example   (PUBLIC_APP_URL)
 M internal/redisx/keys/keys.go      (Audit key)
 M Taskfile.yml                      (dev:mockpay)
 M go.mod go.sum                     (River dbunique deps)
# Docs
 M docs/API.md docs/ARCHITECTURE.md docs/DATA-MODEL.md docs/STACK.md
?? docs/adr/0007-payment-path-details.md
?? docs/reports/P3-payments.md
```

## 9. Next phase readiness
- Before committing, Sheshakanth: run `task dev:mockpay`, `task dev:worker` and `task dev:api` with `APP_ENV=test TEST_ENDPOINTS_ENABLED=true`, get a session and an admission token (`POST /api/v1/sessions`, `POST /api/v1/test/admit`), place a hold and create an order, then open `payment.checkout_url` in a browser and click Pay / Decline / Simulate timeout once each.
- P4 (queue) can rely on: `orders.RefreshAuditCache` writing `sl:{e:<eid>}:audit` every 5 s (fields `oversold`, `duplicates`, `orphans`, `tickets`, `at_ms`; 30 s TTL); `POST /orders` already accepting only admission tokens when `QUEUE_ENFORCED=true`.
- Push and let CI run the race suite on Linux. The orders package is the long one (~4 min).

## 10. Git commands (Sheshakanth runs these)
See the block in the chat summary; it is repeated here:
```bash
git add -A
git status --short
git commit -m "feat(payments): MockPay, idempotent orders, fenced confirm, reconciler" -m "- MockPay service: own DB, append-only ledger, faults, hosted checkout, signed webhooks" -m "- PSP client with deterministic keys, jittered retries and typed errors" -m "- Orders API, ConfirmOrder convergence, webhook inbox, River capture/void/refund jobs" -m "- Reclaim hook settles orders of expired holds; reconciler and live audit cache" -m "- Integration, race and rapid property tests; fix flaky TestIdempotentHold seat pick" -m "Phase P3 - report: docs/reports/P3-payments.md"
```
