"use client";

// Results (DESIGN-BRIEF §3, design-system/pages/results.md): the audit
// certificate. Every number is a field of results/latest/*.json, which
// scripts/build-replay.mjs copies from a curated run in docs/results/.

import { CircleCheck, CircleX, ExternalLink, FileJson, Info } from "lucide-react";
import dynamic from "next/dynamic";
import Link from "next/link";
import type { ReactNode } from "react";

import { formatT } from "@/components/dashboard";
import { Notice } from "@/components/notices";
import { SiteHeader } from "@/components/site-header";
import { Perforation, TicketStub } from "@/components/ticket-stub";
import { formatCount } from "@/lib/format";
import {
  type BaselineRun,
  RESULTS_BASE,
  replayLabel,
  type RunSummary,
  useBaselines,
  useSummary,
  useTimeseries,
} from "@/lib/replay";
import { cn } from "@/lib/utils";

const REPO = "https://github.com/sheshakanthra/sluice";

const ResultsCharts = dynamic(() => import("@/components/results-charts"), {
  ssr: false,
  loading: () => (
    <div className="grid grid-cols-1 gap-6 lg:grid-cols-2" aria-busy="true" aria-label="Loading charts">
      {Array.from({ length: 4 }, (_, i) => (
        <div key={i} className="h-80 animate-pulse rounded-xl bg-sunk motion-reduce:animate-none" />
      ))}
    </div>
  ),
});

export default function ResultsPage() {
  const sum = useSummary();
  const ts = useTimeseries();
  const base = useBaselines();
  const s = sum.data;
  return (
    <>
      <SiteHeader nav current="/results/" />
      <main className="mx-auto flex w-full max-w-6xl flex-1 flex-col gap-16 px-4 pt-8 pb-16 sm:px-8 sm:pt-12">
        {sum.error ? (
          <Notice tone="danger" title="The results files could not be loaded">
            <p>results/latest/summary.json is missing from this build.</p>
          </Notice>
        ) : !s || !ts.data ? (
          <HeadlineSkeleton />
        ) : (
          <>
            <Headline s={s} label={replayLabel(ts.data.source)} note={ts.data.source.note} />
            <Methodology s={s} />
            <section aria-labelledby="charts-title" className="flex flex-col gap-6">
              <SectionTitle id="charts-title" eyebrow="Charts" title="The rush, second by second">
                One row per second from the swarm&apos;s timeseries.csv. The dashed lines mark T0 and the moment the
                last seat sold.
              </SectionTitle>
              <ResultsCharts samples={ts.data.samples} latency={s.latency_ms} soldOutT={s.timing.sold_out_s ?? null} />
            </section>
          </>
        )}
        {base.data && <BaselineMatrix runs={base.data.runs} />}
        {s && <Environment s={s} />}
      </main>
    </>
  );
}

function HeadlineSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading results" className="flex flex-col gap-6">
      <div className="h-24 w-full max-w-xl animate-pulse rounded-sm bg-sunk motion-reduce:animate-none" />
      <div className="h-64 animate-pulse rounded-xl bg-sunk motion-reduce:animate-none" />
    </div>
  );
}

function SectionTitle({
  id,
  eyebrow,
  title,
  children,
}: {
  id: string;
  eyebrow: string;
  title: string;
  children?: ReactNode;
}) {
  return (
    <div className="flex max-w-3xl flex-col gap-2">
      <p className="sign-label text-accent-strong">{eyebrow}</p>
      <h2 id={id} className="font-display text-4xl leading-[2.75rem] font-semibold text-balance text-ink">
        {title}
      </h2>
      {children && <p className="text-base leading-6 text-pretty text-ink-muted">{children}</p>}
    </div>
  );
}

