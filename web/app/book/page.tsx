"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, DoorOpen, Minus, PauseCircle, Plus, Sparkles, Ticket, UserX, X } from "lucide-react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useCallback, useMemo, useState } from "react";

import { EventHeading } from "@/components/event-heading";
import { ErrorProblem, Notice, ReplayNotice, StatePanel } from "@/components/notices";
import { RejoinPanel } from "@/components/rejoin-panel";
import { CoachTabs, type CellState, Legend, SeatGrid, useSelectedByCoach } from "@/components/seat-map";
import { SiteHeader } from "@/components/site-header";
import { Perforation, TicketStub } from "@/components/ticket-stub";
import { Button, buttonVariants } from "@/components/ui/button";
import {
  ApiError,
  createHold,
  createOrder,
  type EventDetail,
  getAvailability,
  getEvent,
  isApiError,
  queueStatus,
  releaseHold,
} from "@/lib/api";
import { formatCount, formatINR, formatMMSS } from "@/lib/format";
import { useEntry, useServerNow } from "@/lib/hooks";
import { withIntent } from "@/lib/intent";
import { useMode } from "@/lib/mode";
import { giveSlotBack, joinLine } from "@/lib/queue";
import { decodeStates, type SeatState, seatInfo, seatLabel } from "@/lib/seats";
import { readEntry, writeEntry, writeOrderMeta } from "@/lib/storage";
import { cn } from "@/lib/utils";

export default function BookPage() {
  return (
    <>
      <SiteHeader />
      <Suspense fallback={<BookSkeleton />}>
        <BookRoute />
      </Suspense>
    </>
  );
}

function BookRoute() {
  const slug = useSearchParams().get("e") ?? "";
  if (!slug) {
    return (
      <Main narrow>
        <StatePanel eyebrow="Seat selection" title="This link is missing its event">
          <p>
            Start from the{" "}
            <Link className="font-semibold text-navy underline underline-offset-4" href="/queue/?e=quiet-tuesday">
              Quiet Tuesday waiting room
            </Link>
            .
          </p>
        </StatePanel>
      </Main>
    );
  }
  return <BookView key={slug} slug={slug} />;
}

function Main({ children, narrow = false }: { children: React.ReactNode; narrow?: boolean }) {
  return (
    <main
      className={cn(
        "mx-auto flex w-full flex-1 flex-col gap-8 px-4 pt-8 pb-32 sm:px-8 sm:pt-12 lg:pb-16",
        narrow ? "max-w-2xl" : "max-w-6xl",
      )}
    >
      {children}
    </main>
  );
}

interface AvailState {
  version: number;
  etag: string | null;
  states: SeatState[];
  available: number;
  held: number;
  sold: number;
}

type Ending = "SOLD_OUT" | "ALL_HELD" | "LEFT" | "EXPIRED" | "REJOIN_LIMIT" | null;

