// Shared queue actions for the queue and book pages.

import { joinQueue, leaveQueue, type JoinResponse } from "./api";
import { ensureSession } from "./session";
import { readEntry, writeEntry } from "./storage";

/** ADR 0008: two rejoins after OFFER_EXPIRED / ADMISSION_EXPIRED. */
export const MAX_REJOINS = 2;

/** Join (or rejoin) the line and store the new queue token. */
export async function joinLine(slug: string, rejoin: boolean): Promise<JoinResponse> {
  const s = await ensureSession();
  const r = await joinQueue(slug, s.session_token, rejoin);
  const prev = readEntry(slug);
  writeEntry(slug, {
    queueToken: r.queue_token ?? undefined,
    ticketNo: r.ticket_no,
    left: undefined,
    admissionToken: undefined,
    admissionExpiresAt: undefined,
    holdId: undefined,
    firstPosition: rejoin ? undefined : prev.firstPosition,
    rejoinsUsed: rejoin ? (prev.rejoinsUsed ?? 0) + 1 : prev.rejoinsUsed,
  });
  return r;
}

/**
 * Give the slot back (DESIGN-BRIEF §5.9): DELETE /queue with the admission
 * token if there is one, else the queue token. Best effort: the server also
 * frees an idle admission on its own, so a failure here is not shown.
 */
export async function giveSlotBack(slug: string): Promise<void> {
  const e = readEntry(slug);
  const token = e.admissionToken ?? e.queueToken;
  if (token) {
    try {
      await leaveQueue(slug, token);
    } catch {
      // ADMISSION_IDLE_TTL frees it server-side anyway.
    }
  }
  writeEntry(slug, {
    left: true,
    queueToken: undefined,
    admissionToken: undefined,
    admissionExpiresAt: undefined,
    holdId: undefined,
  });
}
