import { describe, expect, it } from "vitest";

import { formatPhone, formatPhoneLocal, normalisePhone } from "../phone";

describe("normalisePhone", () => {
  it.each([
    ["0712345678", "+254712345678"],
    ["0712 345 678", "+254712345678"],
    ["0712-345-678", "+254712345678"],
    ["712345678", "+254712345678"],
    ["254712345678", "+254712345678"],
    ["+254712345678", "+254712345678"],
    ["+254 712 345 678", "+254712345678"],
    ["+254 (0) 712 345 678", "+254712345678"],
    ["  0712345678  ", "+254712345678"],
    ["0110123456", "+254110123456"],
    ["110123456", "+254110123456"],
    ["+254110123456", "+254110123456"],
  ])("turns %s into %s", (input, expected) => {
    expect(normalisePhone(input)).toEqual({ ok: true, e164: expected });
  });

  it("passes through a full international number from another country", () => {
    expect(normalisePhone("+256 772 123456")).toEqual({ ok: true, e164: "+256772123456" });
  });

  it.each([
    ["", "Enter your phone number."],
    ["   ", "Enter your phone number."],
    ["07123", "too short"],
    ["07123456789", "too long"],
    ["0212345678", "07 or 01"],
    ["0712 345 67a", "digits only"],
    ["+12", "complete"],
  ])("refuses %j", (input, fragment) => {
    const result = normalisePhone(input);
    expect(result.ok).toBe(false);
    if (!result.ok) expect(result.message).toContain(fragment);
  });

  it("always produces something the contract's Phone pattern accepts", () => {
    const pattern = /^\+[1-9][0-9]{7,14}$/;
    for (const input of ["0712345678", "712345678", "254712345678", "+254712345678", "0110123456"]) {
      const result = normalisePhone(input);
      expect(result.ok && pattern.test(result.e164)).toBe(true);
    }
  });
});

describe("formatPhone", () => {
  it("groups a Kenyan number", () => {
    expect(formatPhone("+254712345678")).toBe("+254 712 345 678");
    expect(formatPhoneLocal("+254712345678")).toBe("0712 345 678");
  });
  it("leaves other numbers and empty values alone", () => {
    expect(formatPhone("+256772123456")).toBe("+256772123456");
    expect(formatPhone(null)).toBe("");
    expect(formatPhoneLocal(undefined)).toBe("");
  });
});
