# SLUICE: interview notes

Every answer points to code or to a report, so any claim can be opened on screen. Headline numbers are deliberately absent: they come from the CI `rush` run and are **filled in P8b from `summary.json`**. Laptop numbers below are labelled as laptop and linked to their report.

---

## 1. The 60-second pitch
"Tatkal booking opens at 10:00 and tens of thousands of people click in the same second for a few hundred seats. Systems like that fail in three ways: they sell a seat twice, they charge someone twice, or they fall over so nobody gets anything.

SLUICE answers each one with a separate mechanism. A **virtual waiting room** takes the stampede: joining is one Redis call, and waiting is lookup-free polling. Anyone who arrives before the sale opens gets a **lottery** position, so speed gives no edge. Only a few hundred admitted users ever touch inventory. Holds go through a **Lua-atomic Redis fast path**, but **Postgres is the judge**: unique constraints and a fenced conditional update make an oversell impossible even if Redis lies or is wiped. Payments are **idempotent at every hop**, against a PSP simulator I built to misbehave on purpose.

Then I proved it. A Go swarm runs 50,000 stateful users against the real stack on a CI runner, and an independent auditor reads both databases and the PSP's ledger afterwards. It counts oversells, duplicate charges, orphan charges and unresolved users, and the run fails on anything but zero. The naive baselines show the same auditor catching 17 oversells and 19 duplicate charges when the guarantees are switched off. The headline numbers come from one public CI run, so you can open them yourself."

*(Baseline numbers: laptop run `p5b-b0`, [P5b report](reports/P5b-rush.md) §4.1. Headline: P8b.)*

## 2. The design story (how it got here)
1. **P1–P3: correctness first, on Postgres alone.** Schema constraints for I1/I2/I4 (`tickets UNIQUE(event_id, seat_no)`, the partial unique index `payments_one_live_per_order`, `event_user_quota` CHECK). Then TTL holds with fencing, then the payment path and MockPay, with its faults and ledger. Every path got concurrency tests under `-race` before anything was fast.
2. **P4: the waiting room.** Lottery pre-queue, an atomic admission tick, signed queue tokens, snapshots so a wiped Redis can be restored (R1).
3. **P5: the swarm and the auditor**, plus three naive baselines: B0 naive everything, B1 naive payments, B2 no waiting room. The baselines are there to prove the auditor actually catches bugs.
4. **P5's measurements found two real problems.** The admission leak kept the drain at 392 s, and in direct mode at 20k users Caddy ejected every replica. Each fix is in an ADR with before and after numbers (§4 below).
5. **P6: the UI**, a static export that is LIVE against the stack and REPLAY on any static host.
6. **P7: hardening.** Readiness isolation, load shedding, a chaos suite, and a CI workflow that turns the rush into a gate.

---

## 3. Hard questions

### Q1. Why is Postgres the source of truth, and why is Redis rebuildable?
- **Postgres decides; Redis only advises.** A seat is sold only by `SellSeats` inside `confirmAuthorized` (`internal/orders/confirm.go`): a conditional `UPDATE ... WHERE state='HELD' AND hold_id=$h`, whose row count must equal the number of seats, plus `tickets UNIQUE(event_id, seat_no)`. Even a buggy or stale Redis that grants a seat twice can't produce two tickets.
- **Why have Redis at all:** contention. One Lua script (`internal/redisx/scripts/hold_seats.lua`) holds seats all-or-nothing in one round trip, so 50k losers are told "taken" in microseconds without opening a Postgres transaction. The queue lives there too, because joining has to cost one call.
- **What happens if Redis dies (R1):**
  - inventory is rebuilt from Postgres seat states (`inventory.Service.Rebuild`, `internal/inventory/rebuild.go`, under a lock);
  - the queue is restored from `queue_snapshots`, written every second by `Snapshotter.Run` (`internal/queue/restore.go`);
  - `queue_join.lua` and `queue_tick.lua` refuse to run (`NOT_INIT`) until `q:init` says the counters were restored, so a wiped Redis can never reissue a ticket number or re-admit from cursor 0 (ADR 0009).
