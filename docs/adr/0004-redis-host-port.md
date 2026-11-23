# ADR 0004: Redis host port 6381 instead of 6380

- **Status:** accepted
- **Date:** 2026-11-23
- **Phase:** P0

## Context
STACK.md §3 mapped SLUICE's Redis to host port 6380 so it would not clash with a local Redis on 6379. On Sheshakanth's laptop another project's container already publishes host port 6380, so `task up` could not bind it. Sheshakanth chose to move SLUICE rather than stop the other project.

## Decision
Publish SLUICE's Redis on host port **6381** (`6381:6379` in `deploy/compose.yml`). The default `REDIS_URL` becomes `redis://localhost:6381/0` in the config code, `.env.example` and STACK.md §4. Inside the Docker network Redis still listens on 6379, so the later full stack (`redis:6379`) is unaffected.

## Consequences
- `task up` coexists with the other local project.
- Any tool or note pointing at `localhost:6380` must use 6381.
- Docs updated: STACK.md §3 and §4, P0-bootstrap.md task 4 (port list).
