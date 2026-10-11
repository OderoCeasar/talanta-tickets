// Every time is shown in Africa/Nairobi, whatever the phone's own time zone is.
//
// Kenya is UTC+3 all year and has not used daylight saving since 1960, so the conversion is
// a fixed offset. Doing it by hand keeps the output identical on every Hermes build (Intl
// time-zone support varies by device) and makes it testable without a time-zone database.

const NAIROBI_OFFSET_MS = 3 * 60 * 60 * 1000;
const DAYS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"] as const;
const MONTHS = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"] as const;

interface NairobiParts {
  year: number;
  month: number;
  day: number;
  weekday: number;
  hour: number;
  minute: number;
  second: number;
}

function toMs(value: string | number | Date): number {
  const ms = typeof value === "string" ? Date.parse(value) : typeof value === "number" ? value : value.getTime();
  if (Number.isNaN(ms)) throw new RangeError(`Not a valid time: ${String(value)}`);
  return ms;
}

export function nairobiParts(value: string | number | Date): NairobiParts {
  // Shift the instant, then read it with the UTC getters: they now give Nairobi wall time.
  const d = new Date(toMs(value) + NAIROBI_OFFSET_MS);
  return {
    year: d.getUTCFullYear(),
    month: d.getUTCMonth(),
    day: d.getUTCDate(),
    weekday: d.getUTCDay(),
    hour: d.getUTCHours(),
    minute: d.getUTCMinutes(),
    second: d.getUTCSeconds(),
  };
}

const two = (n: number) => String(n).padStart(2, "0");

/** `Sat 17 Oct 2026` */
export function formatDate(value: string | number | Date): string {
  const p = nairobiParts(value);
  return `${DAYS[p.weekday]} ${p.day} ${MONTHS[p.month]} ${p.year}`;
}

/** `4:00 pm` */
export function formatTime(value: string | number | Date): string {
  const p = nairobiParts(value);
  const h12 = p.hour % 12 === 0 ? 12 : p.hour % 12;
  return `${h12}:${two(p.minute)} ${p.hour < 12 ? "am" : "pm"}`;
}

/** `Sat 17 Oct 2026, 4:00 pm` */
export function formatDateTime(value: string | number | Date): string {
  return `${formatDate(value)}, ${formatTime(value)}`;
}

/** `16:05:09`, for the ticking clock on a ticket. */
export function formatClock(value: string | number | Date): string {
  const p = nairobiParts(value);
  return `${two(p.hour)}:${two(p.minute)}:${two(p.second)}`;
}

/** The day of the month and short month, for the date tile on an event card. */
export function dateTile(value: string | number | Date): { day: string; month: string; weekday: string } {
  const p = nairobiParts(value);
  return { day: String(p.day), month: MONTHS[p.month].toUpperCase(), weekday: DAYS[p.weekday].toUpperCase() };
}

/** `today`, `tomorrow`, `in 9 days`, or `` once it is in the past. Days are Nairobi calendar days. */
export function daysUntil(value: string | number | Date, now: string | number | Date): string {
  const dayNumber = (v: string | number | Date) => Math.floor((toMs(v) + NAIROBI_OFFSET_MS) / 86_400_000);
  const diff = dayNumber(value) - dayNumber(now);
  if (diff < 0) return "";
  if (diff === 0) return "today";
  if (diff === 1) return "tomorrow";
  return `in ${diff} days`;
}
