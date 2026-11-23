# SPEC: SLUICE

> A ticket booking system built to survive a Tatkal-style rush. It has a virtual waiting room, TTL seat holds with fencing and idempotent payments. The proof is a reproducible test with 50,000 concurrent users on 500 seats: 0 oversells, 0 duplicate charges, with p95 and throughput measured under that load.

## 1. Problem
**Who gets hurt:** people booking flash drops: Tatkal quota opening at 10:00:00, concert drops, IPL ticket sales. Three failures recur:
1. **The site falls over** in the first seconds of the stampede (timeouts, 5xx, blank pages).
2. **Seats get oversold.** Two people are told they own the same seat.
3. **People get charged twice, or charged with no ticket.** Retries, double-clicks and gateway timeouts each cause a second charge.

SLUICE stays up, never oversells, never double-charges and never keeps money without issuing a ticket. It proves all four with a public, reproducible load test.

## 2. Goals
| # | Goal | Proof |
|---|---|---|
| G1 | Survive the stampede: 50k users arrive within 20 s and every one of them gets a definitive answer (ticket or sold out) | Rush run: 100% of users reach a terminal state, error rate < 0.1% |
| G2 | Zero oversells | Invariant I1, enforced in the DB and audited |
| G3 | Zero duplicate charges and zero orphan charges | Invariants I2 and I3, checked against MockPay's ledger |
| G4 | Fairness: a pre-queue lottery at opening, then FIFO. Bots gain nothing from being 50 ms faster | I7 plus queue tests |
| G5 | Measured performance, reproducible in public | Headline run on GitHub Actions with the summary posted on the run page |
| G6 | Portfolio-grade product: a striking UI, a Rush Control dashboard, a results page and interview-ready docs | P6 and P8 deliverables |

## 3. Non-goals (v1)
- Real money or a real gateway. MockPay reproduces Razorpay/Stripe semantics: intents, auth/capture/void/refund, idempotency keys and signed webhooks.
- Real auth, KYC or PII. Users are anonymous sessions and tickets carry no names.
- Multi-region, HA databases, Kubernetes, native apps.
- Seat preferences (window/aisle) beyond picking seats on the map.
- A refund UI or back-office. Refunds and voids exist only as automatic compensation.
- Any real railway, venue or ticketing brand. Every name in the demo is fictional.

## 4. Users
| Persona | Needs |
|---|---|
| **Rider**: on a phone, under pressure, at 10:00:00 | A clear place in line, honest odds, a hold timer, a guaranteed "you were not charged" when things fail |
| **Operator**: watches the drop | Live queue, inventory and correctness counters (Rush Control), Grafana for depth |
| **Reviewer** (recruiter or interviewer) | A working replay of the recorded 50k rush, the results with methodology, and a live demo on request |
| **Bot** (load generator) | Exercises every path at scale, including the misbehaviour real clients produce |

## 5. Features (v1 scope)
**F1 Events and inventory**
- Events with a seat layout, sale open time (T0), pre-queue open time, price and a per-user cap (default 4).
- Two seeded events: `tatkal-chn-sbc` (the rush event, 500 seats: 10 coaches × 50) and `quiet-tuesday` (500 seats, no rush) for trying the flow calmly.
- Admin: reset event, rebuild cache from Postgres, open sale in N seconds.

**F2 Virtual waiting room**
- Anyone who joins before T0 goes into a **pre-queue**. At T0 their order is fixed by a lottery (a uniform random score), so arriving early never beats arriving on time.
- Users who join after T0 queue FIFO behind the pre-queue block.
- An **admission controller** offers places in ticket order, keeping admitted-and-still-seeking users at or below a target derived from the remaining seats and a hard cap.
- An offer has a claim window. Unclaimed offers expire and the user may rejoin at the back (at most 2 rejoins).
- **Status polling is cheap.** Waiting users are served from a signed token plus an in-memory snapshot, and the server tells each client when to poll next (adaptive `retry_after` with jitter).
- **Honest outlook** (LIKELY / POSSIBLE / UNLIKELY) and live seat counts.
- **Sold-out drain:** once sold out, every queued user gets SOLD_OUT on their next poll, with no 5xx anywhere.
- Rate limits: Redis GCRA on mutating endpoints, an in-process limiter on status.

**F3 Seat holds (locks with TTL)**
- Pick specific seats or ask for best-available, 1–4 seats. All-or-nothing.
- A Redis Lua fast path sheds contention atomically. Postgres is authoritative and keeps a fencing version on every seat.
- TTL: 5 min to complete checkout, extended to 10 min once payment starts. A sweeper reclaims expired holds.
- One active hold per user per event (enforced by the DB). Release on demand.

**F4 Orders and payments**
- Order per hold → PSP intent → the customer pays (hosted MockPay checkout, or a saved test method) → confirm → asynchronous capture.
- **Idempotent end to end:** API idempotency keys (Stripe-style), deterministic PSP keys, an inbox for webhooks, a transactional outbox (River) and at most one live payment per order (enforced by the DB).
- Failure paths always end in a known state. If a hold is lost or a quota exceeded, the authorization is **voided**: the user is told "you were not charged".
- A reconciler closes every gap between MockPay's ledger and our orders.

**F5 Live views**
- Seat map with live availability: a polled bitmap with ETag.
- **Rush Control**: live queue, inventory and correctness counters. In demo mode it can launch a bot rush.
- **Results**: the headline numbers, methodology, charts and the baseline matrix.
- **Replay mode**: on the static deploy, Rush Control replays the recorded 50k run.