function Headline({ s, label, note }: { s: RunSummary; label: string; note: string }) {
  const a = s.audit;
  const stat = (k: string, v: ReactNode, sub: ReactNode) => (
    <div className="flex flex-col gap-1 px-6 py-6 sm:px-8">
      <dt className="sign-label text-ink-muted">{k}</dt>
      <dd className="tabular font-display text-5xl leading-none font-semibold text-ink sm:text-6xl">{v}</dd>
      <dd className="text-sm leading-5 text-ink-subtle">{sub}</dd>
    </div>
  );
  return (
    <section aria-labelledby="results-title" className="flex flex-col gap-8">
      <div className="flex flex-col gap-3">
        <p className="sign-label text-accent-strong">Results</p>
        <h1
          id="results-title"
          className="max-w-3xl font-display text-[2.5rem] leading-[1.1] font-semibold text-balance text-ink sm:text-5xl sm:leading-[3.5rem]"
        >
          {formatCount(s.config.users)} people, {formatCount(s.config.seats)} seats, {a?.oversold ?? "—"} sold twice.
        </h1>
        <p className="sign-label inline-flex w-fit flex-wrap items-center gap-2 rounded-sm bg-sunk px-3 py-2 text-navy">
          {label}
        </p>
        <p className="flex max-w-3xl gap-2 text-sm leading-5 text-ink-muted">
          <Info aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
          <span>
            {note}. The 50,000-user headline comes from the CI run; until then this page shows the {s.scenario} run as
            recorded, not scaled.
          </span>
        </p>
      </div>

      <TicketStub aria-label="Headline numbers">
        <dl className="grid grid-cols-1 divide-y divide-rule sm:grid-cols-2 sm:divide-x lg:grid-cols-4 lg:divide-y-0">
          {stat(
            "People in the rush",
            formatCount(s.config.users),
            `peak ${formatCount(s.load.peak_in_system)} in the system at once`,
          )}
          {stat(
            "Seats sold",
            formatCount(s.outcomes.seats_sold),
            `of ${formatCount(s.config.seats)}, sold out at ${s.timing.sold_out_s !== null ? formatT(s.timing.sold_out_s) : "—"}`,
          )}
          {stat(
            "Checkout p95",
            <>
              {s.headline.checkout_p95_ms}
              <span className="ml-1 text-2xl text-ink-muted">ms</span>
            </>,
            "hold → order → pay, swarm-measured",
          )}
          {stat("Peak load", formatCount(s.headline.peak_rps), "requests per second through Caddy")}
        </dl>
        <Perforation />
        <dl className="grid grid-cols-1 sm:grid-cols-3">
          {(
            [
              ["Oversold seats", a?.oversold ?? null],
              ["Duplicate charges", a?.duplicate_charges ?? null],
              ["Orphan charges", a?.orphan_charges ?? null],
            ] as const
          ).map(([k, v]) => (
            <div key={k} className="flex items-center justify-between gap-4 px-6 py-5 sm:px-8">
              <dt className="sign-label text-ink-muted">{k}</dt>
              <dd className="flex items-center gap-2">
                {v === 0 ? (
                  <CircleCheck aria-label="pass" className="size-5 text-success" />
                ) : (
                  <CircleX aria-label="fail" className="size-5 text-danger" />
                )}
                <span className="tabular font-display text-4xl leading-none font-semibold text-ink">{v ?? "—"}</span>
              </dd>
            </div>
          ))}
        </dl>
        <div className="flex flex-col gap-1 bg-sunk px-6 py-4 text-sm leading-5 text-ink-muted sm:flex-row sm:items-center sm:justify-between sm:px-8">
          <p>
            Auditor {a?.passed ? "passed" : "failed"}: exit {a?.exit_code ?? "—"} after settling {a?.settle_s ?? "—"} s
            · unresolved users {a?.unresolved_users ?? "—"}
          </p>
          <p>
            Outcomes: {formatCount(s.outcomes.TICKET)} tickets · {formatCount(s.outcomes.SOLD_OUT)} told sold out ·{" "}
            {formatCount(s.outcomes.PAYMENT_FAILED)} payment failed
          </p>
        </div>
      </TicketStub>
    </section>
  );
}