function BookView({ slug }: { slug: string }) {
  const mode = useMode();
  const router = useRouter();
  const qc = useQueryClient();
  const [entry] = useEntry(slug);
  const live = mode === "live";
  const admitted = !!entry.admissionToken;

  const [ending, setEnding] = useState<Ending>(null);
  const [selection, setSelection] = useState<number[]>([]);
  const [quantity, setQuantity] = useState(2);
  const [coach, setCoach] = useState(1);
  const [flash, setFlash] = useState<ReadonlySet<number>>(new Set());
  const [announce, setAnnounce] = useState("");

  const event = useQuery({
    queryKey: ["event", slug],
    queryFn: () => getEvent(slug),
    enabled: live,
    staleTime: 60_000,
  });
  const ev = event.data;
  const maxSeats = Math.min(4, ev?.max_per_user ?? 4);

  const avail = useQuery<AvailState>({
    queryKey: ["avail", slug],
    enabled: live && !!ev && admitted && !ending,
    refetchInterval: 1000,
    refetchIntervalInBackground: true,
    retry: false,
    queryFn: async () => {
      const prev = qc.getQueryData<AvailState>(["avail", slug]);
      const r = await getAvailability(slug, prev?.etag ?? null);
      if (!r) {
        if (prev) return prev;
        throw new ApiError({ title: "No availability yet", status: 0, code: "NETWORK" });
      }
      return {
        version: r.data.version,
        etag: r.etag,
        states: decodeStates(r.data.states, ev!.capacity),
        ...r.data.counts,
      };
    },
  });
  const states = avail.data?.states ?? null;

  // Watch the turn: a lapsed admission shows up as OFFER_EXPIRED on the status poll.
  const watch = useQuery({
    queryKey: ["queue-watch", slug],
    enabled: live && admitted && !!entry.queueToken && !ending,
    retry: false,
    refetchIntervalInBackground: true,
    refetchInterval: (q) => Math.max(2000, q.state.data?.retry_after_ms ?? 5000),
    queryFn: () => queueStatus(slug, readEntry(slug).queueToken!),
  });
  const watchState = watch.data?.state;

  const now = useServerNow(500);
  const capLeft = entry.admissionExpiresAt ? Date.parse(entry.admissionExpiresAt) - now : null;

  // Seats that were in the selection but flipped away since: derived, not stored.
  const lost = useMemo(() => (states ? selection.filter((s) => states[s] !== "available") : []), [selection, states]);
  const picked = useMemo(() => selection.filter((s) => !lost.includes(s)), [selection, lost]);

  const onToggle = useCallback(
    (seatNo: number) => {
      setSelection((sel) => {
        if (sel.includes(seatNo)) {
          setAnnounce(`Removed ${seatLabel(seatNo, ev?.layout)}. ${sel.length - 1} of ${maxSeats} seats selected.`);
          return sel.filter((s) => s !== seatNo);
        }
        if (sel.length >= maxSeats) {
          setAnnounce(`You can pick up to ${maxSeats} seats. Remove one first.`);
          return sel;
        }
        const info = ev ? seatInfo(seatNo, ev.layout) : null;
        setAnnounce(
          `Selected ${info?.label ?? seatNo}${info ? `, ${info.position}` : ""}. ${sel.length + 1} of ${maxSeats} seats selected.`,
        );
        return [...sel, seatNo];
      });
    },
    [ev, maxSeats],
  );

  const flashSeats = (seats: number[]) => {
    setFlash(new Set(seats));
    window.setTimeout(() => setFlash(new Set()), 2400);
  };

  const rejoin = useMutation({
    mutationFn: () => joinLine(slug, true),
    onSuccess: () => router.push(`/queue/?e=${encodeURIComponent(slug)}`),
    onError: (e) => {
      if (isApiError(e, "REJOIN_LIMIT")) setEnding("REJOIN_LIMIT");
    },
  });
  const joinAgain = useMutation({
    mutationFn: () => joinLine(slug, false),
    onSuccess: () => router.push(`/queue/?e=${encodeURIComponent(slug)}`),
  });

  const book = useMutation({
    mutationFn: async (body: { seat_nos: number[] } | { quantity: number }) => {
      const token = readEntry(slug).admissionToken!;
      const hold = await withIntent(`hold:${slug}:${JSON.stringify(body)}`, (ik) => createHold(slug, token, body, ik));
      writeEntry(slug, { holdId: hold.hold_id });
      try {
        const order = await withIntent(`order:${hold.hold_id}`, (ik) => createOrder(hold.hold_id, token, ik));
        return { hold, order };
      } catch (e) {
        // The turn lapsed between the hold and the order: give the seats back now.
        if (isApiError(e, "ADMISSION_EXPIRED")) {
          await releaseHold(hold.hold_id, token).catch(() => undefined);
          writeEntry(slug, { holdId: undefined });
        }
        throw e;
      }
    },
    onSuccess: ({ hold, order }) => {
      writeOrderMeta(order.order_id, { slug, labels: hold.labels });
      writeEntry(slug, { orderId: order.order_id });
      router.push(`/checkout/?o=${encodeURIComponent(order.order_id)}`);
    },
    onError: async (e) => {
      if (!(e instanceof ApiError)) return;
      switch (e.code) {
        case "SEATS_TAKEN": {
          const taken = e.problem.taken ?? [];
          flashSeats(taken);
          setSelection((sel) => sel.filter((s) => !taken.includes(s)));
          void avail.refetch();
          break;
        }
        case "SOLD_OUT":
          await giveSlotBack(slug);
          setEnding("SOLD_OUT");
          break;
        case "INSUFFICIENT_SEATS":
          if ((e.problem.available ?? 0) === 0) {
            await giveSlotBack(slug);
            setEnding("ALL_HELD");
          } else {
            setQuantity(Math.max(1, Math.min(maxSeats, e.problem.available ?? 1)));
          }
          break;
        case "ADMISSION_EXPIRED":
          setEnding("EXPIRED");
          break;
      }
    },
  });

  const leave = useMutation({
    mutationFn: () => giveSlotBack(slug),
    onSuccess: () => setEnding("LEFT"),
  });

  const selectedByCoach = useSelectedByCoach(picked, ev?.layout);

  if (mode === "replay") {
    return (
      <Main narrow>
        <ReplayNotice what="seat selection" />
      </Main>
    );
  }

  const soldOutNow = avail.data && avail.data.available === 0 && avail.data.held === 0;
  const effectiveEnding: Ending =
    ending ??
    (watchState === "OFFER_EXPIRED" || (capLeft !== null && capLeft <= 0 && !book.isPending)
      ? "EXPIRED"
      : watchState === "SOLD_OUT" || soldOutNow
        ? "SOLD_OUT"
        : null);

  const heading = <EventHeading eyebrow="Pick your seats" event={ev} />;

  if (!admitted && !effectiveEnding && !entry.left) {
    return (
      <Main narrow>
        {heading}
        <StatePanel
          icon={<UserX />}
          eyebrow="Not your turn yet"
          title="Seats open up when the waiting room calls you"
          actions={
            <Link href={`/queue/?e=${encodeURIComponent(slug)}`} className={buttonVariants()}>
              Go to the waiting room <ArrowRight aria-hidden="true" />
            </Link>
          }
        >
          <p>
            Everyone picks seats in the order they reached the front of the line. This browser doesn&apos;t have a turn
            for this departure yet.
          </p>
        </StatePanel>
      </Main>
    );
  }

  if (effectiveEnding === "EXPIRED" || effectiveEnding === "REJOIN_LIMIT") {
    return (
      <Main narrow>
        {heading}
        <RejoinPanel
          rejoinsUsed={entry.rejoinsUsed ?? 0}
          limitReached={effectiveEnding === "REJOIN_LIMIT"}
          reason="Your turn to pick seats ended before any seats were held."
          pending={rejoin.isPending}
          error={rejoin.error && !isApiError(rejoin.error, "REJOIN_LIMIT") ? rejoin.error : null}
          onRejoin={() => rejoin.mutate()}
        />
      </Main>
    );
  }

  if (effectiveEnding === "SOLD_OUT") {
    return (
      <Main narrow>
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
          <p>You were not charged. We&apos;ve given your place to the next person.</p>
        </StatePanel>
      </Main>
    );
  }

  if (effectiveEnding === "ALL_HELD" || effectiveEnding === "LEFT" || (entry.left && !admitted)) {
    const allHeld = effectiveEnding === "ALL_HELD";
    return (
      <Main narrow>
        {heading}
        <StatePanel
          icon={allHeld ? <PauseCircle /> : <DoorOpen />}
          eyebrow={allHeld ? "No free seats right now" : "You left"}
          title={allHeld ? "All remaining seats are on hold right now" : "We've given your place to the next person"}
          actions={
            <Button onClick={() => joinAgain.mutate()} disabled={joinAgain.isPending}>
              {joinAgain.isPending ? "Joining…" : "Join the line again"}
            </Button>
          }
        >
          <p>
            {allHeld
              ? "Some usually come back when other people's holds run out. We've given your turn to the next person so the line keeps moving; join again to wait for released seats."
              : "Nothing was booked and you were not charged."}
          </p>
          {joinAgain.error !== null && <ErrorProblem error={joinAgain.error} />}
        </StatePanel>
      </Main>
    );
  }

  const busy = book.isPending;
  const err = book.error;
  const price = ev?.price_paise ?? 0;
  const activeCoach = ev?.layout.coaches.find((c) => c.coach === coach) ?? ev?.layout.coaches[0];
  const cellState = (seatNo: number): CellState =>
    picked.includes(seatNo) ? "selected" : (states?.[seatNo] ?? "available");
  const holdSelected = () => picked.length > 0 && book.mutate({ seat_nos: [...picked].sort((a, b) => a - b) });
  const holdBest = (n: number) => book.mutate({ quantity: n });

  return (
    <Main>
      <TurnBand capLeft={capLeft} onLeave={() => leave.mutate()} leaving={leave.isPending} />
      {heading}
      <p className="sr-only" aria-live="polite">
        {announce}
      </p>

      <div className="grid grid-cols-1 gap-8 lg:grid-cols-[minmax(0,1fr)_22rem] lg:items-start">
        <section aria-labelledby="map-title" className="flex min-w-0 flex-col gap-6">
          <div className="flex flex-wrap items-baseline justify-between gap-2">
            <h2 id="map-title" className="font-display text-2xl leading-8 font-semibold text-ink uppercase">
              Choose a coach
            </h2>
            {avail.data && (
              <p className="tabular text-sm leading-5 text-ink-muted">
                {formatCount(avail.data.available)} of {formatCount(ev?.capacity ?? 0)} seats free · live
              </p>
            )}
          </div>
          {ev && activeCoach ? (
            <>
              <CoachTabs
                layout={ev.layout}
                states={states}
                active={activeCoach.coach}
                onSelect={setCoach}
                panelId="coach-panel"
                selectedByCoach={selectedByCoach}
              />
              <div className="flex flex-col gap-6 rounded-xl border border-rule bg-card p-4 sm:p-8">
                {states ? (
                  <SeatGrid
                    layout={ev.layout}
                    coach={activeCoach}
                    cellState={cellState}
                    flashSeats={flash}
                    onToggle={onToggle}
                    panelId="coach-panel"
                  />
                ) : (
                  <MapSkeleton />
                )}
                <div className="border-t border-rule pt-4">
                  <Legend />
                </div>
              </div>
            </>
          ) : (
            <MapSkeleton withTabs />
          )}
          {avail.error && !avail.data && <ErrorProblem error={avail.error} />}
        </section>

        <aside aria-label="Your selection" className="flex flex-col gap-4 lg:sticky lg:top-6">
          <SelectionStub
            ev={ev}
            picked={picked}
            lost={lost}
            maxSeats={maxSeats}
            price={price}
            busy={busy}
            onRemove={onToggle}
            onHold={holdSelected}
          />
          <BestAvailable
            quantity={quantity}
            setQuantity={setQuantity}
            maxSeats={maxSeats}
            busy={busy}
            price={price}
            onHold={() => holdBest(quantity)}
          />
          {err && (
            <BookError
              error={err}
              ev={ev}
              orderId={entry.orderId}
              onBest={(n) => holdBest(n)}
              quantity={Math.max(1, picked.length || quantity)}
            />
          )}
        </aside>
      </div>

      {/* Mobile: the hold action stays in reach. */}
      {picked.length > 0 && (
        <div className="fixed inset-x-0 bottom-0 z-20 border-t-2 border-navy bg-card px-4 pt-3 pb-[max(0.75rem,env(safe-area-inset-bottom))] lg:hidden">
          <div className="mx-auto flex max-w-2xl items-center justify-between gap-4">
            <p className="tabular text-sm leading-5 text-ink">
              <span className="font-display text-2xl font-semibold">{picked.length}</span> seat
              {picked.length === 1 ? "" : "s"} · {formatINR(price * picked.length)}
            </p>
            <Button onClick={holdSelected} disabled={busy}>
              {busy ? "Holding…" : `Hold ${picked.length === 1 ? "seat" : "seats"}`}
            </Button>
          </div>
        </div>
      )}
    </Main>
  );
}

