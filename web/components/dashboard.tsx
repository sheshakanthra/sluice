"use client";

// Rush Control and Results building blocks (DESIGN-BRIEF §4): StatTile,
// InvariantStub, LiveSparkline, TimelineScrubber, EventLog. They render what
// they are given; the pages decide whether that is LIVE (/stats) or REPLAY
// (results/latest/*) and say so.

import { CircleCheck, CircleX, Pause, Play, RotateCcw } from "lucide-react";
import { memo, type ReactNode, useId } from "react";

import { TickingNumber } from "@/components/queue-parts";
import { Perforation } from "@/components/ticket-stub";
import { formatCount } from "@/lib/format";
import type { Speed } from "@/lib/replay";
import { cn } from "@/lib/utils";

/** One headline counter on a stub: label above the tear line, numeral below. */
export function StatTile({
  label,
  value,
  unit,
  foot,
  tone = "ink",
  className,
}: {
  label: string;
  value: number | null | undefined;
  unit?: string;
  foot?: ReactNode;
  tone?: "ink" | "navy";
  className?: string;
}) {
  return (
    <div className={cn("flex min-w-0 flex-col rounded-xl border border-rule bg-card", className)}>
      {/* Wraps instead of truncating; on phones two lines are reserved so tiles in a row keep their rules aligned. */}
      <p className="sign-label px-4 pt-4 pb-3 text-balance text-ink-muted max-sm:min-h-[calc(2lh+1.75rem)] sm:px-6">
        {label}
      </p>
      <Perforation />
      <p
        className={cn(
          "flex items-baseline gap-2 px-4 pt-3 font-display text-5xl leading-none font-semibold sm:px-6 sm:text-6xl",
          tone === "navy" ? "text-navy" : "text-ink",
        )}
      >
        {value === null || value === undefined ? (
          <span className="text-ink-subtle" aria-label="no data yet">
            —
          </span>
        ) : (
          <TickingNumber value={formatCount(value)} />
        )}
        {unit && <span className="text-lg font-semibold tracking-wide text-ink-muted">{unit}</span>}
      </p>
      <div className="min-h-10 px-4 pt-2 pb-4 text-sm leading-5 text-ink-subtle sm:px-6">{foot}</div>
    </div>
  );
}

/**
 * One stub per invariant. 0 is the only passing value; a positive count is
 * shown in danger with a cross icon (never colour alone). null = not checked.
 */
export function InvariantStub({ label, value, checked }: { label: string; value: number | null; checked: ReactNode }) {
  const ok = value === 0;
  const bad = value !== null && value > 0;
  return (
    <div className="flex min-w-0 flex-col rounded-xl border border-rule bg-card">
      <div className="flex items-center justify-between gap-2 px-4 pt-4 pb-3 sm:px-6">
        <p className="sign-label text-ink-muted">{label}</p>
        {ok && (
          <span className="sign-label inline-flex items-center gap-1 text-success">
            <CircleCheck aria-hidden="true" className="size-4" /> Pass
          </span>
        )}
        {bad && (
          <span className="sign-label inline-flex items-center gap-1 text-danger">
            <CircleX aria-hidden="true" className="size-4" /> Fail
          </span>
        )}
      </div>
      <Perforation />
      <p
        className={cn(
          "tabular px-4 pt-3 font-display text-6xl leading-none font-semibold sm:px-6",
          bad ? "text-danger" : "text-ink",
        )}
      >
        {value === null ? <span className="text-ink-subtle">—</span> : formatCount(value)}
      </p>
      <p className="px-4 pt-2 pb-4 text-sm leading-5 text-ink-subtle sm:px-6">{checked}</p>
    </div>
  );
}

export interface SparkSeries {
  label: string;
  values: readonly (number | null)[];
  /** CSS colour (a token var). */
  color: string;
  dashed?: boolean;
}

/**
 * A tiny SVG line chart: no axes, a max label and the latest value in text, so
 * it reads without colour. Each series is distinguished by colour *and* dash.
 */