- **Proven by:** chaos case C1 (`redis-cli FLUSHALL` at T0+15 s mid-rush) passed with 0 oversold, 0 duplicate charges and 0 unresolved on the laptop ([P7a report](reports/P7a-hardening.md) §2, [ADR 0014](adr/0014-chaos-suite-details.md)).

### Q2. How do seat holds with TTL and fencing work? Why not Redlock?
- **Holding:** `inventory.Service.hold` (`internal/inventory/hold.go`) takes the Redis grant (`redisGrant`), then writes the authoritative hold row in Postgres (`commit`) with `expires_at` from PG's clock. If the PG commit fails, `compensate` undoes the Redis grant.
- **Expiry (I5):** the sweeper (`SweepOnce` → `reclaimExpired`, `internal/inventory/sweeper.go`) reclaims expired holds in PG first, then mirrors the change to Redis, within 2 intervals (≤ 2 s).
- **Fencing (I6):** the hold id is the fencing token. A confirm sells seats only `WHERE hold_id = $h`, and if fewer rows than seats change it returns `errHoldLost`. `failAuthorized` then fails the order, and the authorization is **voided**, so the user sees "you were not charged".
- **Not Redlock:** Redlock is a distributed lock, and a lock can be lost to a GC pause or clock drift while the holder still believes it owns it. That's the classic fencing argument. SLUICE needs no lock at all: the storage layer (Postgres) checks the token on every write, so a stale holder physically can't commit.

### Q3. How are payments idempotent, and what survives a crash?
Four layers, each in its own place:
1. **Client → API: Idempotency-Key.** `Idempotency.claim` (`internal/httpx/idempotency.go`) inserts `(user, key, request_hash, locked_until)`.
   - A completed key replays the stored response, flagged with a header.
   - The same key on a different body gets `IDEMPOTENCY_KEY_REUSED`.
   - A concurrent duplicate gets 409 `REQUEST_IN_PROGRESS` with `Retry-After`.
   - **Crash recovery:** if the process died mid-request, the key stays STARTED until `locked_until`, then the next retry **reclaims** it and re-executes (`ReclaimIdempotencyKey`). `finish` drops the key on a 5xx, so a failure can be retried and only a definite answer is cached.
2. **API → PSP: deterministic keys.** `payments.CreateKey(orderID, attempt)`, `ConfirmKey(intent)` and `CaptureKey(intent)` (`internal/payments/wire.go`). A retry after a crash or a timeout sends the **same** key, so the PSP returns the same intent and the same result.
3. **Database backstop.** The partial unique index `payments_one_live_per_order` makes a second live payment for an order a constraint violation. `confirmAuthorized` then lands in its `duplicate` branch and voids it.
4. **Webhooks and the reconciler.** See Q5 and Q4.

### Q4. The PSP times out *after* it committed. What happens?
The API doesn't know whether the card was charged. It retries `Confirm` with the same `confirm:<intent>` key. MockPay's idempotency layer (`Server.idempotent`, `internal/mockpay/`) returns the stored result of the first commit instead of authorizing again. If the client gave up entirely, the order stays PENDING and three things pull it to the truth: the client's `/confirm` poll, the webhook, and the reconciler (`Service.Reconcile`, `internal/orders/reconcile.go`, every 30 s), which lists intents at the PSP. MockPay injects this fault on demand (`pm_timeout`, "Simulate timeout" on the checkout page) and at a random rate (`MOCKPAY_TIMEOUT_AFTER_COMMIT_RATE=0.02` in the rush profile).

### Q5. Duplicated and reordered webhooks?
- **Duplicates:** every webhook lands in an inbox keyed `(provider, event_id)`, and `ProcessWebhook` (`internal/orders/process.go`) returns at once if `processed_at` is set. A duplicate is a no-op.
- **Reordering:** state transitions are monotonic (`internal/fsm`), so a "captured" that arrives before "authorized" can't regress the state. The webhook path converges through the same `confirmAuthorized` code as the client's confirm, so every order of arrival ends in the same state.
- **Faults in every rush:** the rush profile duplicates 10% and reorders 5% of webhooks, with up to 2 s of delay (`deploy/env/rush.env`).

