import { ApiError, type ApiErrorCode } from "@talanta/shared";
import { describe, expect, it } from "vitest";

import { backToMapMessage, errorMessage, holdEnded, isEventBusy, isRetryable, isSignedOutError, livePaymentId, paymentOutcome, shouldRetryQuery, unavailableSeatIds } from "../errors";

const apiError = (status: number, code: ApiError["code"], details: ApiError["details"] = null, retryAfterSeconds: number | null = null) =>
  new ApiError({ status, code, message: `server text for ${code}`, details, retryAfterSeconds });

describe("errorMessage", () => {
  it("says how many seats were lost and that the rest are kept", () => {
    expect(errorMessage(apiError(409, "SEATS_UNAVAILABLE", { unavailable_seat_ids: [910005] }))).toBe(
      "1 seat was just taken by someone else. The rest are still selected.",
    );
    expect(errorMessage(apiError(409, "SEATS_UNAVAILABLE", { unavailable_seat_ids: [1, 2, 3] }))).toContain("3 seats were just taken");
  });

  it("explains the ticket limit with the numbers from the server", () => {
    const details = { allowed: 6, held_or_bought: 4, requested: 3, remaining: 2 };
    expect(errorMessage(apiError(422, "TICKET_LIMIT_EXCEEDED", details))).toBe("You can buy up to 6 tickets for this event. You can choose 2 more.");
    expect(errorMessage(apiError(422, "TICKET_LIMIT_EXCEEDED", { ...details, held_or_bought: 6, remaining: 0 }))).toContain("already have the most tickets");
  });

  it("explains sales windows", () => {
    expect(errorMessage(apiError(409, "EVENT_NOT_ON_SALE"))).toBe("Tickets for this event are not on sale right now.");
    expect(errorMessage(apiError(409, "SALES_CLOSED"))).toBe("Ticket sales for this event have closed.");
    const opens = { event_status: "PUBLISHED" as const, sales_open_at: "2026-10-17T06:00:00Z", sales_close_at: null };
    expect(errorMessage(apiError(409, "SALES_NOT_OPEN", opens))).toBe("Ticket sales open on Sat 17 Oct 2026, 9:00 am.");
  });

  it("tells a rate-limited fan how long to wait", () => {
    expect(errorMessage(apiError(429, "RATE_LIMITED", { retry_after_seconds: 20 }, 20))).toBe("Too many tries. Please try again in 20 seconds.");
    expect(errorMessage(apiError(429, "RATE_LIMITED", null, 90))).toBe("Too many tries. Please try again in 2 minutes.");
    expect(errorMessage(apiError(429, "RATE_LIMITED"))).toBe("Too many tries. Please try again in a moment.");
  });

  it("explains best-available failures", () => {
    expect(errorMessage(apiError(409, "NO_ADJACENT_SEATS", { category_id: 7, quantity: 4, available: 3 }))).toContain("Only 3 seats are left");
    expect(errorMessage(apiError(409, "NO_ADJACENT_SEATS", { category_id: 7, quantity: 4, available: 900 }))).toContain("could not find 4 seats next to each other");
  });

  it("covers sign-in, hold and payment codes in plain words", () => {
    expect(errorMessage(apiError(401, "OTP_INVALID"))).toContain("code is not right");
    expect(errorMessage(apiError(401, "OTP_EXPIRED"))).toContain("expired");
    expect(errorMessage(apiError(429, "OTP_ATTEMPTS_EXCEEDED"))).toContain("new code");
    expect(errorMessage(apiError(409, "HOLD_EXPIRED"))).toContain("ran out");
    expect(errorMessage(apiError(409, "PAYMENT_IN_PROGRESS"))).toContain("still in progress");
    expect(errorMessage(apiError(502, "PAYMENT_PROVIDER_UNAVAILABLE"))).toBe("Payments are not available right now. No money was taken. Try again in a moment.");
    expect(errorMessage(ApiError.network(new Error("offline")))).toContain("could not reach the server");
  });

  it("never shows a raw code: every fan-facing code has its own wording", () => {
    const codes: ApiErrorCode[] = [
      "VALIDATION_FAILED", "IDEMPOTENCY_KEY_REQUIRED", "UNAUTHENTICATED", "TOKEN_EXPIRED", "OTP_INVALID", "OTP_EXPIRED",
      "REFRESH_TOKEN_INVALID", "REFRESH_TOKEN_REUSED", "FORBIDDEN", "MOCK_NOT_ALLOWED", "NOT_FOUND", "SEATS_UNAVAILABLE",
      "NO_ADJACENT_SEATS", "EVENT_NOT_ON_SALE", "SALES_NOT_OPEN", "SALES_CLOSED", "HOLD_EXPIRED", "HOLD_NOT_ACTIVE",
      "HOLD_ALREADY_BOOKED", "BOOKING_NOT_PAYABLE", "PAYMENT_IN_PROGRESS", "PAYLOAD_TOO_LARGE", "TICKET_LIMIT_EXCEEDED",
      "IDEMPOTENCY_KEY_REUSED", "RATE_LIMITED", "OTP_ATTEMPTS_EXCEEDED", "INTERNAL", "PAYMENT_PROVIDER_UNAVAILABLE",
    ];
    for (const code of codes) {
      const text = errorMessage(apiError(400, code));
      expect(text, code).not.toContain("server text");
      expect(text, code).not.toMatch(/[A-Z]{3,}_[A-Z]/);
      expect(text.length, code).toBeGreaterThan(10);
    }
  });

  it("falls back to the server's message for a code the app has no wording for", () => {
    expect(errorMessage(apiError(409, "CATEGORY_IN_USE"))).toBe("server text for CATEGORY_IN_USE");
    expect(errorMessage(new Error("boom"))).toBe("Something went wrong. Please try again.");
  });
});

