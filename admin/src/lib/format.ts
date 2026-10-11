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
