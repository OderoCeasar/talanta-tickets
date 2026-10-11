import { ApiError, fieldErrors, type ApiErrorCode, type ClientErrorCode } from "@talanta/shared";
import { formatWait } from "./format";

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
};

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
 * Splits a server validation answer into messages for the fields this form shows and the
 * rest. The rest is returned so it can be shown above the form instead of being lost.
 */
export function splitFieldErrors<K extends string>(
  error: unknown,
  known: readonly K[],
): { fields: Partial<Record<K, string>>; other: string[] } {
  const all = fieldErrors(error);
  const fields: Partial<Record<K, string>> = {};
  const other: string[] = [];
  for (const [name, message] of Object.entries(all)) {
    if ((known as readonly string[]).includes(name)) fields[name as K] = message;
    else other.push(message);
  }
  return { fields, other };
}

export function isCode(error: unknown, ...codes: Code[]): error is ApiError {
  return error instanceof ApiError && codes.includes(error.code);
}
