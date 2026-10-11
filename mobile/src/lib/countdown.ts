// The hold countdown is worked out from the server's `expires_at`, never from a timer that
// started when the screen opened: a timer drifts while the app is in the background, and a
// fan who reopens the app must see the time that is really left.

/**
 * How far the server's clock is ahead of the phone's, in milliseconds.
 *
 * The hold answer carries both `expires_at` and `remaining_seconds` (the server's view when
 * it answered). Together they give the server's "now", which corrects for a phone whose
 * clock is wrong. `receivedAtMs` is the phone's clock when the answer arrived.
 */
export function serverOffsetMs(expiresAt: string, remainingSeconds: number, receivedAtMs: number): number {
  const serverNow = Date.parse(expiresAt) - remainingSeconds * 1000;
  return serverNow - receivedAtMs;
}

/** Milliseconds left on the hold; never negative. `nowMs` is the phone's clock. */
export function remainingMs(expiresAt: string | null, nowMs: number, offsetMs = 0): number {
  if (!expiresAt) return 0;
  const end = Date.parse(expiresAt);
  if (Number.isNaN(end)) return 0;
  return Math.max(0, end - (nowMs + offsetMs));
}

/** `9:59`, `0:07`. Rounds up, so the display reaches `0:00` only when the time is really gone. */
export function formatCountdown(ms: number): string {
  const total = Math.max(0, Math.ceil(ms / 1000));
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return `${minutes}:${String(seconds).padStart(2, "0")}`;
}

/** Under a minute left: the countdown turns to its warning colour. */
export function isUrgent(ms: number): boolean {
  return ms <= 60_000;
}
