# SLUICE

**A ticket booking system that survives a Tatkal-style rush:** a virtual waiting room with a fair pre-queue lottery, Lua-atomic TTL seat holds with Postgres fencing, and end-to-end idempotent payments.

Target, which will be proven by a public CI run: **50,000 concurrent users on 500 seats → 0 oversells, 0 duplicate charges**, with checkout p95 and throughput measured under that load.

> 🚧 Being built. The full README arrives in P8. Until then, see [`docs/SPEC.md`](docs/SPEC.md) and [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md).

Stack: Go · PostgreSQL 18 · Redis 8 · River · Next.js · Caddy · Prometheus/Grafana · k6 · GitHub Actions.
