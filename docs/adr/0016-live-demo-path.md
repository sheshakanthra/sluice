# ADR 0016: Live demo path: same-origin public URLs, cloudflared in Docker, demo profile, demo rush deferred

- **Status:** accepted
- **Date:** 2026-11-26
- **Phase:** P8 (session P8a)

## Context
DEPLOY.md B describes the live demo as `task demo` + `task tunnel`. At the start of P8a both tasks were `TODO(P8)` stubs and `deploy/env/demo.env.example` didn't exist. Verifying the flow end to end turned up these gaps:

1. **The public URLs could not be same-origin.** DEPLOY.md says `MOCKPAY_PUBLIC_URL=/pay`, but `internal/config` accepted only absolute http(s) URLs with a host, so `/pay` failed to load. `PUBLIC_APP_URL` had the same rule. Behind a Cloudflare Quick Tunnel the public host is random and changes every run, so any absolute URL would send visitors to `localhost` after checkout. MockPay's `returnURL` (`internal/mockpay/checkout.go`) also dropped any return URL without an http(s) scheme, so even a relative merchant URL would have lost the "Return to SLUICE" link and the 303 redirect.
2. **cloudflared was not installed** on the laptop. DEPLOY.md suggested `winget install`.
3. **`/pay/metrics` was public.** Caddy's `handle_path /pay/*` proxies all of MockPay, including its Prometheus `/metrics`. That broke the DEPLOY.md security checklist ("Prometheus ... not exposed through the tunnel") on the front door, and so on the tunnel too. This predates P8.
4. **The demo rush (`POST /api/v1/demo/rush`) is not implemented.** Neither is `/healthz` reporting `mode: "demo"`.

## Decision
1. **Public URLs may be same-origin paths.** `MOCKPAY_PUBLIC_URL` and `PUBLIC_APP_URL` are read by `reader.publicURL`, which accepts either an absolute http(s) URL with a host or an absolute path starting with a single `/`. Protocol-relative `//host` and `/\host` are refused, because browsers would treat them as another origin. MockPay's `returnURL` applies the same rule. The demo profile uses `MOCKPAY_PUBLIC_URL=/pay` and `PUBLIC_APP_URL=/`, so `checkout_url` is `/pay/checkout/<id>` and the return redirect is `/order?o=...`, both resolved by the browser against whatever host the tunnel has.
2. **cloudflared runs from its pinned image (`cloudflare/cloudflared:2026.11.2`) on the compose network** with `--url http://caddy:8000`. There's no host install and no account. The container can reach only the compose network, and the one URL it serves is the front door, so Grafana (:3001), Prometheus (:9090), Postgres and Redis can't be behind the tunnel by construction. The visitor's IP arrives in `X-Forwarded-For` from Cloudflare's edge. cloudflared and Caddy are private-range peers that the existing `TRUSTED_PROXIES` rule already trusts, so per-IP rate limits apply to the real visitor IP (verified in the P8a report).
3. **Caddy answers 404 for `/pay/metrics*`.** Prometheus scrapes `mockpay:8090/metrics` directly on the compose network, which doesn't change.
4. **Demo profile `deploy/env/demo.env.example`:**
   - `APP_ENV=demo`, so the config refuses the dev keys and every unsafe mode. `TEST_ENDPOINTS_ENABLED=false`, `QUEUE_ENFORCED=true`, `BASELINE=off`, `RATE_LIMITS_ENABLED=true`, and `PPROF_ADDR` is empty.
   - Production TTLs (5m / 10m / 10m / 2m idle).
   - **MockPay random faults are off** (`MOCKPAY_FAIL_RATE=0`, `MOCKPAY_TIMEOUT_AFTER_COMMIT_RATE=0`), so a visitor's booking is predictable. The faults stay one click away on the TEST MODE checkout ("Decline", "Simulate timeout"). Duplicate (10%) and reordered (5%) webhooks stay on: the visitor can't see them, and every demo booking exercises the dedupe path.
   - The secrets are the placeholder `generate-with-task-demo`. `deploy/scripts/demo-env.mjs`, the first step of `task demo`, writes the gitignored `deploy/env/demo.env` once, with 32 random bytes per secret (TOKEN_KEYS, ADMIN_TOKEN, MOCKPAY_API_KEY, MOCKPAY_WEBHOOK_SECRET). Later runs keep that file, so tokens already issued stay valid; deleting it rotates every secret. If the placeholders are left in, the profile fails closed: `demo1:generate-with-task-demo` isn't base64, so the config refuses to boot. `TestDemoEnvExample` checks the profile is complete, refuses to load with placeholders, and loads with the safe modes once generated.
5. **The demo rush is deferred to P8b (Sheshakanth's call, P8a session).** `DEMO_MODE=true` is set in the profile, but nothing reads it yet. `/healthz` keeps reporting `mode: "live"`, so the web never shows the "Start a demo rush" button and never calls a missing endpoint. In P8a the live demo is a real booking on `quiet-tuesday` (and the Tatkal event, once its sale opens). The rush story is told by Rush Control's replay on the static site.

## Consequences
- `task demo` and `task tunnel` work on this laptop with no install beyond Docker, Node and pnpm. The P8a report has the run: E1 and E2 Playwright passed against `localhost:8000` and against the public `trycloudflare.com` URL.
- The same `publicURL` rule applies in every profile, so dev and rush keep their absolute URLs unchanged.
- DEPLOY.md, STACK.md and API.md describe the deferred demo rush as P8b work rather than as something that exists.
- The infra ports (5433, 6381, 9090, 3001) are still published on all host interfaces, as in every earlier phase. They're never behind the tunnel, but anyone on the same LAN can reach them. This is pre-existing and out of P8a scope; it's listed in the report for Sheshakanth.
