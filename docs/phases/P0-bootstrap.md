# P0: Bootstrap and guardrails
**Timebox:** 1.5 h · **Read first:** CLAUDE.md, STACK.md, DESIGN-BRIEF.md §1–2

## Goal
Set up a repo that builds, lints, tests and runs infra with one command each. The skill is installed and the design system generated, and CI is wired. No business logic yet.

## Tasks
1. **UI UX Pro Max.** If `.claude/skills/ui-ux-pro-max/scripts/search.py` is missing, install it:
   - `npm install -g uipro-cli`, then `uipro init --ai claude`. Fallback: `npx -y uipro-cli init --ai claude`.
   - Verify: the file exists and `python3` (or `python`) `--version` works.
   - Add `.claude/skills/ui-ux-pro-max/` to `.gitignore`. It's a reinstallable tool; the generated `design-system/` is what gets committed.
   - Generate the master design system with the exact P0 command in DESIGN-BRIEF §2, then append the **Brand overrides** section to `design-system/MASTER.md`.
2. **Repo hygiene:**
   - `.gitignore` (Go, Node, `.env`, `loadtest/results/*` except `.gitkeep`, `web/out`, `.next`, coverage, the skill folder)
   - `.gitattributes`: already in the kit (`* text=auto eol=lf` plus binary rules). Extend it if needed.
   - `.editorconfig`, `LICENSE` (MIT, Sheshakanth), and `.env.example` with **every** variable from STACK.md §4. Keep the existing `README.md` stub; P8 rewrites it.
3. **Go module** `github.com/sheshakanthra/sluice`:
   - `cmd/sluice/main.go` with subcommands `api | worker | mockpay | migrate | seed | rush | audit | version`. Unimplemented ones print "not implemented in P0" and exit 2.
   - `internal/config`: typed config, env parsing, validation, fail-fast. `BASELINE` and `TEST_ENDPOINTS_ENABLED` are refused unless `APP_ENV` is `rush` or `test`.
   - `internal/obs`: slog JSON logger, Prometheus registry, optional pprof server on `PPROF_ADDR`.
   - `internal/httpx`:
     - router on stdlib `ServeMux`
     - middleware: request id, recover, timeout, access log, RED metrics, CORS
     - problem+json helper (RFC 9457) with the code catalogue from API.md §1.3
     - `/healthz`, `/readyz` (readyz pings PG and Redis)
   - `sluice api` serves on `HTTP_ADDR` with sane server timeouts (`ReadHeaderTimeout` 5 s, `IdleTimeout` 120 s) and graceful shutdown on SIGINT/SIGTERM.
4. **Infra** in `deploy/compose.yml`:
   - `postgres:18` (pin the exact minor). The volume mounts at **`/var/lib/postgresql`**; 18+ changed the path, and the old `/data` mount fails. Healthcheck `pg_isready`. Init script `deploy/postgres/init/01-mockpay-db.sql` creates the `mockpay` DB.
   - `redis:8` (pinned) with `--appendonly yes --appendfsync everysec --maxmemory-policy noeviction`, plus a healthcheck.
   - `prom/prometheus` and `grafana/grafana` (anonymous viewer, provisioned Prometheus datasource).
   - Host ports per STACK.md §3 (5433, 6380, 9090, 3001).
5. **Taskfile.yml**: `up`, `down`, `dev:api`, `lint`, `test`, `test:race`, `gen` (stub), `dev:web`, `web:build`, `web:lint`. Mark the later ones (`stack:*`, `rush*`, `audit`, `chaos`, `demo`, `tunnel`, `e2e`) as `TODO(Pn)` stubs. Everything must work in Git Bash on Windows.
6. **Lint config:** `.golangci.yml` in v2 format (errcheck, govet, staticcheck, gosec, errorlint, bodyclose, noctx, contextcheck, sqlclosecheck, rowserrcheck, gocritic, revive with sane defaults).
7. **Web:** `web/` via create-next-app:
   - TypeScript, App Router, Tailwind v4, ESLint, pnpm
   - `output: 'export'`, `images.unoptimized: true`, `trailingSlash: true`
   - `shadcn` init
   - tokens from MASTER.md plus brand overrides mapped into `globals.css` (`@theme`), with JetBrains Mono and the body font loaded via `next/font`
   - one placeholder landing page that already uses the tokens: dark `#0B0F12`, an accent CTA, and tabular numerals in a "50,000 · 0 · 0" strip
8. **CI** `.github/workflows/ci.yml` on push and PR:
   - Go job: setup-go, Task, `golangci-lint`, `go test -race ./...`, `govulncheck`
   - Web job: pnpm install, lint, typecheck, build
9. **Docs:** fill in STACK.md §1.1 with the exact versions installed and write `docs/adr/0001-stack.md` (why Go, Redis+PG, River, Taskfile).

## Acceptance
- [ ] `task up` → all containers healthy (`docker compose ps` shows healthy).
- [ ] `task dev:api`, then `curl -s localhost:8080/healthz` → 200, and `/readyz` → 200 while infra is up and 503 after `task down`.
- [ ] `task lint test` is green, and `task test:race` is green (or the report says "race verified in CI only" because there's no gcc).
- [ ] `task web:build` produces `web/out/index.html`.
- [ ] `design-system/MASTER.md` exists and includes Brand overrides.
- [ ] `go run ./cmd/sluice version` prints the version and commit (via `-ldflags`, or `unknown` in dev).
- [ ] `.env.example` covers every env var the config reads (a unit test asserts this by parsing the file).
- [ ] No mutating git command was run during the session.

## Report extras
Pinned versions table, a screenshot description of the placeholder page, and any Windows-specific friction you hit.

**Commit hint:** `chore(bootstrap): repo skeleton, infra, CI, design system`
