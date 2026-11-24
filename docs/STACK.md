# STACK: SLUICE

Everything here is free and open source and runs locally from Docker images. **No accounts are needed to build, test or load-test.** See CLAUDE.md rule 2.

## 1. Stack
| Layer | Choice | Version floor | Why |
|---|---|---|---|
| Backend language | **Go** | latest stable (≥ 1.25) | Goroutines are cheap enough for 50k users; container-aware GOMAXPROCS; single static binary |
| HTTP | `net/http` (Go 1.22+ method/path patterns) + small in-house middleware | stdlib | No framework needed; fewer dependencies |
| Postgres driver | `jackc/pgx/v5` + `pgxpool` | v5 | Fastest Go driver; River and sqlc support it natively |
| SQL codegen | **sqlc** (`sql_package: pgx/v5`) | latest | Type-safe queries with no ORM magic |
| Migrations | **goose v3** (SQL files embedded via `embed.FS`) | v3 | Plain SQL, embeddable, runs from `sluice migrate` |
| Jobs / outbox | **River** (`riverqueue/river`, `riverpgxv5`) | latest | Transactional enqueue in the same PG tx: an outbox without extra infra |
| Redis client | `redis/go-redis/v9` | v9 | Official client; script helpers (EVALSHA fallback) |
| Rate limiting | `go-redis/redis_rate/v10` (GCRA) + `golang.org/x/time/rate` (in-process) | v10 | GCRA is smooth and fair; in-process for the hot status path |
| Tokens | `golang-jwt/jwt/v5` (HS256, key ring with `kid`) | v5 | Stateless, so status polls need no lookups |
| IDs | `google/uuid` (v7 and v5) + PG 18 `uuidv7()` | ≥ 1.6 | Time-ordered PKs; deterministic hold IDs |
| Logs / metrics | `log/slog` + `prometheus/client_golang` | stdlib / latest | Structured JSON logs; RED metrics |
| Tests | `testing` + **testcontainers-go** + `pgregory.net/rapid` (property tests) | latest | Real Postgres/Redis in tests; state-machine fuzzing |
| Load: swarm | `sluice rush` (custom) + `HdrHistogram/hdrhistogram-go` | — | 50k stateful users with dedicated connections; correct percentiles |
| Load: cross-check | **k6** OSS (run from the `grafana/k6` Docker image) | ≥ 1.0 | Industry-standard tool to validate the swarm's numbers |
| Chaos | **Toxiproxy** (`ghcr.io/shopify/toxiproxy`) + `docker compose kill/pause` | latest | Latency, timeouts and resets between services |
| Database | **PostgreSQL 18** (`postgres:18`) | 18 | `uuidv7()` built in; async I/O |
| Cache / coordination | **Redis 8** open source (`redis:8`) | 8.x | Lua, sorted sets; AGPL/OSS |
| Front door | **Caddy 2** (`caddy:2`) | 2.x | Static files plus reverse proxy plus health checks in ~20 lines |
| Observability | **Prometheus** + **Grafana OSS** (provisioned) | latest | Free, local, recognisable |
| Web | **Next.js** (App Router, `output: 'export'`) + React + TypeScript | latest stable (≥ 15) | Static export served by Caddy or Vercel |
| Styling / UI | **Tailwind CSS v4** + **shadcn/ui** + `lucide-react` + `motion` | latest | Fast, accessible primitives; UI UX Pro Max supports this stack |
| Data fetching | **TanStack Query v5** | v5 | Polling, ETag, retries, cache |
| Charts | **Recharts** | latest | Results and replay charts |
| QR on ticket | `qrcode` (client-side) | latest | No server dependency |
| E2E | **Playwright** | latest | Flows, video recording, axe accessibility checks |
| Design system | **UI UX Pro Max** skill (`nextlevelbuilder/ui-ux-pro-max-skill`) | latest | Generates `design-system/MASTER.md`; pre-delivery checklist |
| Task runner | **Task** (`go-task/task` v3) | v3 | Cross-platform (Windows Git Bash) with no make |
| Lint | golangci-lint **v2**, `govulncheck`, ESLint, Prettier, `tsc --noEmit` | v2 | Quality gates in CI |
| CI | **GitHub Actions** (`ubuntu-latest`: 4 vCPU / 16 GB, free and unlimited for public repos) | — | Also the canonical rush environment |
| Static hosting | **Vercel Hobby** (P8; replay mode) | — | Free, no card |
| Live demo tunnel | **Cloudflare Quick Tunnel** (`cloudflared tunnel --url`) | latest | No account; HTTPS URL for interviews |

