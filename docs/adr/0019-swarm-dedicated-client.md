# ADR 0019: The swarm drives its dedicated connections itself

- **Status:** accepted
- **Date:** 2026-11-27
- **Phase:** P8 (swarm-efficiency session)

## Context
H 50000 on CI (run `45930086030`, commit `175c887`) held every invariant, but the generator starved:
- the swarm container used 2.43 mean / 3.50 p95 cores of the 4-core runner;
- lag p99 was 2678.8 ms, with 66.6% of requests timing out at 2064 mean rps and up to 128,866 goroutines.

LOADTEST §1 requires one keep-alive TCP connection per user (`--conn dedicated`), and the swarm got that from one `http.Transport` per user. Each transport runs a read goroutine and a write goroutine per connection, and every request moves between three goroutines over channels. The client added a context, two `time.AfterFunc` timers and an `httptrace` hook per request on top.

A local profile at 15,000 users (`p8eff-before-15k`) put about 36% of the swarm's CPU samples under `persistConn.readLoop`/`writeLoop`, 14% re-dialling, and 17% in the scheduler. GC used 59.17 CPU-s.

The 1 s generator self-check also reported 12.20 "cores" on 4 cores. The sampler read process CPU and the wall clock in two calls, and a goroutine descheduled between them (seconds, in a starved run) paired a CPU value with a much later wall time.

## Decision
- **Dedicated mode uses `userConn`** (`internal/rush/conn.go`), a minimal HTTP/1.1 client over the user's one keep-alive TCP connection, driven by the user's own goroutine:
  - It writes the request bytes and reads the response itself, with Content-Length, chunked bodies, `Connection: close`, HTTP/1.0 and 1xx all handled.
  - The per-call timeout is a socket deadline set once the connection is dialled or reused. That is the same span as before: from the send, not from the dial.
  - The 4 KiB read buffer comes from a pool and is only held while a response is read.
  - When the run ends, the connection closes and any request in flight unblocks.
- **The server sees the same thing:**
  - one TCP connection per user, kept alive and re-dialled after an error or a timeout;
  - byte-identical requests (`TestUserConnWireFormat` captures every request shape from both clients and compares the raw bytes and the connection count);
  - the same transparent one-time retry of a replayable request on a dead reused connection, which is net/http's own rule (`TestUserConnStaleKeepAlive`);
  - the same twin connection for the double-submit.
- Pooled mode keeps net/http.
- No change to the request pattern, pacing, arrival curve, timeouts, budgets or gates.
- **The CPU sampler brackets the CPU read with two wall-clock reads.** It retries a bracket wider than 20 ms up to 5 times, stamps the CPU value at the bracket midpoint, and leaves a sample it can't bracket out of max and mean (`cpu_samples_invalid`).
- **Whole-run cost goes into `summary.json` `generator`:** `cpu_seconds`, `cpu_cores_run_mean`, `cpu_ms_per_1k_requests`, `gc_cycles`, `gc_cpu_seconds` and `heap_live_peak_mb`.
- **Profiles:** with `--pprof` (on in every `task rush*`, so in every CI artifact) the swarm writes `cpu.pprof` and `heap.pprof` into its result directory. `task rush:profile` runs a profiled H-mix rush at `USERS` (15000).

## Consequences
- The swarm's cost per user and per request drops (numbers in `docs/reports/P8-swarm-efficiency.md`). The H run's generator check is the gate that shows whether 4 cores are now enough.
- `userConn` supports `http://` targets only. That's the only kind the swarm uses (Caddy on the compose network).
- Most of the remaining swarm CPU is kernel time for socket writes, reads, connects and epoll. Lowering it would mean fewer connections or requests, which would change what the server sees, so it stays.
- Docs: LOADTEST §1, §3.3, §3.4 and §7; `docs/results/README.md` (new generator fields).
