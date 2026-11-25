"use client";

// Event (DESIGN-BRIEF §3): route and time in IST, price, seats left (live),
// countdown to T0 and the fairness note. States: not open, pre-queue open,
// on sale, sold out. REPLAY shows the explainer instead.

import { useQuery } from "@tanstack/react-query";
import { ArrowRight, CircleSlash, Shuffle } from "lucide-react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense } from "react";

import { EventHeading } from "@/components/event-heading";
import { ErrorProblem, ReplayNotice, StatePanel } from "@/components/notices";
import { SeatCounts, TickingNumber } from "@/components/queue-parts";
import { SiteHeader } from "@/components/site-header";
import { Perforation, SignalDot, TicketStub } from "@/components/ticket-stub";
import { buttonVariants } from "@/components/ui/button";
import { type EventSummary, getEvent } from "@/lib/api";
import { formatCount, formatCountdown, formatIST } from "@/lib/format";
import { useServerNow } from "@/lib/hooks";
import { useMode } from "@/lib/mode";
import { cn } from "@/lib/utils";

export default function EventPage() {
  return (
    <>
      <SiteHeader nav />
      <main className="mx-auto flex w-full max-w-3xl flex-1 flex-col gap-8 px-4 pt-8 pb-16 sm:px-8 sm:pt-12">
        <Suspense fallback={<EventHeading eyebrow="Departure" event={undefined} />}>
          <EventRoute />
        </Suspense>
      </main>
    </>
  );
}

function EventRoute() {
  const slug = useSearchParams().get("e") ?? "";
  const mode = useMode();
  if (!slug)
    return (
      <StatePanel icon={<CircleSlash />} eyebrow="Departure" title="Which train?">
        <p>This link is missing the event. Pick one from the start page.</p>
        <Link href="/" className={cn(buttonVariants({ variant: "secondary" }), "self-start")}>
          Back to the start
        </Link>
      </StatePanel>
    );
  if (mode === "checking") return <EventHeading eyebrow="Departure" event={undefined} />;
  if (mode === "replay") return <ReplayNotice what="this departure" />;
  return <EventView slug={slug} />;
}

type Phase = "not-open" | "prequeue" | "on-sale" | "sold-out" | "closed";

function phaseOf(ev: EventSummary, now: number): Phase {
  if (ev.status === "SOLD_OUT" || (ev.status === "ON_SALE" && ev.counts.available + ev.counts.held === 0))
    return "sold-out";
  if (ev.status === "CLOSED") return "closed";
  if (ev.status === "ON_SALE" || now >= Date.parse(ev.sale_opens_at)) return "on-sale";
  if (now >= Date.parse(ev.prequeue_opens_at)) return "prequeue";
  return "not-open";
}

const PHASE_COPY: Record<Phase, { label: string; cta: string | null }> = {
  "not-open": { label: "Not open yet", cta: null },
  prequeue: { label: "Pre-queue open", cta: "Join the pre-queue" },
  "on-sale": { label: "On sale", cta: "Join the line" },
  "sold-out": { label: "Sold out", cta: null },
  closed: { label: "Closed", cta: null },
};

function EventView({ slug }: { slug: string }) {
  const q = useQuery({ queryKey: ["event", slug], queryFn: () => getEvent(slug), refetchInterval: 5000 });
  const now = useServerNow(500);
  const ev = q.data;
  if (q.error && !ev) return <ErrorProblem error={q.error} />;
  if (!ev) return <EventHeading eyebrow="Departure" event={undefined} />;

  const phase = phaseOf(ev, now);
  const copy = PHASE_COPY[phase];
  const t0 = Date.parse(ev.sale_opens_at);
  const pre = Date.parse(ev.prequeue_opens_at);
  const target = phase === "not-open" ? pre : t0;

  return (
    <>
      <EventHeading eyebrow="Departure" event={ev} />

      <TicketStub aria-label="Sale">
        <div className="flex flex-wrap items-center justify-between gap-3 px-6 pt-6 pb-4 sm:px-8">
          <p className="sign-label inline-flex items-center gap-2 text-ink-muted">
            {(phase === "on-sale" || phase === "prequeue") && <SignalDot live />}
            {copy.label}
          </p>
          <p className="tabular text-sm text-ink-muted">Sale opens {formatIST(ev.sale_opens_at)}</p>
        </div>
        <Perforation />
        <div className="flex flex-col gap-2 px-6 pt-6 pb-6 sm:px-8">
          {phase === "not-open" || phase === "prequeue" ? (
            <>
              <p className="sign-label text-ink-muted">
                {phase === "not-open" ? "Pre-queue opens in" : "Sale opens in"}
              </p>
              <p className="font-display text-[4.5rem] leading-none font-semibold text-ink sm:text-[6rem]">
                <TickingNumber value={formatCountdown(target - now)} />
              </p>
            </>
          ) : phase === "sold-out" ? (
            <>
              <p className="sign-label text-ink-muted">Seats left</p>
              <p className="tabular font-display text-[4.5rem] leading-none font-semibold text-ink sm:text-[6rem]">0</p>
              <p className="text-base leading-6 text-ink-muted">
                Sold out. Every seat found an owner, and nobody got the same seat twice.
              </p>
            </>
          ) : (
            <>
              <p className="sign-label text-ink-muted">Seats left</p>
              <p className="font-display text-[4.5rem] leading-none font-semibold text-ink sm:text-[6rem]">
                <TickingNumber value={formatCount(ev.counts.available)} />
              </p>
              <div className="pt-4">
                <SeatCounts counts={ev.counts} />
              </div>
            </>
          )}
        </div>
        {copy.cta && (
          <div className="px-6 pb-6 sm:px-8 sm:pb-8">
            <Link href={`/queue/?e=${ev.slug}`} className={cn(buttonVariants({ size: "block" }))}>
              {copy.cta}
              <ArrowRight aria-hidden="true" />
            </Link>
          </div>
        )}
      </TicketStub>

      <section
        aria-labelledby="fair-title"
        className="flex gap-4 rounded-sm border-l-4 border-l-navy bg-card p-4 sm:p-6"
      >
        <Shuffle aria-hidden="true" className="mt-1 size-5 shrink-0 text-navy" />
        <div className="flex flex-col gap-1">
          <h2 id="fair-title" className="font-semibold text-ink">
            Arriving early doesn&apos;t help
          </h2>
          <p className="text-base leading-6 text-ink-muted">
            Positions at opening are randomised. Everyone in the pre-queue at{" "}
            {formatIST(ev.sale_opens_at, { seconds: true })} gets a random place in line, so there&apos;s no prize for
            being first, but don&apos;t be late: people who arrive after it join the back.
          </p>
        </div>
      </section>
    </>
  );
}