### 1.1 Pinned versions (filled in by P0 with the exact versions installed)
Source commands: `go version`, `node -v`, `pnpm -v`, `task --version`, `golangci-lint --version`, `govulncheck -version`, `docker version`, `go.mod`, `web/package.json`, `deploy/compose.yml`.

| Component | Exact version | Recorded in |
|---|---|---|
| Go | `go 1.27.0` + `toolchain go1.27.4` (local: go1.27.4 windows/amd64) | `go.mod` |
| Node / pnpm | Node v24.21.0 / pnpm 11.9.0 | `web/package.json` (`engines`, `packageManager`) |
| Next.js / React / Tailwind | Next 16.4.6 / React 19.3.0 / Tailwind CSS 4.3.3 | `web/package.json` |
| Postgres image | `postgres:18.7` | `deploy/compose.yml`, `internal/app/api_integration_test.go` |
| Redis image | `redis:8.10.3` | `deploy/compose.yml`, `internal/app/api_integration_test.go` |
| Prometheus / Grafana | `prom/prometheus:v3.16.0` / `grafana/grafana:13.3.1` | `deploy/compose.yml` |
| Caddy / Toxiproxy / k6 | `caddy:2.11.9` (P5); k6 later in P5, Toxiproxy P7 | `deploy/compose.app.yml` |
| Build / runtime images | `golang:1.27.4` (build stage) / `gcr.io/distroless/static-debian12:nonroot` | `deploy/docker/Dockerfile` |
| pgx / go-redis / client_golang / uuid / golang-jwt | pgx v5.13.0 / go-redis v9.23.0 / client_golang v1.25.0 / uuid v1.6.0 / golang-jwt/jwt/v5 v5.3.1 (P2) | `go.mod` |
| testcontainers-go | v0.45.0 (+ modules/postgres, modules/redis v0.45.0) | `go.mod` |
| River / goose / sqlc / rapid | River v0.51.2 (`river`, `riverdriver/riverpgxv5`; migrator only until P3) / goose v3.30.1 / sqlc 1.31.1 via the `sqlc/sqlc:1.31.1` Docker image (ADR 0005) / rapid v1.3.0 | `go.mod`, Taskfile (`SQLC_IMAGE`) |
| golangci-lint / govulncheck / Task | golangci-lint v2.15.0 / govulncheck v1.10.0 / Task 3.54.0 | `.github/workflows/ci.yml` (`env`) |
| Docker (local) | Docker Desktop, engine 28.3.3 | (local only) |

