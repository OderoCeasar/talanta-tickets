import { describe, expect, it } from "vitest";
import { centsToShillingsInput, formatCount, formatKes, parseShillingsToCents } from "./money";

const cents = (input: string) => {
  const parsed = parseShillingsToCents(input);
  return parsed.ok ? parsed.cents : null;
};

describe("parseShillingsToCents", () => {
  it("reads whole shillings", () => {
    expect(cents("1500")).toBe(150000);
    expect(cents("0")).toBe(0);
    expect(cents("  500 ")).toBe(50000);
    expect(cents("007")).toBe(700);
  });

  it("reads thousands separators", () => {
    expect(cents("1,500")).toBe(150000);
    expect(cents("1,000,000")).toBe(100000000);
  });

  it("reads one or two decimal places without float arithmetic", () => {
    expect(cents("1500.5")).toBe(150050);
    expect(cents("1,500.50")).toBe(150050);
    expect(cents("0.07")).toBe(7);
    // 1.1 * 100 and 4.35 * 100 are 110.00000000000001 and 434.99999999999994 as floats.
    expect(cents("1.1")).toBe(110);
    expect(cents("4.35")).toBe(435);
    expect(cents("19.99")).toBe(1999);
  });

  it("refuses anything that is not plainly a price", () => {
    for (const bad of ["", " ", "12.345", "1e3", "-5", "+5", "1,50", "1,5000", ",500", "1.", ".5", "1 500", "KES 5", "0x10", "١٢", "5.5.5", "NaN", "Infinity"]) {
      expect(parseShillingsToCents(bad).ok, bad).toBe(false);
    }
  });

  it("refuses prices too large to be a ticket", () => {
    expect(cents("99999999.99")).toBe(9999999999);
    expect(parseShillingsToCents("100000000").ok).toBe(false);
  });
});

describe("formatting cents", () => {
  it("shows KES with separators and drops .00", () => {
    expect(formatKes(150000)).toBe("KES 1,500");
    expect(formatKes(150050)).toBe("KES 1,500.50");
    expect(formatKes(7)).toBe("KES 0.07");
    expect(formatKes(0)).toBe("KES 0");
    expect(formatKes(5956800000)).toBe("KES 59,568,000");
    expect(formatKes(-250000)).toBe("-KES 2,500");
  });

  it("round-trips through the price box", () => {
    for (const value of [0, 7, 110, 435, 150000, 150050, 9999999999]) {
      expect(cents(centsToShillingsInput(value))).toBe(value);
    }
  });

  it("groups counts", () => {
    expect(formatCount(59568)).toBe("59,568");
    expect(formatCount(999)).toBe("999");
  });
});