### Q6. Late payment vs hold expiry: who wins?
Postgres does. The sweeper's reclaim and the confirm transaction both lock the hold row. If the sweeper commits first, the confirm finds the hold not live (`errHoldLost`), so the order fails and the authorization is recorded **and voided**. If the order already FAILED or EXPIRED, the authorization takes the `late` branch: it's recorded so `charged` tells the truth, then voided. Either way no seat is sold twice and no money is kept (I3), and the auditor's `orphan_charges` check proves it after every run.

### Q7. Why a queue lottery, and what makes admission fair?
- **Lottery (pre-queue):**
  - Joining before T0 gets a uniform random score with 53 random bits (`randScore`, `internal/queue/queue.go`) in a sorted set.
  - At T0 the pre-queue size is **frozen** inside a script (`queue_join.lua` or `queue_tick.lua`, whichever runs first), and positions are ranks in score order.
  - Because no pre-queue entry can be added after the freeze, there are no duplicates and no gaps.
  - The argument: arriving at 09:59:10 or 09:59:59 is the same, so bots gain nothing from speed and nobody needs to refresh at exactly 10:00:00.
- **After T0:** strict FIFO (`presize + INCR tail`).
- **Fair admission (I7):** `queue_tick.lua` runs as one atomic script per event every 100 ms.
  - It offers tickets strictly in order from a monotonic cursor, and checks the sold-out flag first, so nobody is admitted after sell-out.
  - It bounds admitted-and-seeking users to `target = min(QUEUE_MAX_ACTIVE, max(QUEUE_MIN_ADMIT, ceil(avail × 1.5)))`.
  - Several workers can run it at once without over-admitting.
- **Checked from outside:** the auditor's `admitted_after_soldout` check counts violations of the sold-out rule from swarm records.

### Q8. What is the admission idle lease, and why does it exist?
Admission is a **sliding lease**, not a fixed window.
- **Claiming:** `queue_claim.lua` writes the lease (`now + ADMISSION_IDLE_TTL`) and a hard cap (`claim + ADMISSION_TTL`).
- **Renewing:** every request with the admission token renews it through `Lease.Touch` (`internal/queue/lease.go`, one `ZADD q:active XX GT`). `XX` never resurrects an ended admission, and `GT` never shortens one.
- **Spending:** `POST /holds` and `POST /orders` use `Keyring.RequireLive` (`internal/auth/http.go`), which refuses a lapsed lease with 403 `ADMISSION_EXPIRED`.
- **Releasing:** finishing the checkout, failing it or leaving releases the admission inside the same Lua script that moves the seats.
- **Why:** the admission leak (§4.3). Users who were done shopping still counted as "seeking", so the controller saw the shop as full and stopped admitting.

### Q9. How do load shedding and readiness isolation work?
- **Readiness isolation:** `/readyz` uses its own 2-connection Postgres pool and Redis client. It serves a cached state and refreshes it in the background (`Readiness`, `internal/httpx/health.go`), so a busy replica can't look dead to Caddy. On SIGTERM it answers 503 for 3 s before draining.
- **Shedding:** `httpx.Shed` (`internal/httpx/shed.go`) admits at most `MAX_IN_FLIGHT=96` **write** requests per replica. The next one waits up to 50 ms, then gets 503 `OVERLOADED` with `Retry-After: 1`. Reads and probes are never shed, because the status poll is the waiting room's heartbeat.
- **Caddy:** `health_fails 3` and `max_fails 100` in 5 s tolerate a CPU-starved box.
- Why each number was picked, with measurements: [ADR 0013](adr/0013-readiness-and-load-shedding.md), and §4.4 below.

### Q10. How was p95 measured? What about coordinated omission?
- **"Checkout p95"** (SPEC §5) is the client-side p95 over `POST /holds`, `/orders`, `/orders/{id}/pay` and `/orders/{id}/confirm` during the rush window.
- **Coordinated omission:** every request is recorded against its **intended** start time, not the time it actually went out (`Recorder.Record(rt, intended, end, ...)`, `internal/rush/metrics.go`; `intended` is threaded through `user.send` in `internal/rush/user.go`). If the generator falls behind schedule, that delay counts as latency instead of silently disappearing.
- **Recording:** HdrHistogram per route, from 1 µs to 60 s at 3 significant digits.
- **Self-check:** the swarm measures its own schedule lag (`RecordLag`). A run with lag p99 > 100 ms is flagged **generator-bound** and can't be used for SLO claims (LOADTEST §3.4). That rule is why the 20k direct runs are reported as "the cascade is gone", not as latency numbers.

