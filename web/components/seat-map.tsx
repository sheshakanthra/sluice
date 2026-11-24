"use client";

import { Check, Clock3, X } from "lucide-react";
import { type KeyboardEvent, memo, useCallback, useEffect, useMemo, useRef, useState } from "react";

import type { CoachLayout, Layout } from "@/lib/api";
import { formatCount } from "@/lib/format";
import { coachFree, type SeatPosition, seatPosition, type SeatState, seatInfo } from "@/lib/seats";
import { cn } from "@/lib/utils";

export type CellState = SeatState | "selected" | "yours";

const STATE_WORD: Record<CellState, string> = {
  available: "available",
  held: "held by someone else",
  sold: "sold",
  selected: "selected",
  yours: "held for you",
};

// ---------- SeatCell ----------

interface SeatCellProps {
  seatNo: number;
  number: number;
  coachNumber: number;
  position: SeatPosition;
  state: CellState;
  tabbable: boolean;
  flash: boolean;
  onActivate: (seatNo: number) => void;
  onKey: (seatNo: number, e: KeyboardEvent<HTMLDivElement>) => void;
  register: (seatNo: number, el: HTMLDivElement | null) => void;
}

/** One seat. Memoised: a bitmap update re-renders only the seats that flipped. */
export const SeatCell = memo(function SeatCell({
  seatNo,
  number,
  coachNumber,
  position,
  state,
  tabbable,
  flash,
  onActivate,
  onKey,
  register,
}: SeatCellProps) {
  const interactive = state === "available" || state === "selected";
  return (
    <div
      ref={(el) => register(seatNo, el)}
      role="gridcell"
      tabIndex={tabbable ? 0 : -1}
      aria-selected={state === "selected" || state === "yours"}
      aria-disabled={!interactive}
      aria-label={`Coach ${coachNumber}, seat ${number}, ${position}, ${STATE_WORD[state]}`}
      data-state={state}
      onClick={() => interactive && onActivate(seatNo)}
      onKeyDown={(e) => onKey(seatNo, e)}
      className={cn(
        "relative flex size-11 items-center justify-center rounded-sm border-2 font-display text-base leading-none font-semibold transition-colors duration-150 outline-none select-none sm:size-12",
        "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-navy",
        state === "available" &&
          "cursor-pointer border-rule-strong bg-card text-ink-muted hover:border-navy hover:bg-sunk hover:text-navy",
        state === "selected" && "cursor-pointer border-ink bg-signal text-ink hover:bg-signal/85",
        state === "yours" && "border-accent-strong bg-card text-accent-strong ring-4 ring-signal/40",
        state === "held" && "seat-hatch cursor-not-allowed border-rule text-ink-muted",
        state === "sold" && "cursor-not-allowed border-transparent bg-rule text-ink-subtle",
        flash && "animate-pulse ring-4 ring-danger",
      )}
    >
      {state === "held" ? (
        <span className="flex size-6 items-center justify-center rounded-full bg-card">
          <Clock3 aria-hidden="true" className="size-4" />
        </span>
      ) : state === "sold" ? (
        <X aria-hidden="true" className="size-5" />
      ) : (
        <span aria-hidden="true" className="tabular">
          {number}
        </span>
      )}
      {(state === "selected" || state === "yours") && (
        <span
          aria-hidden="true"
          className="absolute -top-2 -right-2 flex size-5 items-center justify-center rounded-full bg-ink text-white"
        >
          <Check className="size-3.5" strokeWidth={3} />
        </span>
      )}
    </div>
  );
});

// ---------- CoachTabs (with minimaps) ----------

const MINI = { cell: 4, gap: 1, aisle: 3 };

