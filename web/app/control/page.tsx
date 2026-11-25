"use client";

// Rush Control (DESIGN-BRIEF §3, design-system/pages/control.md): the
// departures board. LIVE polls /stats every second; REPLAY scrubs through
// results/latest/timeseries.json and labels every number with its run.

import { useMutation, useQuery } from "@tanstack/react-query";
import { Info, Zap } from "lucide-react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useEffect, useMemo, useRef, useState } from "react";

import {
  EventLog,
  formatT,
  InvariantStub,
  LiveSparkline,
  type LogLine,
  StatTile,
  type Stop,
  TimelineScrubber,
} from "@/components/dashboard";
import { ErrorProblem, Notice } from "@/components/notices";
import { SiteHeader } from "@/components/site-header";
import { SignalDot } from "@/components/ticket-stub";
import { Button, buttonVariants } from "@/components/ui/button";
import { demoRushAvailable, getEvent, getStats, type Stats, startDemoRush } from "@/lib/api";
import { formatCount, formatIST } from "@/lib/format";
import { useReducedMotion } from "@/lib/hooks";
import { useMode } from "@/lib/mode";
import { replayLabel, type Sample, usePlayhead, useSummary, useTimeseries } from "@/lib/replay";
import { cn } from "@/lib/utils";

const DEFAULT_EVENT = "tatkal-chn-sbc";

export default function ControlPage() {
  return (
    <>
      <SiteHeader nav current="/control/" />
      <main className="mx-auto flex w-full max-w-6xl flex-1 flex-col gap-8 px-4 pt-8 pb-16 sm:px-8 sm:pt-12">
        <Suspense fallback={<BoardSkeleton />}>
          <ControlRoute />
        </Suspense>
      </main>
    </>
  );
}

function ControlRoute() {
  const mode = useMode();
  const slug = useSearchParams().get("e") || DEFAULT_EVENT;
  if (mode === "checking") return <BoardSkeleton />;
  return mode === "live" ? <LiveBoard slug={slug} /> : <ReplayBoard />;
}

function BoardSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading Rush Control" className="flex flex-col gap-8">
      <div className="h-24 w-full max-w-xl animate-pulse rounded-sm bg-sunk motion-reduce:animate-none" />
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-3">
        {Array.from({ length: 6 }, (_, i) => (
          <div key={i} className="h-40 animate-pulse rounded-xl bg-sunk motion-reduce:animate-none" />
        ))}
      </div>
    </div>
  );
}

// ---------- shared layout ----------

interface BoardData {
  waiting: number | null;
  admitRate: number | null;
  active: number | null;
  available: number | null;
  held: number | null;
  sold: number | null;
  capacity: number | null;
}

function Counters({ d, admitFoot, soldOut }: { d: BoardData; admitFoot: string; soldOut: React.ReactNode }) {
  return (
    <section aria-labelledby="counters-title" className="flex flex-col gap-4">
      <h2 id="counters-title" className="sr-only">
        Counters
      </h2>
      <div className="grid grid-cols-2 gap-4 lg:grid-cols-3">
        <StatTile label="Waiting in line" value={d.waiting} foot="Joined minus admitted (leavers still counted)" />
        <StatTile label="Admitted per second" value={d.admitRate} foot={admitFoot} tone="navy" />
        <StatTile label="Shopping now" value={d.active} foot="Admitted, choosing or paying" />
        <StatTile
          label="Seats available"
          value={d.available}
          foot={d.capacity !== null ? `of ${formatCount(d.capacity)} seats` : undefined}
        />
        <StatTile label="Seats on hold" value={d.held} foot="Held for a checkout, may come back" />
        <StatTile label="Seats sold" value={d.sold} foot={soldOut} tone="navy" />
      </div>
    </section>
  );
}

function Correctness({
  c,
  checked,
  note,
}: {
  c: { oversold: number | null; duplicate_charges: number | null; orphan_charges: number | null };
  checked: string;
  note: React.ReactNode;
}) {
  return (
    <section aria-labelledby="correct-title" className="flex flex-col gap-4">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <h2 id="correct-title" className="font-display text-3xl leading-10 font-semibold text-ink">
          Correctness
        </h2>
        <p className="text-sm leading-5 text-ink-muted">{note}</p>
      </div>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <InvariantStub label="Oversold seats" value={c.oversold} checked={checked} />
        <InvariantStub label="Duplicate charges" value={c.duplicate_charges} checked={checked} />
        <InvariantStub label="Orphan charges" value={c.orphan_charges} checked={checked} />
      </div>
    </section>
  );
}

// ---------- LIVE ----------

interface LiveSample {
  at: number;
  rttMs: number;
  s: Stats;
}

