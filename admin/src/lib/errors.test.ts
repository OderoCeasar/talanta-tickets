import { ApiError } from "@talanta/shared";
import { describe, expect, it } from "vitest";
import { BLOCK_BUSY_MESSAGE, categoryInUseText, describeError, fieldMessage, isCode, splitFieldErrors } from "./errors";

const busy = new ApiError({ status: 409, code: "EVENT_BUSY", message: "event is busy" });

const invalid = (fields: { field: string; rule: string; message: string }[]) =>
  new ApiError({ status: 400, code: "VALIDATION_FAILED", message: "validation failed", details: { fields } });

describe("describeError", () => {
  it("says a busy event in plain words, without the server's text", () => {
    expect(describeError(busy)).toBe("This event is being published or changed right now. Wait a moment and try again.");
  });

  it("gives the same busy wording whatever other overrides a screen passes", () => {
    expect(describeError(busy, { NOT_FOUND: "This category no longer exists." })).toBe(describeError(busy));
  });

  it("lets the block editor use its own busy wording", () => {
    expect(describeError(busy, { EVENT_BUSY: BLOCK_BUSY_MESSAGE })).toBe(BLOCK_BUSY_MESSAGE);
    expect(BLOCK_BUSY_MESSAGE).not.toContain("This event");
  });

  it("falls back to the server's message for a code with no wording here", () => {
    const error = new ApiError({ status: 409, code: "VENUE_MISMATCH", message: "Server words." });
    expect(describeError(error)).toBe("Server words.");
  });

  it("does not show a thrown non-API error's text", () => {
    expect(describeError(new TypeError("x is undefined"))).toBe("Something unexpected happened. Try again.");
  });
});

describe("isCode", () => {
  it("matches the code of an API error only", () => {
    expect(isCode(busy, "EVENT_BUSY")).toBe(true);
    expect(isCode(busy, "CATEGORY_IN_USE")).toBe(false);
    expect(isCode(new Error("EVENT_BUSY"), "EVENT_BUSY")).toBe(false);
  });
});

describe("fieldMessage", () => {
  it("words the rules a published event adds", () => {
    expect(fieldMessage({ field: "sales_close_at", rule: "required", message: "is required" })).toBe(
      "A published event must keep the time its ticket sales close. Pick a date and time.",
    );
    expect(fieldMessage({ field: "sales_open_at", rule: "required", message: "is required" })).toBe(
      "A published event must keep the time its ticket sales open. Pick a date and time.",
    );
    expect(fieldMessage({ field: "kickoff_at", rule: "future", message: "must be in the future" })).toBe(
      "This event is published, so kickoff cannot be moved to a time that has already passed.",
    );
    expect(fieldMessage({ field: "price_cents", rule: "gt", message: "must be greater than 0" })).toBe(
      "This event is published, so the price must be above 0.",
    );
  });

  it("keeps the server's message for every other field and rule", () => {
    expect(fieldMessage({ field: "title", rule: "required", message: "Title is required." })).toBe("Title is required.");
    expect(fieldMessage({ field: "sales_close_at", rule: "after_open", message: "Must be after sales open." })).toBe("Must be after sales open.");
  });
});

describe("splitFieldErrors", () => {
  it("puts each message on its field and returns the rest", () => {
    const error = invalid([
      { field: "sales_close_at", rule: "required", message: "is required" },
      { field: "title", rule: "max", message: "Title is too long." },
      { field: "surprise", rule: "unknown", message: "Unknown field surprise." },
    ]);
    expect(splitFieldErrors(error, ["title", "sales_close_at"] as const)).toEqual({
      fields: {
        sales_close_at: "A published event must keep the time its ticket sales close. Pick a date and time.",
        title: "Title is too long.",
      },
      other: ["Unknown field surprise."],
    });
  });

  it("keeps the first message for a field", () => {
    const error = invalid([
      { field: "price_cents", rule: "gt", message: "first" },
      { field: "price_cents", rule: "type", message: "second" },
    ]);
    expect(splitFieldErrors(error, ["price_cents"] as const).fields.price_cents).toBe("This event is published, so the price must be above 0.");
  });

  it("gives nothing for an error without fields", () => {
    expect(splitFieldErrors(busy, ["title"] as const)).toEqual({ fields: {}, other: [] });
    expect(splitFieldErrors(null, ["title"] as const)).toEqual({ fields: {}, other: [] });
    expect(splitFieldErrors(new Error("no"), ["title"] as const)).toEqual({ fields: {}, other: [] });
  });
});

describe("categoryInUseText", () => {
  it("says seats are on sale when nothing is sold or held", () => {
    expect(categoryInUseText({ seats: 1312, sold: 0, held: 0 })).toBe("1,312 seats are on sale in this category.");
    expect(categoryInUseText({ seats: 1, sold: 0, held: 0 })).toBe("1 seat is on sale in this category.");
  });

  it("gives the sold and held counts when any seat is taken", () => {
    expect(categoryInUseText({ seats: 28784, sold: 150, held: 2 })).toBe("It has 28,784 seats in this event: 150 sold, 2 held.");
    expect(categoryInUseText({ seats: 40, sold: 0, held: 3 })).toBe("It has 40 seats in this event: 0 sold, 3 held.");
  });

  it("does not invent a seat count the server did not send", () => {
    expect(categoryInUseText({ sold: 4, held: 1 })).toBe("Seats in this category are taken: 4 sold, 1 held.");
    expect(categoryInUseText({ seats: 0, sold: 0, held: 0 })).toBe("Seats or bookings of this event still use this category.");
    expect(categoryInUseText(null)).toBe("Seats or bookings of this event still use this category.");
    expect(categoryInUseText({ seats: "many", sold: -1 })).toBe("Seats or bookings of this event still use this category.");
  });
});
