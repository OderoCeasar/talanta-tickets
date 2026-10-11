import { describe, expect, it } from "vitest";
import { formatBucket, formatClock, formatDate, formatDateTime, fromNairobiInput, toNairobiInput } from "./format";

describe("Nairobi time", () => {
  it("shows UTC times three hours ahead, whatever the machine's zone", () => {
    expect(formatDateTime("2026-11-14T13:00:00Z")).toBe("14 Nov 2026, 16:00");
    expect(formatClock("2026-11-14T13:04:09Z")).toBe("16:04:09");
  });

  it("moves the date when Nairobi is already in the next day", () => {
    expect(formatDate("2026-11-14T21:30:00Z")).toBe("15 Nov 2026");
    expect(formatDateTime("2026-12-31T22:15:00Z")).toBe("1 Jan 2027, 01:15");
  });

  it("labels chart buckets by Nairobi day and hour", () => {
    // A day bucket starts at Nairobi midnight, which is 21:00 UTC the day before.
    expect(formatBucket("2026-10-13T21:00:00Z", "day")).toBe("14 Oct");
    expect(formatBucket("2026-10-13T21:00:00Z", "hour")).toBe("14 Oct, 00:00");
  });

  it("fills a date box with Nairobi wall-clock time", () => {
    expect(toNairobiInput("2026-11-14T13:00:00Z")).toBe("2026-11-14T16:00");
    expect(toNairobiInput("2026-11-14T22:30:00Z")).toBe("2026-11-15T01:30");
    expect(toNairobiInput(null)).toBe("");
  });

  it("reads a date box as Nairobi time", () => {
    expect(fromNairobiInput("2026-11-14T16:00")).toBe("2026-11-14T13:00:00Z");
    expect(fromNairobiInput("2026-11-15T01:30")).toBe("2026-11-14T22:30:00Z");
    expect(fromNairobiInput("")).toBeNull();
    expect(fromNairobiInput("2026-02-31T10:00")).toBeNull();
    expect(fromNairobiInput("14/11/2026 16:00")).toBeNull();
  });

  it("round-trips", () => {
    for (const iso of ["2026-01-01T00:00:00Z", "2026-06-30T20:59:00Z", "2026-12-31T21:00:00Z"]) {
      expect(fromNairobiInput(toNairobiInput(iso))).toBe(iso);
    }
  });
});