## 2. Repo layout
```
sluice/
├── CLAUDE.md                  # Claude Code manual (always read)
├── README.md                  # P8
├── LICENSE                    # MIT
├── Taskfile.yml
├── .claude/settings.json      # git guardrails (deny rules)
├── .gitignore  .gitattributes  .editorconfig  .env.example
├── go.mod  go.sum
├── cmd/sluice/main.go         # subcommands: api|worker|mockpay|migrate|seed|rush|audit|probe|version (probe = distroless healthcheck, ADR 0010)
├── internal/
│   ├── app/                   # wiring per subcommand
│   ├── config/                # env parsing + validation (fail fast)
│   ├── httpx/                 # router, middleware (request id, recover, timeout, logging,
│   │                          #   metrics, CORS, rate limit, idempotency), problem+json
│   ├── auth/                  # JWT issue/verify (session|queue|admission), key ring
│   ├── db/                    # pgxpool, tx helpers (WithTx retry), migrator; gen/ = sqlc output
│   ├── events/                # event catalogue: layout model, seed, GET /events[/{id}] (ADR 0005)
│   ├── fsm/                   # generic transition table (+ fsmtest) for the domain state machines
│   ├── redisx/                # client, script loader; scripts/*.lua (embedded); keys/ = the only key-name builder
│   ├── inventory/             # holds: Redis fast path + PG authority; naive/ (baseline only)
│   ├── queue/                 # waiting room: join/status/claim, snapshot cache, controller
│   ├── orders/                # order + payment state machines, ConfirmOrder, webhook inbox,
│   │                          #   capture/void/refund, reconciler, live audit cache, HTTP routes
│   ├── payments/              # MockPay client, webhook verification, naive/ (baseline only)
│   ├── jobs/                  # River clients (insert-only for the API, worker), workers, periodic
│   │                          #   jobs (reconcile, audit_cache), retry backoff; the sweeper is a ticker
│   ├── mockpay/               # PSP simulator: intents, ledger, webhooks, faults, checkout page;
│   │                          #   migrations/ (own goose schema, applied at start-up), queries/, mpdb/ (sqlc)
│   ├── stats/                 # live stats; reads the audit cache that orders.RefreshAuditCache writes
│   ├── demo/                  # demo rush (bots) for live mode
│   ├── rush/                  # swarm load generator + reporter
│   ├── audit/                 # invariant auditor
│   ├── obs/                   # slog, metrics registry, pprof
│   └── testutil/              # pgtest: testcontainers Postgres per package, one DB per test;
│                              #   redistest: Redis 8 per package, FLUSHALL per test (no t.Parallel)
├── db/
│   ├── embed.go               # package dbfs: //go:embed migrations/*.sql
│   ├── migrations/            # goose: 0001_init.sql, 0002_queue_snapshot_state.sql, 0003_naive_baseline.sql, ...
│   ├── queries/               # sqlc query files
│   └── sqlc.yaml
├── web/                       # Next.js static-export app (scripts/screenshots.mjs = Quality-bar review)
├── design-system/             # UI UX Pro Max output: MASTER.md (+ Direction notes) + pages/*.md; contrast.py
├── loadtest/
│   ├── k6/                    # spike-queue.js, checkout.js
│   ├── manual/                # phase manual-check scripts (p4-flow.py: open-sale → queue → buy)
│   └── results/               # raw runs (gitignored); curated copies go to docs/results/
├── deploy/
│   ├── compose.yml            # infra: postgres, redis, prometheus, grafana
│   ├── compose.app.yml        # migrate + seed one-shots, api1..3, worker, mockpay, caddy (+ web static volume)
│   ├── compose.rush.yml       # swarm container (profile rush; sysctls, ulimits, results volume)
│   ├── compose.chaos.yml      # toxiproxy wiring
│   ├── env/                   # rush.env, demo.env.example (no real secrets)
│   ├── docker/Dockerfile      # multi-stage → distroless static image with the sluice binary
│   ├── caddy/Caddyfile
│   ├── postgres/init/01-mockpay-db.sql
│   ├── prometheus/prometheus.yml
│   └── grafana/{provisioning,dashboards}/
├── docs/                      # specs, phases, reports, adr, results
└── .github/workflows/         # ci.yml, rush.yml
```

## 3. Ports
| Service | Host port | Notes |
|---|---|---|
| Caddy (front door: web + `/api` + `/pay`) | **8000** | Full stack, demo and tunnel target |
| api (single process in dev) | **8080** | In the stack, api1..3 listen on 8080 inside the network only |
| worker (health + metrics) | 9091 | |
| mockpay | 8090 | Browser reaches it as `/pay/*` via Caddy in the stack |
| web dev server | 3000 | `task dev:web` |
| Postgres | **5433** → 5432 | Not 5432, so it can't clash with a local install |
| Redis | **6381** → 6379 | Not 6379, same reason; not 6380 either, which another local project uses (ADR 0004) |
| Prometheus | 9090 | |
| Grafana | 3001 | Anonymous viewer locally |
| Toxiproxy API | 8474 | Chaos only |
| pprof | 6060 | Only when `PPROF_ADDR` is set; never public |

