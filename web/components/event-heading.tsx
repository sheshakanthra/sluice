import { ArrowRight } from "lucide-react";

import type { EventSummary } from "@/lib/api";
import { formatINR, formatIST } from "@/lib/format";

/** The page's h1: what this booking is for. Falls back to a skeleton. */
export function EventHeading({ eyebrow, event }: { eyebrow: string; event: EventSummary | undefined }) {
  const [from, to] = (event?.venue ?? "").split("→").map((s) => s.trim());
  // Demo names already end with the route ("Quiet Tuesday, Chennai → Bengaluru").
  const routeInName = !!event && event.name.includes(event.venue);
  return (
    <div className="flex flex-col gap-2">
      <p className="sign-label text-accent-strong">{eyebrow}</p>
      {event ? (
        <>
          <h1 className="font-display text-[2rem] leading-10 font-semibold text-balance text-ink sm:text-5xl sm:leading-[3.5rem]">
            {event.name}
          </h1>
          <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-base leading-6 text-ink-muted">
            {!routeInName && (
              <>
                {to ? (
                  <span className="inline-flex items-center gap-1 font-semibold text-ink">
                    {from}
                    <ArrowRight aria-label="to" className="size-4 text-ink-subtle" />
                    {to}
                  </span>
                ) : (
                  <span className="font-semibold text-ink">{event.venue}</span>
                )}
                <span aria-hidden="true">·</span>
              </>
            )}
            <span className="tabular">Departs {formatIST(event.starts_at, { seconds: false })}</span>
            <span aria-hidden="true">·</span>
            <span className="tabular">{formatINR(event.price_paise)} a seat</span>
          </p>
        </>
      ) : (
        <>
          <span className="sr-only">Loading the event</span>
          <span aria-hidden="true" className="h-10 w-4/5 animate-pulse rounded-sm bg-sunk sm:h-14" />
          <span aria-hidden="true" className="h-6 w-3/5 animate-pulse rounded-sm bg-sunk" />
        </>
      )}
    </div>
  );
}
