# ADR 0008: Queue script details: freeze rule, tick guards, rejoin eligibility, status transitions

- **Status:** accepted
- **Date:** 2026-11-23
- **Phase:** P4 (session P4a: tasks 1–3)

## Context
P4a builds the queue scripts (DATA-MODEL §5), the API side (join, status, leave) and the admission controller (ARCHITECTURE §5). Building them surfaced gaps in the contracts:
1. **Freeze race.** The contract said "pre path: ZADD pre NX" if `now < open`. A join whose clock reads just before T0 can then land after a tick (or another join) already froze `presize`. Its lottery rank would be above `presize` and collide with a post-T0 ticket.
2. **`queue_tick` had no inventory guard.** With no inventory loaded (a cold start, or a FLUSHALL before the rebuild), `SCARD avail = 0` and `ZCARD holds = 0` read exactly like "sold out", and the tick would set `q:soldout` for good.
3. **`queue_tick` needs T0**, but its ARGV list had no `sale_opens_ms`. `meta.sale_opens_ms` is written only by an inventory rebuild, so re-timing the sale would leave it stale.
4. **admit_rate_ema** was named but not defined. Concurrent controllers tick at irregular, sometimes identical, instants.
5. **seeking** was `ZCARD active − SCARD holding`. A holder who isn't in `active` (admitted through `/test/admit`, or whose admission lapsed while they hold) undercounts seeking.
6. **Rejoin "only after OFFER_EXPIRED"** wasn't enforced anywhere, and a rejoin request from a user who is still waiting had no defined answer.
7. **`queue_resolve` before the freeze**, and **`queue_claim` for a ticket above the cursor** (a replica's snapshot ahead of Redis after a restore), had no defined replies.
8. **`queue_leave`** took only `uid`, so an unclaimed offer kept its permit until it expired.
9. **Event status transitions**: concurrent controllers, and an admin re-timing the sale, must not flip a SOLD_OUT event back to ON_SALE.

## Decision
- **Freeze rule:** the pre-queue ZADD happens only while `presize` is unset. Every join and tick first freezes `presize` if `now ≥ T0`. After the freeze, every join takes the post-T0 path, whatever its own clock says. Every pre member therefore has rank ≤ `presize`, and tickets are exactly 1..n (`TestFreezeRace`).
- **`queue_tick` checks `meta.loaded = 1` first and returns `NOT_LOADED` otherwise.** The controller skips that event; the sweeper's `EnsureLoaded` rebuilds it.
- **`queue_tick` ARGV gains `sale_opens_ms` and `rate_tau_ms`:** `now_ms, sale_opens_ms, max_active, overadmit, min_admit, max_per_tick, offer_ttl_ms, rate_tau_ms`. The controller reads `sale_opens_at` from PG every second. `ADMITTING` also returns `target, seeking, outstanding, expired_offers`, which tests and (later) metrics use.
- **admit_rate_ema is a time-decayed counter:** `rate = rate·e^(−dt/τ) + k/τ` with τ = 5 s, in admissions per second. It is stored in `q:rate`, with its clock in the new key `q:rate_at`. It is well-defined for any dt ≥ 0, including two controllers in the same millisecond.
- **seeking = `ZCARD active − ZINTERCARD(active, holding)`:** the admitted users who hold nothing, counted exactly.
- **Rejoin eligibility lives in `queue_join`:** the ticket is ≤ cursor, its offer is gone or past its deadline, and the user isn't admitted. A rejoin request from anyone else returns their current entry unchanged and doesn't consume a rejoin. The user entry is `"<pre|ticket>:<rejoins>"`. The third rejoin returns `REJOIN_LIMIT`, which the API answers with **409 `REJOIN_LIMIT`** (new error code).
- **`queue_resolve` returns `PENDING`** while `presize` is unset; the API keeps answering PREQUEUE (retry 0.5 s). The API calls resolve only once its snapshot shows `presize > 0`. A `NOT_FOUND` (lost entry) makes the API join the user again, which lands them at the back.
- **`queue_claim` returns `WAITING`** when the ticket is above the cursor.
- **`queue_leave` takes `uid, ticket`** (ticket 0 = none). It frees the offer as well as the admission. Before the freeze it also removes the lottery entry; after the freeze it never does, because that would shift everyone's rank.
- **Status transitions use `TransitionEventStatus`** (a conditional UPDATE with an expected-from list): SCHEDULED → ON_SALE on the first ADMITTING or PAUSED tick, and SCHEDULED/ON_SALE → SOLD_OUT on SOLD_OUT. The controller runs one loop per non-CLOSED event and re-reads the event list every second.

## Consequences
- DATA-MODEL §4 (`q:rate_at`, the `q:user` format) and §5 (the five queue rows), ARCHITECTURE §5 (the guards and the seeking definition), and API.md §1.3 (`REJOIN_LIMIT`) were updated to match.
- The controller needs PG as well as Redis. When PG is down, ticks still run (the transition is skipped and the error logged), so admission doesn't stop.
- A user can still leave and join afresh to get past the rejoin limit. They land at the back, so fairness holds; the limit only stops cheap churn through rejoin.
