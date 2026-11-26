"use client";

import { useMutation, useQuery } from "@tanstack/react-query";
import { ArrowRight, CircleCheckBig, Clock3, DoorOpen, Hourglass, PauseCircle, RefreshCw, Ticket } from "lucide-react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useEffectEvent, useState } from "react";

import { EventHeading } from "@/components/event-heading";
import { ErrorProblem, Notice, ReplayNotice, StatePanel } from "@/components/notices";
import { OutlookBadge, outlookCopy, ProgressRail, SeatCounts, TickingNumber } from "@/components/queue-parts";
import { RejoinPanel } from "@/components/rejoin-panel";
import { SiteHeader } from "@/components/site-header";
import { Perforation, SignalDot, TicketStub } from "@/components/ticket-stub";
import { Button, buttonVariants } from "@/components/ui/button";
import { ApiError, getEvent, isApiError, type QueueStatus, queueStatus } from "@/lib/api";
import { etaRange, formatCount, formatCountdown, formatIST, formatMMSS } from "@/lib/format";
import { useEntry, useLocalNow, useServerNow } from "@/lib/hooks";
import { useMode } from "@/lib/mode";
import { backoffMs, stretchMs } from "@/lib/backoff";
import { giveSlotBack, joinLine } from "@/lib/queue";
import { retryAfterMs } from "@/lib/retry";
import { readEntry, writeEntry } from "@/lib/storage";
import { cn } from "@/lib/utils";

export default function QueuePage() {
  return (
    <>
      <SiteHeader />
      <main className="mx-auto flex w-full max-w-2xl flex-1 flex-col gap-8 px-4 pt-8 pb-16 sm:px-8 sm:pt-12">
        <Suspense fallback={<QueueSkeleton />}>
          <QueueRoute />
        </Suspense>
      </main>
    </>
  );
}

function QueueRoute() {
  const slug = useSearchParams().get("e") ?? "";
  if (!slug) return <NoEvent />;
  return <QueueView key={slug} slug={slug} />;
}

const TERMINAL = new Set(["SOLD_OUT", "OFFER_EXPIRED", "CLOSED"]);

