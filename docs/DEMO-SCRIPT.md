# SLUICE: 3-minute demo script

The labels in quotes are the real on-screen text (`web/app`, `web/components`, `internal/mockpay/checkout.go`) as of P8a. The flow below was run end to end through a Cloudflare Quick Tunnel by Playwright E1/E2 in P8a ([report](reports/P8a-docs-demo.md) §4).

## Before the call (5 minutes, laptop on power, sleep off)
```bash
task demo      # first run builds images + web; prints the local URLs
task tunnel    # leave running; copy the https://<random>.trycloudflare.com line
```
Open three tabs:
1. **Replay:** the Vercel URL, or locally `task web:serve` → http://localhost:4173/control/ (no backend, REPLAY mode).
2. **Live:** the `trycloudflare.com` URL (send it to the interviewer so they can book too).
3. **Grafana:** http://localhost:3001/d/sluice (local only; never shared through the tunnel).

Until P8b the replay is the **laptop** run `p5b-smoke` (5,000 users), and the page says "Replay of local run p5b-smoke". Say so out loud: the headline 50k CI run replaces it in P8b. The "Start a demo rush" button doesn't exist yet (P8b), so this script doesn't use it.

---

## 0:00–0:20 · The problem (live tab, landing page)
**Show:** the hero, "Tatkal opens at 10:00:00 IST" and "Fifty thousand people. Five hundred seats. One honest queue."
**Say:** "Tatkal opens at ten sharp. Tens of thousands of people click in the same second for a few hundred seats. Systems like this fail three ways: a seat sold twice, a card charged twice, or the site falls over. SLUICE is built so the first two can't happen and the third is bounded. And it's measured, not claimed."

## 0:20–1:05 · The rush, replayed (replay tab, Rush Control)
**Show:** `/control/`. Press play on the replay timeline, switch to **10×**, and click the stops "T0, first sale" and then "Sold out, end".
**Point at:** the tiles "Waiting in line", "Admitted per second", "Shopping now", "Seats on hold", "Seats sold", then the correctness panel: "Oversold seats", "Duplicate charges", "Orphan charges".
**Say:** "This is a recorded run. Thousands join before T0 and get a lottery position, so clicking fastest gives no edge. The admission controller lets in only as many shoppers as there are seats, times one and a half, every hundred milliseconds. Watch 'Shopping now': it stays small while 'Waiting in line' is huge. That's the whole trick: inventory never sees the stampede. And the correctness panel comes from an independent auditor, not from the app's own counters."

## 1:05–1:30 · Proof (replay tab, Results)
**Show:** `/results/`: the headline numbers, then "Take a guarantee away and it breaks".
**Say:** "Same auditor, three broken versions. Remove the safe holds and idempotent payments and it finds seats sold twice and people charged twice. Remove only the payment guarantees and it finds double charges. The full design is zero on every line. An auditor that has never failed proves nothing, so I built the failures first." *(Laptop baseline numbers until P8b; the page shows the runner as "Local laptop (Docker Desktop)".)*

## 1:30–2:30 · A real booking, live (live tab, through the tunnel)
1. Scroll to "Pick a train" and click **Quiet Tuesday** (always on sale). Click **"Join the line"**.
   **Say:** "This is the real stack on my laptop: three API replicas, a worker, Postgres, Redis and a payment simulator, behind one front door."
2. The waiting room admits you almost at once. Click **"Pick seats now"** (or let the 3-2-1 move you).
3. On the seat map, click **"Hold best 2 seats"**.
   **Say:** "That hold is one Lua script in Redis, all-or-nothing, but Postgres is the judge. The seats are only sold if they're still held by this exact hold id when payment confirms. That's fencing: a late payment from an expired hold can't sell anything."
4. On checkout, click **"Pay ₹… on MockPay"**. The MockPay page shows the **TEST MODE** banner and the buttons "Pay ₹…", "Decline (simulate a refused card)" and "Simulate timeout (authorize, then answer late)".
   **Say:** "MockPay is a simulator I wrote so I could break payments on purpose: timeouts after it has already charged, duplicate and out-of-order webhooks. Every hop has an idempotency key, so a retry can never charge twice."
5. Click **"Pay ₹…"**. You land on the order page with two ticket codes (`SLC-XXXX-XXXX`) and a "charged" line.
   **Say:** "Two seats and one charge. And if I'd clicked Decline, the page would say I wasn't charged, and I could try again on the same hold."

*(If there's time, run the decline path: hold again, then "Decline (simulate a refused card)". The order page says nothing was charged and offers "Try paying again" on the same hold.)*

## 2:30–2:50 · Under the hood (Grafana tab)
**Show:** the SLUICE dashboard, the "Traffic" row with "RPS by route (api, all replicas)", and the request you just made.
**Say:** "Everything is instrumented: per-route latency, load shedding, pool waits, admission rate. When the 20k-user test without the waiting room took the site down, these graphs showed the health check queueing behind the database pool. That became ADR 13."

## 2:50–3:00 · Close
**Say:** "Postgres is the source of truth, Redis is rebuildable, and the waiting room keeps the load off inventory. The numbers come from a public CI run with 50,000 simulated users, gated on zero oversells and zero duplicate charges. The code, the ADRs and the reports with every miss are in the repo."

---

## If something goes wrong live
| Symptom | Do |
|---|---|
| Tunnel URL shows a Cloudflare error page right after `task tunnel` | cloudflared prints "it may take some time to be reachable". Wait until the log shows "Registered tunnel connection", then reload. |
| Tunnel died | Ctrl+C, `task tunnel` again: **the URL changes**, so resend it. |
| "This booking belongs to another browser" | Bookings are tied to the browser that made them. Use the same tab. |
| Quiet Tuesday shows few seats left | Every E2E and demo run books seats. `task stack:down ENV=demo -- -v` then `task demo` gives a fresh database (the secrets in `demo.env` are kept). |
| Anything else | Switch to the replay tab: it has no backend to fail. |

After the call: Ctrl+C the tunnel, then `task stack:down ENV=demo`.
