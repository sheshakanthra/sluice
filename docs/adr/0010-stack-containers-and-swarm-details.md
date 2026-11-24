# ADR 0010: Full-stack containers and swarm details

- **Status:** accepted
- **Date:** 2026-11-24
- **Phase:** P5 (session P5a: tasks 1–3)

## Context
P5a containerises the stack (`deploy/docker/Dockerfile`, `deploy/compose.app.yml`, `deploy/caddy/Caddyfile`, `deploy/env/rush.env`), builds the swarm (`internal/rush`) and adds `deploy/compose.rush.yml`. Building and running them exposed gaps the specs do not cover:
1. **Healthchecks on distroless.** P5 requires `gcr.io/distroless/static-debian12:nonroot` and a healthcheck on every service. That image has no shell, curl or wget.
2. **Who is a trusted proxy.** `TRUSTED_PROXIES` defaults to `127.0.0.1/32,172.16.0.0/12` (STACK.md §4). Docker can give a compose network a `192.168.x` or `10.x` subnet. If Caddy's address is not trusted, every swarm user shares Caddy's IP and the per-IP limits (sessions 20/min/IP) refuse almost everyone. Caddy itself also drops an incoming `X-Forwarded-For` unless its peer is trusted.
3. **Connection budget.** Three APIs, the worker and MockPay each open a pool of `DB_MAX_CONNS` (24): up to 120 connections, more than Postgres's default `max_connections` (100).
4. **`up --wait` and one-shots.** `docker compose up --wait` reports a one-shot service that exits (even with 0) as a failure unless another service depends on it with `service_completed_successfully`.
5. **Swarm behaviour the mix leaves open** (LOADTEST §3.2–3.3): the timeout for mutating calls, how `/confirm` polls inside the 10/min/user limit, what a user does after a third `OFFER_EXPIRED`, and which IK a hold retry uses.