// ---------- pieces ----------

function TurnBand({ capLeft, onLeave, leaving }: { capLeft: number | null; onLeave: () => void; leaving: boolean }) {
  const low = capLeft !== null && capLeft < 60_000;
  return (
    <div
      className={cn(
        "flex flex-col gap-3 rounded-sm border-l-4 bg-card p-4 sm:flex-row sm:items-center sm:justify-between",
        low ? "border-l-warning" : "border-l-navy",
      )}
    >
      <p className="flex flex-wrap items-baseline gap-x-2 text-base leading-6 text-ink">
        <span className="sign-label text-ink-muted">Your turn</span>
        {capLeft !== null ? (
          <>
            <span className={cn("tabular font-display text-2xl font-semibold", low ? "text-warning" : "text-navy")}>
              {formatMMSS(capLeft)}
            </span>
            <span className="text-ink-muted">at most to hold seats. An idle turn ends sooner.</span>
          </>
        ) : (
          <span className="text-ink-muted">Pick seats and hold them to start checkout</span>
        )}
      </p>
      <Button
        variant="quiet"
        size="md"
        className="-ml-4 self-start sm:ml-0 sm:self-auto"
        onClick={onLeave}
        disabled={leaving}
      >
        <DoorOpen aria-hidden="true" />
        {leaving ? "Leaving…" : "Leave, give my turn away"}
      </Button>
    </div>
  );
}