function Methodology({ s }: { s: RunSummary }) {
  const steps = [
    {
      title: "A real swarm",
      body: `${formatCount(s.config.users)} simulated people, each its own state machine: session, join, poll status, hold, order, pay. ${s.config.arrival ? `Arrivals: ${s.config.arrival}.` : ""} Seed ${s.config.seed ?? "—"}.`,
    },
    {
      title: "The full stack",
      body: `${s.config.api_replicas} API replicas behind Caddy, a worker and MockPay, on Postgres ${s.env.postgres} and Redis ${s.env.redis} with durability on. Holds last ${s.config.hold_ttl_s} s, payment holds ${s.config.payment_hold_ttl_s} s.`,
    },
    {
      title: "Measured at the client",
      body: "Latency is timed by the swarm around each request, so it includes Caddy and the network. p95 is per route over the whole run; checkout is hold + order + pay.",
    },
    {
      title: "Audited afterwards",
      body: "The auditor settles payments, then checks every invariant in Postgres: oversold seats, duplicate and orphan charges, quotas, stuck holds, inventory drift. Any non-zero count fails the run.",
    },
  ];
  return (
    <section
      aria-labelledby="method-title"
      className="grid grid-cols-1 gap-8 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)] lg:gap-16"
    >
      <SectionTitle id="method-title" eyebrow="Methodology" title="How the numbers were made">
        The short version of docs/LOADTEST.md §1–4. Nothing is sampled, scaled or picked from several runs.
      </SectionTitle>
      <ol className="flex flex-col">
        {steps.map((st, i) => (
          <li key={st.title} className="grid grid-cols-[40px_1fr] gap-x-4 sm:gap-x-6">
            <div className="flex flex-col items-center">
              <span className="tabular flex size-10 items-center justify-center rounded-full border-2 border-navy bg-card font-display text-lg font-bold text-navy">
                {i + 1}
              </span>
              {i < steps.length - 1 && <span aria-hidden="true" className="w-0.5 flex-1 bg-navy" />}
            </div>
            <div className={cn("flex flex-col gap-1 pt-1.5", i < steps.length - 1 && "pb-8")}>
              <h3 className="font-display text-2xl leading-8 font-semibold text-ink uppercase">{st.title}</h3>
              <p className="max-w-prose text-base leading-6 text-pretty text-ink-muted">{st.body}</p>
            </div>
          </li>
        ))}
      </ol>
    </section>
  );
}

const SCENARIO_COPY: Record<string, string> = {
  B0: "No locks, no keys",
  B1: "Locks, no idempotency",
  B2: "Locks + idempotency, no queue",
  SMOKE: "Full SLUICE, with the queue",
  H: "Full SLUICE, with the queue",
};

function Cell({ v, good }: { v: number | null | undefined; good: boolean }) {
  if (v === null || v === undefined) return <span className="text-ink-subtle">n/a</span>;
  return (
    <span className={cn("inline-flex items-center gap-1 font-semibold", good ? "text-ink" : "text-danger")}>
      {good ? (
        <CircleCheck aria-hidden="true" className="size-4 text-success" />
      ) : (
        <CircleX aria-hidden="true" className="size-4" />
      )}
      {formatCount(v)}
    </span>
  );
}

