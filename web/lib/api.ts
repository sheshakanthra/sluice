// Typed client for docs/API.md: one function per endpoint the web app uses.
//
// - Base URL: NEXT_PUBLIC_API_BASE_URL in dev, same-origin ("/api/v1") otherwise.
// - Errors: RFC 9457 problem+json becomes an ApiError with the problem's code.
//   Network failures and timeouts are ApiErrors too (code NETWORK / TIMEOUT),
//   so callers branch on one type.
// - Clock: every response's Date header narrows the server clock offset, so
//   timers (hold, admission) count against the server, not the device clock.
// - Idempotency: mutating IK endpoints take a key; lib/intent.ts creates one per
//   user intent and reuses it on retry.

const BASE = (process.env.NEXT_PUBLIC_API_BASE_URL ?? "").replace(/\/+$/, "") + "/api/v1";
const DEFAULT_TIMEOUT_MS = 10_000;

// ---------- types (API.md §3) ----------

export interface Counts {
  available: number;
  held: number;
  sold: number;
  capacity: number;
}

export interface Session {
  user_id: string;
  session_token: string;
  expires_at: string;
}

export type EventStatus = "SCHEDULED" | "ON_SALE" | "SOLD_OUT" | "CLOSED";

export interface CoachLayout {
  coach: number;
  label: string;
  first_seat_no: number;
  rows: number;
  seats: number;
}

export interface Layout {
  version: number;
  kind: string;
  cols: number;
  aisle_after_col: number;
  seats_per_coach: number;
  coaches: CoachLayout[];
}

export interface EventSummary {
  id: string;
  slug: string;
  name: string;
  venue: string;
  status: EventStatus;
  starts_at: string;
  prequeue_opens_at: string;
  sale_opens_at: string;
  capacity: number;
  price_paise: number;
  currency: string;
  max_per_user: number;
  counts: Counts;
}

export interface EventDetail extends EventSummary {
  layout: Layout;
}

export interface Availability {
  version: number;
  counts: Counts;
  states: string;
}

export type QueueState = "PREQUEUE" | "WAITING" | "PAUSED" | "ADMITTED" | "SOLD_OUT" | "OFFER_EXPIRED" | "CLOSED";
export type Outlook = "LIKELY" | "POSSIBLE" | "UNLIKELY";

export interface JoinResponse {
  state: QueueState;
  queue_token: string | null;
  ticket_no: number | null;
  opens_at: string;
  retry_after_ms: number;
}

export interface QueueStatus {
  state: QueueState;
  ticket_no: number | null;
  position?: number;
  ahead?: number;
  eta_s?: number;
  outlook?: Outlook;
  counts: Counts;
  retry_after_ms: number;
  queue_token: string | null;
  admission_token: string | null;
  admission_expires_at?: string;
  opens_at: string;
}

export interface Hold {
  hold_id: string;
  status: string;
  seat_nos: number[];
  labels: string[];
  fence: number;
  expires_at: string;
}

export type OrderStatus = "PENDING_PAYMENT" | "CONFIRMED" | "FAILED" | "EXPIRED" | "REFUNDED";
export type PaymentStatus = "CREATED" | "AUTHORIZED" | "CAPTURED" | "VOIDED" | "FAILED" | "REFUNDED";
export type PaymentMethod = "pm_ok" | "pm_decline" | "pm_timeout" | "pm_slow";

export interface Ticket {
  seat_no: number;
  label: string;
  code: string;
}

export interface Order {
  order_id: string;
  event_id?: string;
  status: OrderStatus;
  amount_paise: number;
  currency: string;
  seat_nos: number[];
  hold_expires_at: string | null;
  payment: { attempt: number; status: PaymentStatus; intent_id?: string; checkout_url?: string } | null;
  charged: boolean;
  tickets: Ticket[];
  failure_code: string | null;
}

export interface Stats {
  ts: string;
  event_status: EventStatus;
  sale_opens_at: string;
  sold_out_at: string | null;
  queue: { joined: number; waiting: number; cursor: number; offers: number; active: number; admit_rate: number };
  inventory: Counts;
  correctness: {
    oversold: number | null;
    duplicate_charges: number | null;
    orphan_charges: number | null;
    checked_at: string | null;
  };
  demo_rush: { running: boolean };
}

// ---------- errors ----------

export interface Problem {
  type?: string;
  title: string;
  status: number;
  code: string;
  detail?: string;
  taken?: number[];
  available?: number;
  request_id?: string;
}

export class ApiError extends Error {
  readonly problem: Problem;
  /** Seconds from Retry-After, when the server sent one. */
  readonly retryAfterS: number | null;

