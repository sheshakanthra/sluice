# ADR 0006: Hold path details: quota placement, repair script, rebuild lock, availability source

- **Status:** accepted
- **Date:** 2026-11-23
- **Phase:** P2

## Context
P2 builds the hold path from ARCHITECTURE §4.2/§4.4, DATA-MODEL §4–6 and API §1.2/§3.4/§3.8. Building it surfaced a few things the specs leave open or get slightly wrong:
1. **Where the quota pre-check runs.** API §3.4 and the `hold_seats` contract say the per-user quota is "pre-checked in PG by the API". If that happens before the Redis script, every request in a rush (including the thousands Redis rejects in microseconds) costs one Postgres round trip, which undermines ARCHITECTURE §2 mechanism 2 ("losers are rejected ... without touching Postgres").
2. **"Mark seats for repair"** (§4.2) has no script. After a compensation, `hold_release` returns all the hold's seats to `avail`, including the one PG says someone else holds, so Redis keeps offering a seat PG will refuse.
3. **What a hold script does when the event isn't loaded** (after `FLUSHALL`, or before the worker starts). With an empty `avail` set, every hold would report `TAKEN`, which is wrong and won't fix itself until the worker's next tick.
4. **Fence and `ver` after a rebuild.** A fresh Redis starts `fence` and `ver` at 0. Fences must stay monotonic (I6), and a reused `ver` would let a client's old `ETag` match a different seat map.
5. **Availability with `HOLD_PATH=pg_only`.** Redis isn't maintained on that path, so a Redis-sourced bitmap would be wrong.
6. **Replay bytes.** `idempotency_keys.response_body` is `jsonb`, which normalises key order and whitespace.

## Decision
- **Quota is checked inside the hold's PG transaction**, after the Redis grant and before `InsertHold` (`checkQuota` in `internal/inventory/hold.go`). It's the same query and the same 422 `QUOTA_EXCEEDED`. A refusal is compensated like any other PG refusal. Requests Redis rejects never touch Postgres. On `pg_only`, the check runs first inside the transaction.
- **New script `inventory_repair.lua`**: it forgets a list of hold ids without touching `avail`, then applies per-seat PG states (`A`/`H`/`S`). Compensation is now `hold_release` followed by a repair of the seats PG says are HELD (unexpired) or SOLD. A PG takeover runs a repair that drops the expired holds and frees their other seats.
- **Hold scripts return `NOT_LOADED`** when `meta.loaded` ≠ 1. The API then runs `EnsureLoaded`: a `SET NX PX 30s` lock on `sl:{e:<eid>}:rebuild_lock`, then a rebuild from PG, then one retry. If another process holds the lock, the request gets 503 `DEPENDENCY_UNAVAILABLE` with `Retry-After: 1`. The worker also calls `EnsureLoaded` for every open event on each sweeper tick, so start-up and any flush are covered within one interval.
- **`inventory_load.lua`** sets `fence = max(current, max(holds.fence) in PG)` and `ver = max(current + 1, now_ms)`. `ETag`s are `"r<ver>"` (Redis source) or `"p<sum of seat versions>"` (PG source), so the two sources can't collide.
- **Availability reads Postgres** (`ListSeatStates`) when `HOLD_PATH=pg_only` or the event isn't loaded in Redis. The bitmap packs 2 bits per seat, MSB first (seat *i* is in byte *i*/4 at shift 6 − 2·(*i* mod 4)).
- **Replays are semantically identical, not byte-identical:** same status and the same JSON values, but key order and spacing follow `jsonb`. Changing the column type needs a migration and buys nothing for JSON clients.
- Smaller choices recorded here:
  - `NS_HOLD` = UUIDv5(`NameSpaceURL`, `https://sluice.dev/ns/hold`).
  - Every hold expiry is truncated to the millisecond, so Redis `exp_ms`, PG `expires_at` and seat `held_until` are the same instant.
  - `DELETE /holds/{id}` on a RELEASED or EXPIRED hold is a 204 no-op. On PAYMENT_PENDING or CONSUMED it's 409 `ORDER_STATE`.
  - `Consume` only mirrors the confirm into Redis; the PG half belongs to P3's ConfirmOrder transaction.
  - P2 doesn't gate holds on `events.status`; admission (P4) controls timing.
  - With `pg_only`, best-available uses `FOR UPDATE SKIP LOCKED`. It can answer `INSUFFICIENT` while the last seats are locked by in-flight transactions that later roll back. That's acceptable for a comparison path, and the race tests use distinct users, so nothing rolls back there.

## Consequences
- A rush costs Postgres one transaction per Redis grant and nothing per rejection. The quota check is still a pre-check; I4 is enforced by `quota_cap` at confirm (P3).
- Redis drift converges: every PG refusal repairs the seats involved, and the sweeper's Redis pass resolves any hold whose `exp` passed, using PG as the judge. No path lets Redis decide a sale: PG conditional updates with row-count checks remain the authority (I1, I6).
- A rebuild while holds are in flight can drop a just-committed grant from Redis. PG still refuses a second hold on those seats, so the cost is retries, never seats (documented on `Rebuild`).
- Docs updated in this phase: DATA-MODEL §4–6, API §1.2, §3.4, §3.8, §3.12, ARCHITECTURE §4.2, STACK §2.