export const LiveSparkline = memo(function LiveSparkline({
  title,
  series,
  unit,
  marker,
  height = 96,
}: {
  title: string;
  series: SparkSeries[];
  unit: string;
  /** Index of a vertical marker (the playhead). */
  marker?: number;
  height?: number;
}) {
  const id = useId();
  const n = Math.max(1, ...series.map((s) => s.values.length));
  const max = Math.max(1, ...series.flatMap((s) => s.values.map((v) => v ?? 0)));
  const w = 300;
  const x = (i: number) => (n <= 1 ? 0 : (i / (n - 1)) * w);
  const y = (v: number) => height - 4 - (v / max) * (height - 8);
  const path = (vals: readonly (number | null)[]) => {
    let d = "";
    let pen = false;
    vals.forEach((v, i) => {
      if (v === null) {
        pen = false;
        return;
      }
      d += `${pen ? "L" : "M"}${x(i).toFixed(1)},${y(v).toFixed(1)}`;
      pen = true;
    });
    return d;
  };
  const latest = (vals: readonly (number | null)[]) => {
    for (let i = vals.length - 1; i >= 0; i--) if (vals[i] !== null) return vals[i];
    return null;
  };
  return (
    <figure className="flex flex-col gap-3" aria-labelledby={`${id}-cap`}>
      <figcaption id={`${id}-cap`} className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
        <span className="sign-label text-ink-muted">{title}</span>
        <span className="tabular text-xs text-ink-subtle">
          max {formatCount(Math.round(max))} {unit}
        </span>
      </figcaption>
      <svg
        viewBox={`0 0 ${w} ${height}`}
        preserveAspectRatio="none"
        className="h-24 w-full overflow-visible"
        role="img"
        aria-label={series
          .map((s) => `${s.label}: latest ${latest(s.values)?.toFixed(1) ?? "no data"} ${unit}`)
          .join("; ")}
      >
        <line x1="0" x2={w} y1={height - 4} y2={height - 4} stroke="var(--rule)" strokeWidth="1" />
        {series.map((s) => (
          <path
            key={s.label}
            d={path(s.values)}
            fill="none"
            stroke={s.color}
            strokeWidth="2"
            strokeDasharray={s.dashed ? "5 4" : undefined}
            vectorEffect="non-scaling-stroke"
            strokeLinejoin="round"
          />
        ))}
        {marker !== undefined && (
          <line
            x1={x(marker)}
            x2={x(marker)}
            y1="0"
            y2={height}
            stroke="var(--ink)"
            strokeWidth="1"
            vectorEffect="non-scaling-stroke"
          />
        )}
      </svg>
      <ul className="flex flex-wrap gap-x-6 gap-y-1 text-sm leading-5 text-ink-muted">
        {series.map((s) => {
          const v = latest(s.values);
          return (
            <li key={s.label} className="flex items-center gap-2">
              <svg aria-hidden="true" width="20" height="8" className="shrink-0">
                <line
                  x1="0"
                  x2="20"
                  y1="4"
                  y2="4"
                  stroke={s.color}
                  strokeWidth="2"
                  strokeDasharray={s.dashed ? "5 4" : undefined}
                />
              </svg>
              {s.label}
              <span className="tabular font-semibold text-ink">
                {v === null ? "—" : `${v < 10 ? v.toFixed(1) : formatCount(Math.round(v))} ${unit}`}
              </span>
            </li>
          );
        })}
      </ul>
    </figure>
  );
});

export interface Stop {
  index: number;
  label: string;
}

