import { ArrowRight, ArrowUpRight, Info } from "lucide-react";
import Link from "next/link";

import { EventCards, ResultStrip } from "@/components/landing-parts";
import { SiteHeader } from "@/components/site-header";
import { Perforation, PerforationVertical, TicketStub } from "@/components/ticket-stub";

// Landing (DESIGN-BRIEF §3): the pitch, the two events, "how it survives a
// rush" in four stops, the latest recorded result (labelled with its run) and
// the way into Rush Control. Static: the live parts are client islands.

const stops = [
  {
    name: "Waiting room",
    guarantee: "Fair order",
    body: "Join before 10:00 and a lottery sets your place, so being 50 ms faster buys nothing. After that it is strictly first come, first served.",
  },
  {
    name: "Seat hold",
    guarantee: "No oversell",
    body: "Your seats are held for five minutes in one atomic step: all of them or none. The database has the final say on every seat, so two people can never both own seat\u00a027.",
  },
  {
    name: "Payment",
    guarantee: "No double charge",
    body: "Every retry reuses the same key, so a double tap, a refresh or a gateway timeout cannot charge you twice. If anything fails, you are told plainly: you were not charged.",
  },
  {
    name: "Audit",
    guarantee: "Proven in public",
    body: "After every rush an auditor checks the payment ledger against the issued tickets. The numbers on this site come from that run, never typed by hand.",
  },
] as const;

const focusRing =
  "focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-navy focus-visible:outline-solid";

export default function Home() {
  return (
    <>
      <SiteHeader nav />

      <main className="flex flex-1 flex-col">
        <section
          aria-labelledby="hero-title"
          className="mx-auto grid w-full max-w-6xl grid-cols-1 items-start gap-12 px-4 pt-12 pb-16 sm:px-8 sm:pt-16 lg:grid-cols-[minmax(0,1.05fr)_minmax(0,1fr)] lg:gap-16 lg:pt-24 lg:pb-24"
        >
          <div className="flex flex-col gap-8">
            <div className="flex flex-col gap-2">
              <p className="sign-label text-accent-strong">Tatkal opens at</p>
              <p className="flex items-baseline gap-3 font-display leading-none font-semibold text-navy">
                <span className="tabular text-[5.5rem] tracking-[-0.01em] sm:text-[8rem]">10:00:00</span>
                <span className="text-2xl font-semibold tracking-[0.08em] text-ink-muted sm:text-4xl">IST</span>
              </p>
            </div>

            <div className="flex flex-col gap-4">
              <h1
                id="hero-title"
                className="max-w-xl font-display text-[2.5rem] leading-[1.1] font-semibold text-balance text-ink sm:text-5xl sm:leading-[3.5rem]"
              >
                Fifty thousand people. Five hundred seats. One honest queue.
              </h1>
              <p className="max-w-xl text-lg leading-7 text-pretty text-ink-muted">
                SLUICE is a booking system built for the minute everyone clicks at once: a fair waiting room, seat holds
                that cannot be sold twice, and payments that cannot charge you twice.
              </p>
            </div>

            <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:gap-4">
              <Link
                href="/control/"
                className={`group inline-flex h-12 cursor-pointer items-center justify-center gap-2 rounded-sm bg-primary px-6 text-base font-semibold text-primary-foreground transition-colors duration-150 hover:bg-accent-strong-hover active:translate-y-px ${focusRing}`}
              >
                Open Rush Control
                <ArrowRight
                  aria-hidden="true"
                  className="size-4 transition-transform duration-150 group-hover:translate-x-0.5"
                />
              </Link>
              <a
                href="https://github.com/sheshakanthra/sluice"
                className={`inline-flex h-12 cursor-pointer items-center justify-center gap-2 rounded-sm border border-rule-strong bg-card px-6 text-base font-semibold text-navy transition-colors duration-150 hover:border-navy hover:bg-sunk active:translate-y-px ${focusRing}`}
              >
                Source on GitHub
                <ArrowUpRight aria-hidden="true" className="size-4" />
              </a>
            </div>
          </div>

          <RushTicket />
        </section>

        <ResultStrip />
        <EventCards />
        <RouteMap />
      </main>

      <footer className="border-t border-rule bg-card">
        <div className="mx-auto flex w-full max-w-6xl flex-col gap-2 px-4 py-8 text-sm leading-5 text-ink-subtle sm:flex-row sm:items-center sm:justify-between sm:px-8">
          <p>All names in the demo are fictional. MockPay is a simulator: no real money moves.</p>
          <p className="flex flex-wrap gap-x-4">
            <Link href="/how/" className="underline underline-offset-4 hover:text-navy">
              How it works
            </Link>
            <span>MIT licensed · built in public</span>
          </p>
        </div>
      </footer>
    </>
  );
}

