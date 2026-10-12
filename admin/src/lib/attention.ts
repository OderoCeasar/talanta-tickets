import type { Schemas } from "./api";

// Words for the two lists staff act on: alerts (a payment went wrong) and the refund queue.
// Kept free of React so the wording can be tested on its own.

export type Alert = Schemas["Alert"];
export type AlertKind = Schemas["AlertKind"];
export type Refund = Schemas["Refund"];
export type RefundStatus = Schemas["RefundStatus"];

const ALERT_KIND_LABELS: Record<AlertKind, string> = {
  REFUND_REQUIRED: "Paid, but got no seats",
  AMOUNT_MISMATCH: "Paid the wrong amount",
  PAYMENT_UNRESOLVED: "No answer from M-Pesa",
};

/** The kind of an alert in plain words. A kind this build does not know keeps the page working. */
export function alertKindLabel(kind: string): string {
  return ALERT_KIND_LABELS[kind as AlertKind] ?? "Needs a look";
}

/** True when the server has put a refund in the queue for this kind of alert. */
export function alertHasRefund(kind: string): boolean {
  return kind === "REFUND_REQUIRED" || kind === "AMOUNT_MISMATCH";
}

/** What staff should do about an alert of this kind, in one sentence. */
export function alertNextStep(kind: string): string {
  if (alertHasRefund(kind)) return "The money has to go back: a refund for it is waiting in the refund queue.";
  if (kind === "PAYMENT_UNRESOLVED") {
    return "No refund is queued. Look this payment up in the M-Pesa portal to see whether the money arrived.";
  }
  return "Read the message and check the booking.";
}

const REFUND_STATUS_LABELS: Record<RefundStatus, string> = {
  PENDING: "Waiting to be paid back",
  DONE: "Paid back",
};

export function refundStatusLabel(status: string): string {
  return REFUND_STATUS_LABELS[status as RefundStatus] ?? status;
}

/**
 * The text of a count badge when only one page of the list was loaded: "7", or "100+" when
 * the server says there are more beyond the page. Null when there is nothing to count.
 */
export function badgeText(loaded: number, hasMore: boolean): string | null {
  if (loaded <= 0) return null;
  return hasMore ? `${loaded}+` : String(loaded);
}

/** "1 alert", "3 alerts", "100+ alerts" — for the words next to a badge and for screen readers. */
export function countPhrase(loaded: number, hasMore: boolean, one: string, many: string): string {
  return `${badgeText(loaded, hasMore) ?? "0"} ${loaded === 1 && !hasMore ? one : many}`;
}

/** Browser and server clocks differ; an answer this much older than our request was not caused by it. */
const ALREADY_DONE_TOLERANCE_MS = 2 * 60 * 1000;

/**
 * Completing a refund is safe to repeat: the server answers 200 with the refund as it stands,
 * and keeps the first completion time. So when the completion time in the answer is clearly
 * older than our request, someone else had already marked it and our click changed nothing.
 */
export function wasAlreadyCompleted(refund: Pick<Refund, "completed_at">, requestSentAtMs: number): boolean {
  if (!refund.completed_at) return false;
  const completed = new Date(refund.completed_at).getTime();
  return Number.isFinite(completed) && completed < requestSentAtMs - ALREADY_DONE_TOLERANCE_MS;
}
