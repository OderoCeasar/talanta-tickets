import { describe, expect, it } from "vitest";

import { qrPath } from "../qrPath";

describe("qrPath", () => {
  const token = `TLT1.${"a".repeat(96)}.${"b".repeat(86)}`;

  it("draws a ticket token as a square code with a quiet zone", () => {
    const { size, path } = qrPath(token);
    // Version 1 is 21 modules; a 190-character token needs a much larger code, plus 4 quiet modules a side.
    expect(size).toBeGreaterThan(21 + 8);
    expect(path.startsWith("M")).toBe(true);
    // Nothing is drawn inside the quiet zone.
    const xs = [...path.matchAll(/M(\d+) (\d+)/g)].map((m) => [Number(m[1]), Number(m[2])]);
    expect(Math.min(...xs.flat())).toBeGreaterThanOrEqual(4);
    expect(Math.max(...xs.flat())).toBeLessThan(size - 4);
  });

  it("gives different codes for different tickets and the same code for the same ticket", () => {
    expect(qrPath(token).path).toBe(qrPath(token).path);
    expect(qrPath(token).path).not.toBe(qrPath(token.replace("a", "c")).path);
  });
});
