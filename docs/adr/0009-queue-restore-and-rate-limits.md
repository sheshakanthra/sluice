# ADR 0009: Queue restore gate, snapshot contents, open-sale, rate-limit details

- **Status:** accepted
- **Date:** 2026-11-23
- **Phase:** P4 (session P4b: tasks 4–7)

## Context
P4b builds queue snapshots and restore (R1), rate limiting, `/stats` and the queue metrics. The specs left gaps:
1. **Nothing stopped a wiped queue from running.** After a FLUSHALL, `q:tail` and `q:cursor` read 0. The first join would issue ticket `presize + 1` again (a duplicate of a ticket someone already holds), and once the inventory rebuild set `meta.loaded`, the first tick would offer tickets 1, 2, … again.
2. **The snapshot writer could erase the snapshot.** A writer that runs between the flush and the restore would store zeros over the last good row.
3. **The spec's offer window can admit twice.** "Tickets in `(cursor − OFFER_CLAIM_TTL × admit_rate, cursor]` get fresh offers" also re-offers tickets that were already claimed, because `q:active` is gone too. The P4 restore test requires "nobody is admitted twice".
4. **`open-sale` was listed in API.md but never built**, and the manual check needs it. Re-timing a queue that already froze `presize`, or that holds tickets, would break the lottery and people's places. The 5 s event cache (API) and the 1 s event list (controller) would keep using the old T0 for a few seconds, so a join or tick could re-freeze the queue at the old T0.
5. **`/stats` `sold_out_at`** has no source: `events` has no such column.
6. **redis_rate key prefix:** the library stores `rate:<key>`, not the documented `sl:rl:*`.
7. **Migration 0002** was reserved for P5's naive baseline.

## Decision
- **Restore gate `q:init`.** `queue_join` and `queue_tick` return `NOT_INIT` while `sl:{e}:q:init` is missing. The API join path (`Service.join`) and the worker's `Ticker` then call `Restorer.Restore` and retry once. The restore reads `queue_snapshots` (no row means a fresh event: zeros) and runs `queue_restore.lua`, which does nothing if `q:init` already exists, so any number of API replicas and workers may race to restore. Join without a restorer, or a restore that does not take, answers 503 `DEPENDENCY_UNAVAILABLE` (Retry-After 1).
- **Snapshot contents (migration `0002_queue_snapshot_state.sql`).** The snapshot also carries `admit_rate`, the outstanding offer tickets and the live admissions (uid plus expiry). Both lists are bounded by the admission target (about `QUEUE_MAX_ACTIVE × QUEUE_OVERADMIT`). A restore re-creates exactly those offers, with a fresh claim deadline, and those admissions, with their original expiries. It does not use the spec's rate window. A user who had claimed gets their existing admission back (`queue_claim` → `ADMITTED exp 0`), not a new one.
- **The writer skips unrestored queues.** `queue_dump.lua` reports `q:init`, and the `Snapshotter` writes nothing when it is 0. The upsert only replaces a row whose `taken_at` is not newer (`taken_at` is stamped before the read), so a slow writer never regresses the row.
- **`queue_claim` returns `ADMITTED exp new`.** `new = 1` only when the claim created the admission. `queue_admitted_total` counts only those.
- **`POST /admin/events/{id}/open-sale`** (admin token; `{opens_in_s: 0..86400}`):
  - It restores first, then runs `queue_reopen.lua`. If anybody holds a ticket or a lottery entry, or the event sold out, it answers 409 with the new error code **`EVENT_STATE`**.
  - Otherwise it clears the frozen counters and sets **`q:opens`**, a T0 override that `queue_join` and `queue_tick` prefer to the T0 their caller passes. That closes the stale-cache window in item 4.
  - PG gets the new `sale_opens_at`, status SCHEDULED, `prequeue_opens_at` moved earlier if needed and `starts_at` moved later if needed, plus a zero snapshot row.
- **`sold_out_at` is always `null`** in `/stats` until a later phase adds a column. `/stats` otherwise follows API.md §3.9: cached ≤ 1 s per replica, status from PG, queue from the snapshot cache, seats from the live counts, correctness from `sl:{e}:audit`.
- **Rate limits:**
  - redis_rate GCRA with keys `rate:sl:rl:<rule>:<ip|user>:<subject>`.
  - The user is the verified subject of any SLUICE token (`Keyring.Subject`), checked before the route's own `Require`. That costs one extra HMAC verify on limited routes.
  - The GCRA fails open on Redis errors.
  - Status uses an in-process token bucket: 3/s, burst 6, 16-shard LRU of 200,000 users per replica.
  - The router gained a per-pattern decorator (`Router.Wrap`) so packages register routes unchanged.
- **Migration numbering.** The queue snapshot state takes 0002, and P5's naive baseline becomes `0003_naive_baseline.sql`.

## Consequences
- DATA-MODEL §1/§4/§5/§6, API.md §1.3/§1.5/§2/§3.9, ARCHITECTURE §5, STACK.md (repo layout) and the P5 phase file were updated.
- A fresh event's first join (or tick) costs one extra PG read and one extra script. After a flush, joins wait for at most one restore.
- **Inside the last snapshot interval (≤ 1 s before a flush):**
  - Offers issued in that interval are offered again as the cursor advances. Nobody loses their place.
  - A claim made in that interval can be repeated once by the same uid. It is still one entry in `q:active`, and HOLD_LIMIT and the per-user PG quota still bind, so no seat effect is possible.
- **Lottery entries are not snapshotted** (up to 50k members). After a flush, a pre-queue user who had not yet resolved their ticket is re-joined at the back on their next poll. Flushes before T0 lose the lottery draw for those users.
- open-sale is a demo/test tool. It cannot re-time a queue with entries; a full event reset stays out of scope.

## Addendum (P4b fixes): PSP connection bound
- **Context.** A 200-goroutine order-creation stress test (300 concurrent creates) overloaded MockPay: the PSP client opened one connection per concurrent call with no upper bound, MockPay's accept queue overflowed on a busy Windows host, and refused dials plus their retries kept it overflowing (one run: 8,869 refused dials and 2,985 timeouts over ~45 s; the API answered 503 `DEPENDENCY_UNAVAILABLE` throughout, with no wrong state).
- **Decision.** `payments.NewTransport` bounds the client to `MaxConnsPerHost = 64` connections per PSP host per process, all kept alive between calls. A call beyond the bound waits for a connection inside its per-attempt timeout. Tests wrap this same transport with their fault injector instead of `http.DefaultTransport` (which keeps only 2 idle connections per host).
- **Consequences.** With 3 API replicas MockPay sees at most 192 concurrent calls. A burst larger than 64 in-flight PSP calls on one replica queues instead of dialling. API.md §5 states the bound.
