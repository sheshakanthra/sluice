import { AlertTriangle, CircleSlash, Info, MonitorPlay, ShieldCheck, WifiOff } from "lucide-react";
import Link from "next/link";
import type { ReactNode } from "react";

import { buttonVariants } from "@/components/ui/button";
import { ApiError } from "@/lib/api";
import { cn } from "@/lib/utils";

type Tone = "info" | "warning" | "danger" | "success" | "neutral";

const TONE: Record<Tone, { bar: string; icon: string }> = {
  info: { bar: "border-l-info", icon: "text-info" },
  warning: { bar: "border-l-warning", icon: "text-warning" },
  danger: { bar: "border-l-danger", icon: "text-danger" },
  success: { bar: "border-l-success", icon: "text-success" },
  neutral: { bar: "border-l-navy", icon: "text-navy" },
};

/** A flat notice: tone bar on the left, icon, title, body, optional actions. */
export function Notice({
  tone = "neutral",
  icon,
  title,
  children,
  actions,
  className,
  live,
}: {
  tone?: Tone;
  icon?: ReactNode;
  title: ReactNode;
  children?: ReactNode;
  actions?: ReactNode;
  className?: string;
  live?: "polite" | "assertive";
}) {
  const t = TONE[tone];
  return (
    <div
      role={live === "assertive" ? "alert" : live ? "status" : undefined}
      className={cn("flex gap-4 rounded-sm border border-l-4 border-rule bg-card p-4 sm:p-6", t.bar, className)}
    >
      {icon && (
        <span aria-hidden="true" className={cn("mt-0.5 shrink-0 [&_svg]:size-5", t.icon)}>
          {icon}
        </span>
      )}
      <div className="flex min-w-0 flex-col gap-2">
        <p className="text-lg leading-6 font-semibold text-ink">{title}</p>
        {children && <div className="text-base leading-6 text-pretty text-ink-muted">{children}</div>}
        {actions && <div className="mt-2 flex flex-col gap-3 sm:flex-row sm:flex-wrap">{actions}</div>}
      </div>
    </div>
  );
}

/** DESIGN-BRIEF §5.4: every failure path says plainly whether money moved. */
export function NotChargedNotice({
  heading = "Payment didn't go through",
  seats,
  actions,
}: {
  heading?: string;
  /** What happened to the seats, in one sentence. */
  seats: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <Notice tone="danger" icon={<ShieldCheck />} title={heading} actions={actions} live="polite">
      <p>
        <strong className="font-semibold text-ink">You were not charged.</strong> {seats}
      </p>
    </Notice>
  );
}

interface Human {
  title: string;
  body: string;
}

/** Problem codes as human sentences (API.md §1.3). */
export function humanProblem(err: unknown): Human {
  if (!(err instanceof ApiError)) {
    return { title: "Something went wrong", body: "Nothing was booked or charged. Please try again." };
  }
  const wait = err.retryAfterS !== null ? ` Next try in ${err.retryAfterS}s.` : "";
  switch (err.code) {
    case "NETWORK":
      return {
        title: "Can't reach SLUICE",
        body: "Check your connection. We'll keep trying, and your place is saved on this browser.",
      };
    case "TIMEOUT":
      return {
        title: "The server is slow to answer",
        body: "We'll try again with the same request, so nothing happens twice.",
      };
    case "NO_API":
      return { title: "No live stack here", body: "This address only serves the recorded replay." };
    case "RATE_LIMITED":
      return { title: "One moment", body: `Too many requests from this browser in a short time.${wait}` };
    case "DEPENDENCY_UNAVAILABLE":
      return { title: "Part of SLUICE is briefly unavailable", body: `Nothing was booked or charged.${wait}` };
    case "INTERNAL":
      return {
        title: "Something broke on our side",
        body: `Nothing was booked or charged.${err.problem.request_id ? ` Reference ${err.problem.request_id}.` : ""}`,
      };
    case "TOKEN_EXPIRED":
    case "TOKEN_INVALID":
      return { title: "Your pass has expired", body: "Rejoin the line and we'll pick up from there." };
    case "NOT_FOUND":
      return { title: "We couldn't find that", body: "The link may be old, or it belongs to a different browser." };
    case "QUOTA_EXCEEDED":
      return {
        title: "You've reached the ticket limit",
        body: "Each person can book a limited number of seats for this event.",
      };
    case "HOLD_LIMIT":
      return { title: "You already hold seats for this event", body: "Finish or release that booking first." };
    case "HOLD_EXPIRED":
      return { title: "Your hold ran out", body: "The seats went back to the pool. You were not charged." };
    case "PAYMENT_ATTEMPTS_EXHAUSTED":
      return { title: "That was the last payment attempt", body: "Each booking allows three. You were not charged." };
    case "ORDER_STATE":
      return { title: "This booking has already moved on", body: "Refreshing shows where it stands now." };
    case "REQUEST_IN_PROGRESS":
      return { title: "Still working on your last tap", body: "We'll pick up its answer instead of sending it twice." };
    default:
      return { title: err.problem.title || "Something went wrong", body: err.problem.detail ?? "Please try again." };
  }
}

