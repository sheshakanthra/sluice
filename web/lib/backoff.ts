// Client pacing (API.md §3.3, ADR 0017). The same rules as the swarm
// (internal/rush/client.go), so the load test stays a faithful model of
// real users. Dependency-free so `node --test` can load it directly.

/** First backoff ceiling; it doubles per failed attempt. */
export const BACKOFF_BASE_MS = 500;
/** Highest backoff ceiling (the server's own retry_after may be higher). */
export const BACKOFF_CAP_MS = 30_000;
/** Clients may stretch retry_after by up to this fraction, never shorten it. */
export const POLL_JITTER = 0.1;

/**
 * Full-jitter exponential backoff for failed attempt `attempt` (0-based):
 * U(0, min(30 s, 500 ms x 2^attempt)), never below `floorMs`, the server's
 * own pacing (its last retry_after_ms or a Retry-After header).
 */
export function backoffMs(attempt: number, floorMs = 0, u: number = Math.random()): number {
  const ceil = Math.min(BACKOFF_CAP_MS, BACKOFF_BASE_MS * 2 ** Math.min(Math.max(attempt, 0), 16));
  return Math.max(floorMs, Math.floor(u * ceil));
}

/** retry_after x U(1, 1 + POLL_JITTER): jitter that never polls sooner than asked. */
export function stretchMs(ms: number, u: number = Math.random()): number {
  return Math.round(ms * (1 + POLL_JITTER * u));
}

/** A fixed client-side interval x U(0.8, 1.2), so tabs don't poll in step. */
export function jitterMs(ms: number, u: number = Math.random()): number {
  return Math.round(ms * (0.8 + 0.4 * u));
}