function RushTicket() {
  return (
    <TicketStub aria-labelledby="ticket-title" className="w-full lg:mt-6">
      <div className="flex flex-col gap-4 p-6 sm:p-8">
        <div className="flex items-center justify-between gap-4">
          <h2 id="ticket-title" className="sign-label text-ink-muted">
            Rush ticket · demo
          </h2>
          <p className="sign-label tabular text-ink-subtle">Coach C1–C10</p>
        </div>
        <div className="flex flex-col gap-1">
          <p className="flex flex-wrap items-center gap-x-3 gap-y-1 font-display text-2xl leading-8 font-semibold text-ink uppercase">
            <span>Chennai</span>
            <ArrowRight aria-label="to" className="size-5 text-ink-subtle" />
            <span>Bengaluru</span>
          </p>
          <p className="tabular text-sm leading-5 text-ink-muted">Train 40500 · Sluice Demo Special · 500 seats</p>
        </div>
      </div>

      <Perforation />

      <div className="flex flex-col">
        <dl className="flex flex-col gap-1 px-6 pt-6 pb-4 sm:px-8 sm:pt-8">
          <dt className="sign-label text-ink-muted">People in the rush</dt>
          <dd className="tabular font-display text-[4.5rem] leading-none font-semibold text-ink sm:text-[5.5rem]">
            50,000
          </dd>
        </dl>
        <div className="grid grid-cols-[1fr_auto_1fr]">
          <dl className="flex flex-col gap-1 px-6 pt-2 pb-6 sm:px-8 sm:pb-8">
            <dt className="sign-label text-ink-muted">Oversold seats</dt>
            <dd className="tabular font-display text-5xl leading-none font-semibold text-ink">0</dd>
          </dl>
          <PerforationVertical className="my-0 mb-6" />
          <dl className="flex flex-col gap-1 px-6 pt-2 pb-6 sm:px-8 sm:pb-8">
            <dt className="sign-label text-ink-muted">Double charges</dt>
            <dd className="tabular font-display text-5xl leading-none font-semibold text-ink">0</dd>
          </dl>
        </div>
      </div>

      <div className="flex flex-col gap-2 bg-sunk px-6 py-4 sm:flex-row sm:items-center sm:justify-between sm:px-8">
        <p className="tabular font-display text-xl leading-7 font-semibold tracking-wide text-navy" aria-hidden="true">
          50,000 · 0 · 0
        </p>
        <p className="flex items-center gap-2 text-sm leading-5 text-ink-muted">
          <Info aria-hidden="true" className="size-4 shrink-0" />
          Targets until a public CI run proves them
        </p>
      </div>
    </TicketStub>
  );
}

function RouteMap() {
  return (
    <section id="route" aria-labelledby="route-title" className="scroll-mt-8 border-t border-rule bg-card">
      <div className="mx-auto grid w-full max-w-6xl grid-cols-1 gap-12 px-4 py-16 sm:px-8 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)] lg:gap-16 lg:py-24">
        <div className="flex flex-col gap-4 lg:sticky lg:top-8 lg:self-start">
          <p className="sign-label text-accent-strong">The route</p>
          <h2 id="route-title" className="font-display text-4xl leading-[2.75rem] font-semibold text-balance text-ink">
            Four stops between “Book now” and a seat nobody else can own.
          </h2>
          <p className="text-base leading-6 text-pretty text-ink-muted">
            Each stop has one job and one guarantee. Break a guarantee and the audit fails the run.
          </p>
        </div>

        <ol className="flex flex-col">
          {stops.map((stop, i) => {
            const last = i === stops.length - 1;
            return (
              <li key={stop.name} className="grid grid-cols-[40px_1fr] gap-x-4 sm:gap-x-6">
                <div className="flex flex-col items-center">
                  <span
                    className={
                      last
                        ? "tabular flex size-10 items-center justify-center rounded-full bg-signal font-display text-lg font-bold text-ink"
                        : "tabular flex size-10 items-center justify-center rounded-full border-2 border-navy bg-card font-display text-lg font-bold text-navy"
                    }
                  >
                    {i + 1}
                  </span>
                  {!last && <span aria-hidden="true" className="w-0.5 flex-1 bg-navy" />}
                </div>
                <div className={last ? "flex flex-col gap-2 pt-1.5" : "flex flex-col gap-2 pt-1.5 pb-10"}>
                  <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
                    <h3 className="font-display text-2xl leading-8 font-semibold text-ink uppercase">{stop.name}</h3>
                    <span className="sign-label rounded-sm bg-sunk px-2 py-1 text-navy">{stop.guarantee}</span>
                  </div>
                  <p className="max-w-prose text-base leading-6 text-pretty text-ink-muted">{stop.body}</p>
                </div>
              </li>
            );
          })}
        </ol>
      </div>
    </section>
  );
}
