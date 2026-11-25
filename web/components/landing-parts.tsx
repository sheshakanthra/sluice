"use client";

// Client islands of the landing page: the latest recorded result (from
// results/latest/summary.json, labelled with its run) and the two event cards
// (live seat counts in LIVE, a plain description in REPLAY).

import { useQuery } from "@tanstack/react-query";
import { ArrowRight, Clock3, Users } from "lucide-react";
import Link from "next/link";

import { listEvents } from "@/lib/api";
import { formatCount, formatIST } from "@/lib/format";
import { useMode } from "@/lib/mode";
import { useSummary, useTimeseries, replayLabel } from "@/lib/replay";
import { cn } from "@/lib/utils";

const focusRing =
  "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-navy focus-visible:outline-solid";

export function ResultStrip() {
  const sum = useSummary();
  const ts = useTimeseries();
  const s = sum.data;
  const items: [string, string][] = s
    ? [
        ["People", formatCount(s.config.users)],
        ["Oversold", String(s.audit?.oversold ?? "—")],
        ["Duplicate charges", String(s.audit?.duplicate_charges ?? "—")],
        ["Checkout p95", `${s.headline.checkout_p95_ms} ms`],
      ]
    : [];
  return (
    <section aria-labelledby="strip-title" className="border-t border-rule bg-navy text-white">
      <div className="mx-auto flex w-full max-w-6xl flex-col gap-6 px-4 py-8 sm:px-8 lg:flex-row lg:items-center lg:justify-between">
        <div className="flex flex-col gap-1">
          <h2 id="strip-title" className="sign-label text-navy-soft">
            Latest recorded run
          </h2>
          <p className="text-sm leading-5 text-navy-soft">
            {ts.data ? replayLabel(ts.data.source) : "Loading the run label…"}
          </p>
        </div>
        {s ? (
          <dl className="grid grid-cols-2 gap-x-8 gap-y-4 sm:grid-cols-4">
            {items.map(([k, v]) => (
              <div key={k} className="flex flex-col gap-1">
                <dt className="sign-label text-navy-soft">{k}</dt>
                <dd className="tabular font-display text-4xl leading-none font-semibold">{v}</dd>
              </div>
            ))}
          </dl>
        ) : (
          <div className="h-16 w-full max-w-md animate-pulse rounded-sm bg-white/10 motion-reduce:animate-none" />
        )}
        <Link
          href="/results/"
          className="inline-flex min-h-11 cursor-pointer items-center gap-2 self-start rounded-sm font-semibold text-white underline underline-offset-4 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-white lg:self-center"
        >
          Full results <ArrowRight aria-hidden="true" className="size-4" />
        </Link>
      </div>
    </section>
  );
}

const EVENTS = [
  {
    slug: "tatkal-chn-sbc",
    kind: "The rush",
    title: "Demo Express, Tatkal drop",
    body: "Everyone arrives at once. A waiting room, a lottery at T0, and 500 seats that go in minutes.",
    icon: Users,
  },
  {
    slug: "quiet-tuesday",
    kind: "The quiet one",
    title: "Quiet Tuesday",
    body: "Always open, never crowded. Walk the whole flow calmly: line, seats, payment, ticket.",
    icon: Clock3,
  },
] as const;

export function EventCards() {
  const mode = useMode();
  const events = useQuery({ queryKey: ["events"], queryFn: listEvents, enabled: mode === "live", staleTime: 5000 });
  return (
    <section aria-labelledby="events-title" className="mx-auto w-full max-w-6xl px-4 py-16 sm:px-8 lg:py-24">
      <div className="mb-8 flex flex-col gap-2">
        <p className="sign-label text-accent-strong">Two departures</p>
        <h2 id="events-title" className="font-display text-4xl leading-[2.75rem] font-semibold text-ink">
          Pick a train
        </h2>
      </div>
      <ul className="grid grid-cols-1 gap-6 md:grid-cols-2">
        {EVENTS.map((e) => {
          const live = events.data?.find((x) => x.slug === e.slug);
          const Icon = e.icon;
          return (
            <li key={e.slug}>
              <Link
                href={`/event/?e=${e.slug}`}
                className={cn(
                  "group flex h-full cursor-pointer flex-col gap-4 rounded-xl border border-rule bg-card p-6 transition-colors duration-150 hover:border-navy sm:p-8",
                  focusRing,
                )}
              >
                <span className="flex items-center justify-between gap-4">
                  <span className="sign-label text-ink-muted">{e.kind}</span>
                  <Icon aria-hidden="true" className="size-5 text-navy" />
                </span>
                <span className="font-display text-3xl leading-9 font-semibold text-ink">{e.title}</span>
                <span className="text-base leading-6 text-ink-muted">{e.body}</span>
                <span className="mt-auto flex flex-wrap items-center justify-between gap-2 border-t border-rule pt-4 text-sm leading-5">
                  <span className="tabular text-ink-muted">
                    {mode === "live" && live
                      ? `${formatCount(live.counts.available)} of ${formatCount(live.counts.capacity)} seats left · opens ${formatIST(live.sale_opens_at, { seconds: false })}`
                      : mode === "live"
                        ? "Loading seats…"
                        : "Live seat counts need the running stack"}
                  </span>
                  <span className="inline-flex items-center gap-1 font-semibold text-navy">
                    Details
                    <ArrowRight
                      aria-hidden="true"
                      className="size-4 transition-transform duration-150 group-hover:translate-x-0.5"
                    />
                  </span>
                </span>
              </Link>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
