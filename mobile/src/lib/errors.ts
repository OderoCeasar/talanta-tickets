// One place that turns an API error code into words a fan can act on. Screens never show a
// raw code, and never invent their own wording for the same failure.

import { ApiError, type ApiErrorCode, type ClientErrorCode, type components } from "@talanta/shared";

import { formatDateTime } from "./time";

type Schemas = components["schemas"];
type Code = ApiErrorCode | ClientErrorCode;

function waitText(seconds: number | null): string {
  if (seconds === null || seconds <= 0) return "in a moment";
  if (seconds < 60) return `in ${seconds} second${seconds === 1 ? "" : "s"}`;
  const minutes = Math.ceil(seconds / 60);
  return `in ${minutes} minute${minutes === 1 ? "" : "s"}`;
}

function has<K extends string>(details: unknown, key: K): details is Record<K, unknown> {
  return typeof details === "object" && details !== null && key in details;
}

/** The seat IDs a `SEATS_UNAVAILABLE` answer says were lost. Empty for any other error. */
export function unavailableSeatIds(error: unknown): number[] {
  if (!(error instanceof ApiError) || error.code !== "SEATS_UNAVAILABLE") return [];
  const d = error.details;
  return has(d, "unavailable_seat_ids") && Array.isArray(d.unavailable_seat_ids) ? (d.unavailable_seat_ids as number[]) : [];
}

const FIXED: Partial<Record<Code, string>> = {
  NETWORK_ERROR: "We could not reach the server. Check your connection and try again.",
  UNEXPECTED_RESPONSE: "The server gave an answer we did not understand. Please try again.",
  INTERNAL: "Something went wrong on our side. Please try again.",
  UNAUTHENTICATED: "Please sign in to continue.",
  TOKEN_EXPIRED: "Your session has ended. Please sign in again.",
  REFRESH_TOKEN_INVALID: "Your session has ended. Please sign in again.",
  REFRESH_TOKEN_REUSED: "Your session has ended. Please sign in again.",
  FORBIDDEN: "This account cannot do that.",
  NOT_FOUND: "We could not find that. It may have been removed.",
  OTP_INVALID: "That code is not right. Check the SMS and try again.",
  OTP_EXPIRED: "That code has expired. Ask for a new one.",
  VALIDATION_FAILED: "Something you entered is not valid. Check it and try again.",
  PAYLOAD_TOO_LARGE: "That was too much to send at once.",
  EVENT_NOT_ON_SALE: "Tickets for this event are not on sale right now.",
  SALES_CLOSED: "Ticket sales for this event have closed.",
  HOLD_EXPIRED: "Your time to pay ran out and the seats were released. Choose seats again.",
  HOLD_NOT_ACTIVE: "These seats are no longer held for you. Choose seats again.",
  HOLD_ALREADY_BOOKED: "These seats already have a booking. Open it to continue paying.",
  BOOKING_NOT_PAYABLE: "This booking can no longer be paid for.",
  PAYMENT_IN_PROGRESS: "A payment is already waiting for your M-Pesa PIN. Check your phone.",
  PAYMENT_PROVIDER_UNAVAILABLE: "M-Pesa is not answering right now. No money was taken. Please try again.",
  IDEMPOTENCY_KEY_REQUIRED: "We could not start your booking. Please try again.",
  IDEMPOTENCY_KEY_REUSED: "We could not start your booking. Go back to your seats and try again.",
  MOCK_NOT_ALLOWED: "Test payments are switched off on this server.",
};