function LiveBoard({ slug }: { slug: string }) {
  const event = useQuery({ queryKey: ["event", slug], queryFn: () => getEvent(slug), staleTime: 60_000 });
  const [history, setHistory] = useState<LiveSample[]>([]);
  const stats = useQuery({
    queryKey: ["stats", slug],
    refetchInterval: 1000,
    refetchIntervalInBackground: true,
    retry: false,
    queryFn: async () => {
      const t = performance.now();
      const s = await getStats(slug);
      return { s, rttMs: performance.now() - t, at: Date.now() };
    },
  });
  const latest = stats.data;
  useEffect(() => {
    if (!latest) return;
    // Append each fresh poll to the session history (bounded to 2 minutes).
    // eslint-disable-next-line react-hooks/set-state-in-effect -- history is derived from poll results over time
    setHistory((h) => [...h.slice(-119), latest]);
  }, [latest]);
  const demo = useQuery({ queryKey: ["demo-rush-available"], queryFn: demoRushAvailable, staleTime: Infinity });

  const log = useMemo(() => liveLog(history), [history]);
  const s = latest?.s;
  const soldOutAt = useMemo(() => history.find((h) => h.s.event_status === "SOLD_OUT"), [history]);

  if (stats.error && !s) {
    return (
      <>
        <BoardHeading eyebrow="Rush Control · live" title={event.data?.name ?? slug} />
        <ErrorProblem error={stats.error} />
      </>
    );
  }

  const opens = s ? Date.parse(s.sale_opens_at) : null;
  return (
    <>
      <BoardHeading eyebrow="Rush Control · live" title={event.data?.name ?? slug}>
        <p className="flex flex-wrap items-center gap-x-3 gap-y-1 text-sm leading-5 text-ink-muted">
          <SignalDot live />
          <span>
            {s ? `${statusCopy(s.event_status)} · updated ${formatIST(s.ts)}` : "Connecting to /stats…"} · every 1 s
          </span>
        </p>
      </BoardHeading>

      {demo.data && <DemoRush />}

      <Counters
        d={{
          waiting: s?.queue.waiting ?? null,
          admitRate: s ? Math.round(s.queue.admit_rate) : null,
          active: s?.queue.active ?? null,
          available: s?.inventory.available ?? null,
          held: s?.inventory.held ?? null,
          sold: s?.inventory.sold ?? null,
          capacity: s?.inventory.capacity ?? null,
        }}
        admitFoot="The admission controller's current rate"
        soldOut={
          soldOutAt && opens
            ? `Sold out by ${formatT((Date.parse(soldOutAt.s.ts) - opens) / 1000)} (seen by this page)`
            : s && s.event_status !== "SCHEDULED"
              ? `On sale since ${formatIST(s.sale_opens_at)}`
              : s
                ? `Sale opens ${formatIST(s.sale_opens_at)}`
                : undefined
        }
      />

      <Correctness
        c={{
          oversold: s?.correctness.oversold ?? null,
          duplicate_charges: s?.correctness.duplicate_charges ?? null,
          orphan_charges: s?.correctness.orphan_charges ?? null,
        }}
        checked={
          s?.correctness.checked_at
            ? `Checked ${formatIST(s.correctness.checked_at)}`
            : "Not checked yet: the worker checks on-sale events every 5 s"
        }
        note="From the worker's audit of Postgres, refreshed every 5 s while on sale"
      />

      <div className="grid grid-cols-1 gap-8 rounded-xl border border-rule bg-card p-4 sm:p-6 lg:grid-cols-[3fr_2fr]">
        <div className="flex flex-col gap-6">
          <LiveSparkline
            title="/stats round trip, measured by this browser"
            unit="ms"
            series={[{ label: "Round trip", values: history.map((h) => h.rttMs), color: "var(--navy)" }]}
          />
          <p className="flex gap-2 text-sm leading-5 text-ink-muted">
            <Info aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
            Server-side p95 per route is measured by the swarm during a rush; see the replay on{" "}
            <Link href="/results/" className="font-semibold text-navy underline underline-offset-4">
              Results
            </Link>
            .
          </p>
        </div>
        <EventLog
          lines={log}
          empty="Watching for changes. Lines appear when the sale status, seats or checks change."
        />
      </div>
    </>
  );
}

function statusCopy(st: Stats["event_status"]): string {
  return { SCHEDULED: "Not open yet", ON_SALE: "On sale", SOLD_OUT: "Sold out", CLOSED: "Closed" }[st];
}

