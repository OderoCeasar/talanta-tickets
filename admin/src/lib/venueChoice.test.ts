import { describe, expect, it } from "vitest";
import { venueProblem } from "./venueChoice";

describe("venueProblem", () => {
  it("is nothing once a venue has loaded", () => {
    expect(venueProblem({ isError: false, isSuccess: true, count: 1 })).toBeNull();
  });

  it("says loading only while the answer is still on its way", () => {
    expect(venueProblem({ isError: false, isSuccess: false, count: 0 })).toMatchObject({ label: "Loading…", missing: false });
  });

  it("says no venue, not loading, when the server answers with an empty list", () => {
    expect(venueProblem({ isError: false, isSuccess: true, count: 0 })).toMatchObject({
      label: "No venue has been set up",
      missing: true,
    });
  });

  it("says the load failed when it did", () => {
    expect(venueProblem({ isError: true, isSuccess: false, count: 0 })).toMatchObject({
      label: "Could not load the venue",
      missing: false,
    });
  });
});