### Q11. Why a custom swarm *and* k6?
A k6 VU costs megabytes, so 50,000 stateful users, each with its own session, its own keep-alive connection, its own idempotency keys and a full journey, don't fit on one machine. Goroutines do. k6 (`grafana/k6` from Docker) cross-checks the numbers with an industry tool at lower concurrency: K1 is a queue-join spike and K2 is checkout capacity (`loadtest/k6/`, LOADTEST §5). Two independent instruments that agree are worth more than one I wrote myself.

### Q12. How does the auditor prove 0 oversells and 0 duplicate charges?
It doesn't trust the system under test. `sluice audit` (`internal/audit/`):
1. **Settles first.** It triggers reconcile and waits until there are no pending River jobs, no holds past expiry, no undelivered webhooks and no intents young enough to still be orphan candidates. Then it runs reconcile again (ADR 0011).
2. **Oversold** (`auditor.oversold`): it counts seats with more than one **distinct** order in `tickets`, plus distinct (seat, order) pairs beyond capacity, reading Postgres directly.
3. **Duplicate charges** (`auditor.money`): it reads **MockPay's own ledger** (`/v1/ledger/summary`), a different database from SLUICE's, and counts orders with more than one captured-not-refunded intent. **Orphan charges** are captured money whose order isn't CONFIRMED.
4. **The rest:** quota, stuck holds, Redis-vs-PG drift, ticket/order consistency, admitted-after-sold-out and unresolved users.
5. **Exit code:** 0 pass, 1 fail, 2 settle timeout. The CI gate (`loadtest/ci/verdict.py`) fails the workflow on anything but 0.

Its sensitivity is tested by the naive baselines. On the laptop, B0 reported **17 oversold and 19 duplicate charges**, B1 **27 duplicate charges** with 0 oversold, and the full design 0 everywhere (`docs/results/p5b/<run>/summary.json`, [P5b report](reports/P5b-rush.md) §4.1). An auditor that has never failed proves nothing.

### Q13. Why is CI the canonical environment, and what are its caveats?
The laptop shares one Docker VM between the swarm, Caddy, 3 API replicas, Postgres, Redis, Prometheus and Grafana, plus the IDE. Latency there is noise: in P7b three interleaved SMOKE runs of the same code spread 1.6× (§4.2). A `workflow_dispatch` run on a fresh `ubuntu-latest` runner is reproducible by anyone, publishes its artifact, and pins a commit SHA. The caveats go on the results page: it's one shared-CPU machine, the generator competes with the system, and MockPay stands in for a real PSP. That proves correctness under real concurrency. It isn't a capacity plan.

### Q14. What breaks first at 10×?
On one box, Postgres CPU. In P7a the pool metrics showed all 3 × 24 request connections checked out, with ~2.6 cores of Postgres on a shared laptop. More connections would add contention, not throughput ([ADR 0013](adr/0013-readiness-and-load-shedding.md)). The path forward is already prepared (ARCHITECTURE §8):
- Redis Cluster (every event key shares the hash tag `{e:<id>}`);
- read replicas or caching for availability;
- more admission workers (the tick is atomic);
- a CDN for the static queue page;
- River queues partitioned per job kind.

### Q15. What would change for real payments?
- **Integration:** a real PSP's webhook signature scheme, a real hosted checkout or SDK, and its own idempotency window, which usually expires. The deterministic keys stay, but the reconciler would become the primary truth for anything older than that window.
- **Money flows:** real refund timing and settlement reports. Authorizations expire, so captures need a deadline.
- **Compliance:** PCI scope stays at zero by keeping card entry on the PSP's page, which is why the hosted-checkout flow exists.
- **Operations:** alerting on `orphan_charges` greater than zero in production, not only in the auditor.

---

## 4. Honest misses, and what I did about each

