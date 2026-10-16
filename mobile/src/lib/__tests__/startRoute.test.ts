import { describe, expect, it } from "vitest";

import { startRoute } from "../startRoute";

describe("startRoute", () => {
  it("waits while the session is being read", () => {
    expect(startRoute({ status: "loading", role: null, welcomeSeen: false })).toBeNull();
  });

  it("waits for the welcome flag when signed out", () => {
    expect(startRoute({ status: "signedOut", role: null, welcomeSeen: null })).toBeNull();
  });

  it("shows the welcome slides once to a new, signed-out phone", () => {
    expect(startRoute({ status: "signedOut", role: null, welcomeSeen: false })).toBe("/welcome");
    expect(startRoute({ status: "signedOut", role: null, welcomeSeen: true })).toBe("/events");
  });

  it("never shows the slides to someone already signed in", () => {
    expect(startRoute({ status: "signedIn", role: "FAN", welcomeSeen: false })).toBe("/events");
    expect(startRoute({ status: "signedIn", role: "FAN", welcomeSeen: null })).toBe("/events");
  });

  it("sends gate staff and admins to the scanner, slides or not", () => {
    expect(startRoute({ status: "signedIn", role: "GATE_STAFF", welcomeSeen: false })).toBe("/gate");
    expect(startRoute({ status: "signedIn", role: "ADMIN", welcomeSeen: true })).toBe("/gate");
  });
});
