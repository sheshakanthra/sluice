"use client";

// Results charts (DESIGN-BRIEF §3): RPS over time, p95 per route, queue depth,
// seats sold. Loaded with next/dynamic so Recharts never blocks the headline.
// Series differ by colour *and* dash pattern; every chart has a text summary.

import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  LabelList,
  Line,
  LineChart,
  ReferenceLine,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";

import { formatT } from "@/components/dashboard";
import { formatCount } from "@/lib/format";
import { useReducedMotion } from "@/lib/hooks";
import type { LatencyStat, Sample } from "@/lib/replay";

const AXIS = { stroke: "var(--rule-strong)", tick: { fill: "var(--ink-subtle)", fontSize: 12 } } as const;
const GRID = <CartesianGrid stroke="var(--rule)" strokeDasharray="2 4" vertical={false} />;
const tooltipStyle = {
  contentStyle: { background: "var(--card)", border: "1px solid var(--rule-strong)", borderRadius: 4, fontSize: 13 },
  labelStyle: { color: "var(--ink)", fontWeight: 600 },
  itemStyle: { color: "var(--ink)" },
} as const;

interface LegendItem {
  label: string;
  color: string;
  dash?: string;
}

function Frame({
  title,
  summary,
  legend,
  children,
}: {
  title: string;
  summary: string;
  legend?: LegendItem[];
  children: React.ReactElement;
}) {
  return (
    <figure className="flex min-w-0 flex-col gap-3 rounded-xl border border-rule bg-card p-4 sm:p-6">
      <figcaption className="flex flex-col gap-1">
        <span className="font-display text-2xl leading-8 font-semibold text-ink">{title}</span>
        <span className="text-sm leading-5 text-ink-muted">{summary}</span>
      </figcaption>
      <div className="h-56 w-full sm:h-64" aria-hidden="true">
        <ResponsiveContainer width="100%" height="100%">
          {children}
        </ResponsiveContainer>
      </div>
      {legend && (
        <ul className="flex flex-wrap gap-x-6 gap-y-1 text-sm text-ink-muted">
          {legend.map((i) => (
            <li key={i.label} className="flex items-center gap-2">
              <svg aria-hidden="true" width="20" height="8">
                <line x1="0" x2="20" y1="4" y2="4" stroke={i.color} strokeWidth="2" strokeDasharray={i.dash} />
              </svg>
              {i.label}
            </li>
          ))}
        </ul>
      )}
    </figure>
  );
}

function Markers({ soldOutT }: { soldOutT: number | null }) {
  return (
    <>
      <ReferenceLine
        x={0}
        stroke="var(--ink)"
        strokeDasharray="3 3"
        label={{ value: "T0", fill: "var(--ink)", fontSize: 12, position: "insideTopLeft" }}
      />
      {soldOutT !== null && (
        <ReferenceLine
          x={soldOutT}
          stroke="var(--success)"
          strokeDasharray="3 3"
          label={{ value: "Sold out", fill: "var(--success)", fontSize: 12, position: "insideTopRight" }}
        />
      )}
    </>
  );
}

const xAxis = (
  <XAxis
    dataKey="t"
    type="number"
    domain={["dataMin", "dataMax"]}
    tickFormatter={(t: number) => formatT(t)}
    {...AXIS}
    tickCount={6}
  />
);

