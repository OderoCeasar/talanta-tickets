import { ApiError, type ApiErrorCode } from "@talanta/shared";
import { describe, expect, it } from "vitest";

import { errorMessage, isRetryable, isSignedOutError, paymentOutcome, unavailableSeatIds } from "../errors";

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
    expect(errorMessage(apiError(409, "PAYMENT_IN_PROGRESS"))).toContain("already waiting");
    expect(errorMessage(apiError(502, "PAYMENT_PROVIDER_UNAVAILABLE"))).toContain("No money was taken");
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
    expect(isRetryable(apiError(409, "SEATS_UNAVAILABLE"))).toBe(false);
    expect(isSignedOutError(apiError(401, "UNAUTHENTICATED"))).toBe(true);
    expect(isSignedOutError(apiError(403, "FORBIDDEN"))).toBe(false);
  });
});

describe("paymentOutcome", () => {
  const p = (status: Parameters<typeof paymentOutcome>[0]["status"], failure_reason: Parameters<typeof paymentOutcome>[0]["failure_reason"] = null, booking_status: Parameters<typeof paymentOutcome>[0]["booking_status"] = "PENDING_PAYMENT") =>
    paymentOutcome({ status, failure_reason, booking_status });

  it("names each outcome and offers a retry only where one can work", () => {
    expect(p("PENDING")).toMatchObject({ tone: "waiting", title: "Check your phone", canRetry: false });
    expect(p("SUCCESS", null, "PAID")).toMatchObject({ tone: "success", title: "Payment received" });
    expect(p("FAILED", "cancelled")).toMatchObject({ tone: "failed", title: "Payment cancelled", canRetry: true });
    expect(p("FAILED", "wrong_pin")).toMatchObject({ tone: "failed", title: "Wrong PIN", canRetry: true });
    expect(p("FAILED", "insufficient_funds")).toMatchObject({ tone: "failed", title: "Not enough money", canRetry: true });
    expect(p("TIMEOUT", "timeout")).toMatchObject({ tone: "failed", title: "Payment timed out", canRetry: true });
    expect(p("FAILED", "other")).toMatchObject({ tone: "failed", canRetry: true });
  });

  it("promises a refund, and no retry, when money arrived but tickets were not issued", () => {
    expect(p("FAILED", "wrong_amount", "REFUND_REQUIRED")).toMatchObject({ tone: "refund", title: "The amount paid was wrong", canRetry: false });
    expect(p("SUCCESS", null, "REFUND_REQUIRED")).toMatchObject({ tone: "refund", canRetry: false });
    expect(p("SUCCESS", null, "REFUND_REQUIRED").body).toContain("refunded");
  });

  it("does not say 'paid' before the booking is", () => {
    expect(p("SUCCESS", null, "PENDING_PAYMENT")).toMatchObject({ tone: "waiting" });
  });
});