/** A top-down sketch of one coach: one square per seat. Decorative; the tab text carries the count. */
const CoachMinimap = memo(function CoachMinimap({
  coach,
  layout,
  states,
}: {
  coach: CoachLayout;
  layout: Layout;
  states: string;
}) {
  const step = MINI.cell + MINI.gap;
  const w = coach.rows * step;
  const h = layout.cols * step + MINI.aisle;
  const rects = [];
  for (let i = 0; i < coach.seats; i++) {
    const row = Math.floor(i / layout.cols);
    const col = i % layout.cols;
    const y = col * step + (col >= layout.aisle_after_col ? MINI.aisle : 0);
    const s = states[i];
    rects.push(
      <rect
        key={i}
        x={row * step}
        y={y}
        width={MINI.cell}
        height={MINI.cell}
        rx={0.75}
        className={s === "a" ? "fill-navy" : s === "h" ? "fill-rule-strong" : "fill-rule"}
      />,
    );
  }
  return (
    <svg aria-hidden="true" width={w} height={h} viewBox={`0 0 ${w} ${h}`} className="shrink-0">
      {rects}
    </svg>
  );
});

export function CoachTabs({
  layout,
  states,
  active,
  onSelect,
  panelId,
  selectedByCoach,
}: {
  layout: Layout;
  states: SeatState[] | null;
  active: number;
  onSelect: (coach: number) => void;
  panelId: string;
  selectedByCoach: Map<number, number>;
}) {
  const refs = useRef(new Map<number, HTMLButtonElement>());
  const onKey = (e: KeyboardEvent<HTMLButtonElement>, idx: number) => {
    const n = layout.coaches.length;
    let next = -1;
    if (e.key === "ArrowRight") next = (idx + 1) % n;
    else if (e.key === "ArrowLeft") next = (idx - 1 + n) % n;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = n - 1;
    if (next < 0) return;
    e.preventDefault();
    const c = layout.coaches[next].coach;
    onSelect(c);
    refs.current.get(c)?.focus();
  };
  return (
    <div role="tablist" aria-label="Coaches" className="grid grid-cols-5 gap-2 lg:grid-cols-10">
      {layout.coaches.map((c, idx) => {
        const free = coachFree(states, c);
        const sel = selectedByCoach.get(c.coach) ?? 0;
        const isActive = c.coach === active;
        const mini = states
          ? states
              .slice(c.first_seat_no, c.first_seat_no + c.seats)
              .map((s) => s[0])
              .join("")
          : "";
        return (
          <button
            key={c.coach}
            ref={(el) => {
              if (el) refs.current.set(c.coach, el);
              else refs.current.delete(c.coach);
            }}
            type="button"
            role="tab"
            id={`coach-tab-${c.coach}`}
            aria-selected={isActive}
            aria-controls={panelId}
            tabIndex={isActive ? 0 : -1}
            onClick={() => onSelect(c.coach)}
            onKeyDown={(e) => onKey(e, idx)}
            aria-label={`${c.label}, ${free === null ? "loading" : `${free} free`}${sel ? `, ${sel} selected` : ""}`}
            className={cn(
              "relative flex min-h-16 cursor-pointer flex-col items-start gap-1 rounded-sm border-2 px-2 py-2 text-left transition-colors duration-150",
              "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-navy",
              isActive ? "border-navy bg-navy text-white" : "border-rule bg-card text-ink hover:border-navy",
            )}
          >
            <span className="flex w-full items-center justify-between gap-1">
              <span className="font-display text-lg leading-6 font-bold">{c.label}</span>
              {sel > 0 && (
                <span className="tabular flex size-5 items-center justify-center rounded-full bg-signal font-display text-xs font-bold text-ink">
                  {sel}
                </span>
              )}
            </span>
            {mini && (
              <span className={cn("hidden rounded-[2px] p-0.5 sm:block", isActive ? "bg-white" : "bg-paper")}>
                <CoachMinimap coach={c} layout={layout} states={mini} />
              </span>
            )}
            <span className={cn("tabular text-xs leading-4", isActive ? "text-navy-soft" : "text-ink-subtle")}>
              {free === null ? "…" : `${formatCount(free)} free`}
            </span>
          </button>
        );
      })}
    </div>
  );
}

// ---------- SeatGrid: one coach, 3 + aisle + 2 ----------