### 4.1 Lighthouse mobile Performance on the laptop
- **Miss:** Performance 62 / 62 / 71 on `/queue`, `/book` and `/control` against a ≥ 90 target. Accessibility was 100 and Best Practices 96 (Lighthouse 13.6.0, simulated 4× CPU throttling, [P6b report](reports/P6b-frontend.md) §4.2).
- **Cause, from the audit JSON:**
  - TBT is mostly Next's framework chunk (React DOM plus the client runtime), which app code can't shrink.
  - LCP is render-delayed. The routes are keyed by query string (`?e=`), so the static export can only prerender a shell.
  - On `/book`, the seat map's first mount is a 1,481 ms long task.
- **What I did:** reported it with both runs, not the best of several. I proposed three fixes: prerender per-event paths, defer the seat-grid mount, and ship a static `mode.json` so the probe never 404s. Fix (a) changes public URLs, so it was left as the owner's scope call. The JS budget is enforced separately ([ADR 0012](adr/0012-js-budget-framework-plus-app.md)).

### 4.2 Laptop latency noise
- **Miss:** after the P7a hardening, SMOKE checkout p95 read 823.8–1925.1 ms against 329.7 ms in P5b. P7b's median of 3 was 1232.9 ms against a 250 ms SLO ([P7b report](reports/P7b-hardening.md) §4).
- **What I did:** instead of tuning blind, I ran the **whole pre-P7a tree** as a control, interleaved with HEAD runs. If reverting everything isn't faster, no single revert can be. The control was no faster (842.8 / 1300.5 ms) and GC was ruled out. The finding is that the laptop is CPU-contended, with Caddy at 2.3–4.4 cores as the open lead, so the SLO question goes to CI. No knob was turned just to hit a number.

### 4.3 The admission leak
- **Miss:** at 2,000 users the drain (last user resolved) took 399.2 s against a 300 s SLO, while the sale itself took seconds ([P5a report](reports/P5a-rush.md)).
- **Cause:** the controller counts `seeking = active − holding`. Buyers whose hold became a sale, and users who gave up, still sat in `active` until `ADMISSION_TTL`, so `target − seeking` went negative and freed seats waited minutes for anyone to be offered them.
- **What I did, in two measured rounds:**
  1. Release the admission inside the same Lua script that consumes or releases the hold. That took stale admissions from 718 to 364, but the drain only moved to 392.1 s, which proved the first fix was incomplete.
  2. The sliding idle lease (Q8), plus clients that leave on giving up. That took stale admissions from 364 to 7 and the drain to **216.5 s**.
- **Lesson:** measure each fix separately. Recorded in ARCHITECTURE §5 and the ADR 0010 addenda.

### 4.4 The 20k cascade
- **Miss:** in direct mode (B2, no waiting room) at 20k users, Caddy answered 49,105 requests with `no upstreams available` and the error rate hit 57.28% ([P5b report](reports/P5b-rush.md) §4.4).
- **Cause:** `/readyz` pinged Postgres through the **same** exhausted request pool, missed Caddy's 2 s health timeout, and every replica was ejected.
- **What I did** ([ADR 0013](adr/0013-readiness-and-load-shedding.md)), in steps, each measured with `task rush:b2 USERS=20000`: a dedicated health pool with a cached and background-refreshed state, write-only load shedding at `MAX_IN_FLIGHT=96`, tolerant Caddy health settings, and compensating the Redis grant when a hold's commit is abandoned. That last fix closed an inventory drift of 6 that the first attempt exposed. `no upstreams available` fell to **0**.
- **Still open, and said so:** those 20k runs are generator-bound, and B2 at 20k still ends with 7 unresolved users. Correctness held throughout (0 oversold, 0 duplicate charges). This is exactly the overload the waiting room exists to prevent, which is the strongest argument for the waiting room.

### 4.5 Smaller things found on the way
- **Postgres healthcheck race:** `pg_isready` over the Unix socket answered from the entrypoint's temporary server, so migrate hit `connection refused`. The fix was to check over TCP ([P7b report](reports/P7b-hardening.md) §1).
- **`/pay/metrics` was public** through the front door. MockPay's Prometheus metrics were reachable on :8000 and so would have been reachable through the demo tunnel. Caddy now answers 404, and Prometheus still scrapes in-network ([ADR 0016](adr/0016-live-demo-path.md), [P8a report](reports/P8a-docs-demo.md)).