describe("unavailableSeatIds", () => {
  it("reads the lost seats from a 409", () => {
    expect(unavailableSeatIds(apiError(409, "SEATS_UNAVAILABLE", { unavailable_seat_ids: [5, 9] }))).toEqual([5, 9]);
  });
  it("is empty for anything else", () => {
    expect(unavailableSeatIds(apiError(409, "HOLD_EXPIRED"))).toEqual([]);
    expect(unavailableSeatIds(new Error("x"))).toEqual([]);
    expect(unavailableSeatIds(apiError(409, "SEATS_UNAVAILABLE", null))).toEqual([]);
  });
});

describe("error kinds", () => {
  it("knows which failures are worth retrying", () => {
    expect(isRetryable(ApiError.network(null))).toBe(true);
    expect(isRetryable(apiError(500, "INTERNAL"))).toBe(true);
    expect(isRetryable(apiError(502, "PAYMENT_PROVIDER_UNAVAILABLE"))).toBe(true);
    expect(isRetryable(apiError(409, "SEATS_UNAVAILABLE"))).toBe(false);
    expect(isSignedOutError(apiError(401, "UNAUTHENTICATED"))).toBe(true);
    expect(isSignedOutError(apiError(403, "FORBIDDEN"))).toBe(false);
  });
});

describe("EVENT_BUSY", () => {
  const busy = apiError(409, "EVENT_BUSY");

  it("is said in plain words, not as seats gone or sales closed", () => {
    expect(errorMessage(busy)).toBe("The organiser is updating this event. Try again in a moment.");
    expect(errorMessage(busy)).not.toMatch(/taken|not on sale|closed|EVENT_BUSY/i);
  });

  it("can be retried and loses the fan no seats", () => {
    expect(isEventBusy(busy)).toBe(true);
    expect(isRetryable(busy)).toBe(true);
    expect(unavailableSeatIds(busy)).toEqual([]);
    expect(holdEnded(busy)).toBeNull();
  });

  it("is not confused with the other 409s", () => {
    expect(isEventBusy(apiError(409, "SEATS_UNAVAILABLE", { unavailable_seat_ids: [1] }))).toBe(false);
    expect(isEventBusy(apiError(409, "EVENT_NOT_ON_SALE"))).toBe(false);
    expect(isEventBusy(ApiError.network(null))).toBe(false);
    expect(isEventBusy(new Error("x"))).toBe(false);
  });
});

describe("a hold that can no longer be paid for", () => {
  it("tells too little time left from a hold that ran out", () => {
    const details = { hold_id: 5, expires_at: "2026-10-11T23:00:08Z" };
    expect(holdEnded(apiError(409, "HOLD_EXPIRED", { ...details, hold_status: "ACTIVE" }))).toBe("too_late");
    expect(holdEnded(apiError(409, "HOLD_EXPIRED", { ...details, hold_status: "EXPIRED" }))).toBe("ended");
    expect(holdEnded(apiError(409, "HOLD_EXPIRED"))).toBe("ended");
    expect(holdEnded(apiError(409, "HOLD_NOT_ACTIVE", { ...details, hold_status: "RELEASED" }))).toBe("ended");
    expect(holdEnded(apiError(409, "PAYMENT_IN_PROGRESS"))).toBeNull();
    expect(holdEnded(apiError(502, "PAYMENT_PROVIDER_UNAVAILABLE"))).toBeNull();
  });

  it("has a clear line for the seat map in both cases", () => {
    expect(backToMapMessage("too_late")).toContain("too little time left to pay");
    expect(backToMapMessage("ended")).toContain("ran out");
  });
});

describe("PAYMENT_IN_PROGRESS", () => {
  it("names the live payment to follow", () => {
    expect(livePaymentId(apiError(409, "PAYMENT_IN_PROGRESS", { payment_id: 66, payment_status: "PENDING" }))).toBe(66);
    expect(livePaymentId(apiError(409, "PAYMENT_IN_PROGRESS"))).toBeNull();
    expect(livePaymentId(apiError(409, "EVENT_BUSY"))).toBeNull();
  });
});