export function SeatGrid({
  layout,
  coach,
  cellState,
  flashSeats,
  onToggle,
  panelId,
}: {
  layout: Layout;
  coach: CoachLayout;
  cellState: (seatNo: number) => CellState;
  flashSeats: ReadonlySet<number>;
  onToggle: (seatNo: number) => void;
  panelId: string;
}) {
  const cells = useRef(new Map<number, HTMLDivElement>());
  const [focusSeat, setFocusSeat] = useState<number | null>(null);
  const pendingFocus = useRef<number | null>(null);

  const register = useCallback((seatNo: number, el: HTMLDivElement | null) => {
    if (el) cells.current.set(seatNo, el);
    else cells.current.delete(seatNo);
  }, []);

  useEffect(() => {
    if (pendingFocus.current !== null) {
      cells.current.get(pendingFocus.current)?.focus();
      pendingFocus.current = null;
    }
  }, [focusSeat]);

  const first = coach.first_seat_no;
  const cols = layout.cols;
  const inCoach = (n: number | null) => n !== null && n >= first && n < first + coach.seats;
  const tabbable = inCoach(focusSeat)
    ? focusSeat
    : (Array.from({ length: coach.seats }, (_, i) => first + i).find((n) => cellState(n) === "available") ?? first);

  const onKey = useCallback(
    (seatNo: number, e: KeyboardEvent<HTMLDivElement>) => {
      const idx = seatNo - first;
      const row = Math.floor(idx / cols);
      const col = idx % cols;
      const rows = Math.ceil(coach.seats / cols);
      let next: number | null = null;
      switch (e.key) {
        case "ArrowRight":
          next = col < cols - 1 ? idx + 1 : null;
          break;
        case "ArrowLeft":
          next = col > 0 ? idx - 1 : null;
          break;
        case "ArrowDown":
          next = row < rows - 1 ? idx + cols : null;
          break;
        case "ArrowUp":
          next = row > 0 ? idx - cols : null;
          break;
        case "Home":
          next = e.ctrlKey ? 0 : row * cols;
          break;
        case "End":
          next = e.ctrlKey ? coach.seats - 1 : Math.min(coach.seats - 1, row * cols + cols - 1);
          break;
        case "Enter":
        case " ":
          e.preventDefault();
          onToggle(seatNo);
          return;
        default:
          return;
      }
      e.preventDefault();
      if (next === null || next < 0 || next >= coach.seats) return;
      pendingFocus.current = first + next;
      setFocusSeat(first + next);
    },
    [first, cols, coach.seats, onToggle],
  );

  const activate = useCallback(
    (seatNo: number) => {
      setFocusSeat(seatNo);
      onToggle(seatNo);
    },
    [onToggle],
  );

  const rows = Math.ceil(coach.seats / cols);
  const colTemplate = `1.5rem repeat(${layout.aisle_after_col}, auto) 1.5rem repeat(${cols - layout.aisle_after_col}, auto)`;

  return (
    <div
      id={panelId}
      role="tabpanel"
      aria-labelledby={`coach-tab-${coach.coach}`}
      className="flex flex-col items-center gap-4"
    >
      <p className="sign-label flex items-center gap-2 text-ink-muted">
        <span aria-hidden="true" className="h-0.5 w-6 bg-rule-strong" />
        Front of coach {coach.label}
        <span aria-hidden="true" className="h-0.5 w-6 bg-rule-strong" />
      </p>
      <div className="flex flex-col gap-2 rounded-xl border-2 border-rule bg-paper px-3 py-4 sm:px-6">
        <div
          aria-hidden="true"
          className="grid items-end gap-1.5 pb-1 sm:gap-2"
          style={{ gridTemplateColumns: colTemplate }}
          title="W window, M middle, A aisle"
        >
          <span />
          {Array.from({ length: cols }, (_, c) => {
            const pos = seatPosition(c + 1, layout);
            return [
              c === layout.aisle_after_col ? (
                <span key={`ah-${c}`} className="text-center text-[10px] leading-3 text-ink-subtle">
                  aisle
                </span>
              ) : null,
              <span key={c} className="sign-label text-center text-ink-subtle">
                {pos === "window" ? "W" : pos === "aisle" ? "A" : "M"}
              </span>,
            ];
          })}
        </div>
        <div
          role="grid"
          aria-label={`Coach ${coach.label} seats. Use the arrow keys to move, Enter or Space to select.`}
          aria-rowcount={rows}
          aria-colcount={cols}
          className="flex flex-col gap-2"
        >
          {Array.from({ length: rows }, (_, r) => (
            <div
              key={r}
              role="row"
              aria-rowindex={r + 1}
              className="grid items-center gap-1.5 sm:gap-2"
              style={{ gridTemplateColumns: colTemplate }}
            >
              <span role="rowheader" className="tabular text-center text-xs leading-4 font-semibold text-ink-subtle">
                <span className="sr-only">Row </span>
                {r + 1}
              </span>
              {Array.from({ length: cols }, (_, c) => {
                const idx = r * cols + c;
                const aisle = c === layout.aisle_after_col;
                const seatNo = first + idx;
                const info = idx < coach.seats ? seatInfo(seatNo, layout) : null;
                return [
                  aisle ? <span key={`aisle-${c}`} aria-hidden="true" className="h-full" /> : null,
                  info ? (
                    <SeatCell
                      key={seatNo}
                      seatNo={seatNo}
                      number={info.number}
                      coachNumber={coach.coach}
                      position={info.position}
                      state={cellState(seatNo)}
                      tabbable={seatNo === tabbable}
                      flash={flashSeats.has(seatNo)}
                      onActivate={activate}
                      onKey={onKey}
                      register={register}
                    />
                  ) : (
                    <span key={`gap-${c}`} aria-hidden="true" />
                  ),
                ];
              })}
            </div>
          ))}
        </div>
      </div>
      <p className="text-xs leading-4 text-ink-subtle">W window · M middle · A aisle</p>
    </div>
  );
}

