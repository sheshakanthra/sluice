// The anonymous session (API.md §3.1). One per browser, kept in localStorage,
// renewed when it is within 10 minutes of expiry. Concurrent callers share one
// in-flight request so a page never creates two users. A transient failure
// is retried with jittered backoff (lib/retry.ts).

import { createSession, type Session } from "./api";
import { withBackoff } from "./retry";
import { readJSON, writeJSON } from "./storage";

const RENEW_BEFORE_MS = 10 * 60 * 1000;
let inflight: Promise<Session> | null = null;

export function storedSession(): Session | null {
  const s = readJSON<Session>("session");
  if (!s || Date.parse(s.expires_at) - Date.now() < RENEW_BEFORE_MS) return null;
  return s;
}

export function ensureSession(): Promise<Session> {
  const s = storedSession();
  if (s) return Promise.resolve(s);
  inflight ??= withBackoff(createSession)
    .then((fresh) => {
      writeJSON("session", fresh);
      return fresh;
    })
    .finally(() => {
      inflight = null;
    });
  return inflight;
}
