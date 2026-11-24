"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  ArrowRight,
  Check,
  CreditCard,
  ExternalLink,
  Landmark,
  Loader2,
  Lock,
  RotateCcw,
  TimerOff,
} from "lucide-react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { Suspense, useEffect, useState } from "react";

import { HoldTimer, PriceSummary, useHoldLeft } from "@/components/checkout-parts";
import { EventHeading } from "@/components/event-heading";
import { ErrorProblem, NotChargedNotice, ReplayNotice, StatePanel } from "@/components/notices";
import { SiteHeader } from "@/components/site-header";
import { Perforation, TicketStub } from "@/components/ticket-stub";
import { Button, buttonVariants } from "@/components/ui/button";
import { confirmOrder, getEvent, getOrder, isApiError, newPaymentAttempt, type Order, payOrder } from "@/lib/api";
import { formatINR } from "@/lib/format";
import { withIntent } from "@/lib/intent";
import { useMode } from "@/lib/mode";
import { seatLabel } from "@/lib/seats";
import { storedSession } from "@/lib/session";
import { readOrderMeta } from "@/lib/storage";

export default function CheckoutPage() {
  return (
    <>
      <SiteHeader />
      <main className="mx-auto flex w-full max-w-6xl flex-1 flex-col gap-8 px-4 pt-8 pb-16 sm:px-8 sm:pt-12">
        <Suspense fallback={<CheckoutSkeleton />}>
          <CheckoutRoute />
        </Suspense>
      </main>
    </>
  );
}

function CheckoutRoute() {
  const id = useSearchParams().get("o") ?? "";
  if (!id) {
    return (
      <StatePanel eyebrow="Checkout" title="This link is missing its booking">
        <p>Checkout links come from the seat map once your seats are held.</p>
      </StatePanel>
    );
  }
  return <CheckoutView key={id} orderId={id} />;
}

/** Order states the checkout cares about. */
function phase(o: Order): "pay" | "processing" | "declined" | "confirmed" | "failed" | "expired" {
  if (o.status === "CONFIRMED") return "confirmed";
  if (o.status === "EXPIRED") return "expired";
  if (o.status === "FAILED" || o.status === "REFUNDED") return "failed";
  const p = o.payment?.status;
  if (p === "FAILED" || p === "VOIDED") return "declined";
  if (p === "AUTHORIZED" || p === "CAPTURED") return "processing";
  return "pay";
}

const CONFIRM_EVERY_MS = 6000; // confirm is limited to 10/min per user (API.md §2)