/** ErrorProblem: a problem+json error rendered as a designed notice. */
export function ErrorProblem({
  error,
  actions,
  className,
}: {
  error: unknown;
  actions?: ReactNode;
  className?: string;
}) {
  const h = humanProblem(error);
  const code = error instanceof ApiError ? error.code : "";
  const icon =
    code === "NETWORK" || code === "TIMEOUT" ? <WifiOff /> : code === "RATE_LIMITED" ? <Info /> : <AlertTriangle />;
  const tone: Tone = code === "RATE_LIMITED" || code === "TIMEOUT" ? "warning" : "danger";
  return (
    <Notice tone={tone} icon={icon} title={h.title} actions={actions} className={className} live="assertive">
      <p>{h.body}</p>
    </Notice>
  );
}

/** Booking pages on a static host (REPLAY): say so, and how to run it live. */
export function ReplayNotice({ what }: { what: string }) {
  return (
    <section aria-labelledby="replay-title" className="flex flex-col gap-6">
      <Notice
        tone="info"
        icon={<MonitorPlay />}
        title={<span id="replay-title">This is the replay site, so {what} isn&apos;t live here</span>}
      >
        <p>
          Booking needs the running stack: the waiting room, seat holds and payments are real services, not mock-ups.
          The Rush Control and Results pages replay a recorded CI run instead.
        </p>
      </Notice>
      <div className="rounded-sm border border-rule bg-card p-4 sm:p-6">
        <p className="sign-label text-ink-muted">Run it yourself in 2 commands</p>
        <ol className="mt-4 flex flex-col gap-3 font-mono text-sm text-ink">
          <li className="rounded-sm bg-sunk px-4 py-3">task web:build</li>
          <li className="rounded-sm bg-sunk px-4 py-3">task stack:up</li>
        </ol>
        <p className="mt-4 text-sm leading-5 text-ink-muted">
          Then open <span className="font-mono text-ink">http://localhost:8000/queue?e=quiet-tuesday</span>.
        </p>
      </div>
      <Link href="/" className={cn(buttonVariants({ variant: "secondary" }), "self-start")}>
        Back to the start
      </Link>
    </section>
  );
}

/** A designed full-panel state (sold out, left the line, no event...). */
export function StatePanel({
  icon = <CircleSlash />,
  eyebrow,
  title,
  children,
  actions,
}: {
  icon?: ReactNode;
  eyebrow: string;
  title: string;
  children?: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <section className="flex flex-col gap-4 rounded-xl border border-rule bg-card p-6 sm:p-8" aria-live="polite">
      <span
        aria-hidden="true"
        className="flex size-12 items-center justify-center rounded-full bg-sunk text-navy [&_svg]:size-6"
      >
        {icon}
      </span>
      <p className="sign-label text-ink-muted">{eyebrow}</p>
      <h1 className="font-display text-4xl leading-10 font-semibold text-balance text-ink">{title}</h1>
      {children && <div className="flex flex-col gap-2 text-base leading-6 text-pretty text-ink-muted">{children}</div>}
      {actions && <div className="mt-2 flex flex-col gap-3 sm:flex-row sm:flex-wrap">{actions}</div>}
    </section>
  );
}
