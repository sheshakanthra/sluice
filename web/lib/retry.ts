// Retrying a failed call the way the swarm does (API.md §3.3, ADR 0017):
// only transient failures (timeout, network, 5xx, 429, REQUEST_IN_PROGRESS),
// with full-jitter exponential backoff that never undercuts Retry-After.

import { ApiError } from "./api";
import { backoffMs } from "./backoff";

/** Attempts for one session or join call: about 7.5 s of backoff at most. */
export const SEND_ATTEMPTS = 5;

/** Retry-After in milliseconds when the error carries one, else 0. */
export function retryAfterMs(e: unknown): number {
  return e instanceof ApiError && e.retryAfterS !== null ? e.retryAfterS * 1000 : 0;
}

/** Worth sending again: nothing was decided, and there is an API to ask. */
export function isTransient(e: unknown): boolean {
  return e instanceof ApiError && e.transient && e.code !== "NO_API";
}

/** Run fn, retrying transient failures with backoff, up to `attempts` tries. */
export async function withBackoff<T>(fn: () => Promise<T>, attempts = SEND_ATTEMPTS): Promise<T> {
  for (let attempt = 0; ; attempt++) {
    try {
      return await fn();
    } catch (e) {
      if (!isTransient(e) || attempt + 1 >= attempts) throw e;
      await new Promise((resolve) => setTimeout(resolve, backoffMs(attempt, retryAfterMs(e))));
    }
  }
}