function QueueView({ slug }: { slug: string }) {
  const mode = useMode();
  const router = useRouter();
  const [entry] = useEntry(slug);
  const live = mode === "live";

  const event = useQuery({
    queryKey: ["event", slug],
    queryFn: () => getEvent(slug),
    enabled: live,
    staleTime: 60_000,
  });

  const [rejoinLimit, setRejoinLimit] = useState(false);
  const join = useMutation({
    mutationFn: (rejoin: boolean) => joinLine(slug, rejoin),
    onError: (e) => {
      if (isApiError(e, "REJOIN_LIMIT")) setRejoinLimit(true);
    },
  });

  // Auto-join on arrival (join is idempotent per user, so a refresh is safe).
  const wantsJoin = live && !entry.queueToken && !entry.left;
  const { mutate: joinMutate, isIdle: joinIdle } = join;
  useEffect(() => {
    if (wantsJoin && joinIdle) joinMutate(false);
  }, [wantsJoin, joinIdle, joinMutate]);

  const status = useQuery<QueueStatus>({
    queryKey: ["queue", slug],
    queryFn: async () => {
      const token = readEntry(slug).queueToken;
      if (!token) throw new ApiError({ title: "No ticket", status: 401, code: "TOKEN_INVALID" });
      try {
        const r = await queueStatus(slug, token);
        // PREQUEUE → WAITING re-issues the queue token with the drawn ticket.
        const patch: Parameters<typeof writeEntry>[1] = {};
        if (r.queue_token) patch.queueToken = r.queue_token;
        if (r.ticket_no !== null) patch.ticketNo = r.ticket_no;
        if (r.state === "WAITING" && r.position !== undefined && readEntry(slug).firstPosition === undefined) {
          patch.firstPosition = r.position;
        }
        if (r.state === "ADMITTED" && r.admission_token) {
          patch.admissionToken = r.admission_token;
          patch.admissionExpiresAt = r.admission_expires_at;
        }
        if (Object.keys(patch).length) writeEntry(slug, patch);
        return r;
      } catch (e) {
        // An expired or foreign token: forget it and the auto-join asks again.
        if (isApiError(e, "TOKEN_EXPIRED", "TOKEN_INVALID")) writeEntry(slug, { queueToken: undefined });
        throw e;
      }
    },
    enabled: live && !!entry.queueToken && !entry.left,
    retry: false,
    refetchIntervalInBackground: true,
    refetchInterval: (q) => {
      const d = q.state.data;
      if (d && (TERMINAL.has(d.state) || d.state === "ADMITTED") && !q.state.error) return false;
      // A failed poll backs off with full jitter, never sooner than the
      // server's last retry_after_ms or Retry-After (ADR 0017); a good one
      // waits retry_after_ms stretched by up to 10%, never shortened.
      const e = q.state.error;
      if (e) {
        const floor = Math.max(retryAfterMs(e), d?.retry_after_ms ?? 0);
        return backoffMs(q.state.fetchFailureCount - 1, floor);
      }
      return stretchMs(Math.max(500, d?.retry_after_ms ?? 2000));
    },
  });

  const leave = useMutation({ mutationFn: () => giveSlotBack(slug) });

  if (mode === "replay") return <ReplayNotice what="the waiting room" />;

  const s = status.data;
  const state = rejoinLimit ? "REJOIN_LIMIT" : entry.left ? "LEFT" : (s?.state ?? join.data?.state);

  const heading = <EventHeading eyebrow="Waiting room" event={event.data} />;
  const statusError = status.error && !isApiError(status.error, "TOKEN_EXPIRED", "TOKEN_INVALID") ? status.error : null;

  if (state === "LEFT") {
    return (
      <>
        {heading}
        <StatePanel
          icon={<DoorOpen />}
          eyebrow="You left the line"
          title="We've given your place to the next person"
          actions={
            <Button onClick={() => join.mutate(false)} disabled={join.isPending}>
              {join.isPending ? "Joining…" : "Join the line again"}
            </Button>
          }
        >
          <p>Nothing was booked and you were not charged. Joining again puts you at the back of the line.</p>
          {join.error !== null && <ErrorProblem error={join.error} />}
        </StatePanel>
      </>
    );
  }

  if (state === "REJOIN_LIMIT" || state === "OFFER_EXPIRED") {
    return (
      <>
        {heading}
        <RejoinPanel
          rejoinsUsed={entry.rejoinsUsed ?? 0}
          limitReached={state === "REJOIN_LIMIT"}
          reason="Your turn to pick seats ran out before seats were held."
          pending={join.isPending}
          error={join.error && !isApiError(join.error, "REJOIN_LIMIT") ? join.error : null}
          onRejoin={() => join.mutate(true)}
        />
      </>
    );
  }

  if (state === "SOLD_OUT") {
    return (
      <>
        {heading}
        <StatePanel
          icon={<Ticket />}
          eyebrow="Sold out"
          title="Every seat found an owner, and nobody got the same seat twice"
          actions={
            <Link href="/" className={buttonVariants({ variant: "secondary" })}>
              Back to the start
            </Link>
          }
        >
          <p>
            We&apos;re sorry you missed out. You were never charged, and if a seat is released it goes to the next
            person still in the line.
          </p>
        </StatePanel>
      </>
    );
  }

  if (state === "CLOSED") {
    return (
      <>
        {heading}
        <StatePanel icon={<Clock3 />} eyebrow="Sales closed" title="Booking for this departure has closed">
          <p>Nothing was booked and you were not charged.</p>
        </StatePanel>
      </>
    );
  }

  const fatal = !s && (join.error ?? statusError);

  return (
    <>
      {heading}
      {state === "ADMITTED" && s ? (
        <AdmittedStub status={s} onGo={() => router.push(`/book/?e=${encodeURIComponent(slug)}`)} />
      ) : state === "PREQUEUE" ? (
        <PrequeueStub
          opensAt={s?.opens_at ?? join.data?.opens_at}
          updatedAt={status.dataUpdatedAt || join.submittedAt}
          nextAt={s ? status.dataUpdatedAt + s.retry_after_ms : null}
        />
      ) : (state === "WAITING" || state === "PAUSED") && s ? (
        <WaitingStub
          status={s}
          first={entry.firstPosition}
          paused={state === "PAUSED"}
          updatedAt={status.dataUpdatedAt}
        />
      ) : fatal ? null : (
        <QueueSkeleton stubOnly />
      )}

      {fatal && (
        <ErrorProblem
          error={fatal}
          actions={
            <Button variant="secondary" onClick={() => (join.error ? join.mutate(false) : status.refetch())}>
              <RefreshCw aria-hidden="true" /> Try again now
            </Button>
          }
        />
      )}
      {s && statusError && <ErrorProblem error={statusError} />}

      {state === "PAUSED" && (
        <Notice tone="warning" icon={<PauseCircle />} title="All remaining seats are on hold right now" live="polite">
          <p>Some usually come back when holds run out. Stay here: your place is kept.</p>
        </Notice>
      )}
      {state === "WAITING" && s?.outlook && (
        <Notice
          tone={s.outlook === "UNLIKELY" ? "warning" : "neutral"}
          icon={<Hourglass />}
          title="What are my chances?"
        >
          <p>{outlookCopy(s.outlook)}</p>
        </Notice>
      )}

      {(state === "WAITING" || state === "PAUSED" || state === "PREQUEUE") && (
        <LeaveLine pending={leave.isPending} onLeave={() => leave.mutate()} />
      )}
    </>
  );
}

