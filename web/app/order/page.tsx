"use client";

import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowRight, CircleCheckBig, Hourglass, RotateCcw } from "lucide-react";
import Link from "next/link";
import { useSearchParams } from "next/navigation";
import { Suspense, useEffect, useState } from "react";

import { TicketCard, useHoldLeft } from "@/components/checkout-parts";
import { EventHeading } from "@/components/event-heading";
import { ErrorProblem, Notice, NotChargedNotice, ReplayNotice, StatePanel } from "@/components/notices";
import { SiteHeader } from "@/components/site-header";
import { buttonVariants } from "@/components/ui/button";
import { confirmOrder, getEvent, getOrder, type Order } from "@/lib/api";
import { formatINR, formatMMSS } from "@/lib/format";
import { withIntent } from "@/lib/intent";
import { useMode } from "@/lib/mode";
import { storedSession } from "@/lib/session";
import { readOrderMeta, writeEntry } from "@/lib/storage";

export default function OrderPage() {
  return (
    <>
      <SiteHeader />
      <main className="mx-auto flex w-full max-w-3xl flex-1 flex-col gap-8 px-4 pt-8 pb-16 sm:px-8 sm:pt-12">
        <Suspense fallback={<OrderSkeleton />}>
          <OrderRoute />
        </Suspense>
      </main>
    </>
  );
}

function OrderRoute() {
  const params = useSearchParams();
  const id = params.get("o") ?? "";
  // MockPay's redirect adds intent= and status= (API.md §5).
  const returned = params.has("intent");
  if (!id) {
    return (
      <StatePanel eyebrow="Your booking" title="This link is missing its booking">
        <p>Ticket links come from checkout once a payment is confirmed.</p>
      </StatePanel>
    );
  }
  return <OrderView key={id} orderId={id} returned={returned} />;
}

const CONFIRM_EVERY_MS = 6000;