function CheckoutView({ orderId }: { orderId: string }) {
  const mode = useMode();
  const router = useRouter();
  const qc = useQueryClient();
  const live = mode === "live";
  // Read once: this view renders on the client only (under Suspense).
  const [session] = useState(() => (typeof window === "undefined" ? null : storedSession()));
  const [meta] = useState(() => (typeof window === "undefined" ? null : readOrderMeta(orderId)));
  const [processing, setProcessing] = useState(false);

  const order = useQuery({
    queryKey: ["order", orderId],
    enabled: live && !!session,
    queryFn: () => getOrder(orderId, session!.session_token),
    refetchIntervalInBackground: true,
    refetchInterval: (q) => {
      const o = q.state.data;
      if (!o || o.status !== "PENDING_PAYMENT") return false;
      return processing || phase(o) === "processing" ? 2000 : 10_000;
    },
  });
  const o = order.data;
  const ph = o ? phase(o) : null;

  const event = useQuery({
    queryKey: ["event", o?.event_id ?? meta?.slug],
    queryFn: () => getEvent((o?.event_id ?? meta?.slug)!),
    enabled: live && !!(o?.event_id ?? meta?.slug),
    staleTime: 60_000,
  });
  const ev = event.data;
  const left = useHoldLeft(o?.hold_expires_at);

  // Confirmed: the ticket page takes over.
  useEffect(() => {
    if (ph === "confirmed") router.replace(`/order/?o=${encodeURIComponent(orderId)}`);
  }, [ph, orderId, router]);

  // While the bank is still deciding, sync with the PSP at the advised pace.
  const waiting = live && !!session && (processing || ph === "processing");
  useEffect(() => {
    if (!waiting || !session) return;
    const tick = () =>
      withIntent(`confirm:${orderId}`, (ik) => confirmOrder(orderId, session.session_token, ik))
        .then((r) => qc.setQueryData(["order", orderId], r))
        .catch(() => undefined);
    const id = window.setInterval(tick, CONFIRM_EVERY_MS);
    return () => window.clearInterval(id);
  }, [waiting, orderId, session, qc]);

  const pay = useMutation({
    mutationFn: () =>
      withIntent(`pay:${orderId}:${o?.payment?.attempt ?? 1}`, (ik) =>
        payOrder(orderId, session!.session_token, "pm_ok", ik),
      ),
    onMutate: () => setProcessing(true),
    onSuccess: (r) => {
      qc.setQueryData(["order", orderId], r);
      // Still PENDING with the attempt unanswered (pm_slow): keep confirming.
      setProcessing(r.status === "PENDING_PAYMENT" && r.payment?.status === "CREATED");
    },
    onError: (e) => {
      // A timeout may still have charged: keep polling until the server knows.
      if (!(isApiError(e) && e.transient)) setProcessing(false);
    },
  });

  const retry = useMutation({
    mutationFn: () =>
      withIntent(`attempt:${orderId}:${(o?.payment?.attempt ?? 1) + 1}`, (ik) =>
        newPaymentAttempt(orderId, session!.session_token, ik),
      ),
    onSuccess: (r) => qc.setQueryData(["order", orderId], r),
  });

  const hosted = () => {
    const url = o?.payment?.checkout_url;
    if (url) window.location.assign(url);
  };

  if (mode === "replay") return <ReplayNotice what="checkout" />;
  if (live && !session) {
    return (
      <StatePanel eyebrow="Checkout" title="This booking belongs to another browser">
        <p>Bookings are tied to the browser that made them. Open the link there to pay or see your ticket.</p>
      </StatePanel>
    );
  }

  const labels = o
    ? (meta?.labels ?? o.seat_nos.map((s) => seatLabel(s, ev && "layout" in ev ? ev.layout : undefined)))
    : [];
  const heading = <EventHeading eyebrow="Checkout" event={ev} />;

  if (order.error && !o) {
    return (
      <>
        {heading}
        <ErrorProblem
          error={order.error}
          actions={
            <Button variant="secondary" onClick={() => order.refetch()}>
              Try again
            </Button>
          }
        />
      </>
    );
  }
  if (!o) return <CheckoutSkeleton />;

  const timeUp = left !== null && left <= 0;

  if (ph === "expired" || ph === "failed" || (timeUp && ph !== "processing" && !processing && ph !== "confirmed")) {
    const expired = ph === "expired" || timeUp;
    return (
      <>
        {heading}
        <div className="flex flex-col gap-6">
          {o.charged ? (
            <StatePanel
              icon={<TimerOff />}
              eyebrow="Booking ended"
              title={`You were charged ${formatINR(o.amount_paise)}`}
            >
              <p>This booking ended after the money moved. The reconciler refunds it automatically.</p>
            </StatePanel>
          ) : (
            <NotChargedNotice
              heading={expired ? "Your hold ran out" : "This booking didn't go through"}
              seats={
                ph === "pay" || ph === "declined"
                  ? "We're releasing your seats back to the pool now."
                  : "Your seats have been released."
              }
              actions={
                meta?.slug ? (
                  <Link href={`/queue/?e=${encodeURIComponent(meta.slug)}`} className={buttonVariants()}>
                    Back to the waiting room
                  </Link>
                ) : undefined
              }
            />
          )}
          {o.failure_code === "PAYMENT_ATTEMPTS_EXHAUSTED" && (
            <p className="text-base leading-6 text-ink-muted">Each booking allows three payment attempts.</p>
          )}
        </div>
      </>
    );
  }

  const isProcessing = processing || ph === "processing";

  return (
    <>
      {heading}
      <div className="grid grid-cols-1 gap-8 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)] lg:items-start">
        <TicketStub aria-labelledby="hold-title">
          <div className="p-6 sm:p-8">
            <h2 id="hold-title" className="sr-only">
              Your hold
            </h2>
            <HoldTimer left={left} />
            <p className="mt-3 text-base leading-6 text-ink-muted">
              They&apos;re yours until the timer ends. If it runs out, the seats go back and you&apos;re not charged.
            </p>
          </div>
          <Perforation />
          <div className="p-6 sm:p-8">
            <PriceSummary labels={labels} pricePaise={ev?.price_paise} amountPaise={o.amount_paise} />
          </div>
        </TicketStub>

        <section aria-labelledby="pay-title" className="flex flex-col gap-6">
          <h2 id="pay-title" className="font-display text-2xl leading-8 font-semibold text-ink uppercase">
            {isProcessing ? "Confirming your payment" : "Pay"}
          </h2>

          {isProcessing ? (
            <Processing />
          ) : (
            <>
              {ph === "declined" && (
                <NotChargedNotice
                  seats={<>Your seats are still held for you. Try again with the same or another method.</>}
                  actions={
                    o.payment && o.payment.attempt < 3 ? (
                      <Button onClick={() => retry.mutate()} disabled={retry.isPending}>
                        <RotateCcw aria-hidden="true" />
                        {retry.isPending
                          ? "Starting a new attempt…"
                          : `Try again (attempt ${o.payment.attempt + 1} of 3)`}
                      </Button>
                    ) : undefined
                  }
                />
              )}
              {ph === "pay" && (
                <PayOptions
                  amount={o.amount_paise}
                  attempt={o.payment?.attempt ?? 1}
                  hostedReady={!!o.payment?.checkout_url}
                  onHosted={hosted}
                  onSaved={() => pay.mutate()}
                  busy={pay.isPending}
                />
              )}
            </>
          )}
          {pay.error !== null && !isProcessing && <ErrorProblem error={pay.error} />}
          {retry.error !== null && <ErrorProblem error={retry.error} />}
          <p className="flex items-start gap-2 text-sm leading-5 text-ink-subtle">
            <Lock aria-hidden="true" className="mt-0.5 size-4 shrink-0" />
            Every tap reuses one payment key, so a double tap, a refresh or a timeout can&apos;t charge you twice.
          </p>
        </section>
      </div>
    </>
  );
}