export default function ResultsCharts({
  samples,
  latency,
  soldOutT,
}: {
  samples: Sample[];
  latency: Record<string, LatencyStat>;
  soldOutT: number | null;
}) {
  const reduced = useReducedMotion();
  const anim = !reduced;
  const peak = samples.reduce((m, s) => (s.rps_total > m.rps_total ? s : m), samples[0]);
  const maxWaiting = samples.reduce((m, s) => Math.max(m, s.waiting), 0);
  const routes = ["status", "join", "hold", "order", "pay", "checkout"]
    .filter((r) => latency[r] && latency[r].n > 0)
    .map((r) => ({ route: r, p95: latency[r].p95, n: latency[r].n }));

  return (
    <div className="grid grid-cols-1 gap-6 lg:grid-cols-2">
      <Frame
        title="Requests per second"
        summary={`Peak ${formatCount(peak.rps_total)} req/s at ${formatT(peak.t)}, of which ${formatCount(peak.rps_status)} were queue-status polls.`}
      >
        <AreaChart accessibilityLayer={false} data={samples} margin={{ top: 16, right: 8, bottom: 0, left: 0 }}>
          {GRID}
          {xAxis}
          <YAxis {...AXIS} width={48} tickFormatter={(v: number) => formatCount(v)} />
          <Tooltip {...tooltipStyle} labelFormatter={(t) => formatT(Number(t))} />
          <Markers soldOutT={soldOutT} />
          <Area
            type="monotone"
            dataKey="rps_total"
            name="All routes"
            stroke="var(--navy)"
            fill="var(--navy)"
            fillOpacity={0.12}
            strokeWidth={2}
            isAnimationActive={anim}
          />
        </AreaChart>
      </Frame>

      <Frame
        title="p95 latency per route"
        summary={`Whole-run p95 from summary.json: ${routes.map((r) => `${r.route} ${r.p95} ms`).join(", ")}.`}
      >
        <BarChart
          accessibilityLayer={false}
          data={routes}
          layout="vertical"
          margin={{ top: 8, right: 56, bottom: 0, left: 8 }}
        >
          <CartesianGrid stroke="var(--rule)" strokeDasharray="2 4" horizontal={false} />
          <XAxis type="number" {...AXIS} tickFormatter={(v: number) => `${v}`} unit=" ms" />
          <YAxis type="category" dataKey="route" {...AXIS} width={64} />
          <Tooltip {...tooltipStyle} formatter={(v) => [`${String(v)} ms`, "p95"]} />
          <Bar dataKey="p95" fill="var(--navy)" radius={[0, 4, 4, 0]} isAnimationActive={anim}>
            <LabelList
              dataKey="p95"
              position="right"
              fill="var(--ink)"
              fontSize={12}
              formatter={(v) => `${String(v)} ms`}
            />
          </Bar>
        </BarChart>
      </Frame>

      <Frame
        title="Queue depth"
        summary={`Up to ${formatCount(maxWaiting)} waiting (server: joined minus admitted, leavers still counted). The swarm's in-system count shows people leaving once they are told "sold out".`}
        legend={[
          { label: "Waiting (server)", color: "var(--navy)" },
          { label: "In system (swarm)", color: "var(--info)", dash: "5 4" },
          { label: "Shopping (server)", color: "var(--warning)", dash: "1 3" },
        ]}
      >
        <LineChart accessibilityLayer={false} data={samples} margin={{ top: 16, right: 8, bottom: 0, left: 0 }}>
          {GRID}
          {xAxis}
          <YAxis {...AXIS} width={48} tickFormatter={(v: number) => formatCount(v)} />
          <Tooltip {...tooltipStyle} labelFormatter={(t) => formatT(Number(t))} />
          <Markers soldOutT={soldOutT} />
          <Line
            type="monotone"
            dataKey="waiting"
            name="Waiting"
            stroke="var(--navy)"
            strokeWidth={2}
            dot={false}
            isAnimationActive={anim}
          />
          <Line
            type="monotone"
            dataKey="in_system"
            name="In system"
            stroke="var(--info)"
            strokeDasharray="5 4"
            strokeWidth={2}
            dot={false}
            isAnimationActive={anim}
          />
          <Line
            type="monotone"
            dataKey="active"
            name="Shopping"
            stroke="var(--warning)"
            strokeDasharray="1 3"
            strokeWidth={2}
            dot={false}
            isAnimationActive={anim}
          />
        </LineChart>
      </Frame>

      <Frame
        title="Seats sold"
        summary={`Sold climbs to ${formatCount(samples.at(-1)?.sold ?? 0)}; held seats either sell or come back to the pool.`}
        legend={[
          { label: "Sold", color: "var(--navy)" },
          { label: "On hold", color: "var(--warning)", dash: "5 4" },
          { label: "Available", color: "var(--ink-subtle)", dash: "1 3" },
        ]}
      >
        <LineChart accessibilityLayer={false} data={samples} margin={{ top: 16, right: 8, bottom: 0, left: 0 }}>
          {GRID}
          {xAxis}
          <YAxis {...AXIS} width={48} />
          <Tooltip {...tooltipStyle} labelFormatter={(t) => formatT(Number(t))} />
          <Markers soldOutT={soldOutT} />
          <Line
            type="stepAfter"
            dataKey="sold"
            name="Sold"
            stroke="var(--navy)"
            strokeWidth={2}
            dot={false}
            isAnimationActive={anim}
          />
          <Line
            type="stepAfter"
            dataKey="held"
            name="On hold"
            stroke="var(--warning)"
            strokeDasharray="5 4"
            strokeWidth={2}
            dot={false}
            isAnimationActive={anim}
          />
          <Line
            type="stepAfter"
            dataKey="available"
            name="Available"
            stroke="var(--ink-subtle)"
            strokeDasharray="1 3"
            strokeWidth={2}
            dot={false}
            isAnimationActive={anim}
          />
        </LineChart>
      </Frame>
    </div>
  );
}