function liveLog(history: LiveSample[]): LogLine[] {
  const out: LogLine[] = [];
  for (let i = 1; i < history.length; i++) {
    const a = history[i - 1].s;
    const b = history[i].s;
    const at = new Date(b.ts).toLocaleTimeString("en-GB", { timeZone: "Asia/Kolkata", hour12: false });
    const key = `${b.ts}:`;
    if (a.event_status !== b.event_status)
      out.push({ key: key + "st", at, text: `Event is now ${statusCopy(b.event_status)}` });
    if (b.inventory.sold !== a.inventory.sold)
      out.push({
        key: key + "sold",
        at,
        text: `${formatCount(b.inventory.sold)} seats sold (+${b.inventory.sold - a.inventory.sold})`,
      });
    if (b.queue.joined !== a.queue.joined)
      out.push({ key: key + "join", at, text: `${formatCount(b.queue.joined)} people have joined the line` });
    if (b.correctness.checked_at && b.correctness.checked_at !== a.correctness.checked_at)
      out.push({
        key: key + "chk",
        at,
        text: `Audit: oversold ${b.correctness.oversold ?? "—"}, duplicate charges ${b.correctness.duplicate_charges ?? "—"}`,
      });
  }
  return out.reverse().slice(0, 40);
}

function DemoRush() {
  const start = useMutation({ mutationFn: () => startDemoRush(2000, 20) });
  return (
    <Notice
      tone="info"
      icon={<Zap />}
      title="Start a demo rush"
      actions={
        <Button onClick={() => start.mutate()} disabled={start.isPending || start.isSuccess}>
          {start.isSuccess ? "Rush starting in 20 s" : "Start a demo rush"}
        </Button>
      }
    >
      <p>2,000 bots join the Tatkal event, which opens in 20 s. Join the line yourself and compete fairly.</p>
      {start.error && <ErrorProblem error={start.error} />}
    </Notice>
  );
}

// ---------- REPLAY ----------

function ReplayBoard() {
  const ts = useTimeseries();
  const sum = useSummary();
  if (ts.error || sum.error) {
    return (
      <Notice tone="danger" title="The recorded run could not be loaded">
        <p>results/latest/timeseries.json or summary.json is missing from this build.</p>
      </Notice>
    );
  }
  if (!ts.data || !sum.data) return <BoardSkeleton />;
  return (
    <ReplayView
      samples={ts.data.samples}
      label={replayLabel(ts.data.source)}
      note={ts.data.source.note}
      summary={sum.data}
    />
  );
}

function ReplayView({
  samples,
  label,
  note,
  summary,
}: {
  samples: Sample[];
  label: string;
  note: string;
  summary: NonNullable<ReturnType<typeof useSummary>["data"]>;
}) {
  const reduced = useReducedMotion();
  const t0 = Math.max(
    0,
    samples.findIndex((s) => s.t >= 0),
  );
  const ph = usePlayhead(samples.length, t0);
  const started = useRef(false);
  useEffect(() => {
    if (started.current || reduced) return;
    started.current = true;
    ph.setPlaying(true);
  }, [reduced, ph]);

  const cur = samples[ph.index];
  const prev = samples[Math.max(0, ph.index - 1)];
  const stops = useMemo(() => replayStops(samples), [samples]);
  const allLog = useMemo(() => replayLog(samples), [samples]);
  const log = useMemo(() => allLog.filter((l) => l.index <= ph.index).reverse(), [allLog, ph.index]);
  const soldOutIdx = samples.findIndex((s) => s.sold_out === 1);
  const a = summary.audit;

  return (
    <>
      <BoardHeading
        eyebrow="Rush Control · replay"
        title={`${formatCount(summary.config.users)} people, ${formatCount(summary.config.seats)} seats`}
      >
        <p className="sign-label inline-flex w-fit flex-wrap items-center gap-2 rounded-sm bg-sunk px-3 py-2 text-navy">
          {label}
        </p>
        <p className="text-sm leading-5 text-ink-muted">
          {note}. Every number below is a row of that run&apos;s timeseries.csv.
        </p>
      </BoardHeading>

      <TimelineScrubber
        index={ph.index}
        length={samples.length}
        tAt={(i) => samples[i]?.t ?? 0}
        stops={stops}
        playing={ph.playing}
        speed={ph.speed}
        onSeek={ph.seek}
        onToggle={ph.toggle}
        onSpeed={ph.setSpeed}
      />

      <Counters
        d={{
          waiting: cur.waiting,
          admitRate: Math.max(0, cur.cursor - prev.cursor),
          active: cur.active,
          available: cur.available,
          held: cur.held,
          sold: cur.sold,
          capacity: summary.config.seats,
        }}
        admitFoot="Queue cursor advance in this second"
        soldOut={
          soldOutIdx >= 0 && ph.index >= soldOutIdx
            ? `Sold out at ${formatT(samples[soldOutIdx].t)}`
            : `Selling · ${formatT(cur.t)}`
        }
      />

      <Correctness
        c={{
          oversold: a?.oversold ?? null,
          duplicate_charges: a?.duplicate_charges ?? null,
          orphan_charges: a?.orphan_charges ?? null,
        }}
        checked={a ? `Auditor, after settling ${a.settle_s} s · exit ${a.exit_code}` : "No audit in this run"}
        note="The auditor's verdict for the whole run (audit.json), not per second"
      />

      <div className="grid grid-cols-1 gap-8 rounded-xl border border-rule bg-card p-4 sm:p-6 lg:grid-cols-[3fr_2fr]">
        <div className="flex flex-col gap-8">
          <LiveSparkline
            title="p95 latency, swarm-measured"
            unit="ms"
            marker={ph.index}
            series={[
              {
                label: "Checkout",
                values: samples.map((s, i) => (i <= ph.index && s.p95_checkout_ms > 0 ? s.p95_checkout_ms : null)),
                color: "var(--navy)",
              },
              {
                label: "Queue status",
                values: samples.map((s, i) => (i <= ph.index && s.t >= -10 ? s.p95_status_ms : null)),
                color: "var(--info)",
                dashed: true,
              },
            ]}
          />
          <LiveSparkline
            title="Requests per second"
            unit="req/s"
            marker={ph.index}
            series={[
              {
                label: "All routes",
                values: samples.map((s, i) => (i <= ph.index ? s.rps_total : null)),
                color: "var(--navy)",
              },
            ]}
          />
        </div>
        <EventLog lines={log} empty="Press play: lines appear as the recorded rush unfolds." />
      </div>

      <p className="text-sm leading-5 text-ink-muted">
        Want it live?{" "}
        <Link href="/how/#run" className={cn(buttonVariants({ variant: "quiet", size: "md" }), "px-1")}>
          Run the stack in 2 commands
        </Link>
      </p>
    </>
  );
}

