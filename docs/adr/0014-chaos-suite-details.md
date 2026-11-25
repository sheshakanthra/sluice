# ADR 0014: Chaos suite details

- **Status:** accepted
- **Date:** 2026-11-25
- **Phase:** P7 (session P7a)

## Context
The P7 phase file defines C1–C4 as "the SMOKE-sized rush plus one fault", and `docs/LOADTEST.md` §4 listed C1–C4 at 10,000 users. P7a was asked to run them at SMOKE size on the laptop. The phase file leaves open how the faults are timed and where Toxiproxy sits, and which checks decide pass or fail.

## Decision
- `task chaos` runs `loadtest/chaos/run.sh`. Each case gets a fresh stack (`down -v`) with the overlay `deploy/compose.chaos.yml`, then a SMOKE rush (`USERS`, default 5,000; 500 seats; `--audit`).
- Toxiproxy `ghcr.io/shopify/toxiproxy:2.12.0` sits only between **api1..3** and Postgres, Redis and MockPay (`deploy/toxiproxy/toxiproxy.json`). The worker, MockPay itself and the auditor connect directly, so a fault on the API path never blinds the judge.
- Faults are timed from the swarm's own progress lines (`T0 +15s |`), not wall-clock sleeps:
  - C1 `redis-cli FLUSHALL` at T0+15 s.
  - C2 `docker compose kill api2` at T0+10 s; api2 stays down.
  - C3 Toxiproxy on the API→MockPay proxy for the whole run: `latency` 2,000 ms on 30% of connections, plus `timeout` 3,000 ms on 5%.
  - C4 `docker compose pause postgres` for 3 s at T0+20 s.
- Pass rule (`loadtest/chaos/verdict.py`): oversold, duplicate charges, orphan charges and quota violations = 0 in every case. C1 also needs unresolved = 0, and C4 needs stuck holds = 0. The error rate, the errors in T−2…T+10 s and the peak errors/s are reported, not judged.
- The suite table goes to `loadtest/results/chaos-<stamp>.md`; curated copies go to `docs/results/p7a/`.

## Consequences
- The suite runs in about 20 minutes on the laptop. The 10,000-user variant is one variable away (`USERS=10000 task chaos`).
- The Toxiproxy hop adds latency to every API dependency call. In P7a most chaos errors fell in the sale-open burst (T0+1…5 s), not around the faults; the report shows both.
- Docs updated: `docs/LOADTEST.md` §4 (C1–C4 row), `docs/STACK.md` (image versions, layout).
