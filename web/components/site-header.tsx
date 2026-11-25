"use client";

import Link from "next/link";
import type { ReactNode } from "react";

import { SignalDot } from "@/components/ticket-stub";
import { type Mode, useMode } from "@/lib/mode";
import { cn } from "@/lib/utils";

export const focusRing =
  "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-navy focus-visible:outline-solid";

const NAV = [
  { href: "/control/", label: "Rush Control" },
  { href: "/results/", label: "Results" },
  { href: "/how/", label: "How it works" },
] as const;

/**
 * The navy sign band at the top of every page. Booking pages pass nav={false}:
 * a rider mid-checkout gets no exits that aren't part of the flow.
 */
export function SiteHeader({
  children,
  nav = false,
  current,
}: {
  children?: ReactNode;
  nav?: boolean;
  current?: (typeof NAV)[number]["href"];
}) {
  const links = nav && (
    <ul className="flex items-center gap-1">
      {NAV.map((l) => (
        <li key={l.href}>
          <Link
            href={l.href}
            aria-current={current === l.href ? "page" : undefined}
            className={cn(
              "flex min-h-11 cursor-pointer items-center rounded-sm px-2 text-sm font-semibold text-navy-soft transition-colors duration-150 hover:text-white focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-white sm:px-3",
              current === l.href && "text-white underline decoration-signal decoration-2 underline-offset-8",
            )}
          >
            {l.label}
          </Link>
        </li>
      ))}
    </ul>
  );
  return (
    <header className="bg-navy text-white">
      <div className="mx-auto flex h-14 w-full max-w-6xl items-center justify-between gap-4 px-4 sm:px-8">
        <Link
          href="/"
          aria-label="SLUICE home"
          className="flex min-h-11 cursor-pointer items-center gap-3 rounded-sm focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-white"
        >
          <SignalDot />
          <span className="font-display text-2xl leading-none font-bold tracking-[0.18em]">SLUICE</span>
        </Link>
        <div className="flex items-center gap-3">
          {links && (
            <nav aria-label="Main" className="hidden md:block">
              {links}
            </nav>
          )}
          {children}
          <ModeBadge />
        </div>
      </div>
      {links && (
        <nav aria-label="Main" className="border-t border-white/15 md:hidden">
          <div className="mx-auto w-full max-w-6xl px-2 sm:px-6">{links}</div>
        </nav>
      )}
    </header>
  );
}

const MODE_COPY: Record<Mode, { label: string; title: string }> = {
  checking: { label: "Connecting", title: "Checking whether a live SLUICE stack is reachable" },
  live: { label: "Live", title: "Connected to a running SLUICE stack: every number is real" },
  replay: { label: "Replay", title: "No live stack here: pages show recorded runs, labelled as replays" },
};

/** LIVE vs REPLAY, always visible (DESIGN-BRIEF §3). */
export function ModeBadge({ className }: { className?: string }) {
  const mode = useMode();
  const copy = MODE_COPY[mode];
  return (
    <span
      title={copy.title}
      className={cn(
        "sign-label inline-flex h-7 items-center gap-2 rounded-sm border px-2",
        mode === "live" ? "border-white/40 text-white" : "border-navy-soft/40 text-navy-soft",
        className,
      )}
    >
      {mode === "live" ? (
        <span aria-hidden="true" className="size-2 animate-signal rounded-full bg-signal" />
      ) : (
        <span aria-hidden="true" className="size-2 rounded-full border border-navy-soft" />
      )}
      <span>{copy.label}</span>
      <span className="sr-only">mode</span>
    </span>
  );
}