function PayOptions({
  amount,
  attempt,
  hostedReady,
  onHosted,
  onSaved,
  busy,
}: {
  amount: number;
  attempt: number;
  hostedReady: boolean;
  onHosted: () => void;
  onSaved: () => void;
  busy: boolean;
}) {
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-col gap-4 rounded-xl border-2 border-navy bg-card p-6">
        <div className="flex items-start gap-4">
          <span
            aria-hidden="true"
            className="flex size-11 shrink-0 items-center justify-center rounded-sm bg-sunk text-navy"
          >
            <Landmark className="size-5" />
          </span>
          <div className="flex flex-col gap-1">
            <p className="text-lg leading-6 font-semibold text-ink">Pay on the MockPay page</p>
            <p className="text-sm leading-5 text-ink-muted">
              A test payment gateway. You&apos;ll choose Pay or Decline there and come straight back.
            </p>
          </div>
        </div>
        <Button size="block" onClick={onHosted} disabled={!hostedReady || busy}>
          Pay {formatINR(amount)} on MockPay
          <ExternalLink aria-hidden="true" />
        </Button>
      </div>
      <div className="flex flex-col gap-4 rounded-xl border border-rule bg-card p-6">
        <div className="flex items-start gap-4">
          <span
            aria-hidden="true"
            className="flex size-11 shrink-0 items-center justify-center rounded-sm bg-sunk text-navy"
          >
            <CreditCard className="size-5" />
          </span>
          <div className="flex flex-col gap-1">
            <p className="text-lg leading-6 font-semibold text-ink">Saved test card</p>
            <p className="tabular text-sm leading-5 text-ink-muted">Test method pm_ok · always approves</p>
          </div>
        </div>
        <Button variant="secondary" onClick={onSaved} disabled={busy}>
          {busy ? "Paying…" : `Pay ${formatINR(amount)} with saved card`}
          {!busy && <ArrowRight aria-hidden="true" />}
        </Button>
      </div>
      {attempt > 1 && <p className="tabular text-sm leading-5 text-ink-subtle">Attempt {attempt} of 3</p>}
    </div>
  );
}

function Processing() {
  return (
    <ol className="flex flex-col gap-0 rounded-xl border border-rule bg-card p-6" aria-live="polite">
      {[
        { label: "Payment sent", done: true },
        { label: "Confirming with the bank: don't close this tab", done: false },
        { label: "Issuing your tickets", done: false, later: true },
      ].map((s, i, all) => (
        <li key={s.label} className="grid grid-cols-[2rem_1fr] gap-x-4">
          <span className="flex flex-col items-center">
            <span
              className={
                s.done
                  ? "flex size-8 items-center justify-center rounded-full bg-navy text-white"
                  : s.later
                    ? "flex size-8 items-center justify-center rounded-full border-2 border-rule-strong bg-card text-ink-subtle"
                    : "flex size-8 items-center justify-center rounded-full border-2 border-navy bg-card text-navy"
              }
              aria-hidden="true"
            >
              {s.done ? (
                <Check className="size-4" strokeWidth={3} />
              ) : s.later ? (
                i + 1
              ) : (
                <Loader2 className="size-4 animate-spin motion-reduce:animate-none" />
              )}
            </span>
            {i < all.length - 1 && <span aria-hidden="true" className="w-0.5 flex-1 bg-rule" />}
          </span>
          <p
            className={
              i < all.length - 1 ? "pt-1 pb-6 text-base leading-6 text-ink" : "pt-1 text-base leading-6 text-ink-muted"
            }
          >
            {s.label}
          </p>
        </li>
      ))}
      <li className="mt-4 text-sm leading-5 text-ink-muted">
        We check with the bank every few seconds and show the answer here. If it can&apos;t be confirmed, you won&apos;t
        be charged.
      </li>
    </ol>
  );
}

function CheckoutSkeleton() {
  return (
    <div className="flex flex-col gap-8" aria-busy="true">
      <EventHeading eyebrow="Checkout" event={undefined} />
      <div className="grid grid-cols-1 gap-8 lg:grid-cols-2">
        <TicketStub>
          <div className="flex flex-col gap-4 p-6 sm:p-8">
            <p className="sign-label text-ink-muted">Loading your hold</p>
            <span aria-hidden="true" className="h-24 w-2/3 animate-pulse rounded-sm bg-sunk" />
          </div>
        </TicketStub>
        <span aria-hidden="true" className="h-64 animate-pulse rounded-xl bg-sunk" />
      </div>
    </div>
  );
}