**F6 Proof and ops**
- `sluice rush`: a swarm of 50k concurrent simulated users. `sluice audit`: the invariant auditor.
- Naive baselines that reproduce oversells and duplicate charges, which proves the auditor can catch them.
- Chaos suite: Redis wipe, API crash, gateway latency and timeouts, Postgres pause.
- Prometheus metrics, a Grafana dashboard, pprof.
- CI: lint/test/race on every push, plus a manual rush workflow that posts its summary publicly.

## 6. Metric definitions (use these exact definitions in reports)
| Metric | Definition |
|---|---|
| **Oversold seats** | Seats with more than 1 issued ticket, plus `max(0, tickets − capacity)`. Target **0**. |
| **Duplicate charges** | Orders (MockPay `merchant_ref`) with more than 1 captured intent net of refunds. Target **0**. |
| **Orphan charges** | Captured intents whose order is not CONFIRMED and not refunded, after the system settles (≤ 2 reconciler cycles). Target **0**. |
| **Quota violations** | (event, user) pairs with tickets > `max_per_user`. Target **0**. |
| **Stuck holds** | Holds still ACTIVE/PAYMENT_PENDING more than 2 sweeper intervals past expiry. Target **0**. |
| **Inventory drift** | Seats whose Redis state ≠ Postgres state after settle. Target **0**. |
| **Concurrent users** | Simulated users that each have their own session, their own keep-alive TCP connection and a live journey, all inside the system at the same moment. Reported as `peak_in_system` (joined minus terminal). For the headline run it must be ≥ 49,000. |
| **Terminal outcome** | Each user ends as TICKET, SOLD_OUT, NO_SEAT, PAYMENT_FAILED or ABANDONED (by design). **Unresolved users must be 0.** |
| **Error rate** | (5xx + transport errors + timeouts) / all requests. 409/422/429 are expected business responses and are counted separately. Target **< 0.1%**. |
| **Checkout p95** | Client-side p95 over `POST /holds`, `POST /orders`, `POST /orders/{id}/pay` and `POST /orders/{id}/confirm` during the rush window, measured from each request's intended start time. **This is the "p95" in the resume bullet.** |
| **Queue status p95** | Client-side p95 of `GET /queue/status` measured the same way. |
| **Throughput** | Peak and mean req/s over 1 s buckets, seats sold per second, time from T0 to sold-out, time from T0 to the last user reaching a terminal state ("drain"). |

## 7. SLOs
Canonical environment: GitHub Actions `ubuntu-latest` (4 vCPU / 16 GB, free for public repos) running the full stack and the swarm on the same machine.

| SLO | Target |
|---|---|
| I1–I8 violations | 0 (hard fail) |
| Unresolved users | 0 (hard fail) |
| Error rate | < 0.1% |
| Checkout p95 | ≤ 250 ms |
| Queue status p95 | ≤ 50 ms |
| Queue join p95 | ≤ 100 ms |
| Drain time (T0 to last terminal user) | ≤ 5 min with rush TTLs |

The correctness targets are hard. The latency targets are goals: if one is missed, report the real number and the bottleneck. Never tune for a number in a way that breaks rule 5 in CLAUDE.md.

## 8. The claim (resume rules)
Template:
> **SLUICE**: rush-proof ticket booking (Go, PostgreSQL, Redis, Next.js). Load-tested **50,000 concurrent users on 500 seats: 0 oversells, 0 duplicate charges, checkout p95 N ms** at X req/s peak. Built a virtual waiting room with a fair pre-queue lottery, Lua-atomic TTL seat holds with Postgres fencing, and end-to-end idempotent payments with reconciliation.

You may use the claim only when all of the following hold:
- The numbers come from one headline CI run (`summary.json` from the `rush` workflow), with its run URL and commit SHA kept in `docs/results/RESULTS.md`.
- In that run every invariant is 0 and unresolved users is 0.
- "p95" means checkout p95 as defined above. The environment (4 vCPU CI runner, single machine) is stated on the results page.

## 9. Definition of done (project)
- [ ] P0–P8 reports exist and every acceptance check is ✅.
- [ ] The CI headline run is green and its numbers are in RESULTS.md, the results page and the README.
- [ ] The baseline matrix shows the naive runs failing and the full design passing.
- [ ] The chaos suite passes with 0 invariant violations.
- [ ] The Vercel replay site is live, the live demo works through the tunnel, and the README, INTERVIEW.md and resume bullets are written.

## 10. Stretch backlog (only after P8 is ✅)
- ALTCHA proof-of-work on join (bot friction, open source, no account).
- Window/aisle preference for best-available.
- OpenTelemetry tracing (Tempo) alongside metrics.
- A post-sold-out waitlist that notifies users when seats come back.
- Tamil/English UI toggle.

## 11. Glossary
| Term | Meaning |
|---|---|
| T0 | Sale opening instant (`sale_opens_at`) |
| Pre-queue | Users who joined before T0. Their order is fixed by lottery at T0 |
| Ticket (queue) | A user's place number in line (1-based) |
| Cursor | The highest ticket number offered admission so far |
| Offer | A time-boxed invitation to enter, issued in ticket order |
| Claim | The user accepts an offer and receives an admission token |
| Hold | A temporary, exclusive reservation of 1–4 seats with an expiry |
| Fence / version | A monotonic number that changes on every seat transition. A stale holder can't confirm |
| Reclaim | Expiring a hold and returning its seats (by the sweeper, or by a competing hold in Postgres) |
| Live payment | A payment in AUTHORIZED or CAPTURED state |
| Orphan charge | Money captured with no confirmed ticket and no refund |
| Drain | Getting every queued user to a terminal state |
| Settle | Waiting until jobs, the sweeper and the reconciler are idle before auditing |