/** Plain words for any failure. Falls back to the server's own message, then a generic line. */
export function errorMessage(error: unknown): string {
  if (!(error instanceof ApiError)) return "Something went wrong. Please try again.";
  const d = error.details;

  switch (error.code) {
    case "SEATS_UNAVAILABLE": {
      const n = unavailableSeatIds(error).length;
      if (n === 1) return "1 seat was just taken by someone else. The rest are still selected.";
      if (n > 1) return `${n} seats were just taken by someone else. The rest are still selected.`;
      return "Some of those seats were just taken by someone else.";
    }
    case "TICKET_LIMIT_EXCEEDED": {
      if (has(d, "allowed") && has(d, "remaining")) {
        const { allowed, remaining } = d as Schemas["TicketLimitDetails"];
        if (remaining <= 0) return `You already have the most tickets one person can buy for this event (${allowed}).`;
        return `You can buy up to ${allowed} tickets for this event. You can choose ${remaining} more.`;
      }
      return "That is more tickets than one person can buy for this event.";
    }
    case "NO_ADJACENT_SEATS": {
      if (has(d, "quantity") && has(d, "available")) {
        const { quantity, available } = d as Schemas["NoAdjacentSeatsDetails"];
        if (available < quantity) return `Only ${available} seat${available === 1 ? " is" : "s are"} left in this category. Try fewer seats or another category.`;
        return `We could not find ${quantity} seats next to each other. Try fewer seats, another category, or pick them on the map.`;
      }
      return "We could not find those seats together. Try fewer seats or another category.";
    }
    case "SALES_NOT_OPEN": {
      if (has(d, "sales_open_at") && typeof d.sales_open_at === "string") return `Ticket sales open on ${formatDateTime(d.sales_open_at)}.`;
      return "Ticket sales for this event have not opened yet.";
    }
    case "RATE_LIMITED":
      return `Too many tries. Please try again ${waitText(error.retryAfterSeconds)}.`;
    case "OTP_ATTEMPTS_EXCEEDED":
      return "Too many wrong codes. Ask for a new code.";
    default:
      break;
  }

  return FIXED[error.code] ?? (error.message || "Something went wrong. Please try again.");
}

/** True when signing in again is the only way forward. */
export function isSignedOutError(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401;
}

/** True when trying the same request again could work (no connection, or a fault on our side). */
export function isRetryable(error: unknown): boolean {
  if (!(error instanceof ApiError)) return true;
  return error.status === 0 || error.status >= 500 || error.status === 429;
}

export type PaymentTone = "success" | "waiting" | "failed" | "refund";

export interface PaymentOutcome {
  tone: PaymentTone;
  title: string;
  body: string;
  /** Whether "Try again" makes sense (the caller still checks the hold is alive). */
  canRetry: boolean;
}

/** What a payment's state means to the fan, in plain words. */
export function paymentOutcome(payment: Pick<Schemas["Payment"], "status" | "failure_reason" | "booking_status">): PaymentOutcome {
  if (payment.booking_status === "REFUND_REQUIRED" || payment.booking_status === "REFUNDED") {
    return {
      tone: "refund",
      title: payment.failure_reason === "wrong_amount" ? "The amount paid was wrong" : "We could not give you these seats",
      body:
        payment.failure_reason === "wrong_amount"
          ? "M-Pesa sent a different amount from the price of your seats, so no tickets were issued. Your money will be refunded. You do not need to do anything."
          : "Your payment arrived after the seats had been released, so no tickets were issued. Your money will be refunded. You do not need to do anything.",
      canRetry: false,
    };
  }
  if (payment.status === "SUCCESS") {
    if (payment.booking_status === "PAID") return { tone: "success", title: "Payment received", body: "Your tickets are ready.", canRetry: false };
    return { tone: "waiting", title: "Payment received", body: "We are getting your tickets ready.", canRetry: false };
  }
  if (payment.status === "INITIATED" || payment.status === "PENDING") {
    return { tone: "waiting", title: "Check your phone", body: "Enter your M-Pesa PIN on the prompt we sent to your phone.", canRetry: false };
  }
  if (payment.status === "REVERSED") {
    return { tone: "refund", title: "Payment reversed", body: "M-Pesa returned this payment to you. No tickets were issued.", canRetry: false };
  }
  switch (payment.failure_reason) {
    case "cancelled":
      return { tone: "failed", title: "Payment cancelled", body: "You cancelled the M-Pesa prompt. No money was taken.", canRetry: true };
    case "wrong_pin":
      return { tone: "failed", title: "Wrong PIN", body: "The M-Pesa PIN was not right. No money was taken.", canRetry: true };
    case "insufficient_funds":
      return { tone: "failed", title: "Not enough money", body: "There is not enough in this M-Pesa account. Top up, or use another number.", canRetry: true };
    case "timeout":
      return { tone: "failed", title: "Payment timed out", body: "The M-Pesa prompt was not answered in time. No money was taken.", canRetry: true };
    case "wrong_amount":
      return { tone: "refund", title: "The amount paid was wrong", body: "No tickets were issued. Your money will be refunded.", canRetry: false };
    default:
      return { tone: "failed", title: "Payment did not go through", body: "M-Pesa could not complete the payment. If any money left your account, M-Pesa will return it.", canRetry: true };
  }
}