  constructor(problem: Problem, retryAfterS: number | null = null) {
    super(problem.detail ?? problem.title);
    this.name = "ApiError";
    this.problem = problem;
    this.retryAfterS = retryAfterS;
  }

  get code(): string {
    return this.problem.code;
  }
  get status(): number {
    return this.problem.status;
  }
  /** Worth retrying with the same idempotency key (nothing was decided). */
  get transient(): boolean {
    const s = this.problem.status;
    return s === 0 || s >= 500 || s === 429 || this.problem.code === "REQUEST_IN_PROGRESS";
  }
}

export function isApiError(e: unknown, ...codes: string[]): e is ApiError {
  return e instanceof ApiError && (codes.length === 0 || codes.includes(e.code));
}

// ---------- server clock ----------
// The Date header is floored to the second and stamped somewhere between our
// send and receive. Each response therefore bounds the offset
// (server − local) to [date − recv, date + 1000 − sent]; intersecting the
// bounds across responses converges on the true offset. If they ever stop
// overlapping (the device clock jumped), start over.

let offsetLo = -Infinity;
let offsetHi = Infinity;

function noteServerDate(res: Response, sentAt: number, recvAt: number) {
  const header = res.headers.get("Date");
  if (!header) return;
  const date = Date.parse(header);
  if (Number.isNaN(date)) return;
  const lo = date - recvAt;
  const hi = date + 1000 - sentAt;
  const nextLo = Math.max(offsetLo, lo);
  const nextHi = Math.min(offsetHi, hi);
  if (nextLo > nextHi) {
    offsetLo = lo;
    offsetHi = hi;
  } else {
    offsetLo = nextLo;
    offsetHi = nextHi;
  }
}

/** Milliseconds to add to Date.now() to get the server's clock. */
export function serverOffsetMs(): number {
  if (!Number.isFinite(offsetLo) || !Number.isFinite(offsetHi)) return 0;
  return (offsetLo + offsetHi) / 2;
}

export function serverNow(): number {
  return Date.now() + serverOffsetMs();
}

// ---------- transport ----------

interface RequestOptions {
  method?: "GET" | "POST" | "DELETE";
  token?: string | null;
  body?: unknown;
  idempotencyKey?: string;
  etag?: string | null;
  timeoutMs?: number;
  signal?: AbortSignal;
}

interface Result<T> {
  status: number;
  data: T | null;
  etag: string | null;
  replayed: boolean;
}

async function call<T>(path: string, opts: RequestOptions = {}): Promise<Result<T>> {
  const headers: Record<string, string> = { Accept: "application/json, application/problem+json" };
  if (opts.token) headers.Authorization = `Bearer ${opts.token}`;
  if (opts.idempotencyKey) headers["Idempotency-Key"] = opts.idempotencyKey;
  if (opts.etag) headers["If-None-Match"] = opts.etag;
  if (opts.body !== undefined) headers["Content-Type"] = "application/json";

  const timeout = AbortSignal.timeout(opts.timeoutMs ?? DEFAULT_TIMEOUT_MS);
  const signal = opts.signal ? AbortSignal.any([opts.signal, timeout]) : timeout;
  const sentAt = Date.now();
  let res: Response;
  try {
    res = await fetch(BASE + path, {
      method: opts.method ?? "GET",
      headers,
      body: opts.body === undefined ? undefined : JSON.stringify(opts.body),
      signal,
      cache: "no-store",
    });
  } catch (e) {
    const timedOut = timeout.aborted;
    if (opts.signal?.aborted) throw e;
    throw new ApiError({
      title: timedOut ? "The server took too long to answer" : "Can't reach SLUICE",
      status: 0,
      code: timedOut ? "TIMEOUT" : "NETWORK",
    });
  }
  noteServerDate(res, sentAt, Date.now());

  const retryAfter = res.headers.get("Retry-After");
  const retryAfterS = retryAfter !== null && /^\d+$/.test(retryAfter) ? Number(retryAfter) : null;

  if (res.status === 304) {
    return { status: 304, data: null, etag: res.headers.get("ETag"), replayed: false };
  }
  const isJSON = (res.headers.get("Content-Type") ?? "").includes("json");
  let payload: unknown = null;
  if (res.status !== 204 && isJSON) {
    try {
      payload = await res.json();
    } catch {
      payload = null;
    }
  }
  if (!res.ok) {
    const p = (payload ?? {}) as Partial<Problem>;
    throw new ApiError(
      {
        title: p.title ?? `Request failed (${res.status})`,
        status: res.status,
        code: p.code ?? (res.status >= 500 ? "DEPENDENCY_UNAVAILABLE" : "UNKNOWN"),
        detail: p.detail,
        taken: p.taken,
        available: p.available,
        request_id: p.request_id ?? res.headers.get("X-Request-Id") ?? undefined,
        type: p.type,
      },
      retryAfterS,
    );
  }
  if (res.status !== 204 && !isJSON) {
    // A static host answering with its HTML fallback: there is no API here.
    throw new ApiError({ title: "No API at this address", status: 0, code: "NO_API" });
  }
  return {
    status: res.status,
    data: payload as T | null,
    etag: res.headers.get("ETag"),
    replayed: res.headers.get("Idempotent-Replayed") === "true",
  };
}