function SelectionStub({
  ev,
  picked,
  lost,
  maxSeats,
  price,
  busy,
  onRemove,
  onHold,
}: {
  ev: EventDetail | undefined;
  picked: number[];
  lost: number[];
  maxSeats: number;
  price: number;
  busy: boolean;
  onRemove: (seatNo: number) => void;
  onHold: () => void;
}) {
  return (
    <TicketStub aria-labelledby="sel-title">
      <div className="flex flex-col gap-2 p-6">
        <p id="sel-title" className="sign-label text-ink-muted">
          Seats selected
        </p>
        <p className="tabular font-display text-[4.5rem] leading-none font-semibold text-ink">
          {picked.length}
          <span className="text-3xl text-ink-subtle"> / {maxSeats}</span>
        </p>
      </div>
      <Perforation />
      <div className="flex flex-col gap-4 p-6">
        {picked.length === 0 ? (
          <p className="text-base leading-6 text-ink-muted">
            Tap free seats on the map, or let us pick the best ones together below.
          </p>
        ) : (
          <ul className="flex flex-wrap gap-2" aria-label="Selected seats">
            {picked.map((s) => (
              <li key={s}>
                <button
                  type="button"
                  onClick={() => onRemove(s)}
                  aria-label={`Remove ${seatLabel(s, ev?.layout)}`}
                  className="inline-flex h-11 cursor-pointer items-center gap-2 rounded-sm border-2 border-ink bg-signal px-3 font-display text-lg font-semibold text-ink transition-colors duration-150 hover:bg-signal/85 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-navy"
                >
                  {seatLabel(s, ev?.layout)}
                  <X aria-hidden="true" className="size-4" />
                </button>
              </li>
            ))}
          </ul>
        )}
        {lost.length > 0 && (
          <p className="text-sm leading-5 text-danger" role="alert">
            {lost.map((s) => seatLabel(s, ev?.layout)).join(", ")} {lost.length === 1 ? "was" : "were"} just taken by
            someone else and {lost.length === 1 ? "has" : "have"} been removed.
          </p>
        )}
        <dl className="flex items-baseline justify-between border-t border-rule pt-4">
          <dt className="text-base leading-6 text-ink-muted">
            {picked.length} × {formatINR(price)}
          </dt>
          <dd className="tabular font-display text-3xl leading-9 font-semibold text-ink">
            {formatINR(price * picked.length)}
          </dd>
        </dl>
        <Button size="block" onClick={onHold} disabled={busy || picked.length === 0}>
          {busy
            ? "Holding your seats…"
            : picked.length
              ? `Hold ${picked.length === 1 ? "this seat" : "these seats"}`
              : "Pick a seat first"}
          {!busy && picked.length > 0 && <ArrowRight aria-hidden="true" />}
        </Button>
        <p className="text-sm leading-5 text-ink-subtle">
          Holding takes the seats off sale for you while you pay. You&apos;re not charged until you confirm.
        </p>
      </div>
    </TicketStub>
  );
}

