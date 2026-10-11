import type { Schemas } from "./api";

// Times arrive as UTC and are always shown in Nairobi time, whatever the computer is set to.
const nairobiDateTime = new Intl.DateTimeFormat("en-GB", {
  timeZone: "Africa/Nairobi",
  day: "numeric",
  month: "short",
  year: "numeric",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});

const nairobiDate = new Intl.DateTimeFormat("en-GB", {
  timeZone: "Africa/Nairobi",
  day: "numeric",
  month: "short",
  year: "numeric",
});

/** "14 Oct 2026, 12:00" in Nairobi time. */
export function formatDateTime(iso: string): string {
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : nairobiDateTime.format(date);
}

/** "14 Oct 2026" in Nairobi time. */
export function formatDate(iso: string): string {
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : nairobiDate.format(date);
}

const nairobiTime = new Intl.DateTimeFormat("en-GB", {
  timeZone: "Africa/Nairobi",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});

const nairobiDayMonth = new Intl.DateTimeFormat("en-GB", { timeZone: "Africa/Nairobi", day: "numeric", month: "short" });
const nairobiHour = new Intl.DateTimeFormat("en-GB", {
  timeZone: "Africa/Nairobi",
  day: "numeric",
  month: "short",
  hour: "2-digit",
  minute: "2-digit",
  hour12: false,
});

/** "16:04:09" in Nairobi time, for "last updated". */
export function formatClock(iso: string): string {
  const date = new Date(iso);
  return Number.isNaN(date.getTime()) ? iso : nairobiTime.format(date);
}

/** A chart bucket's label: "14 Oct" for a day, "14 Oct, 16:00" for an hour. Nairobi time. */
export function formatBucket(iso: string, bucket: "hour" | "day"): string {
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return iso;
  return bucket === "day" ? nairobiDayMonth.format(date) : nairobiHour.format(date);
}

// Kenya has no daylight saving, so Nairobi is always three hours ahead of UTC. The date and
// time boxes show and take Nairobi wall-clock time whatever the computer's own zone is.
const NAIROBI_OFFSET_MS = 3 * 60 * 60 * 1000;
const LOCAL_SHAPE = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/;

/** A stored UTC time as the value of a `datetime-local` box, in Nairobi time. "" for none. */
export function toNairobiInput(iso: string | null | undefined): string {
  if (!iso) return "";
  const date = new Date(iso);
  if (Number.isNaN(date.getTime())) return "";
  return new Date(date.getTime() + NAIROBI_OFFSET_MS).toISOString().slice(0, 16);
}

/** The value of a `datetime-local` box, read as Nairobi time, as RFC 3339 UTC. Null when empty or malformed. */
export function fromNairobiInput(value: string): string | null {
  const match = LOCAL_SHAPE.exec(value.trim());
  if (!match) return null;
  const [year, month, day, hour, minute] = match.slice(1).map(Number) as [number, number, number, number, number];
  const utc = Date.UTC(year, month - 1, day, hour, minute) - NAIROBI_OFFSET_MS;
  const date = new Date(utc);
  // Date.UTC rolls 31 February over into March; refuse instead of silently moving the day.
  const check = new Date(utc + NAIROBI_OFFSET_MS);
  if (check.getUTCMonth() !== month - 1 || check.getUTCDate() !== day || check.getUTCHours() !== hour) return null;
  return date.toISOString().replace(".000Z", "Z");
}

/** "45 seconds", "1 minute", "3 minutes" — rounded up so the wait is never understated. */
export function formatWait(seconds: number): string {
  if (seconds < 60) {
    const whole = Math.max(1, Math.ceil(seconds));
    return `${whole} second${whole === 1 ? "" : "s"}`;
  }
  const minutes = Math.ceil(seconds / 60);
  return `${minutes} minute${minutes === 1 ? "" : "s"}`;
}

const ROLE_LABELS: Record<Schemas["Role"], string> = {
  ADMIN: "Admin",
  ORGANISER: "Organiser",
  GATE_STAFF: "Gate staff",
  FAN: "Fan",
};

export function roleLabel(role: Schemas["Role"]): string {
  return ROLE_LABELS[role];
}

export function initials(name: string | null, email: string | null): string {
  const source = (name ?? "").trim() || (email ?? "").trim();
  if (!source) return "?";
  const words = source.split(/[\s@._-]+/).filter(Boolean);
  return ((words[0]?.[0] ?? "") + (words[1]?.[0] ?? "")).toUpperCase() || "?";
}