async function json<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const r = await call<T>(path, opts);
  return r.data as T;
}

const enc = encodeURIComponent;

// ---------- endpoints (API.md §2) ----------

export async function healthz(timeoutMs = 1500): Promise<{ status: string; mode?: string }> {
  return json("/healthz", { timeoutMs });
}

export function createSession(): Promise<Session> {
  return json("/sessions", { method: "POST" });
}

export async function listEvents(): Promise<EventSummary[]> {
  const r = await json<{ events: EventSummary[] }>("/events");
  return r.events;
}

export function getEvent(eventId: string): Promise<EventDetail> {
  return json(`/events/${enc(eventId)}`);
}

/** Returns null on 304 (the caller keeps what it has). */
export async function getAvailability(
  eventId: string,
  etag: string | null,
): Promise<{ data: Availability; etag: string | null } | null> {
  const r = await call<Availability>(`/events/${enc(eventId)}/availability`, { etag, timeoutMs: 5000 });
  if (r.status === 304 || r.data === null) return null;
  return { data: r.data, etag: r.etag };
}

export function joinQueue(eventId: string, sessionToken: string, rejoin = false): Promise<JoinResponse> {
  return json(`/events/${enc(eventId)}/queue${rejoin ? "?rejoin=true" : ""}`, {
    method: "POST",
    token: sessionToken,
  });
}

export function queueStatus(eventId: string, queueToken: string): Promise<QueueStatus> {
  return json(`/events/${enc(eventId)}/queue/status`, { token: queueToken, timeoutMs: 5000 });
}

/** Leave the line, or give an admission back (queue or admission token). */
export async function leaveQueue(eventId: string, token: string): Promise<void> {
  await call(`/events/${enc(eventId)}/queue`, { method: "DELETE", token });
}

export function createHold(
  eventId: string,
  admissionToken: string,
  body: { seat_nos: number[] } | { quantity: number },
  idempotencyKey: string,
): Promise<Hold> {
  return json(`/events/${enc(eventId)}/holds`, { method: "POST", token: admissionToken, body, idempotencyKey });
}

export async function releaseHold(holdId: string, admissionToken: string): Promise<void> {
  await call(`/holds/${enc(holdId)}`, { method: "DELETE", token: admissionToken });
}

export function createOrder(holdId: string, admissionToken: string, idempotencyKey: string): Promise<Order> {
  return json("/orders", { method: "POST", token: admissionToken, body: { hold_id: holdId }, idempotencyKey });
}

export function payOrder(
  orderId: string,
  sessionToken: string,
  paymentMethod: PaymentMethod,
  idempotencyKey: string,
): Promise<Order> {
  return json(`/orders/${enc(orderId)}/pay`, {
    method: "POST",
    token: sessionToken,
    body: { payment_method: paymentMethod },
    idempotencyKey,
    // pm_timeout / pm_slow hang on purpose; give them room before we call it a timeout.
    timeoutMs: 15_000,
  });
}

export function newPaymentAttempt(orderId: string, sessionToken: string, idempotencyKey: string): Promise<Order> {
  return json(`/orders/${enc(orderId)}/payments`, { method: "POST", token: sessionToken, idempotencyKey });
}

export function confirmOrder(orderId: string, sessionToken: string, idempotencyKey: string): Promise<Order> {
  return json(`/orders/${enc(orderId)}/confirm`, {
    method: "POST",
    token: sessionToken,
    idempotencyKey,
    timeoutMs: 15_000,
  });
}

export function getOrder(orderId: string, sessionToken: string): Promise<Order> {
  return json(`/orders/${enc(orderId)}`, { token: sessionToken });
}

export function getStats(eventId: string): Promise<Stats> {
  return json(`/stats/${enc(eventId)}`, { timeoutMs: 3000 });
}
