# ADR 0018: A live hold outlives the admission's cap (spend grace)

- **Status:** accepted
- **Date:** 2026-11-27
- **Phase:** P8 (swarm-efficiency session)

## Context
H 50000 on CI answered 401 `TOKEN_EXPIRED` on `hold`, `leave` and `order`: 14 times in run `45930086030`, and 17 times in run `45781258193` (`hold` 3, `leave` 8, `order` 5, `release` 1; its `summary.json` is on disk at `loadtest/results/ci-45781258193-h-50000`).

The admission token's `exp` is the admission's hard cap: claim + `ADMISSION_TTL` (3 min in `rush`, 10 min by default; API.md §1.1). The worst legitimate wait between claim and the last admission-token request is longer than that:
- a visible seat map keeps the lease alive up to the cap (ADR 0010 addendum part 4);
- a hold taken just before the cap lives `HOLD_TTL` (1 min in `rush`, 5 min by default);
- the order for that hold, and the release or leave if the rider gives up, all present the admission token.

So a rider who holds at cap − 5 s and orders 10 s later got 401 `TOKEN_EXPIRED` for a hold they own and that still had 50 s to live. The web client then showed "Nothing was held or charged", which was false: the seats stayed held until `HOLD_TTL`. Release and leave after the cap failed the same way, so seats and the slot were not given back early.

This is also a code/spec mismatch. API.md already said `POST /holds` and `POST /orders` past the cap answer 403 `ADMISSION_EXPIRED` (rejoin), but JWT verification refused the token first with 401.

In the CI runs the trigger was the starved generator: lag p99 2.7–3.2 s and 30-attempt retries stretched the users' journeys past the cap. A rider on a slow mobile network, or one who reads the seat map until near the cap, gets to the same place.

## Decision
The key ring gets a **spend grace** equal to `HOLD_TTL` (`Keyring.WithSpendGrace`, wired from `HOLD_TTL` in `internal/app/api.go`). An admission token past `exp`, but no more than `ClockSkew` + grace past it, is accepted only to finish or give back what the admission already spent:
- `POST /orders` (`Keyring.RequireHoldBacked`) goes on, marked `auth.AdmissionPastCap`. The handler accepts it only for the caller's own ACTIVE, unexpired hold of the token's event; anything else is 403 `ADMISSION_EXPIRED`. Before the cap nothing changes: a lapsed idle lease is still 403 (ADR 0010).
- `DELETE /holds/{id}` and `DELETE /queue` (`Require`) accept the token.
- `POST /holds` (`RequireLive`) answers 403 `ADMISSION_EXPIRED`, the documented rejoin answer, instead of 401.
- A past-cap token never renews the lease (`queue_touch` isn't called), so the cap still bounds every new spend.

Beyond the grace the answer is 401 `TOKEN_EXPIRED`, as before. By then any hold the admission paid for has expired.

## Consequences
- A rider can always buy a hold they own while it is live. Nobody gets a seat they don't hold, so the order path's fencing and I1/I6 are untouched.
- I7 is unchanged: a past-cap token can't create a hold, and a holder isn't "seeking".
- An admission token is usable for up to `HOLD_TTL` longer, but only for the user's own hold, release and leave, with the same signature, audience and event checks.
- Tests:
  - `internal/auth/grace_test.go`: every gate before, past and beyond the grace, without a grace, and a 64-goroutine `-race` test.
  - `internal/orders/admission_gate_test.go` `TestOrderCreatePastAdmissionCap`: real Postgres and Redis. Own live hold → 201; someone else's hold or an expired hold → 403 and no order; past the grace → 401.
- Docs: API.md §1.1 (token table, TOKEN_EXPIRED, ADMISSION_EXPIRED rows), ARCHITECTURE §4 (admission lease).
