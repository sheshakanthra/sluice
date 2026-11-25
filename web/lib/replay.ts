"use client";

// REPLAY data (DESIGN-BRIEF §3, DEPLOY.md §A): the files in
// web/public/results/latest/, written by scripts/build-replay.mjs from a curated
// run in docs/results/. Nothing here invents a number: every value shown in
// REPLAY is a field of these files, and the UI labels it with source.run_id.

import { useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useRef, useState } from "react";

export interface ReplaySource {
  kind: "ci" | "local";
  run_id: string;
  git_sha: string;
  scenario: string;
  started_at: string;
  from: string;
  note: string;
}

export interface LatencyStat {
  p50: number;
  p95: number;
  p99: number;
  max: number;
  n: number;
}

export interface RunSummary {
  run_id: string;
  git_sha: string;
  scenario: string;
  started_at: string;
  env: { runner: string; cpus: number; ram_gb: number; go: string; postgres: string; redis: string };
  config: {
    users: number;
    seats: number;
    hold_ttl_s: number;
    payment_hold_ttl_s: number;
    api_replicas: number;
    arrival?: string;
    opens_in_s?: number;
    seed?: number;
    direct?: boolean;
  };
  load: { peak_in_system: number; requests: number; peak_rps: number; mean_rps: number; generator_lag_p99_ms: number };
  outcomes: Record<
    "TICKET" | "SOLD_OUT" | "NO_SEAT" | "PAYMENT_FAILED" | "ABANDONED" | "UNRESOLVED" | "seats_sold",
    number
  >;
  timing: { t0: string; first_sale_s: number; sold_out_s: number | null; drain_s: number };
  latency_ms: Record<string, LatencyStat>;
  errors: { rate: number; "5xx": number; timeouts: number; transport: number };
  business: Record<string, number>;
  audit: {
    mode: string;
    oversold: number | null;
    duplicate_charges: number | null;
    orphan_charges: number | null;
    quota_violations: number | null;
    stuck_holds: number | null;
    inventory_drift: number | null;
    ticket_order_consistency: number | null;
    admitted_after_soldout: number | null;
    unresolved_users: number | null;
    settle_s: number;
    passed: boolean;
    exit_code: number;
  } | null;
  headline: { checkout_p95_ms: number; status_p95_ms: number; peak_rps: number };
}

export const SERIES = [
  "t",
  "rps_total",
  "rps_join",
  "rps_status",
  "rps_hold",
  "rps_order",
  "rps_pay",
  "rps_confirm",
  "p95_status_ms",
  "p95_checkout_ms",
  "errors",
  "in_system",
  "waiting",
  "cursor",
  "offers",
  "active",
  "available",
  "held",
  "sold",
  "sold_out",
] as const;
export type SeriesKey = (typeof SERIES)[number];
export type Sample = Record<SeriesKey, number>;

export interface Timeseries {
  source: ReplaySource;
  t0: string;
  columns: string[];
  /** null = the sampler had no reading that second (never a 0). */
  rows: (number | null)[][];
}

export interface BaselineRun {
  run_id: string;
  scenario: string;
  from: string;
  users: number;
  seats: number;
  queue: boolean;
  git_sha: string;
  tickets: number;
  unresolved: number;
  seats_sold: number;
  audit: {
    mode: string;
    oversold: number | null;
    duplicate_charges: number | null;
    orphan_charges: number | null;
    passed: boolean;
    exit_code: number;
  } | null;
  checkout_p95_ms: number | null;
  error_rate: number;
  peak_rps: number;
  peak_in_system: number;
}

export interface Baselines {
  source: ReplaySource;
  runs: BaselineRun[];
}

export const RESULTS_BASE = "/results/latest";

async function fetchJSON<T>(name: string): Promise<T> {
  const r = await fetch(`${RESULTS_BASE}/${name}`, { cache: "no-cache" });
  if (!r.ok) throw new Error(`${name}: HTTP ${r.status}`);
  return (await r.json()) as T;
}

/**
 * Rows as objects keyed by column name (columns come from the file). Only
 * complete rows are kept: a row with a missing reading (the last second of a
 * run, after the stats sampler stopped) is dropped, never zero-filled.
 */
export function toSamples(ts: Timeseries): Sample[] {
  return ts.rows
    .filter((row) => row.length === ts.columns.length && row.every((v) => v !== null))
    .map((row) => {
      const s = {} as Sample;
      ts.columns.forEach((c, i) => {
        s[c as SeriesKey] = row[i] as number;
      });
      return s;
    });
}

export function useSummary() {
  return useQuery({
    queryKey: ["replay", "summary"],
    queryFn: () => fetchJSON<RunSummary>("summary.json"),
    staleTime: Infinity,
  });
}

export function useTimeseries() {
  return useQuery({
    queryKey: ["replay", "timeseries"],
    queryFn: async () => {
      const ts = await fetchJSON<Timeseries>("timeseries.json");
      return { ...ts, samples: toSamples(ts) };
    },
    staleTime: Infinity,
  });
}

export function useBaselines() {
  return useQuery({
    queryKey: ["replay", "baselines"],
    queryFn: () => fetchJSON<Baselines>("baselines.json"),
    staleTime: Infinity,
  });
}

/** "Replay of local run p5b-smoke · Mon 24 Nov · commit 570aefc". */
export function replayLabel(src: ReplaySource): string {
  const d = new Date(src.started_at).toLocaleDateString("en-IN", {
    weekday: "short",
    day: "numeric",
    month: "short",
    timeZone: "Asia/Kolkata",
  });
  return `Replay of ${src.kind === "ci" ? "CI" : "local"} run ${src.run_id} · ${d} · commit ${src.git_sha}`;
}

export type Speed = 1 | 4 | 10;

/**
 * The replay clock: an index into the 1 s samples that advances `speed`
 * samples per wall-clock second while playing. Under reduced motion it still
 * advances (the numbers are the content), but components skip their tweens.
 */
export function usePlayhead(length: number, startIndex = 0) {
  const [index, setIndex] = useState(startIndex);
  const [playing, setPlaying] = useState(false);
  const [speed, setSpeed] = useState<Speed>(4);
  const acc = useRef(0);

  useEffect(() => {
    if (!playing || length === 0) return;
    let last = performance.now();
    let raf = 0;
    const step = (now: number) => {
      acc.current += ((now - last) / 1000) * speed;
      last = now;
      if (acc.current >= 1) {
        const n = Math.floor(acc.current);
        acc.current -= n;
        setIndex((i) => {
          const next = Math.min(length - 1, i + n);
          if (next === length - 1) setPlaying(false);
          return next;
        });
      }
      raf = requestAnimationFrame(step);
    };
    raf = requestAnimationFrame(step);
    return () => cancelAnimationFrame(raf);
  }, [playing, speed, length]);

  const seek = useCallback(
    (i: number) => {
      acc.current = 0;
      setIndex(Math.max(0, Math.min(length - 1, i)));
    },
    [length],
  );
  const toggle = useCallback(() => {
    setPlaying((p) => {
      if (!p) setIndex((i) => (i >= length - 1 ? 0 : i));
      return !p;
    });
  }, [length]);

  return { index, playing, speed, setSpeed, seek, toggle, setPlaying };
}
