"use client";

// LIVE vs REPLAY (DESIGN-BRIEF §3, "Mode detection"): LIVE only when
// /api/v1/healthz answers JSON with status "ok" within 1.5 s. A timeout, a
// 404 or a static host's HTML fallback all mean REPLAY. Detected once per
// page load and shared by every component.

import { useEffect, useState } from "react";

import { healthz } from "./api";

export type Mode = "checking" | "live" | "replay";

let detection: Promise<Exclude<Mode, "checking">> | null = null;

export function detectMode(): Promise<Exclude<Mode, "checking">> {
  detection ??= healthz(1500).then(
    (h) => (h?.status === "ok" ? "live" : "replay"),
    () => "replay",
  );
  return detection;
}

export function useMode(): Mode {
  const [mode, setMode] = useState<Mode>("checking");
  useEffect(() => {
    let alive = true;
    void detectMode().then((m) => {
      if (alive) setMode(m);
    });
    return () => {
      alive = false;
    };
  }, []);
  return mode;
}