function replayStops(samples: Sample[]): Stop[] {
  const find = (p: (s: Sample) => boolean) => samples.findIndex(p);
  const stops: Stop[] = [{ index: 0, label: "Start" }];
  const t0 = find((s) => s.t >= 0);
  if (t0 > 0) stops.push({ index: t0, label: "T0" });
  const first = find((s) => s.sold > 0);
  if (first > t0) stops.push({ index: first, label: "First sale" });
  const out = find((s) => s.sold_out === 1);
  if (out > 0) stops.push({ index: out, label: "Sold out" });
  stops.push({ index: samples.length - 1, label: "End" });
  // Stops closer than 8% of the timeline would draw overlapping buttons (and
  // fail WCAG 2.5.8 target size), so a close stop folds into the one before it.
  const minGap = Math.max(1, Math.round(samples.length * 0.08));
  const merged: Stop[] = [];
  for (const s of stops) {
    const prev = merged.at(-1);
    if (prev && s.index - prev.index < minGap) {
      prev.label = `${prev.label}, ${s.label.toLowerCase()}`;
    } else {
      merged.push({ ...s });
    }
  }
  return merged;
}

function replayLog(samples: Sample[]): (LogLine & { index: number })[] {
  const out: (LogLine & { index: number })[] = [];
  const add = (index: number, key: string, text: string) =>
    out.push({ index, key, at: formatT(samples[index].t), text });
  let joinedFirst = false;
  let t0 = false;
  let firstSale = false;
  let allHeld = false;
  let nextHundred = 100;
  samples.forEach((s, i) => {
    if (!joinedFirst && s.in_system > 0) {
      joinedFirst = true;
      add(i, "join", "Pre-queue opens: the first people arrive");
    }
    if (!t0 && s.t >= 0) {
      t0 = true;
      add(i, "t0", `T0: ${formatCount(s.in_system)} people in the system, lottery decides their order`);
    }
    if (!firstSale && s.sold > 0) {
      firstSale = true;
      add(i, "first", "First ticket sold");
    }
    if (!allHeld && s.available === 0 && s.held > 0) {
      allHeld = true;
      add(i, "held", `No seat free: ${formatCount(s.held)} on hold, the line pauses`);
    }
    while (s.sold >= nextHundred) {
      add(i, `h${nextHundred}`, `${formatCount(nextHundred)} seats sold`);
      nextHundred += 100;
    }
    if (s.sold_out === 1 && !out.some((l) => l.key === "out")) {
      add(i, "out", `Sold out: ${formatCount(s.sold)} seats, each with exactly one owner`);
    }
  });
  return out;
}

function BoardHeading({ eyebrow, title, children }: { eyebrow: string; title: string; children?: React.ReactNode }) {
  return (
    <div className="flex flex-col gap-3">
      <p className="sign-label text-accent-strong">{eyebrow}</p>
      <h1 className="font-display text-[2rem] leading-10 font-semibold text-balance text-ink sm:text-5xl sm:leading-[3.5rem]">
        {title}
      </h1>
      {children}
    </div>
  );
}
