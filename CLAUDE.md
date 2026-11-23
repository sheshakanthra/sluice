# SLUICE: operating manual for Claude Code

SLUICE is a rush-proof ticket booking system. It has a virtual waiting room, TTL seat holds with fencing and idempotent payments, and a 50,000-user / 500-seat load test has to show zero oversells and zero duplicate charges.

- **Owner:** Sheshakanth. He makes the architecture decisions, runs every git command and approves any scope change.
- **You (Claude Code):** you write the code, tests, docs and phase reports.

## Read order (every session)
1. This file.
2. The phase file you were asked to run: `docs/phases/P<N>-*.md`.
3. The specs that the phase file lists under **Read first**. Don't read everything every time.

| Doc | Decides |
|---|---|
| `docs/SPEC.md` | scope, metric definitions, SLOs, rules for the resume claim |
| `docs/ARCHITECTURE.md` | components, flows, invariants I1–I8 + R1, failure modes, decisions |
| `docs/STACK.md` | stack, versions, repo layout, ports, env vars, Windows notes |
| `docs/DATA-MODEL.md` | Postgres DDL, Redis keyspace, Lua script contracts, state machines |
| `docs/API.md` | HTTP contract, tokens, idempotency, error codes, MockPay API |
| `docs/LOADTEST.md` | the rush: swarm, scenarios, auditor, measurement rules, CI |
| `docs/DESIGN-BRIEF.md` | UI/UX brief plus how to use UI UX Pro Max |
| `docs/DEPLOY.md` | shipping: Vercel replay mode plus live demo |
| `docs/phases/` | the build plan, P0–P8 |
| `docs/reports/` | phase reports, which you write |
| `docs/adr/` | decision records, which you write whenever you deviate from a spec |

## Hard rules (non-negotiable)

### 1. Git belongs to Sheshakanth
- NEVER run a git command that changes anything. That covers add, commit, push, pull, fetch, merge, rebase, reset, restore, checkout, switch, branch, stash, tag, rm, mv, clean, cherry-pick, revert, init, remote and config. `.claude/settings.json` blocks these as well. Don't route around the block with `git -C`, `sh -c`, scripts, Taskfile tasks or CI steps that commit.
- Read-only git is allowed: `git status`, `git diff`, `git log`, `git show`, `git rev-parse`.
- Commit messages you draft carry **no AI co-author or attribution lines**.
- At the end of every phase you hand him the exact commands (see the Phase-End Protocol below).

### 2. Zero cost, zero accounts
- Everything runs from Docker images and open-source tools. Don't use any managed DB, cache or queue (Supabase, Neon, Upstash, Redis Cloud, Aiven and so on), any paid SaaS, cloud trials, Grafana Cloud or k6 Cloud.
- Only three outside services are allowed: GitHub (repo plus Actions, public repo), Vercel Hobby (static site, P8) and Cloudflare Quick Tunnel (no account needed, P8).
- If something seems to need a paid service or a sign-up, stop and record it in the report as a blocker.

### 3. One phase per session
Run only the phase you were given. Once its acceptance checks pass, follow the Phase-End Protocol and **STOP**. Don't start the next phase.

### 4. The invariants are sacred
Invariants I1–I8 and R1 are defined in `docs/ARCHITECTURE.md`. Never weaken a DB constraint, delete a check, loosen an assertion, skip a test or lower isolation to get a green run. When an invariant test fails, fix the code. Any code that touches holds, orders, payments or the queue ships with concurrency tests (many goroutines, `-race`).

### 5. Honest numbers only
Every number in a report, the README or the UI must come from a command run in this repo, and that command has to be shown alongside it. Never fabricate a number, round it in your favour or pick the best of N runs without saying so. Benchmarks keep durability on: no `fsync=off`, no `synchronous_commit=off` and no Redis eviction. When a target is missed, report the real number and the bottleneck you found.

### 6. Keep the docs true
When the implementation has to deviate from a spec, update the spec in the same phase and add `docs/adr/NNNN-<slug>.md` covering context, decision and consequences. The specs must describe what actually exists.

### 7. Secrets
`.env` is gitignored. Keep `.env.example` complete and in sync with what the code reads. No secrets may appear in code, logs, fixtures or reports. Dev secrets are obviously fake values.