function BaselineMatrix({ runs }: { runs: BaselineRun[] }) {
  return (
    <section aria-labelledby="matrix-title" className="flex flex-col gap-6">
      <SectionTitle id="matrix-title" eyebrow="Baselines" title="Take a guarantee away and it breaks">
        The same swarm against deliberately weakened builds. B0–B2 run in direct mode (no waiting room); the B2 rows at
        20k and 50k show where hammering the database directly stops working.
      </SectionTitle>
      <div
        className="overflow-x-auto rounded-xl border border-rule bg-card"
        tabIndex={0}
        role="region"
        aria-labelledby="matrix-title"
      >
        <table className="w-full min-w-[880px] text-left text-sm leading-5">
          <caption className="sr-only">Baseline matrix: invariants and latency per run</caption>
          <thead className="bg-sunk">
            <tr>
              {[
                "Run",
                "Build",
                "Users",
                "Oversold",
                "Duplicate charges",
                "Unresolved users",
                "Audit",
                "Checkout p95",
                "Error rate",
              ].map((h) => (
                <th key={h} scope="col" className="sign-label px-4 py-3 font-semibold whitespace-nowrap text-ink-muted">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {runs.map((r) => (
              <tr key={r.run_id} className="border-t border-rule">
                <th scope="row" className="px-4 py-3 font-semibold whitespace-nowrap text-ink">
                  <span className="tabular">{r.scenario}</span>
                  <span className="block text-xs font-normal text-ink-subtle">{r.run_id}</span>
                </th>
                <td className="px-4 py-3 text-ink-muted">{SCENARIO_COPY[r.scenario] ?? r.scenario}</td>
                <td className="tabular px-4 py-3 text-ink">{formatCount(r.users)}</td>
                <td className="tabular px-4 py-3">
                  <Cell v={r.audit?.oversold} good={r.audit?.oversold === 0} />
                </td>
                <td className="tabular px-4 py-3">
                  <Cell v={r.audit?.duplicate_charges} good={r.audit?.duplicate_charges === 0} />
                </td>
                <td className="tabular px-4 py-3">
                  <Cell v={r.unresolved} good={r.unresolved === 0} />
                </td>
                <td className="px-4 py-3">
                  {r.audit ? (
                    <span className={cn("font-semibold", r.audit.passed ? "text-success" : "text-danger")}>
                      {r.audit.passed ? "Pass" : "Fail"} · exit {r.audit.exit_code}
                    </span>
                  ) : (
                    <span className="text-ink-subtle">n/a</span>
                  )}
                </td>
                <td className="tabular px-4 py-3 text-ink">
                  {r.checkout_p95_ms !== null ? `${formatCount(r.checkout_p95_ms)} ms` : "n/a"}
                </td>
                <td className="tabular px-4 py-3 text-ink">{(r.error_rate * 100).toFixed(2)}%</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="text-sm leading-5 text-ink-muted">
        The B2 20k and 50k rows fail the audit on unresolved users (direct mode saturates and some users never reach an
        outcome), not on oversold seats or double charges. Sources: docs/results/p5b/&lt;run&gt;/summary.json.
      </p>
    </section>
  );
}

function Environment({ s }: { s: RunSummary }) {
  const rows: [string, string][] = [
    ["Runner", s.env.runner === "local" ? "Local laptop (Docker Desktop)" : s.env.runner],
    ["CPUs / RAM", `${s.env.cpus} / ${s.env.ram_gb} GB`],
    ["Go / Postgres / Redis", `${s.env.go} / ${s.env.postgres} / ${s.env.redis}`],
    ["API replicas", String(s.config.api_replicas)],
    ["Commit", s.git_sha],
    ["Started", new Date(s.started_at).toISOString().replace("T", " ").slice(0, 19) + " UTC"],
  ];
  return (
    <section aria-labelledby="env-title" className="grid grid-cols-1 gap-8 lg:grid-cols-2 lg:gap-16">
      <div className="flex flex-col gap-6">
        <SectionTitle id="env-title" eyebrow="Environment" title="Where it ran" />
        <dl className="flex flex-col rounded-xl border border-rule bg-card">
          {rows.map(([k, v]) => (
            <div
              key={k}
              className="flex flex-wrap justify-between gap-x-4 gap-y-1 border-b border-rule px-4 py-3 last:border-b-0 sm:px-6"
            >
              <dt className="text-sm text-ink-muted">{k}</dt>
              <dd className="tabular text-sm font-semibold text-ink">{v}</dd>
            </div>
          ))}
        </dl>
      </div>
      <div className="flex flex-col gap-6">
        <SectionTitle id="raw-title" eyebrow="Raw data" title="Check it yourself" />
        <ul className="flex flex-col gap-3">
          {(["summary.json", "timeseries.json", "baselines.json"] as const).map((f) => (
            <li key={f}>
              <a
                href={`${RESULTS_BASE}/${f}`}
                className="flex min-h-12 cursor-pointer items-center gap-3 rounded-sm border border-rule-strong bg-card px-4 font-semibold text-navy transition-colors duration-150 hover:border-navy hover:bg-sunk focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-navy"
              >
                <FileJson aria-hidden="true" className="size-5" />
                {f}
              </a>
            </li>
          ))}
          <li>
            <a
              href={`${REPO}/tree/main/docs/results`}
              className="flex min-h-12 cursor-pointer items-center gap-3 rounded-sm border border-rule-strong bg-card px-4 font-semibold text-navy transition-colors duration-150 hover:border-navy hover:bg-sunk focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-navy"
            >
              <ExternalLink aria-hidden="true" className="size-5" />
              Curated runs on GitHub
            </a>
          </li>
        </ul>
        <p className="text-sm leading-5 text-ink-muted">
          The CI run link appears here once the headline run is published (P8). Replay it on{" "}
          <Link href="/control/" className="font-semibold text-navy underline underline-offset-4">
            Rush Control
          </Link>
          .
        </p>
      </div>
    </section>
  );
}
