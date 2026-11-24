// Display formatting: IST everywhere with the date, rupees with Indian digit
// grouping (DESIGN-BRIEF §5.6). Money arrives as integer paise.

const IST = "Asia/Kolkata";

const timeFmt = new Intl.DateTimeFormat("en-GB", {
  timeZone: IST,
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});
const timeShortFmt = new Intl.DateTimeFormat("en-GB", {
  timeZone: IST,
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});
const dayFmt = new Intl.DateTimeFormat("en-GB", { timeZone: IST, weekday: "short", day: "numeric", month: "short" });
const numFmt = new Intl.NumberFormat("en-IN");

/** "10:00:00 IST, Sat 22 Nov" */
export function formatIST(iso: string | Date, opts: { seconds?: boolean } = {}): string {
  const d = typeof iso === "string" ? new Date(iso) : iso;
  const time = (opts.seconds === false ? timeShortFmt : timeFmt).format(d);
  return `${time} IST, ${dayFmt.format(d).replace(",", "")}`;
}

/** "41,234" / "1,25,000" */
export function formatCount(n: number): string {
  return numFmt.format(n);
}

/** "₹1,250" (paise kept only when non-zero) */
export function formatINR(paise: number): string {
  const rupees = paise / 100;
  return new Intl.NumberFormat("en-IN", {
    style: "currency",
    currency: "INR",
    minimumFractionDigits: paise % 100 === 0 ? 0 : 2,
    maximumFractionDigits: 2,
  }).format(rupees);
}

/** "04:59" for a duration in ms (floors to whole seconds, never negative). */
export function formatMMSS(ms: number): string {
  const total = Math.max(0, Math.ceil(ms / 1000));
  const m = Math.floor(total / 60);
  const s = total % 60;
  return `${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

/** "01:02:03" when over an hour, else "02:03". */
export function formatCountdown(ms: number): string {
  const total = Math.max(0, Math.ceil(ms / 1000));
  const h = Math.floor(total / 3600);
  if (h === 0) return formatMMSS(ms);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
}

/** Spoken form for screen readers: "4 minutes 59 seconds". */
export function spokenDuration(ms: number): string {
  const total = Math.max(0, Math.ceil(ms / 1000));
  const m = Math.floor(total / 60);
  const s = total % 60;
  const parts = [];
  if (m) parts.push(`${m} minute${m === 1 ? "" : "s"}`);
  if (s || !m) parts.push(`${s} second${s === 1 ? "" : "s"}`);
  return parts.join(" ");
}

/**
 * An honest ETA range from the server's point estimate: ±15 %, rounded
 * outwards to whole minutes. "3–4 min", or "< 1 min".
 */
export function etaRange(etaS: number): string {
  if (etaS < 60) return "< 1 min";
  const lo = Math.max(1, Math.floor((etaS * 0.85) / 60));
  const hi = Math.max(lo + 1, Math.ceil((etaS * 1.15) / 60));
  return `${lo}–${hi} min`;
}
