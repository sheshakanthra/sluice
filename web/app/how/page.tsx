// How it works (DESIGN-BRIEF §3): the architecture diagram, the four mechanisms
// with small sequence illustrations, and the short failure-mode table. Static
// content condensed from docs/ARCHITECTURE.md §1, §2 and §6.

import type { Metadata } from "next";
import type { ReactNode } from "react";

import { SiteHeader } from "@/components/site-header";

export const metadata: Metadata = {
  title: "How it works · SLUICE",
  description: "The waiting room, the Redis fast path, Postgres as the source of truth and idempotent payments.",
};

export default function HowPage() {
  return (
    <>
      <SiteHeader nav current="/how/" />
      <main className="mx-auto flex w-full max-w-6xl flex-1 flex-col gap-24 px-4 pt-8 pb-16 sm:px-8 sm:pt-12">
        <section aria-labelledby="how-title" className="flex flex-col gap-8">
          <div className="flex max-w-3xl flex-col gap-3">
            <p className="sign-label text-accent-strong">How it works</p>
            <h1
              id="how-title"
              className="font-display text-[2.5rem] leading-[1.1] font-semibold text-balance text-ink sm:text-5xl sm:leading-[3.5rem]"
            >
              One front door, three API replicas, and a database that has the last word.
            </h1>
            <p className="text-lg leading-7 text-pretty text-ink-muted">
              The web app is a set of static files. Every request for a seat or a payment goes through Caddy to one of
              three identical API replicas; Redis absorbs the rush, Postgres decides.
            </p>
          </div>
          <ArchitectureDiagram />
        </section>

        <section aria-labelledby="mech-title" className="flex flex-col gap-10">
          <div className="flex max-w-3xl flex-col gap-2">
            <p className="sign-label text-accent-strong">Four mechanisms</p>
            <h2 id="mech-title" className="font-display text-4xl leading-[2.75rem] font-semibold text-balance text-ink">
              Why 50,000 people don&apos;t knock it over
            </h2>
          </div>
          <ol className="grid grid-cols-1 gap-6 md:grid-cols-2">
            <Mechanism
              n={1}
              title="The waiting room absorbs the stampede"
              body="Joining is one cheap Redis call. Polling your place needs no Redis call at all: a signed queue token plus an in-memory snapshot refreshed every 100 ms. Only a few hundred admitted people ever touch the seats."
              steps={[
                ["You", "Join", "Queue"],
                ["Queue", "Signed ticket #41,234", "You"],
                ["You", "Where am I?", "Replica memory"],
                ["Queue", "Your turn: admission token", "You"],
              ]}
            />
            <Mechanism
              n={2}
              title="The Redis fast path sheds contention"
              body="One Lua script holds all your seats or none of them. Someone who loses the race is told in microseconds, without ever reaching Postgres."
              steps={[
                ["You", "Hold C3-27, C3-28", "Redis script"],
                ["Redis script", "Both free: held", "Postgres"],
                ["Rival", "Hold C3-27", "Redis script"],
                ["Redis script", "Taken: try these", "Rival"],
              ]}
            />
            <Mechanism
              n={3}
              title="Postgres is the source of truth"
              body="A unique ticket per seat and conditional updates make an oversell impossible even if Redis is wrong, wiped or stale. Redis can always be rebuilt from Postgres."
              steps={[
                ["Confirm", "Seats still held by hold #h?", "Postgres"],
                ["Postgres", "Yes, all of them: ticket issued", "Confirm"],
                ["Late confirm", "Seats held by hold #old?", "Postgres"],
                ["Postgres", "No: void the payment", "Late confirm"],
              ]}
            />
            <Mechanism
              n={4}
              title="Payments are idempotent at every hop"
              body="Your retry reuses its key, the API's call to the payment provider uses a deterministic key, webhooks are deduplicated, and the database allows one live payment per order. A reconciler sweeps up anything left."
              steps={[
                ["You", "Pay (key k1)", "API"],
                ["API", "Charge (key pay:o1:1)", "MockPay"],
                ["You", "Pay again (key k1)", "API"],
                ["API", "Same answer, no new charge", "You"],
              ]}
            />
          </ol>
        </section>

        <section aria-labelledby="fail-title" className="flex flex-col gap-6">
          <div className="flex max-w-3xl flex-col gap-2">
            <p className="sign-label text-accent-strong">When things break</p>
            <h2 id="fail-title" className="font-display text-4xl leading-[2.75rem] font-semibold text-balance text-ink">
              Failure modes, and why nobody is sold a seat twice
            </h2>
          </div>
          <div
            className="overflow-x-auto rounded-xl border border-rule bg-card"
            tabIndex={0}
            role="region"
            aria-labelledby="fail-title"
          >
            <table className="w-full min-w-[640px] text-left text-sm leading-5">
              <thead className="bg-sunk">
                <tr>
                  {["Failure", "What you'd notice", "Why the guarantees hold"].map((h) => (
                    <th key={h} scope="col" className="sign-label px-4 py-3 text-ink-muted">
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {FAILURES.map(([f, e, w]) => (
                  <tr key={f} className="border-t border-rule align-top">
                    <th scope="row" className="px-4 py-3 font-semibold text-ink">
                      {f}
                    </th>
                    <td className="px-4 py-3 text-ink-muted">{e}</td>
                    <td className="px-4 py-3 text-ink">{w}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>

        <section
          id="run"
          aria-labelledby="run-title"
          className="flex scroll-mt-8 flex-col gap-6 rounded-xl bg-navy p-6 text-white sm:p-10"
        >
          <h2 id="run-title" className="font-display text-4xl leading-[2.75rem] font-semibold">
            Run it yourself in 2 commands
          </h2>
          <p className="max-w-2xl text-base leading-6 text-navy-soft">
            Docker and Task are all you need. Everything runs from open-source images on your machine; no accounts.
          </p>
          <ol className="flex flex-col gap-3 font-mono text-sm">
            <li className="rounded-sm bg-ink px-4 py-3">task web:build</li>
            <li className="rounded-sm bg-ink px-4 py-3">task stack:up</li>
          </ol>
          <p className="text-sm leading-5 text-navy-soft">
            Then open <span className="font-mono text-white">http://localhost:8000</span>: the mode badge turns to Live.
          </p>
        </section>
      </main>
    </>
  );
}

const FAILURES: [string, string, string][] = [
  [
    "Redis wiped mid-rush",
    "The line and seat cache vanish",
    "Postgres is authoritative; inventory is rebuilt from it and the line from snapshots plus your signed token",
  ],
  [
    "Redis down",
    "Joins and holds fail fast and ask you to retry",
    "No write reaches Postgres without a Redis grant, so nothing is half-done",
  ],
  [
    "An API replica dies mid-request",
    "One request fails",
    "Your retry carries the same key and replays or finishes the same work",
  ],
  [
    "The payment provider times out after charging",
    "The page says “confirming with the bank”",
    "The retry uses the same provider key, so it finds the same charge instead of making a new one",
  ],
  [
    "A webhook arrives twice, or never",
    "Nothing",
    "The inbox deduplicates; your page and a reconciler pull the state if it never comes",
  ],
  ["Double tap on Pay", "One payment", "The second request gets the stored answer of the first"],
  [
    "Your hold expires while paying",
    "“You were not charged”",
    "Confirm checks the seats are still held by your exact hold; a late payment is voided",
  ],
  [
    "Two tabs, same person",
    "One hold, one place in line",
    "One active hold per user (a unique index); joining is idempotent",
  ],
];

function Mechanism({
  n,
  title,
  body,
  steps,
}: {
  n: number;
  title: string;
  body: string;
  steps: [string, string, string][];
}) {
  return (
    <li className="flex flex-col gap-4 rounded-xl border border-rule bg-card p-6 sm:p-8">
      <div className="flex items-start gap-4">
        <span className="tabular flex size-10 shrink-0 items-center justify-center rounded-full border-2 border-navy font-display text-lg font-bold text-navy">
          {n}
        </span>
        <h3 className="pt-1 font-display text-2xl leading-8 font-semibold text-ink">{title}</h3>
      </div>
      <p className="text-base leading-6 text-pretty text-ink-muted">{body}</p>
      <ol aria-label="Sequence" className="flex flex-col gap-2 rounded-sm bg-paper p-4">
        {steps.map(([from, msg, to], i) => (
          <li
            key={i}
            className="grid grid-cols-[minmax(0,1fr)_auto_minmax(0,1fr)] items-center gap-2 text-sm leading-5"
          >
            <span className="truncate font-semibold text-navy">{from}</span>
            <span className="flex items-center gap-1 text-ink">
              <span aria-hidden="true" className="h-px w-4 bg-rule-strong" />
              {msg}
              <span aria-hidden="true">→</span>
            </span>
            <span className="truncate text-right font-semibold text-navy">{to}</span>
          </li>
        ))}
      </ol>
    </li>
  );
}

function Box({
  x,
  y,
  w,
  h,
  title,
  sub,
  dark,
}: {
  x: number;
  y: number;
  w: number;
  h: number;
  title: string;
  sub?: string;
  dark?: boolean;
}) {
  return (
    <g>
      <rect
        x={x}
        y={y}
        width={w}
        height={h}
        rx="8"
        fill={dark ? "var(--navy)" : "var(--card)"}
        stroke="var(--navy)"
        strokeWidth="2"
      />
      <text
        x={x + w / 2}
        y={y + (sub ? h / 2 - 4 : h / 2 + 6)}
        textAnchor="middle"
        fontFamily="var(--font-barlow-condensed)"
        fontWeight="600"
        fontSize="20"
        fill={dark ? "#fff" : "var(--ink)"}
      >
        {title}
      </text>
      {sub && (
        <text
          x={x + w / 2}
          y={y + h / 2 + 16}
          textAnchor="middle"
          fontSize="13"
          fill={dark ? "var(--navy-soft)" : "var(--ink-muted)"}
        >
          {sub}
        </text>
      )}
    </g>
  );
}

function Arrow({ d, label, lx, ly }: { d: string; label?: string; lx?: number; ly?: number }): ReactNode {
  return (
    <g>
      <path d={d} fill="none" stroke="var(--ink-muted)" strokeWidth="2" markerEnd="url(#arrow)" />
      {label && (
        <text x={lx} y={ly} fontSize="12" fill="var(--ink-muted)" textAnchor="middle">
          {label}
        </text>
      )}
    </g>
  );
}

function ArchitectureDiagram() {
  return (
    <figure className="flex flex-col gap-3 rounded-xl border border-rule bg-card p-4 sm:p-8">
      <div className="overflow-x-auto">
        <svg
          viewBox="0 0 880 360"
          className="h-auto w-full min-w-[640px]"
          role="img"
          aria-labelledby="arch-title arch-desc"
        >
          <title id="arch-title">SLUICE architecture</title>
          <desc id="arch-desc">
            Browsers and the load-test swarm reach Caddy on port 8000. Caddy serves the static web app, sends /api to
            three API replicas and /pay to MockPay. The replicas use Redis for the queue and the seat fast path and
            Postgres as the source of truth. A worker runs admission ticks, the sweeper, jobs and the reconciler.
          </desc>
          <defs>
            <marker
              id="arrow"
              viewBox="0 0 10 10"
              refX="9"
              refY="5"
              markerWidth="7"
              markerHeight="7"
              orient="auto-start-reverse"
            >
              <path d="M0,0 L10,5 L0,10 z" fill="var(--ink-muted)" />
            </marker>
          </defs>
          <Box x={10} y={60} w={150} h={64} title="Browser" sub="static Next.js app" />
          <Box x={10} y={180} w={150} h={64} title="Swarm" sub="simulated people" />
          <Box x={230} y={120} w={150} h={64} title="Caddy :8000" sub="web · /api · /pay" dark />
          <Box x={460} y={30} w={150} h={52} title="api-1" />
          <Box x={460} y={96} w={150} h={52} title="api-2" />
          <Box x={460} y={162} w={150} h={52} title="api-3" />
          <Box x={460} y={270} w={150} h={64} title="Worker" sub="ticks · sweeper · jobs" />
          <Box x={700} y={40} w={170} h={64} title="Redis 8" sub="queue · fast path" />
          <Box x={700} y={150} w={170} h={64} title="Postgres 18" sub="source of truth" dark />
          <Box x={230} y={270} w={150} h={64} title="MockPay" sub="payment simulator" />
          <Arrow d="M160,92 C195,92 200,140 228,146" />
          <Arrow d="M160,212 C195,212 200,166 228,160" />
          <Arrow d="M380,140 L458,58" label="/api" lx={410} ly={88} />
          <Arrow d="M380,152 L458,122" />
          <Arrow d="M380,164 L458,186" />
          <Arrow d="M305,184 L305,268" label="/pay" lx={328} ly={232} />
          <Arrow d="M610,56 L698,68" />
          <Arrow d="M610,122 L698,176" />
          <Arrow d="M610,188 L698,190" />
          <Arrow d="M610,296 C660,296 680,230 698,206" />
          <Arrow d="M610,300 C700,300 720,130 740,106" />
        </svg>
      </div>
      <figcaption className="text-sm leading-5 text-ink-muted">
        One Go binary runs as api, worker and mockpay. Prometheus and Grafana (not drawn) scrape every service.
      </figcaption>
    </figure>
  );
}
