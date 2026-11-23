# P2: Seat holds with TTL and fencing
**Timebox:** 4 h · **Read first:** ARCHITECTURE.md §2, §3 (I1, I5, I6, R1), §4.2, §4.4 · DATA-MODEL.md §4–6 · API.md §1, §3.4, §3.8

## Goal
Holds are atomic, all-or-nothing and expiring. The Redis fast path handles contention and Postgres has the final say. Under heavy contention there's never a double hold and never a lost seat.

## Tasks
1. **Auth** (`internal/auth`):
   - JWT HS256 with the `TOKEN_KEYS` key ring (kid). Audiences `session`, `queue`, `admission`. Issue and verify, with clock-skew tolerance of 5 s.
   - `POST /api/v1/sessions` (anonymous UUIDv7 user).
2. **Test admission:** `POST /api/v1/test/admit` (only with `TEST_ENDPOINTS_ENABLED` in rush/test) issues admission tokens, so holds can be tested before the queue exists in P4.
3. **Idempotency middleware** (PG-backed, as in API.md §1.2): STARTED/COMPLETED, `locked_until` (30 s), replay with `Idempotent-Replayed: true`, 409 REQUEST_IN_PROGRESS, 422 on hash mismatch, and a GC job stub.
4. **`internal/redisx`:**
   - client from `REDIS_URL`
   - script loader (embed + EVALSHA with NOSCRIPT fallback)
   - scripts `hold_seats`, `hold_best`, `hold_extend`, `hold_consume`, `hold_release`, `inventory_load`, `snapshot`, following the DATA-MODEL §5 contracts
   - every key goes through one key-builder helper (`keys.Avail(eid)` and so on) so the hash tags stay correct
5. **`internal/inventory`:**
   - `Hold(ctx, eventID, userID, idemKey, req)`: hid = UUIDv5(NS_HOLD, uid:key) → Redis script → PG tx (insert hold + conditional seats update; row count must equal n) → on mismatch, compensate (`hold_release`) and flag a repair.
   - `HOLD_PATH=pg_only` skips the Redis step. The fence then comes from the PG sequence `hold_fence_seq`, which is already in `0001_init.sql`.
   - Quota pre-check from `event_user_quota` → 422 QUOTA_EXCEEDED.
   - `Release(holdID, userID)`, `Extend(holdID, ttl)` (used by P3), `Consume(holdID)` (used by P3).
   - `Rebuild(eventID)` per DATA-MODEL §6. The worker calls it at startup when `meta.loaded` is missing, and it's also exposed as `POST /admin/events/{id}/rebuild-cache`.
6. **Sweeper** in `sluice worker`: every `SWEEPER_INTERVAL`, reclaim expired holds, PG first, then Redis (ARCHITECTURE §4.4). Safe with several workers (conditional updates only).
7. **Availability:** `GET /events/{id}/availability` from a per-replica in-memory cache keyed by `ver` (refresh at most every 100 ms), serving a 2-bit packed base64 bitmap plus counts, with ETag/304.
8. **Endpoints:** `POST /events/{id}/holds` (admission + IK) and `DELETE /holds/{id}`.
9. **Metrics:** `holds_requests_total{result}`, `holds_expired_total`, `holds_compensations_total`, `inventory_seats{event,state}`, `redis_script_duration_seconds{script}`.

## Required tests
- **Race, single seat:** 2,000 goroutines (distinct users) hold seat 7 → exactly 1 OK, 1,999 TAKEN. PG has 1 HELD seat and 1 hold row.
- **Race, best-available:** 10,000 users × qty 1 on a 500-seat event → exactly 500 OK, no seat in two holds, and avail = 0.
- **Multi-seat all-or-nothing:** a hold for [1,2,3] where 2 is taken → TAKEN [2], and 1 and 3 are still available.
- **Idempotency:** the same IK twice gives the same hold_id and response, plus the replay header. Two concurrent requests with the same IK → one 201 and one 409/replay, never two holds.
- **One active hold per user:** the second hold → HOLD_LIMIT (Redis and PG agree).
- **Expiry:** with TTL 2 s, the sweeper returns the seats within ≤ 2 intervals and the hold is marked EXPIRED. A second hold can then take the seats.
- **PG takeover:** a seat HELD in PG with an expired `held_until` but missing from Redis (simulated) → the new hold takes over and the old hold is marked EXPIRED in the same tx.
- **Compensation:** inject a PG conflict after the Redis grant → the Redis hold is released and the user gets 409.
- **R1:** run 300 holds, `FLUSHALL`, rebuild → Redis matches PG exactly (seat by seat).
- **pg_only path:** the same race tests pass with `HOLD_PATH=pg_only`.
- Each Lua script has a direct unit test against Redis 8 (testcontainers).

## Acceptance
- [ ] All the tests above pass under `-race` (or in CI if there's no local gcc).
- [ ] Manual check (API running with `APP_ENV=test` and `TEST_ENDPOINTS_ENABLED=true`): `POST /sessions` → `POST /test/admit` → `POST /holds {quantity:2}` → 201. Availability reflects it, and after `HOLD_TTL` the seats are back.
- [ ] `task lint test` is green, and metrics are visible on `/metrics`.

**Commit hint:** `feat(holds): Lua-atomic TTL seat holds with Postgres fencing`
