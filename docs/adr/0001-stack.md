# ADR 0001: Core stack: Go, Postgres + Redis, River, Taskfile

- **Status:** accepted
- **Date:** 2026-11-23
- **Phase:** P0

## Context
SLUICE has to absorb 50,000 concurrent users on one 4 vCPU CI runner and still prove zero oversells and zero duplicate charges (SPEC §2, §7). It must cost nothing and need no accounts (CLAUDE.md rule 2), and it is built on a Windows laptop with Git Bash. This ADR records the stack choices summarised in ARCHITECTURE.md §7 and STACK.md §1, with the exact versions installed in P0 (STACK.md §1.1).

## Decision
- **Go (1.27.x)** for the API, worker, MockPay and the load swarm, as one static binary (`cmd/sluice`) with subcommands. Goroutines make 50k simulated users with their own keep-alive connections affordable on one machine; GOMAXPROCS is container-aware; the stdlib `net/http` mux (method + path patterns) removes the need for a framework.
- **Postgres 18 as the source of truth, Redis 8 as the fast path and coordination layer.** Postgres constraints and conditional updates make I1, I2 and I4 provable even if Redis is wrong or wiped. Redis Lua scripts give single-round-trip atomic holds and queue ticks so contention is shed before it reaches Postgres. Redis is always rebuildable from Postgres (R1).
- **River** (Postgres-backed job queue) for the transactional outbox and background jobs. Jobs are inserted in the same transaction as the business write, so there is no dual-write gap and no extra broker to run.
- **Taskfile (go-task v3)** as the single command surface. It runs natively on Windows without make, and its built-in shell interpreter keeps commands portable between Git Bash, PowerShell and the Linux CI runner.

## Consequences
- The whole backend shares one module, one lint config and one test harness (testcontainers-go against real Postgres 18 and Redis 8, no mocks).
- The correctness argument lives in Postgres. Redis bugs can cost latency, never correctness, and the auditor plus chaos C1 must keep proving that.
- River ties job throughput to Postgres capacity. That is acceptable at this scale; ARCHITECTURE.md §8 describes the 10x path.
- Contributors need Go, Docker, Node/pnpm, Python (design-system generation only) and Task. Race tests on Windows need gcc (STACK.md §5).
- Docs updated: STACK.md §1.1 (pinned versions).
