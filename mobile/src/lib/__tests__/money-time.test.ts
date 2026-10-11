import { describe, expect, it } from "vitest";

import { formatCount, formatKes, sumCents } from "../money";
import { dateTile, daysUntil, formatClock, formatDate, formatDateTime, formatTime, nairobiParts } from "../time";

describe("formatKes", () => {
  it.each([
    [0, "KES 0"],
    [50_000, "KES 500"],
    [100_000, "KES 1,000"],
    [1_000_000, "KES 10,000"],
    [123_456_700, "KES 1,234,567"],
    [150_050, "KES 1,500.50"],
    [5, "KES 0.05"],
    [-250_000, "-KES 2,500"],
  ])("formats %d cents as %s", (cents, text) => {
    expect(formatKes(cents)).toBe(text);
  });

  it("refuses fractions of a cent instead of rounding them", () => {
    expect(() => formatKes(100.5)).toThrow(RangeError);
    expect(() => formatKes(Number.NaN)).toThrow(RangeError);
  });

  it("stays exact where floating point would not", () => {
    // 0.1 + 0.2 !== 0.3 in floats; in cents the sum is exact.
    expect(sumCents([10, 20])).toBe(30);
    expect(formatKes(sumCents([1_999_999, 1, 33]))).toBe("KES 20,000.33");
  });
});

describe("sumCents", () => {
  it("adds seat prices", () => {
    expect(sumCents([100_000, 100_000, 500_000, 50_000])).toBe(750_000);
    expect(sumCents([])).toBe(0);
  });
  it("refuses a float", () => {
    expect(() => sumCents([100, 0.5])).toThrow(RangeError);
  });
});

describe("formatCount", () => {
  it("groups thousands", () => {
    expect(formatCount(59568)).toBe("59,568");
    expect(formatCount(7)).toBe("7");
  });
});

describe("Nairobi time", () => {
  // 13:00 UTC is 16:00 in Nairobi (UTC+3, no daylight saving).
  const kickoff = "2026-10-17T13:00:00Z";

  it("shows the Nairobi wall clock whatever the phone's zone is", () => {
    expect(formatTime(kickoff)).toBe("4:00 pm");
    expect(formatDate(kickoff)).toBe("Sat 17 Oct 2026");
    expect(formatDateTime(kickoff)).toBe("Sat 17 Oct 2026, 4:00 pm");
    expect(formatClock(kickoff)).toBe("16:00:00");
  });

  it("rolls the date over at Nairobi midnight, not UTC midnight", () => {
    // 21:30 UTC on the 17th is 00:30 on the 18th in Nairobi.
    expect(formatDate("2026-10-17T21:30:00Z")).toBe("Sun 18 Oct 2026");
    expect(formatTime("2026-10-17T21:30:00Z")).toBe("12:30 am");
    expect(nairobiParts("2026-12-31T22:00:00Z")).toMatchObject({ year: 2027, month: 0, day: 1, hour: 1 });
  });

  it("formats noon and the morning", () => {
    expect(formatTime("2026-10-17T09:00:00Z")).toBe("12:00 pm");
    expect(formatTime("2026-10-17T05:05:00Z")).toBe("8:05 am");
  });

  it("builds the date tile", () => {
    expect(dateTile(kickoff)).toEqual({ day: "17", month: "OCT", weekday: "SAT" });
  });

  it("counts Nairobi calendar days until an event", () => {
    expect(daysUntil(kickoff, "2026-10-17T03:00:00Z")).toBe("today");
    // 22:00 UTC on the 15th is already the 16th in Nairobi: the match is tomorrow.
    expect(daysUntil(kickoff, "2026-10-15T22:00:00Z")).toBe("tomorrow");
    expect(daysUntil(kickoff, "2026-10-08T10:00:00Z")).toBe("in 9 days");
    expect(daysUntil(kickoff, "2026-10-19T10:00:00Z")).toBe("");
  });

  it("refuses a value that is not a time", () => {
    expect(() => formatDate("not a date")).toThrow(RangeError);
  });
});
