// Seat geometry and the availability bitmap (API.md §3.8, DATA-MODEL §7).

import type { CoachLayout, Layout } from "./api";

export type SeatState = "available" | "held" | "sold";

const STATES: SeatState[] = ["available", "held", "sold", "sold"];

/** Decode "2 bits per seat, MSB first" into one state per seat_no. */
export function decodeStates(b64: string, capacity: number): SeatState[] {
  const bin = atob(b64);
  const out = new Array<SeatState>(capacity);
  for (let i = 0; i < capacity; i++) {
    const byte = bin.charCodeAt(i >> 2);
    out[i] = STATES[(byte >> (6 - 2 * (i & 3))) & 3];
  }
  return out;
}

export type SeatPosition = "window" | "middle" | "aisle";

export interface SeatInfo {
  seatNo: number;
  coach: CoachLayout;
  row: number;
  col: number;
  /** 1-based number inside the coach ("27" in C3-27). */
  number: number;
  label: string;
  position: SeatPosition;
}

export function seatPosition(col: number, layout: Layout): SeatPosition {
  if (col === 1 || col === layout.cols) return "window";
  if (col === layout.aisle_after_col || col === layout.aisle_after_col + 1) return "aisle";
  return "middle";
}

export function seatInfo(seatNo: number, layout: Layout): SeatInfo | null {
  const coach = layout.coaches.find((c) => seatNo >= c.first_seat_no && seatNo < c.first_seat_no + c.seats);
  if (!coach) return null;
  const idx = seatNo - coach.first_seat_no;
  const row = Math.floor(idx / layout.cols) + 1;
  const col = (idx % layout.cols) + 1;
  return {
    seatNo,
    coach,
    row,
    col,
    number: idx + 1,
    label: `${coach.label}-${idx + 1}`,
    position: seatPosition(col, layout),
  };
}

export function seatLabel(seatNo: number, layout: Layout | undefined): string {
  return (layout && seatInfo(seatNo, layout)?.label) ?? `#${seatNo}`;
}

/** Free seats per coach, for the coach tabs' minimap. */
export function coachFree(states: SeatState[] | null, coach: CoachLayout): number | null {
  if (!states) return null;
  let n = 0;
  for (let i = coach.first_seat_no; i < coach.first_seat_no + coach.seats; i++) {
    if (states[i] === "available") n++;
  }
  return n;
}
