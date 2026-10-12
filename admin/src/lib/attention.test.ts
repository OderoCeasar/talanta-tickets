import { describe, expect, it } from "vitest";
import { alertHasRefund, alertKindLabel, alertNextStep, badgeText, countPhrase, refundStatusLabel, wasAlreadyCompleted } from "./attention";

describe("alertKindLabel", () => {
  it("says each kind in plain words, never the code", () => {
    expect(alertKindLabel("REFUND_REQUIRED")).toBe("Paid, but got no seats");
    expect(alertKindLabel("AMOUNT_MISMATCH")).toBe("Paid the wrong amount");
    expect(alertKindLabel("PAYMENT_UNRESOLVED")).toBe("No answer from M-Pesa");
  });

  it("has words for a kind added to the API after this build", () => {
    expect(alertKindLabel("SOMETHING_NEW")).toBe("Needs a look");
  });
});

describe("alertNextStep", () => {
  it("points at the refund queue only for the kinds that queue a refund", () => {
    expect(alertHasRefund("REFUND_REQUIRED")).toBe(true);
    expect(alertHasRefund("AMOUNT_MISMATCH")).toBe(true);
    expect(alertHasRefund("PAYMENT_UNRESOLVED")).toBe(false);
    expect(alertNextStep("AMOUNT_MISMATCH")).toContain("refund queue");
    expect(alertNextStep("REFUND_REQUIRED")).toContain("refund queue");
  });

  it("says that nothing is queued for an unresolved payment", () => {
    expect(alertNextStep("PAYMENT_UNRESOLVED")).toContain("No refund is queued");
    expect(alertNextStep("PAYMENT_UNRESOLVED")).toContain("M-Pesa portal");
  });

  it("does not promise a refund for a kind it does not know", () => {
    expect(alertNextStep("SOMETHING_NEW")).not.toContain("refund");
  });
});

describe("refundStatusLabel", () => {
  it("names both statuses", () => {
    expect(refundStatusLabel("PENDING")).toBe("Waiting to be paid back");
    expect(refundStatusLabel("DONE")).toBe("Paid back");
  });
});

describe("badgeText and countPhrase", () => {
  it("shows nothing for zero", () => {
    expect(badgeText(0, false)).toBeNull();
  });

  it("shows the number, with a plus when the server has more than the page", () => {
    expect(badgeText(7, false)).toBe("7");
    expect(badgeText(100, true)).toBe("100+");
  });

  it("uses the singular only for exactly one", () => {
    expect(countPhrase(1, false, "alert", "alerts")).toBe("1 alert");
    expect(countPhrase(3, false, "alert", "alerts")).toBe("3 alerts");
    expect(countPhrase(1, true, "alert", "alerts")).toBe("1+ alerts");
    expect(countPhrase(0, false, "alert", "alerts")).toBe("0 alerts");
  });
});

describe("wasAlreadyCompleted", () => {
  const sent = Date.parse("2026-10-12T09:00:00Z");

  it("is false when our request is what completed it, even with clocks a little apart", () => {
    expect(wasAlreadyCompleted({ completed_at: "2026-10-12T09:00:01Z" }, sent)).toBe(false);
    expect(wasAlreadyCompleted({ completed_at: "2026-10-12T08:59:10Z" }, sent)).toBe(false);
  });

  it("is true when the completion is clearly older than our request", () => {
    expect(wasAlreadyCompleted({ completed_at: "2026-10-12T08:30:00Z" }, sent)).toBe(true);
  });

  it("is false with no completion time or one that cannot be read", () => {
    expect(wasAlreadyCompleted({ completed_at: null }, sent)).toBe(false);
    expect(wasAlreadyCompleted({ completed_at: "not a date" }, sent)).toBe(false);
  });
});