function OrderView({ orderId, returned }: { orderId: string; returned: boolean }) {
  const mode = useMode();
  const qc = useQueryClient();
  const live = mode === "live";
  const [session] = useState(() => (typeof window === "undefined" ? null : storedSession()));
  const [meta] = useState(() => (typeof window === "undefined" ? null : readOrderMeta(orderId)));

  const order = useQuery({
    queryKey: ["order", orderId],
    enabled: live && !!session,
    queryFn: async () => {
      const o = await getOrder(orderId, session!.session_token);
      // The checkout has ended: this browser's admission is spent (ADR 0010 addendum).
      if (o.status !== "PENDING_PAYMENT" && meta?.slug) {
        writeEntry(meta.slug, { admissionToken: undefined, admissionExpiresAt: undefined, holdId: undefined });
      }
      return o;
    },
    refetchIntervalInBackground: true,
    refetchInterval: (q) => (q.state.data?.status === "PENDING_PAYMENT" ? 2000 : false),
  });
  const o = order.data;

  // Back from the hosted page (or still deciding): sync with the PSP now,
  // then at the rate-limit-safe pace while the order is pending.
  const pending = !o || o.status === "PENDING_PAYMENT";
  const shouldConfirm = live && !!session && pending && (returned || o?.payment?.status === "AUTHORIZED");
  useEffect(() => {
    if (!shouldConfirm || !session) return;
    const run = () =>
      withIntent(`confirm:${orderId}`, (ik) => confirmOrder(orderId, session.session_token, ik))
        .then((r) => qc.setQueryData(["order", orderId], r))
        .catch(() => undefined);
    void run();
    const id = window.setInterval(run, CONFIRM_EVERY_MS);
    return () => window.clearInterval(id);
  }, [shouldConfirm, orderId, session, qc]);

  const eventKey = o?.event_id ?? meta?.slug;
  const event = useQuery({
    queryKey: ["event", eventKey],
    queryFn: () => getEvent(eventKey!),
    enabled: live && !!eventKey,
    staleTime: 60_000,
  });
  const ev = event.data;
  const left = useHoldLeft(o?.hold_expires_at);

  if (mode === "replay") return <ReplayNotice what="your ticket" />;
  if (live && !session) {
    return (
      <StatePanel eyebrow="Your booking" title="This booking belongs to another browser">
        <p>Bookings are tied to the browser that made them. Open the link there to see your ticket.</p>
      </StatePanel>
    );
  }
  if (order.error && !o) {
    return <ErrorProblem error={order.error} actions={<RetryButton onClick={() => order.refetch()} />} />;
  }
  if (!o) return <OrderSkeleton />;

  const checkoutHref = `/checkout/?o=${encodeURIComponent(orderId)}`;
  const queueHref = meta?.slug ? `/queue/?e=${encodeURIComponent(meta.slug)}` : "/";

  if (o.status === "CONFIRMED") {
    return (
      <>
        <Confirmed order={o} />
        <div className="flex flex-col gap-6">
          {o.tickets.map((t, i) => (
            <TicketCard key={t.code} ticket={t} event={ev} index={i} total={o.tickets.length} />
          ))}
        </div>
        <p className="text-sm leading-5 text-ink-subtle">
          These tickets are saved to this browser&apos;s booking. Show the QR code or the ticket code when you board.
        </p>
      </>
    );
  }

  const heading = <EventHeading eyebrow="Your booking" event={ev} />;

  if (o.status === "FAILED" || o.status === "EXPIRED" || o.status === "REFUNDED") {
    return (
      <>
        {heading}
        {o.charged ? (
          <Notice tone="warning" title={`Charged ${formatINR(o.amount_paise)}, refund on its way`}>
            <p>The booking ended after the money moved. The reconciler refunds it automatically.</p>
          </Notice>
        ) : (
          <NotChargedNotice
            heading={o.status === "EXPIRED" ? "Your hold ran out" : "This booking didn't go through"}
            seats="Your seats are released."
            actions={
              <Link href={queueHref} className={buttonVariants()}>
                Back to the waiting room
              </Link>
            }
          />
        )}
      </>
    );
  }

  // PENDING_PAYMENT
  const p = o.payment?.status;
  if (p === "FAILED" || p === "VOIDED") {
    return (
      <>
        {heading}
        <NotChargedNotice
          seats={
            left !== null && left > 0 ? (
              <>
                Your seats are still held for <span className="tabular font-semibold text-ink">{formatMMSS(left)}</span>
                .
              </>
            ) : (
              "Your hold is ending, so the seats are being released."
            )
          }
          actions={
            left !== null && left > 0 && (o.payment?.attempt ?? 1) < 3 ? (
              <Link href={checkoutHref} className={buttonVariants()}>
                <RotateCcw aria-hidden="true" /> Try paying again
              </Link>
            ) : undefined
          }
        />
      </>
    );
  }
  if (p === "CREATED" && !returned) {
    return (
      <>
        {heading}
        <Notice
          tone="info"
          icon={<Hourglass />}
          title="Payment not finished yet"
          actions={
            <Link href={checkoutHref} className={buttonVariants()}>
              Back to checkout <ArrowRight aria-hidden="true" />
            </Link>
          }
        >
          <p>
            Nothing has been charged. Your seats are held for{" "}
            <span className="tabular font-semibold text-ink">
              {left !== null ? formatMMSS(left) : "a little longer"}
            </span>
            .
          </p>
        </Notice>
      </>
    );
  }
  return (
    <>
      {heading}
      <Notice tone="info" icon={<Hourglass />} title="Confirming with the bank: don't close this tab" live="polite">
        <p>
          We check every few seconds and the ticket appears here on its own. If it can&apos;t be confirmed, you
          won&apos;t be charged.
        </p>
      </Notice>
    </>
  );
}

function Confirmed({ order }: { order: Order }) {
  return (
    <section className="flex flex-col gap-3" aria-live="polite">
      <p className="sign-label flex items-center gap-2 text-success">
        <CircleCheckBig aria-hidden="true" className="size-4" /> Booking confirmed
      </p>
      <h1 className="font-display text-[2.5rem] leading-[3rem] font-semibold text-balance text-ink sm:text-5xl sm:leading-[3.5rem]">
        You&apos;re on the train. {order.tickets.length === 1 ? "Here's your ticket." : "Here are your tickets."}
      </h1>
      <p className="tabular text-lg leading-7 text-ink-muted">
        Charged <strong className="font-semibold text-ink">{formatINR(order.amount_paise)}</strong>, once.
      </p>
    </section>
  );
}

function RetryButton({ onClick }: { onClick: () => void }) {
  return (
    <button type="button" onClick={onClick} className={buttonVariants({ variant: "secondary" })}>
      Try again
    </button>
  );
}

function OrderSkeleton() {
  return (
    <div className="flex flex-col gap-6" aria-busy="true">
      <span className="sr-only">Loading your booking</span>
      <span aria-hidden="true" className="h-12 w-3/4 animate-pulse rounded-sm bg-sunk" />
      <span aria-hidden="true" className="h-80 animate-pulse rounded-xl bg-sunk" />
    </div>
  );
}