## Decision
- **`sluice probe <url>`** is a new subcommand: GET with a 3 s timeout, exit 0 on 2xx. Every `sluice` container's healthcheck runs it against its own `/readyz`. Caddy (Alpine-based) uses `wget` against `/api/v1/healthz` through the proxy, so "healthy" means "routes to an API".
- **`rush.env` trusts every private range** (`127.0.0.1/32,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16`), and the Caddyfile sets `trusted_proxies static private_ranges`. The swarm sends synthetic client IPs from `198.18.0.0/15` (RFC 2544 benchmarking space, never trusted), about 10 users per IP. The dev default in `.env.example` is unchanged.
- **Postgres runs with `max_connections=200`** (`deploy/compose.yml`). No durability setting changes.
- **Migrate and seed are one-shot services.** `seed` depends on `migrate`, and the APIs depend on `seed` completing, so `task stack:up` (`up -d --wait`) exits 0 with demo events in place.
- **Swarm choices:**
  - Every mutating call has a 5 s client timeout and is retried with the **same** IK on timeout, transport error, 5xx, 429 (after `Retry-After`) or `REQUEST_IN_PROGRESS`. Status polls use a 2 s timeout. LOADTEST's "client timeout (2 s), then retry with the same IK" is read as the status timeout plus same-IK retries; a 2 s timeout on `/pay` would race the API's own 2 s PSP timeout and count every `pm_timeout` as an error.
  - Each hold attempt is a new intent with a new IK (the body changes between pick → best → fewer seats; reusing the key would be a 422).
  - `/confirm` polls use a new IK per poll (a replay would return the stored PENDING answer) and back off from 1 s to 6 s.
  - A user who sees `OFFER_EXPIRED` after two rejoins ends as `NO_SEAT` (and leaves the queue).
  - A declined payment's retry (70%) creates a new attempt and pays with `pm_ok`.
  - The double-submit (5%) uses a second connection, so the two `/pay` calls really are concurrent under `--conn dedicated`.
  - Unexpected answers (anything that is not the journey's normal 2xx/409/422/429 path) are counted per step in `summary.json` `anomalies`, so none hide inside an outcome count.
- **Swarm CPU** is measured with `getrusage` (Linux container). On Windows it falls back to the Go runtime's estimate, which only refreshes at GC.

## Consequences
- The front door works on any Docker subnet. In `rush`, any private-network peer can set `X-Forwarded-For`, which is acceptable for a closed benchmark network and is never used for `demo` (P8 writes its own profile).
- The swarm reads `ADMIN_TOKEN`, `HOLD_TTL` and `PAYMENT_HOLD_TTL` from the same env profile as the servers, plus its own report labels `RUSH_RUNNER`, `RUSH_PG_VERSION`, `RUSH_REDIS_VERSION` and `RUSH_RESULTS_DIR`, which `compose.rush.yml` sets. Those four stay out of `.env.example`, which the config test limits to keys `internal/config` reads.
- `sluice probe` is part of the CLI surface and is tested in `cmd/sluice/main_test.go`.
- `summary.json` has two keys beyond LOADTEST §3.5 (`anomalies`, `generator`), plus `config` extras (event, conn, arrival, seed, direct). `audit` is `null` until `sluice audit` lands (P5b).
- Docs updated: STACK.md §1.1 (Caddy pinned), §2 (subcommands), §4 (`TRUSTED_PROXIES` rush value), LOADTEST.md §3.3 (timeouts and IK rules as above).

## Addendum (2026-11-24): admissions end with the checkout

**Status:** accepted (approved by the owner after the P5a 2,000-user run).

### Context
The admission controller computes `seeking = ZCARD active − ZINTERCARD(active, holding)` (ARCHITECTURE §5). A buyer whose hold became a sale, or whose payment failed, no longer holds seats but stayed in `q:active` until `ADMISSION_TTL` (3 m in `rush`). The controller kept counting them as seeking. With ~400 such users `target − seeking` stayed negative, so seats freed by expired holds waited up to `ADMISSION_TTL` before anyone was offered them. In `smoke-20261124-081215` the time series sat at `sold 443/500, held 0, waiting 1,250, shopping 0` for about 3 minutes, and the drain took 399.2 s against the 5-minute SLO.

### Decision
An admission is released (the user is removed from `q:active`) when the user's checkout ends. That is the first of:
- the order reaches **CONFIRMED**: `hold_consume.lua` (run after ConfirmOrder commits, and by the sweeper's Redis pass) does `ZREM q:active uid` in the same script that moves the seats to `sold` and removes the user from `holding`;
- the order reaches **FAILED** (transaction B, or `MaxAttempts` declines) or **EXPIRED**, the hold is **released** (`DELETE /holds/{id}`), or the hold **expires** (sweeper): `hold_release.lua` with the new `free_admission = 1` argument does the same `ZREM` in the script that returns the seats and leaves `holding`. `settle` runs it again when an order reaches FAILED/EXPIRED after its hold had already ended; that repeat is a no-op;
- a **PG takeover** of an expired hold: `inventory_repair.lua` drops the forgotten hold's user from `q:active` along with `holding`.

Rules:
- **Atomic and idempotent.** The `ZREM` happens in the same Lua call that ends the hold in Redis. A second run finds the hold MISSING and frees nothing new; the script returns `freed` 0|1, counted in `queue_admissions_released_total`.
- **No other live hold or order.** The release is skipped while the user is still in `holding`. PG allows one live hold per (event, user) (`holds_one_active_per_user`), and an order is live only while its hold is, so when a hold ends the user has no other live hold or order. The `holding` check covers the remaining case: a late or repeated mirror of an old hold after the user has held again.
- **Compensation keeps the admission.** When PG refuses a Redis grant, the user is still shopping, so `hold_release` runs with `free_admission = 0`. The same applies to a hold in the Redis index that PG never committed.
- The scripts get the user id from the caller (`orders.user_id`, `holds.user_id`; the sweeper's `ListExpiredHolds` / `GetHoldsByIDs` now return `user_id`), or from the hold hash when it is not known.

### Measured result
Same command as before (`task rush:run USERS=2000`, seed 42, pooled), run `smoke-20261124-110912`:
- The drain went from 399.2 s to **392.1 s**, so the ≤ 5 min SLO is **still missed**.
- The fix did what it was built for: `queue_admissions_released_total` = 390, and the admissions left in `q:active` after the first wave dropped from 718 to 364.
- The remaining 364 are admitted users who never held. The swarm's `INSUFFICIENT_SEATS` with 0 available path ends as SOLD_OUT without `DELETE /queue` (LOADTEST §3.2). No hold or order event exists for them, so they keep the controller at `seeking ≥ target` until `ADMISSION_TTL`.
- The earlier P5a diagnosis (buyers alone) was incomplete. The remaining gap needs a further owner decision (P5a report, Fixes).

### Consequences
- The controller refills the target within one tick of a checkout ending. `ADMISSION_TTL` becomes the backstop for users who never hold and for a Redis mirror that failed (mirrors stay best effort).
- **Tokens are unchanged.** An admission token stays valid until its `exp`, so a user who releases a hold can hold again. They are then outside the controller's `seeking` count, which is bounded per user by `HOLD_LIMIT` (one live hold) and the per-user quota (I4). A later queue-status poll answers `OFFER_EXPIRED` (API.md §3.3), and a rejoin is honoured within the two-rejoin limit.
- I7's bound (admitted-and-seeking ≤ target) is still enforced by `queue_tick.lua` over `q:active`; this change only makes `q:active` stop counting finished checkouts.
- Docs updated: ARCHITECTURE §5, DATA-MODEL §4 (`q:active`) and §5 (`hold_consume`, `hold_release`, `inventory_repair`), API.md §3.3.
- Tests: `TestHoldEndFreesAdmission` (every script path, including idempotence and the user who holds again), `TestConfirmedBuyerFreesAdmission`, `TestFailedPaymentFreesAdmission`, `TestExpiredHoldFreesAdmission`, `TestReleasedHoldFreesAdmission`, and `TestAdmissionRefillsAfterCheckout`. The last one checks out 200 admitted users concurrently with two controllers ticking and the clock frozen, and asserts that the target refills. With the `ZREM` disabled it fails with `cursor still 300`.

## Addendum, part 2 (2026-11-24): idle admissions end on their own

**Status:** accepted (owner-approved after the measured result above).

### Context
Part 1 freed the admissions of users whose checkout ended. The users left in `q:active` were admitted users with no hold and no order: the swarm's `INSUFFICIENT_SEATS` with 0 available path ended as SOLD_OUT without `DELETE /queue`, which LOADTEST §3.2 requires. A real browser that is closed behaves the same way, and no server event exists to end such an admission before `ADMISSION_TTL`.

### Decision
1. **The client leaves on NO_SEAT and ABANDONED.** The swarm enforces LOADTEST §3.2 in one place (`user.end`) and classifies `INSUFFICIENT_SEATS` with 0 available as NO_SEAT (every seat is held, not sold). The P6 UI does the same (DESIGN-BRIEF §5 rule 9).
2. **The server does not rely on it: admission is a sliding lease.**
   - `ADMISSION_IDLE_TTL` (default 2m, `rush` 60s; must be `<= ADMISSION_TTL`) is the idle lease. `ADMISSION_TTL` stays the hard cap.
   - `queue_claim.lua` sets `q:active` uid → `min(now + idle, cap)` and the new `q:cap` uid → cap. The admission token's `exp` is the cap, as before.
   - Every request that presents a valid admission token renews the lease: `Keyring.Require` calls a hook (`Keyring.WithAdmissionTouch`, wired in `internal/app/api.go` to `queue.Lease.Touch`) that runs `queue_touch.lua`: one atomic `ZADD q:active XX GT CH min(now + idle, exp) uid`. `XX` never resurrects an admission that ended; `GT` never shortens one; the token's `exp` bounds the lease by the cap.
   - The renewal is best effort (500 ms timeout, result counted in `queue_admission_touches_total{result=renewed|noop|error}`); a failure is logged and the request proceeds.
   - `queue_tick.lua`'s existing `ZREMRANGEBYSCORE active -inf now` removes idle and capped admissions alike, and now also prunes `q:cap`.

### Measured result (part 2)
Same command (`task rush:run USERS=2000`, seed 42, pooled), run `smoke-20261124-120318`:
- The drain went from 392.1 s to **216.5 s**, so the ≤ 5 min SLO is **met**. Unresolved users 0; checkout p95 133.2 ms.
- Admissions after the first wave fell from 364 to 7. The swarm made 462 `DELETE /queue` calls; the lease renewed 1,729 times (`queue_admission_touches_total{result="renewed"}`, no errors). Idle expiries have no counter of their own, so the split between leaves and lease expiries isn't measured.
- What remains is hold and payment TTLs (60 s / 90 s in `rush`): seats sit in `held` with `active` near 0 until they come back (P5a report, Fixes → Round 2).

### Consequences
- An admitted user who stops making requests leaves the controller's count at most `ADMISSION_IDLE_TTL` after their last request, with no client call. An active user (one request per idle period) is kept until the cap and never past it.
- One extra Redis call (a single `ZADD`) per admission-token request. Session- and queue-token requests are untouched.
- As in part 1, the token stays valid until `exp`: an idle user who returns can still hold, outside the controller's count (bounded by `HOLD_LIMIT` and I4). Rejecting a token whose lease has ended would close this; it is left as an owner decision (P5a report).
- After a Redis flush, `queue_restore` brings back `q:active` (with the lease expiry) but not `q:cap`; a re-claim then reports the lease expiry as the cap, so a restore can shorten an admission but never extend one. The user's existing token keeps its original `exp`.
- Tests: `TestAdmissionLease` (script contract: claim scores, XX/GT/cap, no resurrection after leave, tick pruning), `TestRequireTouchesAdmissionTokens` (only valid admission tokens touch), `TestIdleAdmissionExpiresAndSlotRefills`, `TestActiveAdmissionLivesToHardCap`, `TestConcurrentTouchesNeverResurrect` (300 users, 150 checking out, two controllers; fails with 150 resurrected admissions when `XX` is removed), `TestAdmissionRequestRenewsLease` (POST /holds through the real middleware), and the swarm's `TestNoSeatAndAbandonLeaveQueue`.
- Docs updated: ARCHITECTURE §5, DATA-MODEL §4/§5, API.md §1/§3.3, STACK §4, LOADTEST §3.2, DESIGN-BRIEF §5.

## Addendum, part 3 (2026-11-24): a lapsed lease ends the admission

**Status:** accepted (owner decision, session P5b step 0).

### Context
Parts 1 and 2 took finished and idle users out of the controller's count, but their admission tokens stayed valid until `exp`. Such a user could come back and hold, or create an order, outside `seeking`, bounded only by `HOLD_LIMIT` and I4. The owner decided that a lapsed lease ends the admission.

### Decision
- `POST /holds` and `POST /orders` require a **live** lease in Redis (`q:active` score > now), not only a valid JWT. A lapsed lease gets **403 `ADMISSION_EXPIRED`** (a new code, API.md §1.3), and the client may rejoin (`POST /queue?rejoin=true`, two-rejoin limit). `queue_join`'s rejoin rule already treats a lease with score ≤ now as ended, so the refusal and the rejoin agree.
- **No extra round trip.** Both routes already make one Redis call per request before the handler: the lease renewal. `queue_touch.lua` now also reports the state, `{OK, renewed, LIVE | LAPSED | UNKNOWN}`. The new `Keyring.RequireLive`, used only by those two routes, refuses `LAPSED`. `Keyring.Require`, used by every other admission route such as `DELETE /holds/{id}` and `DELETE /queue`, renews and ignores the state, so a user can always give seats or the slot back.
- **A lapsed lease is never renewed.** Before this change, a lease whose score had passed but that the tick had not yet pruned (≤ one tick interval) was renewed by `ZADD XX GT`, which brought an ended admission back. The script now checks `score > now` first.
- **Fail closed on error.** If the check fails (Redis unreachable, 500 ms timeout), the two routes answer 503 `DEPENDENCY_UNAVAILABLE` with `Retry-After: 1`. The hold path is Redis-first anyway, and a gate that fails open is not a gate.
- **R1.** A missing `q:init` means Redis lost the queue state, and `q:active` can't answer. The script returns `UNKNOWN`, and the token alone decides, as it did before this change. Once `queue_restore` runs, `q:active` comes back from the snapshot and the check applies again. Admissions claimed after the last snapshot (≤ 1 s) are missing from `q:active`, so they get `ADMISSION_EXPIRED` and rejoin.
- **`POST /test/admit`** (rush/test only) writes a lease the way a claim does: `q:active` = min(now + idle, cap) and `q:cap` = cap, in one MULTI. K2 and other test-admitted users therefore pass the check.
- **Direct mode** (`QUEUE_ENFORCED=false`, B0–B2) with a session token is not checked.
- **Swarm:** on `ADMISSION_EXPIRED` from a hold, the user rejoins. On `ADMISSION_EXPIRED` from an order, the user first releases the hold (`DELETE /holds/{id}`) and then rejoins. Rejoins share the existing two-rejoin budget, and a third ends as NO_SEAT.

### Accepted gap
The check and the hold script are two Redis calls, not one script. A release that ends the user's admission (`hold_release`/`hold_consume`) can land between them, so the hold goes ahead on an admission that has just ended. That needs the same user to start a new hold while their previous hold or order is ending, and `HOLD_LIMIT` and I4 still bind. Moving the check into `hold_seats`/`hold_best` would close the gap for holds but not for orders, and both scripts would also need `q:active` and `q:init`. That was judged not worth it.

### Consequences
- Nobody can spend an admission the controller no longer counts. I7's "admitted-and-seeking ≤ target" now holds for returning users too.
- A user who is idle for `ADMISSION_IDLE_TTL` (60 s in `rush`) between admission and hold, or between hold and order, must rejoin. The swarm's think times (1–4 s and 2–6 s) are far below that.
- Tests:
  - `TestAdmissionLease`: LIVE, LAPSED and UNKNOWN, and a lapsed lease is not revived.
  - `TestRequireLiveGate`: 403 on lapsed, 503 on error, UNKNOWN passes, and session tokens and `Require` are unaffected.
  - `TestLapsedAdmissionCannotHold` (real Redis and PG): a lapsed user gets 403 and no hold, both before and after pruning; an active user gets 201; the status says OFFER_EXPIRED and the rejoin is honoured; a released checkout can't hold again.
  - `TestOrderCreateRefusesLapsedAdmission`: lapsed → 403 and no order; live → 201.
  - Mutation check: with the gate disabled, both integration tests fail because the lapsed user gets 201.
