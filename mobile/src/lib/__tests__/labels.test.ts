import { describe, expect, it } from "vitest";

import { standTitle } from "../labels";

describe("standTitle", () => {
  it("tells the two tiers of one side apart", () => {
    expect(standTitle("North Stand", "LOWER")).toBe("North Stand · Lower");
    expect(standTitle("North Stand", "UPPER")).toBe("North Stand · Upper");
  });

  it("does not repeat a tier the name already says", () => {
    expect(standTitle("North Upper", "UPPER")).toBe("North Upper");
  });
});
