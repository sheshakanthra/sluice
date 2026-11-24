// Refresh-safe client state (DESIGN-BRIEF §5.2). Everything lives in
// localStorage under the "sluice:" prefix, and every access is wrapped in
// try/catch: private windows, blocked storage and quota errors must never
// break the booking flow (the page then simply forgets on refresh).

const PREFIX = "sluice:";

export function readJSON<T>(key: string): T | null {
  try {
    const raw = window.localStorage.getItem(PREFIX + key);
    return raw === null ? null : (JSON.parse(raw) as T);
  } catch {
    return null;
  }
}

export function writeJSON(key: string, value: unknown): void {
  try {
    window.localStorage.setItem(PREFIX + key, JSON.stringify(value));
  } catch {
    // Storage unavailable: the in-memory state still works for this tab.
  }
}

export function removeKey(key: string): void {
  try {
    window.localStorage.removeItem(PREFIX + key);
  } catch {
    // ignore
  }
}

/** A user's progress through one event, keyed by the event slug. */
export interface EventEntry {
  queueToken?: string;
  ticketNo?: number | null;
  /** Position at the first WAITING observation: the progress rail's 0 %. */
  firstPosition?: number;
  admissionToken?: string;
  admissionExpiresAt?: string;
  rejoinsUsed?: number;
  holdId?: string;
  orderId?: string;
  /** Set when the user leaves the line or gives their admission back. */
  left?: boolean;
}

// Entries are a tiny external store: components subscribe with
// useSyncExternalStore (lib/hooks.ts useEntry), and every write notifies them,
// whether it came from a component or from a shared action in lib/queue.ts.
const EMPTY: EventEntry = {};
const entryCache = new Map<string, EventEntry>();
const listeners = new Set<() => void>();

export function readEntry(slug: string): EventEntry {
  let e = entryCache.get(slug);
  if (!e) {
    e = readJSON<EventEntry>(`ev:${slug}`) ?? EMPTY;
    entryCache.set(slug, e);
  }
  return e;
}

export function writeEntry(slug: string, patch: Partial<EventEntry>): EventEntry {
  const next: EventEntry = { ...readEntry(slug), ...patch };
  for (const k of Object.keys(next) as (keyof EventEntry)[]) {
    if (next[k] === undefined) delete next[k];
  }
  writeJSON(`ev:${slug}`, next);
  entryCache.set(slug, next);
  listeners.forEach((l) => l());
  return next;
}

export function subscribeEntries(cb: () => void): () => void {
  listeners.add(cb);
  return () => listeners.delete(cb);
}

export const EMPTY_ENTRY = EMPTY;

/** What the checkout and ticket pages need to know about an order. */
export interface OrderMeta {
  slug: string;
  labels: string[];
}

export function readOrderMeta(orderId: string): OrderMeta | null {
  return readJSON<OrderMeta>(`order:${orderId}`);
}

export function writeOrderMeta(orderId: string, meta: OrderMeta): void {
  writeJSON(`order:${orderId}`, meta);
}
