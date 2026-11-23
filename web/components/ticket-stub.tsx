import type { ComponentProps } from "react";

import { cn } from "@/lib/utils";

/**
 * TicketStub is SLUICE's signature element (design-system/MASTER.md, Direction notes):
 * a flat card split by perforated tear lines. It always carries the one number
 * that matters right now (queue position, hold timer, seat, headline result).
 *
 * `punch` must match the colour of the surface the stub sits on, so the
 * semicircle cut-outs read as holes punched through the card.
 */
export function TicketStub({
  className,
  punch = "var(--paper)",
  style,
  ...props
}: ComponentProps<"article"> & { punch?: string }) {
  return (
    <article
      className={cn("relative overflow-hidden rounded-xl border border-rule bg-card text-card-foreground", className)}
      style={{ ["--punch" as string]: punch, ...style }}
      {...props}
    />
  );
}

/** A horizontal tear line across the full width of a TicketStub. */
export function Perforation({ className }: { className?: string }) {
  return <div role="presentation" className={cn("perforation", className)} />;
}

/** A vertical tear line between two stub columns. */
export function PerforationVertical({ className }: { className?: string }) {
  return <div role="presentation" className={cn("perforation-vertical", className)} />;
}

/** The live/attention marker: the only place the raw signal orange appears. */
export function SignalDot({ live = false, className }: { live?: boolean; className?: string }) {
  return (
    <span
      aria-hidden="true"
      className={cn("inline-block size-2.5 shrink-0 rounded-full bg-signal", live && "animate-signal", className)}
    />
  );
}