// ---------- Legend ----------

export function Legend({ showYours = false }: { showYours?: boolean }) {
  const items: { state: CellState; label: string }[] = [
    { state: "available", label: "Free" },
    { state: "selected", label: "Your pick" },
    { state: "held", label: "On hold" },
    { state: "sold", label: "Sold" },
    ...(showYours ? [{ state: "yours" as const, label: "Held for you" }] : []),
  ];
  return (
    <ul aria-label="Seat legend" className="flex flex-wrap gap-x-6 gap-y-3">
      {items.map((it) => (
        <li key={it.state} className="flex items-center gap-2 text-sm leading-5 text-ink-muted">
          <LegendSwatch state={it.state} />
          {it.label}
        </li>
      ))}
    </ul>
  );
}

function LegendSwatch({ state }: { state: CellState }) {
  return (
    <span
      aria-hidden="true"
      className={cn(
        "relative flex size-6 items-center justify-center rounded-[3px] border-2",
        state === "available" && "border-rule-strong bg-card",
        state === "selected" && "border-ink bg-signal text-ink",
        state === "yours" && "border-accent-strong bg-card text-accent-strong",
        state === "held" && "seat-hatch border-rule",
        state === "sold" && "border-transparent bg-rule text-ink-subtle",
      )}
    >
      {state === "selected" || state === "yours" ? (
        <Check className="size-3.5" strokeWidth={3} />
      ) : state === "held" ? (
        <Clock3 className="size-3 rounded-full bg-card" />
      ) : state === "sold" ? (
        <X className="size-3.5" />
      ) : null}
    </span>
  );
}

/** Seats selected per coach, for the tab badges. */
export function useSelectedByCoach(selection: readonly number[], layout: Layout | undefined): Map<number, number> {
  return useMemo(() => {
    const m = new Map<number, number>();
    if (!layout) return m;
    for (const s of selection) {
      const info = seatInfo(s, layout);
      if (info) m.set(info.coach.coach, (m.get(info.coach.coach) ?? 0) + 1);
    }
    return m;
  }, [selection, layout]);
}
