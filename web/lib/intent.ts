// Idempotency keys per user intent (API.md §1.2).
//
// One key per intent ("hold these seats", "pay attempt 2 of this order"),
// stored in localStorage so a retry after a timeout, a double tap or even a
// refresh reuses it, and the server replays instead of acting twice.
// The key is dropped once the server has decided: a success, or a business
// 4xx (which the server stores, so reusing the key would replay the refusal
// forever). Transient failures (network, 5xx, 429, REQUEST_IN_PROGRESS) keep
// the key for the retry.

import { ApiError } from "./api";
import { readJSON, removeKey, writeJSON } from "./storage";

function keyFor(scope: string): string {
  const stored = readJSON<string>(`ik:${scope}`);
  if (stored) return stored;
  const fresh = crypto.randomUUID();
  writeJSON(`ik:${scope}`, fresh);
  return fresh;
}

export async function withIntent<T>(scope: string, run: (key: string) => Promise<T>): Promise<T> {
  const key = keyFor(scope);
  try {
    const out = await run(key);
    removeKey(`ik:${scope}`);
    return out;
  } catch (e) {
    if (e instanceof ApiError && !e.transient) removeKey(`ik:${scope}`);
    throw e;
  }
}