// ---------- stubs ----------

function StubFooter({ updatedAt, nextAt }: { updatedAt: number; nextAt: number | null }) {
  const now = useLocalNow(1000);
  const ago = Math.max(0, Math.round((now - updatedAt) / 1000));
  const nextInMs = nextAt === null ? null : nextAt - now;
  return (
    <div className="flex flex-col gap-2 bg-sunk px-6 py-4 text-sm leading-5 sm:flex-row sm:items-center sm:justify-between sm:px-8">
      <p className="flex items-center gap-2 text-ink-muted">
        <RefreshCw aria-hidden="true" className="size-4 shrink-0" />
        Refreshing won&apos;t lose your place on this browser.
      </p>
      <p className="tabular text-ink-muted">
        Updated {ago <= 1 ? "just now" : `${ago}s ago`}
        {nextInMs !== null && ` · next in ${Math.max(1, Math.ceil(nextInMs / 1000))}s`}
      </p>
    </div>
  );
}

function WaitingStub({
  status,
  first,
  paused,
  updatedAt,
}: {
  status: QueueStatus;
  first: number | undefined;
  paused: boolean;
  updatedAt: number;
}) {
  const position = status.position ?? 0;

  return (
    <TicketStub aria-labelledby="place-label">
      <div className="flex flex-col gap-4 p-6 sm:p-8">
        <div className="flex items-start justify-between gap-4">
          <p id="place-label" className="sign-label flex items-center gap-2 text-ink-muted">
            <SignalDot live={!paused} />
            {paused ? "Line paused" : "Your place in line"}
          </p>
          {status.outlook && <OutlookBadge outlook={status.outlook} />}
        </div>
        <p
          className="font-display text-[5.5rem] leading-none font-semibold tracking-[-0.01em] text-ink sm:text-[8rem]"
          aria-live="off"
        >
          <TickingNumber value={formatCount(position)} prefix="#" />
        </p>
        <PositionAnnouncer position={position} ahead={status.ahead ?? 0} />
        {status.ticket_no !== null && (
          <p className="tabular text-sm leading-5 text-ink-subtle">Ticket {formatCount(status.ticket_no)}</p>
        )}
      </div>
      <Perforation />
      <div className="flex flex-col gap-8 p-6 sm:p-8">
        <dl className="grid grid-cols-2 gap-4">
          <div className="flex flex-col gap-1">
            <dt className="sign-label text-ink-muted">People ahead</dt>
            <dd className="tabular font-display text-4xl leading-10 font-semibold text-ink">
              {formatCount(status.ahead ?? 0)}
            </dd>
          </div>
          <div className="flex flex-col gap-1">
            <dt className="sign-label text-ink-muted">Estimated wait</dt>
            <dd className="font-display text-4xl leading-10 font-semibold text-ink">
              {paused ? "Paused" : status.eta_s !== undefined ? etaRange(status.eta_s) : "—"}
            </dd>
          </div>
        </dl>
        <ProgressRail first={first} position={status.position} />
        <div className="flex flex-col gap-3">
          <p className="sign-label text-ink-muted">Seats on this train</p>
          <SeatCounts counts={status.counts} />
        </div>
      </div>
      <StubFooter updatedAt={updatedAt} nextAt={updatedAt + status.retry_after_ms} />
    </TicketStub>
  );
}