function BestAvailable({
  quantity,
  setQuantity,
  maxSeats,
  busy,
  price,
  onHold,
}: {
  quantity: number;
  setQuantity: (n: number) => void;
  maxSeats: number;
  busy: boolean;
  price: number;
  onHold: () => void;
}) {
  return (
    <section aria-labelledby="best-title" className="flex flex-col gap-4 rounded-xl border border-rule bg-card p-6">
      <div className="flex items-center gap-2">
        <Sparkles aria-hidden="true" className="size-4 text-navy" />
        <h2 id="best-title" className="sign-label text-navy">
          Best available
        </h2>
      </div>
      <p className="text-sm leading-5 text-ink-muted">Seats together where possible, picked for you in one step.</p>
      <div className="flex items-center justify-between gap-4">
        <QuantityStepper value={quantity} min={1} max={maxSeats} onChange={setQuantity} />
        <span className="tabular text-base leading-6 text-ink-muted">{formatINR(price * quantity)}</span>
      </div>
      <Button variant="secondary" onClick={onHold} disabled={busy}>
        Hold best {quantity} {quantity === 1 ? "seat" : "seats"}
      </Button>
    </section>
  );
}

function QuantityStepper({
  value,
  min,
  max,
  onChange,
}: {
  value: number;
  min: number;
  max: number;
  onChange: (n: number) => void;
}) {
  const btn =
    "flex size-11 cursor-pointer items-center justify-center rounded-sm border-2 border-rule-strong bg-card text-navy transition-colors duration-150 hover:border-navy hover:bg-sunk disabled:cursor-not-allowed disabled:opacity-40 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-navy";
  return (
    <div role="group" aria-label="Number of seats" className="flex items-center gap-2">
      <button
        type="button"
        className={btn}
        onClick={() => onChange(value - 1)}
        disabled={value <= min}
        aria-label="Fewer seats"
      >
        <Minus aria-hidden="true" className="size-4" />
      </button>
      <output aria-live="polite" className="tabular w-10 text-center font-display text-3xl font-semibold text-ink">
        {value}
      </output>
      <button
        type="button"
        className={btn}
        onClick={() => onChange(value + 1)}
        disabled={value >= max}
        aria-label="More seats"
      >
        <Plus aria-hidden="true" className="size-4" />
      </button>
    </div>
  );
}

