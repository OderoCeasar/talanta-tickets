import { describe, expect, it } from "vitest";

import { formatCountdown, isUrgent, remainingMs, serverOffsetMs } from "../countdown";

const expiresAt = "2026-10-11T09:25:00Z";
const expiry = Date.parse(expiresAt);

describe("remainingMs", () => {
  it("is the time from now to the server's expiry", () => {
    expect(remainingMs(expiresAt, expiry - 600_000)).toBe(600_000);
    expect(remainingMs(expiresAt, expiry - 1)).toBe(1);
  });

  it("never goes below zero", () => {
    expect(remainingMs(expiresAt, expiry)).toBe(0);
    expect(remainingMs(expiresAt, expiry + 60_000)).toBe(0);
  });

  it("does not depend on when the screen opened: a later 'now' gives less time", () => {
    // The app sat in the background for four minutes; no timer ran, the answer is still right.
    const opened = expiry - 600_000;
    expect(remainingMs(expiresAt, opened + 240_000)).toBe(360_000);
  });

  it("follows a new expiry when the server pushes the hold out for a payment", () => {
    const now = expiry - 30_000;
    expect(remainingMs(expiresAt, now)).toBe(30_000);
    expect(remainingMs("2026-10-11T09:30:30Z", now)).toBe(360_000);
  });

  it("is zero with no hold or a bad timestamp", () => {
    expect(remainingMs(null, expiry)).toBe(0);
    expect(remainingMs("nonsense", expiry)).toBe(0);
  });
});

describe("serverOffsetMs", () => {
  it("is zero when the phone's clock agrees with the server", () => {
    expect(serverOffsetMs(expiresAt, 600, expiry - 600_000)).toBe(0);
  });

  it("corrects a phone whose clock is five minutes fast", () => {
    const serverNow = expiry - 600_000;
    const phoneNow = serverNow + 300_000;
    const offset = serverOffsetMs(expiresAt, 600, phoneNow);
    expect(offset).toBe(-300_000);
    // Without the correction the fan would see 5:00; with it, the true 10:00.
    expect(remainingMs(expiresAt, phoneNow)).toBe(300_000);
    expect(remainingMs(expiresAt, phoneNow, offset)).toBe(600_000);
  });

  it("corrects a phone whose clock is slow, so the countdown does not overrun", () => {
    const serverNow = expiry - 60_000;
    const phoneNow = serverNow - 120_000;
    const offset = serverOffsetMs(expiresAt, 60, phoneNow);
    expect(remainingMs(expiresAt, phoneNow, offset)).toBe(60_000);
  });
});

describe("formatCountdown", () => {
  it.each([
    [600_000, "10:00"],
    [599_001, "10:00"],
    [599_000, "9:59"],
    [61_000, "1:01"],
    [7_000, "0:07"],
    [1, "0:01"],
    [0, "0:00"],
    [-5, "0:00"],
  ])("%d ms reads %s", (ms, text) => {
    expect(formatCountdown(ms)).toBe(text);
  });

  it("flags the last minute", () => {
    expect(isUrgent(60_000)).toBe(true);
    expect(isUrgent(60_001)).toBe(false);
  });
});
