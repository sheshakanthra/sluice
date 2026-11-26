# ADR 0017: Position-aware poll pacing, jittered pre-queue, client backoff floored at the server's pacing

- **Status:** accepted
- **Date:** 2026-11-26
- **Phase:** P8 (H 50k fix session, approved by Sheshakanth as fixes A, B, C and E)

## Context
The first CI headline run (`45781258193`, H 50,000 on a 4-vCPU runner) kept every money and seat invariant at 0 but left 12,967 users UNRESOLVED (`docs/reports/P8-h50k-diagnosis.md`). The runner ran out of CPU, and three pacing rules made the overload worse:

1. **Timeouts sped polling up.** A healthy waiting user polled every `retry_after`, which was at most 15 s. A *failed* poll ignored that and retried after 250 ms to 2 s (`retryable` in `internal/rush/client.go`). The web client did much the same: 2 s × 2^n capped at 15 s, with no jitter. So the more overloaded the system got, the harder every client polled. Status was 83.3% of the 2.63M attempts, 43.85 per user.
2. **The pre-queue converged on T0.** The pre-queue answer was `clamp(time_to_T0 / 2, 0.5 s, 5 s)` with no jitter. The 35k users who arrived before T0 halved their way towards it and then polled in a 0.5 s lockstep, so they all hit `queue_resolve` together. Errors started at T−4 s; by T0 they were 9,772 of 10,239 rps.
3. **Far-back users polled as often as everyone else.** `retry_after = clamp(eta/4, 1 s, 15 s)` puts nearly all of 46k waiting users on the 15 s cap.

A fourth, smaller issue was in the swarm's timeout clock. `context.WithTimeout` started when `do` was called, so a reconnect after a timeout (each timeout closes a dedicated HTTP/1.1 connection) spent part of the 2 s status budget dialling a CPU-starved Caddy.

This ADR changes how the system behaves for real users, not how it is measured. The scenario, its arrival window, the connection mode (`dedicated`), the invariants, the gates and the thresholds are all unchanged.

## Decision
**Server (`internal/queue/service.go`, API.md §3.3, ARCHITECTURE §5):**
- **WAITING `retry_after` is position-aware:**
  - The base is `min(clamp(eta_s / 4, 1 s, 60 s) × U(0.8, 1.2), safeWait(position))`. Near the front that's 1 s; far back it grows, bounded.
  - `safeWait(position) = ((ceil(position / QUEUE_MAX_ADMIT_PER_TICK) − 1) × QUEUE_TICK + OFFER_CLAIM_TTL / 2) / 1.1`. The cursor moves at most `QUEUE_MAX_ADMIT_PER_TICK` tickets per tick, so a user's offer can't appear sooner than that many ticks from now, and the offer then stays claimable for `OFFER_CLAIM_TTL`. Half the TTL is kept as margin (the poll itself, a late tick). The `/ 1.1` leaves room for the client's +10% jitter.
  - The result: **however wrong the admit-rate estimate is, the next poll lands before the earliest possible offer expires.** `TestWaitingRetryNeverSkipsAnOffer` checks this over TTLs of 2–60 s, ticks of 10 ms–1 s, 1–1000 tickets per tick, any rate and positions up to 300k.
  - With the rush config (30 s TTL, 200 per 100 ms tick), a user 46,000 back is told about 34 s instead of 15 s, a user 2,000 back about 14 s, and the front 1 s.
- **PREQUEUE `retry_after` is jittered and spread over T0:**
  - more than 5 s before T0: `clamp(time_to_T0 / 2, 0.5 s, 5 s) × U(0.8, 1.2)`;
  - within 5 s of T0: `time_to_T0 + U(0, spread)` with `spread = min(5 s, OFFER_CLAIM_TTL / 4)`. That is one poll, landing at a uniformly random moment in [T0, T0 + spread], so the pre-queue resolves over 5 s instead of in a 0.5 s lockstep. The lottery decides the order, so polling later costs nobody their place, and the spread is far inside the offer TTL;
  - after T0 and before the freeze: `0.5 s + U(0, 1 s)`.
- `queue.Options` gains `OfferTTL`, `MaxAdmitPerTick` and `Tick`, wired from `OFFER_CLAIM_TTL`, `QUEUE_MAX_ADMIT_PER_TICK` and `QUEUE_TICK`. `NewService` refuses zero values. No new env vars.

**Clients (the swarm, `internal/rush`, and the web app, `web/lib/backoff.ts` + `web/lib/retry.ts`, use the same rules):**
- **A failed attempt backs off with full jitter, floored at the server's pacing:**
  - delay = `max(floor, U(0, min(30 s, 500 ms × 2^attempt)))`. The **cap is 30 s**. `floor` is the server's last `retry_after_ms` for a status poll, or the response's `Retry-After` (5xx, 429).
  - This applies on a timeout, transport error, 5xx or 429 for status, session, join and every other retried call.
  - `REQUEST_IN_PROGRESS` is not a failure and keeps `max(Retry-After, 500 ms)`.
  - The swarm's budget is unchanged at 30 attempts per request. The web app tries a session or join call up to 5 times (about 7.5 s of backoff at most), and its status poll retries indefinitely on the same backoff.
- **A successful poll waits `retry_after × U(1, 1.1)`.** Clients never poll sooner than the server asked. This was `±20%` in the swarm, which could undercut the server.
- **Fixed client intervals are jittered:**
  - the swarm's `/confirm` poll: ±20%;
  - the web seat-map refresh: 1 s ± 20%;
  - the web admitted-user watch: `retry_after × U(1, 1.1)`.
- The swarm's poll ceiling rises from 30 s to 60 s to match the server's cap.

**Swarm timeout clock (E, LOADTEST §3.4):**
- The per-call timeout (2 s status, 5 s other calls) now runs from `httptrace.GotConn`, the moment the request has a connection and is about to be written. So a slow dial or reconnect, or the swarm's own scheduling before the send, isn't charged against the server's budget.
- A hard stop at dial timeout (5 s) + call timeout bounds a connection that never comes.
- **Latency is still recorded from the intended start** (coordinated-omission safe), so dial time and generator lag still count as latency. Only the decision "did this attempt time out?" moved.

## Consequences
- Under overload, total poll volume now falls instead of rising. Every failure stretches that user's next attempt, and nobody retries faster than the server's own pacing.
- Some users now wait longer between polls, up to 60 s, or up to 30 s of backoff after failures. The offer-safety bound is a tested property. The cost is a coarser position display for users far back, which the UI already shows as a range with outlook UNLIKELY.
- A user who keeps failing now takes longer to use up the swarm's 30-attempt budget. With no floor, the expected backoff over 30 attempts is about 6 min, plus up to 1 min of 2 s status timeouts, against about 1 to 2 min before. With a floor (a far-back user's `retry_after`) it can exceed the 15 min `--max-duration`, and the run deadline then ends that user as UNRESOLVED. So UNRESOLVED now means "failed for minutes", as a real user would experience it. `--max-duration` and every gate are unchanged.
- The web client and the swarm share the constants (base 500 ms, cap 30 s, +10% poll jitter). A change to one must be made in both.
- Docs updated: `docs/API.md` §3.3, `docs/ARCHITECTURE.md` §4 and §5, `docs/LOADTEST.md` §3.2–3.4, `docs/DESIGN-BRIEF.md` §7 (polling rule).
- Tests: `internal/queue/retry_internal_test.go`, `internal/rush/backoff_test.go`, `web/lib/backoff.test.mjs` (`pnpm --dir web test`, also in CI).
