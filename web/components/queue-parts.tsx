import { CircleCheck, CircleHelp, TrendingDown } from "lucide-react";

import type { Counts, Outlook } from "@/lib/api";
import { formatCount } from "@/lib/format";
import { cn } from "@/lib/utils";

/**
 * A numeral whose digits roll when they change (MASTER motion: "numerals
 * tick"). Each digit is keyed by its value and place, so only the digits that
 * changed re-mount and animate; reduced motion turns it into a plain swap.
 * Screen readers get the whole number once, not the digit spans.
 */
export function TickingNumber({
  value,
  prefix = "",
  className,
}: {
  value: string;
  prefix?: string;
  className?: string;
}) {
  const chars = [...value];
  return (
    <span className={cn("tabular inline-flex overflow-hidden", className)}>
      <span className="sr-only">
        {prefix}
        {value}
      </span>
      <span aria-hidden="true" className="inline-flex">
        {prefix && <span>{prefix}</span>}
        {chars.map((c, i) => (
          <span
            key={`${chars.length - i}:${c}`}
            className={cn("inline-block", /\d/.test(c) && "animate-in duration-300 fade-in-0 slide-in-from-bottom-3")}
          >
            {c}
          </span>
        ))}
      </span>
    </span>
  );
}

/**
 * ProgressRail: an honest track from where you joined to the front. The
 * fill is computed from the server's position only, so it moves back only if
 * the server says so. Station ticks at each quarter.
 */
export function ProgressRail({ first, position }: { first: number | undefined; position: number | undefined }) {
  const known = first !== undefined && position !== undefined && first > 0;
  const pct = known ? Math.min(100, Math.max(0, ((first - position) / first) * 100)) : 0;
  const moved = known ? Math.max(0, first - position) : 0;
  return (
    <div className="flex flex-col gap-3">
      <div
        role="progressbar"
        aria-label="Progress through the line"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={Math.round(pct)}
        aria-valuetext={known ? `${Math.round(pct)} percent of the way to the front` : "Measuring"}
        className="relative h-3 rounded-full bg-sunk"
      >
        {[25, 50, 75].map((t) => (
          <span key={t} aria-hidden="true" className="absolute top-0 h-3 w-0.5 bg-card" style={{ left: `${t}%` }} />
        ))}
        <span
          aria-hidden="true"
          className="absolute inset-y-0 left-0 rounded-full bg-navy transition-[width] duration-700 ease-out"
          style={{ width: `${pct}%` }}
        />
        <span
          aria-hidden="true"
          className="absolute top-1/2 size-5 -translate-x-1/2 -translate-y-1/2 rounded-full border-[3px] border-card bg-signal transition-[left] duration-700 ease-out"
          style={{ left: `${pct}%` }}
        />
      </div>
      <div className="flex justify-between text-sm leading-5 text-ink-subtle">
        <span>Where you joined</span>
        <span>Front of the line</span>
      </div>
      {known && moved > 0 && (
        <p className="text-sm leading-5 text-ink-muted">
          <span className="tabular font-semibold text-ink">{formatCount(moved)}</span>{" "}
          {moved === 1 ? "person has" : "people have"} gone through since you joined.
        </p>
      )}
    </div>
  );
}

const OUTLOOK: Record<Outlook, { label: string; cls: string; icon: typeof CircleCheck; copy: string }> = {
  LIKELY: {
    label: "Likely",
    cls: "border-success text-success",
    icon: CircleCheck,
    copy: "At the current pace there should be seats left when it's your turn. We can't promise one.",
  },
  POSSIBLE: {
    label: "Possible",
    cls: "border-info text-info",
    icon: CircleHelp,
    copy: "It's close. Seats and the line are moving at a similar pace.",
  },
  UNLIKELY: {
    label: "Unlikely",
    cls: "border-warning text-warning",
    icon: TrendingDown,
    copy: "Seats are running out faster than the line is moving. We'll tell you the moment it's decided.",
  },
};

export function outlookCopy(o: Outlook): string {
  return OUTLOOK[o].copy;
}

/** OutlookBadge: honest odds, never a promise (DESIGN-BRIEF §5.3). */
export function OutlookBadge({ outlook }: { outlook: Outlook }) {
  const o = OUTLOOK[outlook];
  const Icon = o.icon;
  return (
    <span className={cn("sign-label inline-flex h-8 items-center gap-2 rounded-sm border-2 bg-card px-2", o.cls)}>
      <Icon aria-hidden="true" className="size-4" />
      <span>
        <span className="sr-only">Outlook: </span>
        {o.label}
      </span>
    </span>
  );
}

/**
 * SeatCounts: one bar for the whole train. Sold is solid ink, held is
 * hatched, free is navy; the numbers sit underneath, so colour is never the
 * only cue.
 */
export function SeatCounts({ counts }: { counts: Counts }) {
  const cap = Math.max(1, counts.capacity);
  const w = (n: number) => `${(n / cap) * 100}%`;
  return (
    <div className="flex flex-col gap-3">
      <div aria-hidden="true" className="flex h-3 overflow-hidden rounded-full bg-sunk">
        <span className="h-full bg-navy transition-[width] duration-500" style={{ width: w(counts.available) }} />
        <span className="seat-hatch h-full transition-[width] duration-500" style={{ width: w(counts.held) }} />
        <span className="h-full bg-ink-subtle transition-[width] duration-500" style={{ width: w(counts.sold) }} />
      </div>
      <dl className="grid grid-cols-3 gap-2 text-sm leading-5">
        <div className="flex flex-col">
          <dt className="text-ink-subtle">Free</dt>
          <dd className="tabular font-display text-2xl leading-8 font-semibold text-navy">
            {formatCount(counts.available)}
          </dd>
        </div>
        <div className="flex flex-col">
          <dt className="text-ink-subtle">On hold</dt>
          <dd className="tabular font-display text-2xl leading-8 font-semibold text-ink">{formatCount(counts.held)}</dd>
        </div>
        <div className="flex flex-col">
          <dt className="text-ink-subtle">Sold</dt>
          <dd className="tabular font-display text-2xl leading-8 font-semibold text-ink">
            {formatCount(counts.sold)}
            <span className="ml-1 font-sans text-sm font-normal text-ink-subtle">/ {formatCount(counts.capacity)}</span>
          </dd>
        </div>
      </dl>
    </div>
  );
}