describe("shouldRetryQuery", () => {
  it("retries connection trouble and server faults twice", () => {
    expect(shouldRetryQuery(0, ApiError.network(null))).toBe(true);
    expect(shouldRetryQuery(1, apiError(500, "INTERNAL"))).toBe(true);
    expect(shouldRetryQuery(2, apiError(500, "INTERNAL"))).toBe(false);
  });

  it("does not repeat a wrong request", () => {
    expect(shouldRetryQuery(0, apiError(404, "NOT_FOUND"))).toBe(false);
    expect(shouldRetryQuery(0, apiError(409, "EVENT_BUSY"))).toBe(false);
  });

  it("gives a 401 one more go, so a public read is asked again once a dead token is dropped", () => {
    expect(shouldRetryQuery(0, apiError(401, "UNAUTHENTICATED"))).toBe(true);
    expect(shouldRetryQuery(1, apiError(401, "UNAUTHENTICATED"))).toBe(false);
  });
});

describe("paymentOutcome", () => {
  type P = Parameters<typeof paymentOutcome>[0];
  const p = (status: P["status"], failure_reason: P["failure_reason"] = null, booking_status: P["booking_status"] = "PENDING_PAYMENT") =>
    paymentOutcome({ status, failure_reason, booking_status });

  it("names each failure and offers a retry while the booking can still be paid", () => {
    expect(p("PENDING")).toMatchObject({ tone: "waiting", title: "Check your phone", canRetry: false, unsettled: true });
    expect(p("INITIATED")).toMatchObject({ tone: "waiting", canRetry: false, unsettled: true });
    expect(p("FAILED", "cancelled")).toMatchObject({ tone: "failed", title: "Payment cancelled", canRetry: true, unsettled: false });
    expect(p("FAILED", "wrong_pin")).toMatchObject({ tone: "failed", title: "Wrong PIN", canRetry: true });
    expect(p("FAILED", "wrong_pin").body).toContain("No money was taken");
    expect(p("FAILED", "insufficient_funds")).toMatchObject({ tone: "failed", title: "Not enough money", canRetry: true });
    expect(p("FAILED", "timeout")).toMatchObject({ tone: "failed", title: "Payment timed out", canRetry: true });
    expect(p("FAILED", "other")).toMatchObject({ tone: "failed", canRetry: true });
  });

  it("offers no retry once the hold has run out under the booking", () => {
    expect(p("FAILED", "wrong_pin", "EXPIRED")).toMatchObject({ tone: "failed", title: "Wrong PIN", canRetry: false });
    expect(p("TIMEOUT", "timeout", "EXPIRED")).toMatchObject({ tone: "failed", canRetry: false });
    expect(p("FAILED", "cancelled", "CANCELLED")).toMatchObject({ canRetry: false });
  });

  it("SUCCESS means tickets only when the booking is PAID", () => {
    expect(p("SUCCESS", null, "PAID")).toMatchObject({ tone: "success", title: "Payment received", body: "Your tickets are ready." });
    expect(p("SUCCESS", null, "PENDING_PAYMENT")).toMatchObject({ tone: "waiting", unsettled: true });
    expect(p("SUCCESS", null, "PENDING_PAYMENT").body).not.toMatch(/ticket/i);
    expect(p("SUCCESS", null, "EXPIRED").tone).not.toBe("success");
  });

  it("paid but the seats were gone: SUCCESS with REFUND_REQUIRED is a refund, never tickets", () => {
    const o = p("SUCCESS", null, "REFUND_REQUIRED");
    expect(o).toMatchObject({ tone: "refund", title: "We could not give you these seats", canRetry: false, unsettled: false });
    expect(o.body).toContain("We received your payment but could not give you these seats. You will be refunded.");
    expect(p("SUCCESS", null, "REFUNDED")).toMatchObject({ tone: "refund", canRetry: false });
  });

  it("wrong amount: FAILED with REFUND_REQUIRED is a refund with no retry, not an ordinary failure", () => {
    const o = p("FAILED", "wrong_amount", "REFUND_REQUIRED");
    expect(o).toMatchObject({ tone: "refund", title: "The amount paid was wrong", canRetry: false, unsettled: false });
    expect(o.body).toContain("You will be refunded");
    expect(o.body).not.toContain("No money was taken");
    // Even if the booking read is a moment behind, a wrong amount is never "try again".
    expect(p("FAILED", "wrong_amount", "PENDING_PAYMENT")).toMatchObject({ tone: "refund", canRetry: false });
  });

  it("a TIMEOUT may still settle either way, and does not claim no money was taken", () => {
    const o = p("TIMEOUT", "timeout");
    expect(o).toMatchObject({ tone: "failed", title: "M-Pesa has not answered", canRetry: true, unsettled: true });
    expect(o.body).not.toContain("No money was taken");
    expect(o.body).toMatch(/tickets or a refund/);
  });

  it("a booking that is PAID wins over an older failed attempt (a late success, or paid twice)", () => {
    expect(p("TIMEOUT", "timeout", "PAID")).toMatchObject({ tone: "success" });
    expect(p("FAILED", "wrong_pin", "PAID")).toMatchObject({ tone: "success" });
  });
});
