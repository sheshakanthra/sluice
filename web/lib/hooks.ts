"use client";

import { useCallback, useEffect, useState, useSyncExternalStore } from "react";

import { serverNow } from "./api";
import { EMPTY_ENTRY, type EventEntry, readEntry, subscribeEntries, writeEntry } from "./storage";

/** The server-corrected clock, re-rendering every `everyMs`. */
export function useServerNow(everyMs = 250): number {
  const [now, setNow] = useState(() => serverNow());
  useEffect(() => {
    const id = window.setInterval(() => setNow(serverNow()), everyMs);
    return () => window.clearInterval(id);
  }, [everyMs]);
  return now;
}

/** The device clock (for "updated 3 s ago"), re-rendering every `everyMs`. */
export function useLocalNow(everyMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), everyMs);
    return () => window.clearInterval(id);
  }, [everyMs]);
  return now;
}

/** The stored EventEntry for a slug (live-updating), plus a patcher that persists it. */
export function useEntry(slug: string): [EventEntry, (patch: Partial<EventEntry>) => void] {
  const entry = useSyncExternalStore(
    subscribeEntries,
    () => readEntry(slug),
    () => EMPTY_ENTRY,
  );
  const patch = useCallback((p: Partial<EventEntry>) => void writeEntry(slug, p), [slug]);
  return [entry, patch];
}

const subscribeReducedMotion = (cb: () => void) => {
  const mq = window.matchMedia("(prefers-reduced-motion: reduce)");
  mq.addEventListener("change", cb);
  return () => mq.removeEventListener("change", cb);
};

export function useReducedMotion(): boolean {
  return useSyncExternalStore(
    subscribeReducedMotion,
    () => window.matchMedia("(prefers-reduced-motion: reduce)").matches,
    () => false,
  );
}

/** True once mounted on the client (localStorage and the clock are real). */
export function useMounted(): boolean {
  return useSyncExternalStore(
    () => () => {},
    () => true,
    () => false,
  );
}