/** "T−0:12" / "T+3:51" for a time in seconds relative to T0. */
export function formatT(seconds: number): string {
  const sign = seconds < 0 ? "−" : "+";
  const s = Math.abs(Math.round(seconds));
  return `T${sign}${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/**
 * The replay timeline: a native range input (keyboard, screen readers and
 * touch for free) styled as a platform track, with labelled stops, play/pause
 * and 1× / 4× / 10× speed.
 */
export function TimelineScrubber({
  index,
  length,
  tAt,
  stops,
  playing,
  speed,
  onSeek,
  onToggle,
  onSpeed,
}: {
  index: number;
  length: number;
  tAt: (i: number) => number;
  stops: Stop[];
  playing: boolean;
  speed: Speed;
  onSeek: (i: number) => void;
  onToggle: () => void;
  onSpeed: (s: Speed) => void;
}) {
  const id = useId();
  const pct = (i: number) => (length <= 1 ? 0 : (i / (length - 1)) * 100);
  const atEnd = index >= length - 1;
  return (
    <section
      aria-label="Replay timeline"
      className="flex flex-col gap-4 rounded-xl border border-rule bg-card p-4 sm:p-6"
    >
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <button
            type="button"
            onClick={onToggle}
            aria-label={playing ? "Pause replay" : atEnd ? "Replay from the start" : "Play replay"}
            className="flex size-12 cursor-pointer items-center justify-center rounded-sm bg-navy text-white transition-colors duration-150 hover:bg-ink focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-navy active:translate-y-px"
          >
            {playing ? (
              <Pause aria-hidden="true" className="size-5" />
            ) : atEnd ? (
              <RotateCcw aria-hidden="true" className="size-5" />
            ) : (
              <Play aria-hidden="true" className="size-5" />
            )}
          </button>
          <p className="tabular font-display text-3xl leading-none font-semibold text-ink" aria-live="off">
            {formatT(tAt(index))}
          </p>
        </div>
        <div role="group" aria-label="Replay speed" className="flex rounded-sm border border-rule-strong">
          {([1, 4, 10] as const).map((s) => (
            <button
              key={s}
              type="button"
              aria-pressed={speed === s}
              onClick={() => onSpeed(s)}
              className={cn(
                "tabular h-11 min-w-12 cursor-pointer px-3 text-sm font-semibold transition-colors duration-150 first:rounded-l-sm last:rounded-r-sm focus-visible:z-10 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-navy",
                speed === s ? "bg-navy text-white" : "bg-card text-navy hover:bg-sunk",
              )}
            >
              {s}×
            </button>
          ))}
        </div>
      </div>
      <div className="relative pt-1 pb-8">
        <label htmlFor={`${id}-range`} className="sr-only">
          Replay position
        </label>
        <div aria-hidden="true" className="absolute inset-x-0 top-[13px] h-1.5 rounded-full bg-sunk">
          <div className="h-full rounded-full bg-navy" style={{ width: `${pct(index)}%` }} />
        </div>
        <input
          id={`${id}-range`}
          type="range"
          min={0}
          max={Math.max(0, length - 1)}
          step={1}
          value={index}
          onChange={(e) => onSeek(Number(e.target.value))}
          aria-valuetext={formatT(tAt(index))}
          className="scrubber relative z-10 h-8 w-full cursor-pointer appearance-none bg-transparent"
        />
        {stops.map((s) => (
          <button
            key={s.label}
            type="button"
            onClick={() => onSeek(s.index)}
            className="sign-label absolute top-10 inline-flex min-h-6 min-w-6 -translate-x-1/2 cursor-pointer items-center justify-center rounded-sm px-1 whitespace-nowrap text-ink-subtle hover:text-navy focus-visible:outline-2 focus-visible:outline-navy max-sm:[&:not(:first-of-type):not(:last-of-type)]:hidden"
            style={{ left: `clamp(24px, ${pct(s.index)}%, calc(100% - 24px))` }}
            aria-label={`Jump to ${s.label}, ${formatT(tAt(s.index))}`}
          >
            {s.label}
          </button>
        ))}
      </div>
    </section>
  );
}

export interface LogLine {
  key: string;
  at: string;
  text: string;
}

/** The event log ticker: newest first, a polite live region for new lines. */
export function EventLog({ lines, empty }: { lines: LogLine[]; empty: string }) {
  return (
    <section aria-labelledby="log-title" className="flex min-w-0 flex-col gap-3">
      <h2 id="log-title" className="sign-label text-ink-muted">
        Event log
      </h2>
      {lines.length === 0 ? (
        <p className="rounded-sm border border-dashed border-rule-strong p-4 text-sm leading-5 text-ink-muted">
          {empty}
        </p>
      ) : (
        // The log scrolls, so it must be keyboard-reachable (axe scrollable-region-focusable).
        <div
          role="region"
          aria-labelledby="log-title"
          tabIndex={0}
          className="max-h-72 overflow-y-auto rounded-sm focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-navy"
        >
          <ol aria-live="polite" aria-relevant="additions" className="flex flex-col">
            {lines.map((l, i) => (
              <li
                key={l.key}
                className={cn(
                  "grid grid-cols-[64px_1fr] gap-3 border-b border-rule py-2 text-sm leading-5",
                  i === 0 && "animate-in duration-300 fade-in-0",
                )}
              >
                <span className="tabular font-semibold text-navy">{l.at}</span>
                <span className="text-ink">{l.text}</span>
              </li>
            ))}
          </ol>
        </div>
      )}
    </section>
  );
}
