"use client";

import { ArrowRight } from "lucide-react";
import { useMemo } from "react";
import { encode } from "uqr";

import { Perforation, SignalDot, TicketStub } from "@/components/ticket-stub";
import type { EventSummary, Ticket } from "@/lib/api";
import { formatINR, formatIST, formatMMSS, spokenDuration } from "@/lib/format";
import { useServerNow } from "@/lib/hooks";
import { cn } from "@/lib/utils";

const ANNOUNCE_AT = [60, 30, 10];

/**
 * HoldTimer: mm:ss to `expiresAt` on the server-corrected clock. Warning
 * colour under 60 s, a pulse under 10 s (reduced motion: none). Screen
 * readers hear it at 60, 30 and 10 s and when it ends, nothing in between.
 */
export function useHoldLeft(expiresAt: string | null | undefined): number | null {
  const now = useServerNow(250);
  return expiresAt ? Date.parse(expiresAt) - now : null;
}

export function HoldTimer({ left, label = "Seats held for" }: { left: number | null; label?: string }) {
  const secs = left === null ? null : Math.max(0, Math.ceil(left / 1000));
  // The live region's text changes only when a threshold is crossed, so it
  // speaks at 60, 30 and 10 s and at the end, and stays quiet in between.
  const mark = secs === null ? null : secs === 0 ? 0 : [...ANNOUNCE_AT].reverse().find((m) => secs <= m);
  const said =
    mark === null || mark === undefined
      ? ""
      : mark === 0
        ? "Your hold has ended."
        : `${spokenDuration(mark * 1000)} left on your hold.`;

  const low = secs !== null && secs < 60;
  const critical = secs !== null && secs <= 10 && secs > 0;
  return (
    <div className="flex flex-col gap-2">
      <p className={cn("sign-label flex items-center gap-2", low ? "text-warning" : "text-ink-muted")}>
        <SignalDot live={!low} className={low ? "bg-warning" : undefined} />
        {label}
      </p>
      <p
        className={cn(
          "tabular font-display text-[5.5rem] leading-none font-semibold tracking-[-0.01em] transition-colors duration-300 sm:text-[7rem]",
          low ? "text-warning" : "text-ink",
          critical && "animate-pulse",
        )}
        role="timer"
      >
        {secs === null ? "--:--" : formatMMSS(secs * 1000)}
      </p>
      <p className="sr-only" aria-live="polite">
        {said}
      </p>
    </div>
  );
}

/** Seats and the money, rupees with Indian grouping. */
export function PriceSummary({
  labels,
  pricePaise,
  amountPaise,
}: {
  labels: string[];
  pricePaise: number | undefined;
  amountPaise: number;
}) {
  return (
    <dl className="flex flex-col gap-3">
      <div className="flex flex-col gap-2">
        <dt className="sign-label text-ink-muted">Your seats</dt>
        <dd>
          <ul className="flex flex-wrap gap-2" aria-label="Seats">
            {labels.map((l) => (
              <li
                key={l}
                className="tabular inline-flex h-10 items-center rounded-sm border-2 border-ink px-3 font-display text-lg font-semibold text-ink"
              >
                {l}
              </li>
            ))}
          </ul>
        </dd>
      </div>
      <div className="flex items-baseline justify-between border-t border-rule pt-3">
        <dt className="text-base leading-6 text-ink-muted">
          {labels.length} × {pricePaise !== undefined ? formatINR(pricePaise) : "seat"}
        </dt>
        <dd className="tabular text-base leading-6 text-ink">{formatINR(amountPaise)}</dd>
      </div>
      <div className="flex items-baseline justify-between">
        <dt className="text-base leading-6 text-ink-muted">Booking fee</dt>
        <dd className="tabular text-base leading-6 text-ink">{formatINR(0)}</dd>
      </div>
      <div className="flex items-baseline justify-between border-t-2 border-ink pt-3">
        <dt className="text-lg leading-7 font-semibold text-ink">Total</dt>
        <dd className="tabular font-display text-3xl leading-9 font-semibold text-ink">{formatINR(amountPaise)}</dd>
      </div>
    </dl>
  );
}

/** A QR code drawn client-side as one SVG path. */
export function QRCode({ value, size = 128, title }: { value: string; size?: number; title: string }) {
  const path = useMemo(() => {
    const qr = encode(value, { ecc: "M", border: 0 });
    let d = "";
    qr.data.forEach((row, y) =>
      row.forEach((on, x) => {
        if (on) d += `M${x} ${y}h1v1h-1z`;
      }),
    );
    return { d, n: qr.size };
  }, [value]);
  return (
    <svg
      role="img"
      aria-label={title}
      width={size}
      height={size}
      viewBox={`-2 -2 ${path.n + 4} ${path.n + 4}`}
      shapeRendering="crispEdges"
      className="shrink-0 rounded-sm bg-white"
    >
      <path d={path.d} className="fill-ink" />
    </svg>
  );
}

/** TicketCard: the stub as an issued ticket; the number is coach and seat. */
export function TicketCard({
  ticket,
  event,
  index,
  total,
}: {
  ticket: Ticket;
  event: EventSummary | undefined;
  index: number;
  total: number;
}) {
  const [coach, seat] = ticket.label.split("-");
  const [from, to] = (event?.venue ?? "").split("→").map((s) => s.trim());
  return (
    <TicketStub aria-label={`Ticket ${index + 1} of ${total}: ${ticket.label}`}>
      <div className="flex items-center justify-between gap-4 bg-navy px-6 py-3 text-white">
        <p className="sign-label text-navy-soft">
          Ticket {index + 1} of {total}
        </p>
        <p className="sign-label flex items-center gap-2 text-white">
          <SignalDot /> Confirmed
        </p>
      </div>
      <div className="flex flex-col gap-2 p-6 sm:p-8">
        <p className="sign-label text-ink-muted">Coach · seat</p>
        <p className="tabular font-display text-[4.5rem] leading-none font-semibold text-ink sm:text-[5.5rem]">
          {coach} <span className="text-ink-subtle">·</span> {seat}
        </p>
      </div>
      <Perforation />
      <div className="flex flex-col gap-6 p-6 sm:flex-row sm:items-center sm:justify-between sm:p-8">
        <dl className="flex flex-col gap-3 text-base leading-6">
          {event && (
            <div>
              <dt className="sr-only">Departure</dt>
              <dd className="font-display text-xl leading-7 font-semibold text-ink">{event.name.split(",")[0]}</dd>
            </div>
          )}
          {event && (
            <div>
              <dt className="sr-only">Route</dt>
              <dd className="flex flex-wrap items-center gap-1 font-semibold text-ink">
                {to ? (
                  <>
                    {from} <ArrowRight aria-label="to" className="size-4 text-ink-subtle" /> {to}
                  </>
                ) : (
                  event.venue
                )}
              </dd>
            </div>
          )}
          {event && (
            <div>
              <dt className="sign-label text-ink-muted">Departs</dt>
              <dd className="tabular text-ink">{formatIST(event.starts_at, { seconds: false })}</dd>
            </div>
          )}
          <div>
            <dt className="sign-label text-ink-muted">Ticket code</dt>
            <dd className="tabular font-mono text-lg font-semibold tracking-wider text-ink">{ticket.code}</dd>
          </div>
        </dl>
        <QRCode value={ticket.code} title={`QR code for ticket ${ticket.code}`} />
      </div>
    </TicketStub>
  );
}
