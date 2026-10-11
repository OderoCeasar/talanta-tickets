import { describe, expect, it } from "vitest";
import { heatColour, readableOn, soldShare } from "./heat";

const brightness = (hex: string) => {
  const n = Number.parseInt(hex.slice(1), 16);
  return ((n >> 16) & 255) * 299 + ((n >> 8) & 255) * 587 + (n & 255) * 114;
};

describe("heat colour scale", () => {
  it("works out the sold share safely", () => {
    expect(soldShare(77, 308)).toBe(0.25);
    expect(soldShare(0, 308)).toBe(0);
    expect(soldShare(5, 0)).toBe(0);
    expect(soldShare(400, 308)).toBe(1);
  });

  it("gives nothing sold its own neutral colour", () => {
    expect(heatColour(0)).toBe("#eef0ee");
    expect(heatColour(Number.NaN)).toBe("#eef0ee");
    expect(heatColour(0.001)).not.toBe(heatColour(0));
  });

  it("ends on Emerald Ink for a full block", () => {
    expect(heatColour(1)).toBe("#064e3b");
    expect(heatColour(7)).toBe("#064e3b");
  });

  it("only ever gets darker as more is sold", () => {
    let last = brightness(heatColour(0.001));
    for (let step = 1; step <= 100; step++) {
      const now = brightness(heatColour(step / 100));
      expect(now).toBeLessThanOrEqual(last);
      last = now;
    }
    expect(brightness(heatColour(0.5))).toBeLessThan(brightness(heatColour(0.25)));
  });

  it("always returns a #rrggbb colour", () => {
    for (const share of [0, 0.004, 0.33, 0.5, 0.999, 1]) expect(heatColour(share)).toMatch(/^#[0-9a-f]{6}$/);
  });

  it("picks label ink that can be read on the fill", () => {
    expect(readableOn("#064e3b")).toBe("#ffffff");
    expect(readableOn("#eef0ee")).toBe("#1d2321");
    expect(readableOn("#7C3AED")).toBe("#ffffff");
  });
});
