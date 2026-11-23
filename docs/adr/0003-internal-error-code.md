# ADR 0003: Add INTERNAL (500) to the problem+json catalogue

- **Status:** accepted
- **Date:** 2026-11-23
- **Phase:** P0

## Context
API.md §1.3 lists every client-facing error code, and the highest status in it is 503 `DEPENDENCY_UNAVAILABLE`. P0 requires a recover middleware, which has to answer a recovered panic with *something*. Without a catalogue entry it would either have to invent an uncatalogued body or misuse `DEPENDENCY_UNAVAILABLE`, which would mislead clients and corrupt the error-rate breakdown.

## Decision
Add `INTERNAL` (HTTP 500, type `https://sluice.dev/errors/internal`) to the catalogue. It is used only for unexpected faults: recovered panics and programming errors. Business outcomes never use it; a test (`TestBusinessOutcomesAreNever5xx`) asserts that only `INTERNAL` and `DEPENDENCY_UNAVAILABLE` map to 5xx.

## Consequences
- Every response from the API is problem+json with a `request_id`, including crashes, so a 500 seen by the swarm can be traced to its log line and stack.
- Any `INTERNAL` during a rush counts toward the error rate (SPEC §6) and is a bug to fix, never an expected outcome (I8).
- Docs updated: API.md §1.3.
