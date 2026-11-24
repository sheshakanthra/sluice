# ADR 0011: Naive baselines, the auditor's settle and its counting rules

- **Status:** accepted
- **Date:** 2026-11-24
- **Phase:** P5 (session P5b)

## Context
LOADTEST §4 asks for two deliberately broken comparison modes. B0 (`BASELINE=naive`) uses check-then-insert seat sales plus naive payments. B1 (`BASELINE=holds_only`) keeps the correct holds but uses naive payments. LOADTEST §6 defines the auditor in outline. Building them raised details the specs leave open:

1. **Where B0 writes.** It has to be able to sell a seat twice, so it can't touch `seats` or `tickets`, whose constraints (I1) are the point of the real system.
2. **The reconciler would hide B1's bug.** The worker's reconciler (ADR 0007) refunds any captured intent whose order isn't confirmed. It would refund B1's duplicate charges before the audit, so B1 would show duplicates = 0. That number would be honest about the reconciler and dishonest about the payment code.
3. **What counts as oversold.** In B0 a single order's own retry can insert the same seat twice. That is one buyer, not an oversell.
4. **Settle.** LOADTEST §6 says to wait for "no River jobs" and "no undelivered webhooks". But River's periodic jobs (`reconcile`, `audit_cache`) are always scheduled, and a webhook that ran out of attempts is never delivered.
5. **`admitted_after_soldout`.** The swarm sees sold-out on `/stats`, which it polls once a second, so an admission granted in the same second can look "after". A request sent before the swarm saw sold-out can also be answered after it.
6. **The auditor's own exit codes.** §6 defines 0, 1 and 2, but the auditor can also fail to run at all.

## Decision
- **B0 has its own tables.** Migration `0003_naive_baseline.sql` adds `naive_orders` and `naive_sales` with **no unique constraint**. They are used only when `BASELINE=naive`.
  - Hold: read the sold seats, pick free ones, insert a `naive_orders` row. Nothing is reserved.
  - `/pay`: check the seats are free, insert `naive_sales`, then charge with a **random** PSP key and `confirm=true` on every click. There's no idempotency.
  - The real tables stay untouched, and `/stats` can't see `naive_sales`.
- **B1 uses the real hold and order path.** Only `/pay` is replaced: a fresh random-key `confirm=true` intent per click, relinked onto the payment row, then `ConfirmOrder`.
- **Baseline modes skip only the PSP-intent pass.** When `BASELINE` is not `off`, `orders.Options.NoPSPReconcile` turns off that one pass of the reconciler. Every other job still runs, including hold expiry, captures and voids.
  - The baselines model a team that never built reconciliation.
  - The audit still reports **orphan_charges**, so the money left behind is visible.
- **Baselines are locked twice.** `config.Load` refuses `BASELINE` outside `APP_ENV` rush/test. `app.newBaseline` refuses it again, and that lock has its own test. Baseline routes are mounted behind `RequireLive` but **without** the idempotency middleware (API §1.2 does not apply to them).
- **Oversold** is counted as:
  - seats with more than one **distinct** order (`order_id`, or `order_ref` in naive mode), plus
  - `max(0, distinct (seat, order) pairs − capacity)`.
- **Duplicate charges** come from MockPay's ledger, grouped by `merchant_ref` (the order id):
  - The count is the refs with `captured − refunded > 1`.
  - `detail.extra_charges` sums the excess.
  - **Orphan charges** are the refs with net captured money whose order isn't CONFIRMED in `orders` or `naive_orders`.
- **Settle.**
  - **Round 0:** call `POST /admin/reconcile`. Wait for quiet, which means all four of these hold:
    - no River jobs in `available/running/retryable`, except the periodic kinds `reconcile` and `audit_cache`;
    - no holds ACTIVE/PAYMENT_PENDING past `expires_at`;
    - no MockPay deliveries with `delivered_at IS NULL AND attempt < 6`;
    - no AUTHORIZED/CAPTURED intent updated within the orphan age (35 s, the reconciler's 30 s plus a margin), because the reconciler won't touch them yet.
  - In round 0 the reconcile repeats every 30 polls.
  - **Round 1:** reconcile once more and wait for quiet again.
  - The default timeout is 120 s, and the checks run even after a timeout.
  - Exit codes:

    | Exit | Meaning |
    |---|---|
    | 0 | every invariant passed |
    | 1 | an invariant failed |
    | 2 | settle timed out |
    | **3** | the auditor itself could not run: bad flags, or a datastore it couldn't reach |

- **`admitted_after_soldout`.**
  - The swarm records the time each ADMITTED answer's **request was sent**.
  - It counts the ones sent more than **1 s** after the first `/stats` row that showed sold out (a 1-second grace, the polling interval).
  - Direct mode (B0/B1/B2) has no admissions, so the value is `null` (n/a).
- **Checks that don't apply report `null`**, not 0. In naive mode that means `stuck_holds` and `inventory_drift`. `inventory_drift` is also `null` when inventory isn't loaded in Redis.
- **Results.** The audit's verdict is merged into the run's `summary.json` (`audit` block, plus a `records` block from the swarm) and into `report.md`. The full output goes to `audit.json`.

## Consequences
- B0 and B1 show the bugs the real system prevents, and the real system's code and constraints aren't weakened to do it. B0 needs one extra migration that the real system never reads.
- B0's `seats_sold` and `sold_out_s` in `summary.json` come from `/stats`, which can't see `naive_sales`. For B0, the auditor's counts are the source, and the P5b report says so.
- B1's duplicate charges stay in the ledger and are counted. Running the real reconciler on B1 would hide them; that's what B2 (the correct system) is for.
- The settle rules mean a stack with a dead webhook or a stuck periodic job can still settle, and the stuck state shows up in the checks rather than as a timeout.
- Docs updated in the same phase:
  - LOADTEST §3.5 (the `records` block and audit extras) and §6 (settle, counting, exit 3);
  - DATA-MODEL (the naive tables);
  - API §1.2 (baselines skip idempotency);
  - STACK (the `audit` compose service and the scenario env overlays).
