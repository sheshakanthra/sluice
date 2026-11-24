import { RotateCcw, TimerOff } from "lucide-react";

import { ErrorProblem, StatePanel } from "@/components/notices";
import { Button } from "@/components/ui/button";
import { MAX_REJOINS } from "@/lib/queue";

/**
 * OFFER_EXPIRED (queue) and 403 ADMISSION_EXPIRED (book): the turn lapsed.
 * The user may rejoin at the back, twice (ADR 0008).
 */
export function RejoinPanel({
  rejoinsUsed,
  onRejoin,
  pending,
  error,
  reason,
  limitReached,
}: {
  rejoinsUsed: number;
  onRejoin: () => void;
  pending: boolean;
  error: unknown;
  reason: string;
  limitReached: boolean;
}) {
  const left = Math.max(0, MAX_REJOINS - rejoinsUsed);
  if (limitReached || left === 0) {
    return (
      <StatePanel icon={<TimerOff />} eyebrow="Your turn ended" title="You've used both rejoins">
        <p>
          Each person can rejoin the line twice after a turn runs out, so the line stays fair for everyone behind you.
          Nothing was booked and you were not charged.
        </p>
      </StatePanel>
    );
  }
  return (
    <StatePanel
      icon={<TimerOff />}
      eyebrow="Your turn ended"
      title="Your turn timed out, but you can rejoin"
      actions={
        <Button onClick={onRejoin} disabled={pending} aria-disabled={pending}>
          <RotateCcw aria-hidden="true" />
          {pending ? "Rejoining…" : "Rejoin the line"}
        </Button>
      }
    >
      <p>{reason} Nothing was booked and you were not charged.</p>
      <p>
        Rejoining puts you at the back of the line.{" "}
        <strong className="font-semibold text-ink">
          {left} rejoin{left === 1 ? "" : "s"} left
        </strong>
        .
      </p>
      {error !== null && error !== undefined && <ErrorProblem error={error} className="mt-2" />}
    </StatePanel>
  );
}