function BookError({
  error,
  ev,
  orderId,
  onBest,
  quantity,
}: {
  error: unknown;
  ev: EventDetail | undefined;
  orderId: string | undefined;
  onBest: (n: number) => void;
  quantity: number;
}) {
  if (isApiError(error, "SEATS_TAKEN")) {
    const taken = error.problem.taken ?? [];
    return (
      <Notice
        tone="danger"
        title={`${taken.length === 1 ? "A seat was" : "Some seats were"} just taken`}
        live="assertive"
        actions={
          <Button variant="secondary" size="md" onClick={() => onBest(quantity)}>
            Hold best {quantity} instead
          </Button>
        }
      >
        <p>
          {taken.map((s) => seatLabel(s, ev?.layout)).join(", ")} went to someone else a moment before you. Nothing was
          held and you were not charged. Pick again, or let us find {quantity} for you.
        </p>
      </Notice>
    );
  }
  if (isApiError(error, "INSUFFICIENT_SEATS")) {
    const n = error.problem.available ?? 0;
    return (
      <Notice tone="warning" title={`Only ${n} ${n === 1 ? "seat is" : "seats are"} free right now`} live="assertive">
        <p>We&apos;ve set the number to {n}. Nothing was held and you were not charged.</p>
      </Notice>
    );
  }
  if (isApiError(error, "HOLD_LIMIT")) {
    return (
      <Notice
        tone="warning"
        title="You already hold seats for this departure"
        live="assertive"
        actions={
          orderId ? (
            <Link href={`/checkout/?o=${encodeURIComponent(orderId)}`} className={buttonVariants({ size: "md" })}>
              Go to your booking
            </Link>
          ) : undefined
        }
      >
        <p>One hold per person at a time keeps it fair. Finish that booking, or let its timer run out.</p>
      </Notice>
    );
  }
  if (isApiError(error, "NOT_ADMITTED", "TOKEN_INVALID", "TOKEN_EXPIRED")) {
    return (
      <Notice tone="danger" title="This browser's turn isn't valid any more" live="assertive">
        <p>Go back to the waiting room to get a new one. Nothing was held or charged.</p>
      </Notice>
    );
  }
  return <ErrorProblem error={error} />;
}

function MapSkeleton({ withTabs = false }: { withTabs?: boolean }) {
  return (
    <div className="flex flex-col gap-6" aria-busy="true">
      <span className="sr-only">Loading the seat map</span>
      {withTabs && (
        <div className="grid grid-cols-5 gap-2 lg:grid-cols-10">
          {Array.from({ length: 10 }, (_, i) => (
            <span key={i} aria-hidden="true" className="h-16 animate-pulse rounded-sm bg-sunk" />
          ))}
        </div>
      )}
      <div aria-hidden="true" className="mx-auto grid grid-cols-5 gap-2">
        {Array.from({ length: 25 }, (_, i) => (
          <span key={i} className="size-11 animate-pulse rounded-sm bg-sunk sm:size-12" />
        ))}
      </div>
    </div>
  );
}

function BookSkeleton() {
  return (
    <Main>
      <EventHeading eyebrow="Pick your seats" event={undefined} />
      <MapSkeleton withTabs />
    </Main>
  );
}