/** aria-live updates, throttled to one per 15 s (DESIGN-BRIEF §6). */
function PositionAnnouncer({ position, ahead }: { position: number; ahead: number }) {
  const [said, setSaid] = useState<{ at: number; text: string }>({ at: 0, text: "" });
  const text = `You are number ${formatCount(position)} in line, ${formatCount(ahead)} people ahead.`;
  useEffect(() => {
    const due = said.at + 15_000 - Date.now();
    const t = window.setTimeout(() => setSaid({ at: Date.now(), text }), Math.max(0, due));
    return () => window.clearTimeout(t);
  }, [text, said.at]);
  return (
    <p className="sr-only" aria-live="polite">
      {said.text}
    </p>
  );
}

function PrequeueStub({
  opensAt,
  updatedAt,
  nextAt,
}: {
  opensAt: string | undefined;
  updatedAt: number;
  nextAt: number | null;
}) {
  const now = useServerNow(250);
  const left = opensAt ? Date.parse(opensAt) - now : null;
  return (
    <TicketStub aria-labelledby="prequeue-label">
      <div className="flex flex-col gap-4 p-6 sm:p-8">
        <p id="prequeue-label" className="sign-label flex items-center gap-2 text-info">
          <SignalDot live /> Pre-queue · line opens in
        </p>
        <p className="tabular font-display text-[5.5rem] leading-none font-semibold tracking-[-0.01em] text-ink sm:text-[8rem]">
          {left === null ? "--:--" : formatCountdown(left)}
        </p>
        {opensAt && <p className="tabular text-base leading-6 text-ink-muted">at {formatIST(opensAt)}</p>}
      </div>
      <Perforation />
      <div className="flex flex-col gap-4 p-6 sm:p-8">
        <p className="text-lg leading-7 text-pretty text-ink">
          You&apos;re in the pre-queue. When the line opens, everyone here gets a{" "}
          <strong className="font-semibold">random place in line</strong>, so arriving earlier doesn&apos;t help, but
          don&apos;t be late.
        </p>
        <p className="text-base leading-6 text-ink-muted">
          Keep this tab open. Your ticket number appears here the moment the draw happens.
        </p>
      </div>
      <StubFooter updatedAt={updatedAt} nextAt={nextAt} />
    </TicketStub>
  );
}

