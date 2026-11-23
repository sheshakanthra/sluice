# P4: Virtual waiting room
**Timebox:** 4 h · **Read first:** ARCHITECTURE.md §2, §3 (I7, I8, R1), §4.1, §5 · DATA-MODEL.md §4–6 · API.md §3.2–3.3, §3.9

## Goal
50,000 people can show up at once and the system stays calm. Joining costs one Redis call. Waiting costs **zero** Redis calls. Admission is fair, inventory-aware and bounded, and when the event sells out, everyone hears about it within one poll.

## Tasks
1. **Scripts:** `queue_join`, `queue_resolve`, `queue_claim`, `queue_leave` and `queue_tick`, per the DATA-MODEL §5 contracts. Randomness (crypto/rand score) and time always come from Go via ARGV.
2. **`internal/queue`:**
   - **Join**: `POST /events/{id}/queue` (session). Idempotent per user. `?rejoin=true` only after OFFER_EXPIRED, at most 2 times. Issues a queue token.
   - **Snapshot cache**: per replica, per event. Refresh every 100 ms with **one** `snapshot.lua` call and store it in an `atomic.Pointer`.
   - **Status**: `GET /queue/status`.
     - WAITING users: pure in-memory (token verify + snapshot math, per ARCHITECTURE §5), with **zero** Redis calls on this path.
     - PREQUEUE token after T0: one `queue_resolve`, then reissue the token with the ticket.
     - ticket ≤ cursor: `queue_claim`, then issue the admission token.
     - Sold out: SOLD_OUT straight from the snapshot.
   - **Leave**: `DELETE /queue` → `queue_leave`.
   - `/holds` and `/orders` now require an admission token when `QUEUE_ENFORCED=true`. `false` is allowed only in rush/test, for direct-mode baselines.
3. **Admission controller** in the worker: one loop per ON_SALE event every `QUEUE_TICK`. It runs `queue_tick` and maintains `admit_rate_ema`. The event status moves to ON_SALE / SOLD_OUT in PG on transitions.
4. **Snapshots and restore (R1):** the worker writes `queue_snapshots` every 1 s. Restore follows DATA-MODEL §6 and runs automatically when queue keys are missing for an ON_SALE event.
5. **Rate limiting:**
   - redis_rate GCRA middleware for sessions, join, holds, orders, pay and confirm, keyed by IP and/or user, with the limits from API.md §2.
   - An in-process limiter for status (per uid, 3/s, burst 6, bounded LRU).
   - Client IP comes from `TRUSTED_PROXIES` plus `X-Forwarded-For`.
6. **Stats:** `GET /stats/{id}` (API.md §3.9). Cached ≤ 1 s per replica; reads the snapshot plus the `sl:{e:<eid>}:audit` hash.
7. **Metrics:**
   - `queue_joins_total{result}`, `queue_status_total{state}`, `queue_waiting{event}`, `queue_cursor{event}`, `queue_offers{event}`, `queue_active{event}`
   - `queue_admitted_total`, `queue_offer_expired_total`
   - `queue_status_redis_calls_total` (must stay ~0 for waiting users)

## Required tests
- **Lottery:** 10,000 pre-queue joins → after T0, the ticket order matches the random score order and isn't correlated with join order (Spearman |ρ| < 0.05).
- **FIFO after T0:** post-T0 joins get consecutive tickets in arrival order after `presize`.
- **Freeze race:** joins racing with T0 (some just before, some just after, plus concurrent ticks) → every user gets exactly one ticket. No duplicates, no gaps.
- **Admission order:** offers are issued strictly in ascending ticket order.
- **Bounded admission:** with 500 seats and OVERADMIT 1.5, seeking + outstanding never exceeds the target (sampled every tick).
- **Concurrent controllers:** 3 tick loops in parallel never over-admit compared with 1 loop (same final cursor ± one tick's worth).
- **Sold out:** sell every seat → soldout set, no claims succeed afterwards, and status returns SOLD_OUT for every ticket.
- **Offer expiry and rejoin:** an unclaimed offer expires, its permit returns, and rejoin puts the user at the back. The third rejoin → REJOIN_LIMIT.
- **Zero-Redis status:** 10,000 status calls for WAITING users increase the Redis call counter by 0 (instrument the client).
- **Scale (integration):** 50,000 in-process joins complete within the time budget the report states, and Redis memory is reported.
- **Restore:** FLUSHALL mid-queue → restore from snapshot. Users keep positions, give or take one snapshot interval, and nobody is admitted twice.

## Acceptance
- [ ] Every test above is green, plus `task lint test`.
- [ ] Manual check (curl or a small script; the UI arrives in P6): set `open-sale` to 30 s, create 2 sessions and join with both, then poll and watch PREQUEUE → WAITING → ADMITTED, followed by hold → order → pay → ticket.
- [ ] `/stats` reflects queue and inventory live.

**Commit hint:** `feat(queue): pre-queue lottery, FIFO admission controller, zero-lookup status`
