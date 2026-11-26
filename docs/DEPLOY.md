# DEPLOY: shipping SLUICE for ₹0

Hugging Face Docker Spaces, Render, Railway and Fly need a paid plan, a card, or managed DB accounts that break rule 2 (as of Nov 2026, Docker/Gradio Spaces need a paid plan to create). So SLUICE ships as two surfaces:

| Surface | What | Always on? | Cost |
|---|---|---|---|
| **A. Vercel replay site** | The static web app in **REPLAY** mode: landing, Rush Control replaying the recorded 50k CI run, the results page, how-it-works, recorded booking video | Yes | ₹0 (Hobby) |
| **B. Live demo** | The full stack on the laptop (`task demo`), exposed through a **Cloudflare Quick Tunnel** (no account), for interviews and live walkthroughs: real booking plus "Start a demo rush" with bots | On demand | ₹0 |

(Optional later: if he ever has a free Linux VM, `task stack:up ENV=demo` plus Caddy's automatic HTTPS on a domain gives an always-on live version. Not part of v1.)

## A. Vercel replay site
1. Build: `pnpm -C web build`, with Next.js `output: 'export'`, `images.unoptimized: true` and `trailingSlash: true`. The output goes to `web/out/`.
2. Replay data is committed in `web/public/results/latest/` (`summary.json`, `timeseries.json`, `baselines.json`, `booking.webm`/`.mp4`). It's copied from the CI headline run in P8.
3. Vercel setup (Sheshakanth does this in the Vercel dashboard; the click-by-click list is in `docs/reports/P8a-docs-demo.md` §5):
   1. Import the GitHub repo.
   2. Set **Root Directory** to `web` (framework preset Next.js; `output: 'export'` makes Vercel serve the static `out/`). Install and build commands stay at their defaults: pnpm comes from `web/pnpm-lock.yaml` and `packageManager`, and the build runs `next build`.
   3. No environment variables. In particular, **don't set `NEXT_PUBLIC_API_BASE_URL`**: with it unset the app calls same-origin `/api/v1/healthz`, which 404s on Vercel, so the app detects **REPLAY**.
   4. Project name `sluice`, giving `https://sluice.vercel.app` (or the closest free name).
   5. Until P8b copies the CI headline run, the replay data is the **P5b laptop SMOKE run** (`p5b-smoke`, 5,000 users, `source.kind = "local"`). Rush Control labels it "Replay of local run p5b-smoke ..." and Results shows the runner as "Local laptop (Docker Desktop)".
4. Vercel redeploys whenever he pushes. Claude Code never pushes.

## B. Live demo (laptop + tunnel)
```bash
task demo      # = demo.env with fresh secrets (once) + web static build + stack:up ENV=demo (migrate, seed) → prints the URLs
task tunnel    # = cloudflare/cloudflared:2026.11.2 on the compose network, --url http://caddy:8000 → prints https://<random>.trycloudflare.com
task stack:down ENV=demo   # stop (volumes kept)
```
- No `cloudflared` install: the tunnel runs from the pinned Docker image on the compose network (ADR 0016), so the only thing it can expose is `caddy:8000`. Ctrl+C ends it.
- Share the printed URL. Everything is same-origin through Caddy: web at `/`, API at `/api`, MockPay checkout at `/pay`.
- Quick Tunnel limits:
  - 200 in-flight requests, with 429 beyond that. Bots run **inside** the worker, so they never go through the tunnel.
  - **No SSE.** We only poll, so this doesn't matter.
  - The URL changes every run.
  - No uptime guarantee: keep the laptop awake and on power.
- **Demo mode** (`deploy/env/demo.env`, copied from `demo.env.example`):
  - `DEMO_MODE=true`, `TEST_ENDPOINTS_ENABLED=false`, `RATE_LIMITS_ENABLED=true`
  - a strong random `ADMIN_TOKEN`, `TOKEN_KEYS`, `MOCKPAY_API_KEY` and `MOCKPAY_WEBHOOK_SECRET`: `task demo` generates them into the gitignored `demo.env` once (`deploy/scripts/demo-env.mjs`); delete the file to rotate. Left as placeholders, the config refuses to boot
  - production TTLs (5m / 10m)
  - `MOCKPAY_PUBLIC_URL=/pay` and `PUBLIC_APP_URL=/` (same-origin paths; the tunnel host is random, ADR 0016)
  - no random MockPay declines or late answers (`MOCKPAY_FAIL_RATE=0`, `MOCKPAY_TIMEOUT_AFTER_COMMIT_RATE=0`); the visitor triggers faults on the TEST MODE checkout. Duplicate and reordered webhooks stay on
- **Demo rush (not built yet: P8b, ADR 0016).** Until it lands, `/healthz` reports `mode: "live"` in the demo profile too, so the web hides the button. Planned: `POST /api/v1/demo/rush {bots ≤ 3000, opens_in_s 15–60}` resets `tatkal-chn-sbc`, sets T0, and starts a River job in the worker that runs the swarm engine in-process with that many bots. The visitor joins during the countdown and competes fairly through the lottery. There's a global 2-minute cooldown, the bot count is capped at 3,000, and the job aborts after 10 minutes. Rush Control shows it live, including bot-measured p95 from `sl:{e:<eid>}:demo`.
- The quiet event `quiet-tuesday` is always bookable for a calm walkthrough.

## Security checklist before sharing a live URL
- [ ] `ADMIN_TOKEN` and `TOKEN_KEYS` are random, ≥ 32 bytes, and not in git.
- [ ] `TEST_ENDPOINTS_ENABLED=false`, `BASELINE=off`, `QUEUE_ENFORCED=true`.
- [ ] CORS is same-origin only, and the MockPay checkout shows the TEST MODE banner.
- [ ] Grafana, Prometheus, pprof and the Toxiproxy ports are **not** exposed through the tunnel (only `caddy:8000` is tunnelled, and Caddy answers 404 for `/pay/metrics`). Note that the infra ports are still published on all host interfaces, so they're reachable from the LAN (not the tunnel).
- [ ] The demo rush cooldown and bot cap are active (P8b), and per-IP limits are on.
- [ ] No real names, brands or PII anywhere. All events are fictional.

## Release checklist (P8)
1. CI headline run is green. Download the artifact and copy the curated files into `docs/results/<run_id>/` and `web/public/results/latest/`.
2. README results table and the `/results` page show the same numbers as `summary.json` (a script verifies this: `task verify:results`).
3. `pnpm -C web build` succeeds and REPLAY works locally (`npx serve web/out`).
4. Sheshakanth pushes, and Vercel deploys.
5. `task demo` + `task tunnel` work end to end (book on `quiet-tuesday`, then run a demo rush on `tatkal-chn-sbc`).
6. Sheshakanth tags `v1.0.0` (he runs the command; the phase report provides it).
