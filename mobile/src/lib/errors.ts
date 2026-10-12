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
  EVENT_BUSY: "The organiser is updating this event. Try again in a moment.",
  HOLD_EXPIRED: "Your time to pay ran out and the seats were released. Choose seats again.",
  HOLD_NOT_ACTIVE: "These seats are no longer held for you. Choose seats again.",
  HOLD_ALREADY_BOOKED: "These seats already have a booking. Open it to continue paying.",
  BOOKING_NOT_PAYABLE: "This booking can no longer be paid for.",
  PAYMENT_IN_PROGRESS: "A payment for these seats is still in progress. Check your phone for the M-Pesa prompt.",
  PAYMENT_PROVIDER_UNAVAILABLE: "Payments are not available right now. No money was taken. Try again in a moment.",
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

/**
 * True when an organiser's publish, cancel or edit had the event locked. Nothing changed on
 * the server and nothing is wrong with the fan's seats: the same request works a moment later.
 */
export function isEventBusy(error: unknown): boolean {
  return error instanceof ApiError && error.code === "EVENT_BUSY";
}

/**
 * True when trying the same request again could work: no connection, a fault on our side,
 * a rate limit, or the event being locked for a moment by an organiser.
 */
export function isRetryable(error: unknown): boolean {
  if (!(error instanceof ApiError)) return true;
  return error.status === 0 || error.status >= 500 || error.status === 429 || error.code === "EVENT_BUSY";
}

/**
 * Whether a failed query should be sent again by itself. A wrong request stays wrong, so only
 * connection trouble and server faults are retried (twice). A 401 gets one more go: by the
 * time it reaches here the client has either refreshed the session or dropped a dead token,
 * and a public read (events, seat maps) then answers without one.
 */
export function shouldRetryQuery(failureCount: number, error: unknown): boolean {
  if (error instanceof ApiError) {
    if (error.status === 401) return failureCount < 1;
    if (error.status !== 0 && error.status < 500) return false;
  }
  return failureCount < 2;
}

/**
 * Why a hold cannot be paid for any more, from a failed `POST /bookings` or STK start.
 * `too_late` is the server refusing to start a payment with under two minutes left: the
 * hold is still `ACTIVE` but a prompt could not be answered in time. `ended` is a hold that
 * ran out, was released, or whose booking is closed. Null for every other error.
 */
export function holdEnded(error: unknown): "too_late" | "ended" | null {
  if (!(error instanceof ApiError)) return null;
  if (error.code === "HOLD_EXPIRED") {
    return has(error.details, "hold_status") && error.details.hold_status === "ACTIVE" ? "too_late" : "ended";
  }
  if (error.code === "HOLD_NOT_ACTIVE") return "ended";
  return null;
}

/** The line shown on the seat map after checkout sent the fan back. */
export function backToMapMessage(reason: "too_late" | "ended"): string {
  return reason === "too_late"
    ? "There was too little time left to pay, so the seats were released. Choose your seats again for a fresh 10 minutes."
    : "Your time to pay ran out, so the seats were released. Choose your seats again.";
}

/** The payment a `PAYMENT_IN_PROGRESS` answer names, so the app can follow it. Null otherwise. */
export function livePaymentId(error: unknown): number | null {
  if (!(error instanceof ApiError) || error.code !== "PAYMENT_IN_PROGRESS") return null;
  const d = error.details;
  return has(d, "payment_id") && typeof d.payment_id === "number" ? d.payment_id : null;
}

export type PaymentTone = "success" | "waiting" | "failed" | "refund";

export interface PaymentOutcome {
  tone: PaymentTone;
  title: string;
  body: string;
  /** Whether "Try again" makes sense (the caller still checks the hold is alive). */
  canRetry: boolean;
  /** The payment may still change (a live one, or a `TIMEOUT` M-Pesa can settle late): keep asking. */
  unsettled: boolean;
}

