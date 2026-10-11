import { ApiError, type ApiErrorCode, type ApiFieldError, type ClientErrorCode } from "@talanta/shared";
import { formatWait } from "./format";
import { formatCount } from "./money";

type Code = ApiErrorCode | ClientErrorCode;

/**
 * Plain-words messages for the codes this site can meet. A screen can pass its own wording
 * for a code (`overrides`); anything not listed falls back to the server's message, which
 * the contract says is safe to show.
 */
const COMMON: Partial<Record<Code, string | ((error: ApiError) => string)>> = {
  NETWORK_ERROR: "We could not reach the server. Check your internet connection and try again.",
  UNEXPECTED_RESPONSE: "The server is not answering properly right now. Wait a moment and try again.",
  INTERNAL: "Something went wrong on our side. Try again, and tell the site admin if it keeps happening.",
  FORBIDDEN: "Your account is not allowed to do this.",
  VALIDATION_FAILED: "Some details need fixing. Check the marked fields.",
  RATE_LIMITED: (error) =>
    error.retryAfterSeconds === null
      ? "Too many attempts. Wait a little and try again."
      : `Too many attempts. Try again in ${formatWait(error.retryAfterSeconds)}.`,
  UNAUTHENTICATED: "Your session has ended. Sign in again.",
  TOKEN_EXPIRED: "Your session has ended. Sign in again.",
  // Every write to an event (and its categories and painting) can meet this while a publish,
  // a cancel or another person's edit holds the event. Nothing was changed; sending the same
  // thing again a moment later is the fix, so no screen needs its own wording.
  EVENT_BUSY: "This event is being published or changed right now. Wait a moment and try again.",
};

/**
 * `EVENT_BUSY` from a block edit is about the venue, not one event: pass this as the override
 * there, so the words fit.
 */
export const BLOCK_BUSY_MESSAGE =
  "An event at this venue is being published, or someone else is changing this block right now. Wait a moment and try again.";

export type MessageOverrides = Partial<Record<Code, string>>;

export function describeError(error: unknown, overrides: MessageOverrides = {}): string {
  if (!(error instanceof ApiError)) return "Something unexpected happened. Try again.";
  const custom = overrides[error.code];
  if (custom) return custom;
  const common = COMMON[error.code];
  if (typeof common === "function") return common(error);
  return common ?? error.message;
}

/**
 * Plain words for the field rules whose server message an organiser would not act on as it
 * stands, keyed `field:rule`. All three rules are only applied once an event has left
 * `DRAFT`, which is why the words can say "published". Anything not listed keeps the
 * server's own message.
 */
const FIELD_RULE_MESSAGES: Record<string, string> = {
  "sales_open_at:required": "A published event must keep the time its ticket sales open. Pick a date and time.",
  "sales_close_at:required": "A published event must keep the time its ticket sales close. Pick a date and time.",
  "kickoff_at:future": "This event is published, so kickoff cannot be moved to a time that has already passed.",
  "price_cents:gt": "This event is published, so the price must be above 0.",
};

/** The message to show next to a field for one entry of a validation answer. */
export function fieldMessage(entry: Pick<ApiFieldError, "field" | "rule" | "message">): string {
  return FIELD_RULE_MESSAGES[`${entry.field}:${entry.rule}`] ?? entry.message;
}

/**
 * Splits a server validation answer into messages for the fields this form shows and the
 * rest. The rest is returned so it can be shown above the form instead of being lost. The
 * first message for a field wins.
 */
export function splitFieldErrors<K extends string>(
  error: unknown,
  known: readonly K[],
): { fields: Partial<Record<K, string>>; other: string[] } {
  const fields: Partial<Record<K, string>> = {};
  const other: string[] = [];
  const details = error instanceof ApiError ? error.details : null;
  if (!details || !("fields" in details) || !Array.isArray(details.fields)) return { fields, other };
  const seen = new Set<string>();
  for (const entry of details.fields) {
    if (seen.has(entry.field)) continue;
    seen.add(entry.field);
    if ((known as readonly string[]).includes(entry.field)) fields[entry.field as K] = fieldMessage(entry);
    else other.push(fieldMessage(entry));
  }
  return { fields, other };
}

const seatsWord = (count: number) => `${formatCount(count)} ${count === 1 ? "seat" : "seats"}`;

/**
 * Why a category cannot be deleted, from the counts in `CATEGORY_IN_USE`: `seats` is how many
 * seats were published in the category, `sold` and `held` how many of those are taken now.
 * Seats with nothing sold or held means the only obstacle is that the event is published.
 */
export function categoryInUseText(details: unknown): string {
  const counts = (details ?? {}) as { seats?: unknown; sold?: unknown; held?: unknown };
  const count = (value: unknown) => (typeof value === "number" && Number.isFinite(value) && value > 0 ? Math.floor(value) : 0);
  const seats = count(counts.seats);
  const sold = count(counts.sold);
  const held = count(counts.held);
  if (sold === 0 && held === 0) {
    if (seats === 0) return "Seats or bookings of this event still use this category.";
    return `${seatsWord(seats)} ${seats === 1 ? "is" : "are"} on sale in this category.`;
  }
  const taken = `${formatCount(sold)} sold, ${formatCount(held)} held`;
  return seats === 0 ? `Seats in this category are taken: ${taken}.` : `It has ${seatsWord(seats)} in this event: ${taken}.`;
}

export function isCode(error: unknown, ...codes: Code[]): error is ApiError {
  return error instanceof ApiError && codes.includes(error.code);
}