## 4. Environment variables
`.env.example` must list every variable. Defaults are shown. The rush profile (`deploy/env/rush.env`) overrides TTLs so runs finish in minutes.

| Var | Default (dev) | Rush | Meaning |
|---|---|---|---|
| `APP_ENV` | `dev` | `rush` | `dev` \| `test` \| `rush` \| `demo` |
| `LOG_LEVEL` | `info` | `warn` | |
| `HTTP_ADDR` | `:8080` | | API listen address |
| `WORKER_HTTP_ADDR` | `:9091` | | Worker health/metrics |
| `MOCKPAY_HTTP_ADDR` | `:8090` | | |
| `PPROF_ADDR` | *(empty)* | `:6060` | Enables pprof when set |
| `DATABASE_URL` | `postgres://sluice:sluice@localhost:5433/sluice?sslmode=disable` | | |
| `MOCKPAY_DATABASE_URL` | `postgres://sluice:sluice@localhost:5433/mockpay?sslmode=disable` | | Separate DB, same server |
| `DB_MAX_CONNS` | `24` | `24` | Per process |
| `REDIS_URL` | `redis://localhost:6381/0` | | |
| `TOKEN_KEYS` | `dev1:<base64 32 bytes>` | | `kid:secret,...`; the first one signs, all of them verify |
| `ADMIN_TOKEN` | `dev-admin-token-change-me` | | Bearer token for `/admin/*` |
| `CORS_ORIGINS` | `http://localhost:3000` | | Comma-separated |
| `TRUSTED_PROXIES` | `127.0.0.1/32,172.16.0.0/12` | `127.0.0.1/32,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16` | CIDRs allowed to set `X-Forwarded-For`. Rush trusts every private range so Caddy is trusted on any Docker subnet (ADR 0010) |
| `RATE_LIMITS_ENABLED` | `true` | `true` | Swarm sends synthetic XFF from a trusted proxy |
| `HOLD_TTL` | `5m` | `60s` | |
| `PAYMENT_HOLD_TTL` | `10m` | `90s` | Hold TTL after an order is created |
| `ADMISSION_TTL` | `10m` | `3m` | Hard cap of an admission (the admission token's `exp`) |
| `ADMISSION_IDLE_TTL` | `2m` | `60s` | Sliding idle lease, renewed by every request with the admission token; `<= ADMISSION_TTL` (ADR 0010 addendum) |
| `OFFER_CLAIM_TTL` | `60s` | `30s` | |
| `QUEUE_TICK` | `100ms` | | |
| `QUEUE_MAX_ACTIVE` | `1000` | | Hard cap on admitted-and-seeking users |
| `QUEUE_OVERADMIT` | `1.5` | | Target = avail × this |
| `QUEUE_MIN_ADMIT` | `10` | | Floor when only a few seats remain |
| `QUEUE_MAX_ADMIT_PER_TICK` | `200` | | |
| `QUEUE_ENFORCED` | `true` | `true` / `false` for direct-mode baselines | Require an admission token for holds/orders (rush/test may disable) |
| `HOLD_PATH` | `redis_pg` | | `redis_pg` \| `pg_only` |
| `BASELINE` | `off` | `off` / `holds_only` / `naive` | Only allowed when `APP_ENV` is `rush` or `test`; startup fails otherwise |
| `MAX_SEATS_PER_ORDER` | `4` | | |
| `SWEEPER_INTERVAL` | `1s` | | |
| `RECONCILE_INTERVAL` | `30s` | | |
| `MOCKPAY_URL` | `http://localhost:8090` | `http://mockpay:8090` | Server-to-server |
| `MOCKPAY_PUBLIC_URL` | `http://localhost:8090` | `http://localhost:8000/pay` | What browsers see |
| `MOCKPAY_API_KEY` | `dev-mockpay-key` | | |
| `PSP_MAX_CONNS` | `64` | `64` | PSP client connection pool bound per api/worker process; `psp_conn_wait_seconds` shows queueing |
| `MOCKPAY_WEBHOOK_SECRET` | `dev-webhook-secret` | | |
| `PUBLIC_WEBHOOK_URL` | `http://localhost:8080/api/v1/webhooks/mockpay` | `http://caddy:8000/api/v1/webhooks/mockpay` | Where MockPay delivers |
| `PUBLIC_APP_URL` | `http://localhost:3000` | `http://localhost:8000` | Web app origin; the hosted checkout returns to `<PUBLIC_APP_URL>/order?o=<order_id>` (ADR 0007) |
| `MOCKPAY_LATENCY_MS` | `20-60` | `20-60` | Uniform per call |
| `MOCKPAY_FAIL_RATE` | `0` | `0.02` | Random 5xx before commit |
| `MOCKPAY_TIMEOUT_AFTER_COMMIT_RATE` | `0` | `0.02` | Commit, then respond after `MOCKPAY_HANG_MS` |
| `MOCKPAY_HANG_MS` | `3000` | `3000` | Longer than the API's PSP client timeout (2 s) |
| `MOCKPAY_WEBHOOK_DUP_RATE` | `0` | `0.10` | |
| `MOCKPAY_WEBHOOK_REORDER_RATE` | `0` | `0.05` | |
| `MOCKPAY_WEBHOOK_MAX_DELAY_MS` | `0` | `2000` | |
| `DEMO_MODE` | `false` | | Enables `/demo/rush` and live-demo guards |
| `TEST_ENDPOINTS_ENABLED` | `false` | `true` | `/test/*` endpoints; refused unless `APP_ENV` is `rush` or `test` |
| `NEXT_PUBLIC_API_BASE_URL` | `http://localhost:8080` | *(empty)* | Web dev only; production builds use same-origin `/api` |

## 5. Windows setup (Sheshakanth's laptop: Windows, 16 GB RAM, Git Bash)
- **Docker Desktop** with the WSL2 backend. For local rush runs, give WSL more memory in `%UserProfile%\.wslconfig`:
  ```
  [wsl2]
  memory=10GB
  ```
  The 50k headline run is meant for CI. Locally, use `task rush:smoke` (5k users).
- **Task:** `go install github.com/go-task/task/v3/cmd/task@latest` (installs into `%USERPROFILE%\go\bin`, which must be on PATH).
- **Race detector:** `go test -race` needs cgo plus gcc on Windows. Install with `winget install -e --id BrechtSanders.WinLibs.POSIX.UCRT` (or any MinGW-w64 gcc), then `CGO_ENABLED=1`. CI always runs `-race` on Linux. If gcc is missing locally, the phase report must say "race verified in CI only".
- **Line endings:** `.gitattributes` forces LF (`* text=auto eol=lf`). CRLF breaks shell scripts, Lua and SQL inside Linux containers.
- **Python** for UI UX Pro Max: use whichever of `python3 --version` or `python --version` works and use that name consistently.
- **Paths:** Taskfile commands must work in Git Bash and in Task's built-in shell interpreter. Use forward slashes and avoid bash-only features in Taskfile `cmds`.
- testcontainers-go works with Docker Desktop. Docker Desktop must be running before `task test`.

## 6. Free-only ledger
| Thing | Cost | Account needed? |
|---|---|---|
| Docker images (Postgres, Redis, Caddy, Prometheus, Grafana, Toxiproxy, k6) | ₹0 | No |
| Go, Node, pnpm, Python, Task, sqlc, goose, golangci-lint | ₹0 | No |
| GitHub public repo + Actions (`ubuntu-latest`) | ₹0, unlimited minutes on public repos | GitHub (he already has one) |
| Vercel Hobby (static replay site) | ₹0 | Vercel (he already has one) |
| Cloudflare Quick Tunnel | ₹0 | No (no account, no domain) |