### 8. UI work uses UI UX Pro Max
All UI follows `design-system/MASTER.md` (generated in P0) plus the page overrides in `design-system/pages/`. Run the skill's pre-delivery checklist before you call any UI task done.

## Conventions

**Go**
- Version comes from `go.mod`. Code is gofmt/goimports-formatted and golangci-lint v2 clean, and `govulncheck` is clean in CI.
- Anything that does I/O takes `context.Context` as its first parameter, and every outbound call has a timeout.
- Wrap errors with `%w`. Domain outcomes are typed errors (`ErrSeatsTaken`, `ErrSoldOut`, `ErrHoldExpired`, ...). Request paths never panic.
- Use `log/slog` JSON with `request_id`, `event_id` and `user_id`. Never log a token or a secret.
- No global mutable state. Dependencies are wired in `internal/app`.
- SQL goes through sqlc (`internal/db/gen`). Any hand-written SQL needs a comment saying why.
- Redis Lua scripts live only in `internal/redisx/scripts/*.lua`, are embedded with `go:embed`, and each has a Go test.
- Tests are table-driven. Integration tests use testcontainers-go against real Postgres 18 and Redis 8, with no mocks for the DB or Redis. Concurrency tests run under `-race`.
- Money is int64 paise. Time is UTC in `timestamptz` and the UI renders it in IST.
- IDs are UUIDv7 unless a doc specifies a deterministic UUIDv5.

**TypeScript (`web/`)**
- Strict mode with no `any`, and ESLint and Prettier must be clean. Server state goes through TanStack Query. The client holds no secrets.

**Commit messages you draft**
- Conventional Commits. Subject is ≤ 72 chars and in the imperative mood.

## Commands
Run everything through the Taskfile (`task --list`). Each phase adds the tasks it needs.

| Command | Does |
|---|---|
| `task up` / `task down` | infra: Postgres, Redis, Prometheus, Grafana |
| `task stack:up` / `task stack:down` | full stack: 3× api, worker, mockpay, Caddy front door on :8000 |
| `task migrate` / `task seed` | schema plus demo events |
| `task dev:api` / `dev:worker` / `dev:mockpay` / `dev:web` | run one service locally |
| `task gen` | sqlc and any other codegen |
| `task lint` / `task test` / `task test:race` | quality gates |
| `task e2e` | Playwright against the full stack |
| `task rush:smoke` / `task rush` | 5k-user smoke run / 50k-user headline run, each followed by the audit |
| `task audit` | invariant auditor against the running stack |
| `task chaos` | chaos suite |
| `task demo` / `task tunnel` | live demo stack / public Cloudflare quick tunnel |

## Phase-End Protocol (every phase, in this order)
1. Run `task lint test:race` plus the phase's own acceptance checks. Everything must be green, or the phase isn't done. If gcc isn't available on Windows, run `task test` instead and write "race verified in CI only" in the report.
2. Write `docs/reports/P<N>-<slug>.md` from `docs/reports/TEMPLATE.md`. Use real outputs and leave no placeholders.
3. Run `git status --short` (read-only) to see what changed.
4. Print in chat:
   - a summary of at most 12 lines
   - the acceptance checklist, marked ✅ / ❌
   - the git command block, filled in (format below)
5. **STOP.**

Git command block format. Use plain ASCII that's safe in Git Bash, zsh and bash: no backticks, `$`, `!` or double quotes inside the messages, and one `-m` per paragraph.

```bash
git add -A
git status --short
git commit -m "feat(holds): atomic TTL seat holds with fencing" -m "- Redis Lua all-or-nothing multi-seat holds with TTL" -m "- Postgres authoritative hold rows with fencing version" -m "- Sweeper reclaims expired holds; 2k-goroutine race test" -m "Phase P2 - report: docs/reports/P2-holds.md"
```

If the folder isn't a git repo yet, start the block with `git init -b main`.

## When stuck
- Each phase file has a timebox. At 1.5× the timebox, stop and write the report with status `PARTIAL`: what's done, what's left and a proposed cut. Give a commit block, with a `wip(...)` subject, only when build and tests are green. Otherwise give no commit block and list what's broken.
- Never guess silently. Write every assumption into the report.
- Ask Sheshakanth only about scope or architecture changes. For everything else, decide, record it in the report or an ADR, and keep going.