function AdmittedStub({ status, onGo }: { status: QueueStatus; onGo: () => void }) {
  const now = useServerNow(250);
  const [count, setCount] = useState(3);
  const go = useEffectEvent(onGo);
  useEffect(() => {
    if (count <= 0) {
      go();
      return;
    }
    const t = window.setTimeout(() => setCount((c) => c - 1), 1000);
    return () => window.clearTimeout(t);
  }, [count]);
  const left = status.admission_expires_at ? Date.parse(status.admission_expires_at) - now : null;

  return (
    <TicketStub aria-labelledby="admitted-label" className="border-2 border-navy">
      <div className="flex flex-col gap-4 bg-navy p-6 text-white sm:p-8">
        <p id="admitted-label" className="sign-label flex items-center gap-2 text-navy-soft">
          <SignalDot live /> Your turn
        </p>
        <p className="font-display text-[4rem] leading-none font-semibold sm:text-[6rem]" role="status">
          It&apos;s your turn
        </p>
        <p className="tabular text-lg leading-7 text-navy-soft">
          {left !== null && left > 0 ? <>You have {formatMMSS(left)} to pick seats.</> : <>Pick your seats now.</>}
        </p>
      </div>
      <Perforation />
      <div className="flex flex-col gap-4 p-6 sm:p-8">
        <Button size="block" onClick={onGo}>
          <CircleCheckBig aria-hidden="true" />
          Pick seats now
          <ArrowRight aria-hidden="true" />
        </Button>
        <p className="tabular text-center text-sm leading-5 text-ink-muted" aria-live="polite">
          {count > 0 ? `Opening the seat map in ${count}…` : "Opening the seat map…"}
        </p>
      </div>
    </TicketStub>
  );
}

function LeaveLine({ pending, onLeave }: { pending: boolean; onLeave: () => void }) {
  const [confirming, setConfirming] = useState(false);
  return (
    <div className="flex flex-col gap-3 border-t border-rule pt-6">
      {!confirming ? (
        <Button variant="quiet" size="md" className="self-start" onClick={() => setConfirming(true)}>
          <DoorOpen aria-hidden="true" /> I&apos;ve changed my mind: leave the line
        </Button>
      ) : (
        <div className="flex flex-col gap-3" role="group" aria-label="Confirm leaving the line">
          <p className="text-base leading-6 text-ink">
            Leave the line? Your place goes to the next person and you&apos;d join again at the back.
          </p>
          <div className="flex flex-col gap-3 sm:flex-row">
            <Button variant="secondary" size="md" onClick={onLeave} disabled={pending}>
              {pending ? "Leaving…" : "Yes, leave the line"}
            </Button>
            <Button variant="quiet" size="md" onClick={() => setConfirming(false)} autoFocus>
              Stay in line
            </Button>
          </div>
        </div>
      )}
    </div>
  );
}

// ---------- fallbacks ----------

function QueueSkeleton({ stubOnly = false }: { stubOnly?: boolean }) {
  return (
    <div className="flex flex-col gap-8" aria-busy="true">
      {!stubOnly && <EventHeading eyebrow="Waiting room" event={undefined} />}
      <TicketStub>
        <div className="flex flex-col gap-4 p-6 sm:p-8">
          <p className="sign-label text-ink-muted">Finding your place in line</p>
          <span aria-hidden="true" className="h-[5.5rem] w-3/4 animate-pulse rounded-sm bg-sunk sm:h-32" />
        </div>
        <Perforation />
        <div className="flex flex-col gap-4 p-6 sm:p-8">
          <span aria-hidden="true" className="h-10 w-full animate-pulse rounded-sm bg-sunk" />
          <span aria-hidden="true" className="h-3 w-full animate-pulse rounded-full bg-sunk" />
        </div>
      </TicketStub>
    </div>
  );
}

function NoEvent() {
  return (
    <StatePanel
      eyebrow="Waiting room"
      title="Which train are you waiting for?"
      actions={
        <>
          <Link href="/queue/?e=quiet-tuesday" className={cn(buttonVariants())}>
            Quiet Tuesday
          </Link>
          <Link href="/queue/?e=tatkal-chn-sbc" className={cn(buttonVariants({ variant: "secondary" }))}>
            Tatkal rush
          </Link>
        </>
      }
    >
      <p>This link is missing its event. Pick one of the demo departures.</p>
    </StatePanel>
  );
}