const REFUND_NO_SEATS: PaymentOutcome = {
  tone: "refund",
  title: "We could not give you these seats",
  body: "We received your payment but could not give you these seats. You will be refunded. You do not need to do anything.",
  canRetry: false,
  unsettled: false,
};

const REFUND_WRONG_AMOUNT: PaymentOutcome = {
  tone: "refund",
  title: "The amount paid was wrong",
  body: "M-Pesa sent a different amount from the price of your seats, so no tickets were issued. You will be refunded. You do not need to do anything.",
  canRetry: false,
  unsettled: false,
};

/**
 * What a payment's state means to the fan, in plain words. Three fields decide it, in this
 * order (the table is in the contract under the STK operation):
 *
 * 1. `booking_status`. `REFUND_REQUIRED`/`REFUNDED` means money arrived and bought nothing,
 *    whatever `status` says: a wrong amount is a `FAILED` payment, seats that were gone is a
 *    `SUCCESS` one. `PAID` means tickets, even when this payment is not the one that paid.
 * 2. `status`. `SUCCESS` alone never means tickets.
 * 3. `failure_reason`, for the wording of a failure.
 */
export function paymentOutcome(payment: Pick<Schemas["Payment"], "status" | "failure_reason" | "booking_status">): PaymentOutcome {
  const { status, failure_reason: reason, booking_status: booking } = payment;

  if (booking === "REFUND_REQUIRED" || booking === "REFUNDED") return reason === "wrong_amount" ? REFUND_WRONG_AMOUNT : REFUND_NO_SEATS;
  // Defensive: the server always pairs `wrong_amount` with `REFUND_REQUIRED`, but the money did arrive.
  if (reason === "wrong_amount") return REFUND_WRONG_AMOUNT;
  if (booking === "PAID") return { tone: "success", title: "Payment received", body: "Your tickets are ready.", canRetry: false, unsettled: false };

  if (status === "SUCCESS") {
    // Paid, but the booking read is a moment behind (or closed): never promise tickets here.
    return { tone: "waiting", title: "Payment received", body: "We are checking your seats. This takes a moment.", canRetry: false, unsettled: true };
  }
  if (status === "INITIATED" || status === "PENDING") {
    return { tone: "waiting", title: "Check your phone", body: "Enter your M-Pesa PIN on the prompt we sent to your phone.", canRetry: false, unsettled: true };
  }
  if (status === "REVERSED") {
    return { tone: "refund", title: "Payment reversed", body: "M-Pesa returned this payment to you. No tickets were issued.", canRetry: false, unsettled: false };
  }

  // The booking can no longer be paid for (its hold ran out): a failed payment has no retry.
  const canRetry = booking === "PENDING_PAYMENT";
  if (status === "TIMEOUT") {
    return {
      tone: "failed",
      title: "M-Pesa has not answered",
      body: "We did not hear back from M-Pesa about this payment. If money left your account, you will get your tickets or a refund once M-Pesa tells us.",
      canRetry,
      unsettled: true,
    };
  }
  switch (reason) {
    case "cancelled":
      return { tone: "failed", title: "Payment cancelled", body: "You cancelled the M-Pesa prompt. No money was taken.", canRetry, unsettled: false };
    case "wrong_pin":
      return { tone: "failed", title: "Wrong PIN", body: "The M-Pesa PIN was not right. No money was taken.", canRetry, unsettled: false };
    case "insufficient_funds":
      return { tone: "failed", title: "Not enough money", body: "There is not enough in this M-Pesa account. Top up, or use another number.", canRetry, unsettled: false };
    case "timeout":
      return { tone: "failed", title: "Payment timed out", body: "The M-Pesa prompt was not answered in time. No money was taken.", canRetry, unsettled: false };
    default:
      return { tone: "failed", title: "Payment did not go through", body: "M-Pesa could not complete the payment. If any money left your account, M-Pesa will return it.", canRetry, unsettled: false };
  }
}
