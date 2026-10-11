import { ApiError } from "@talanta/shared";
import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";

import { fetchBlockSeats, keys } from "../../api/queries";
import { mergePoll, type BlockSeatsFull } from "./model";

/**
 * While `active`, asks the server what changed since the cached block's marker and patches
 * the TanStack Query cache. `active` must be false when the screen is not in view or the app
 * is in the background, which stops the timer. Becoming active polls at once, so a fan who
 * comes back to the app sees the present, not the moment they left.
 *
 * Returns true when the last few polls failed, so the screen can say updates are paused.
 */
export function useBlockPolling(eventId: number, blockId: number, active: boolean): boolean {
  const qc = useQueryClient();
  const [failures, setFailures] = useState(0);

  useEffect(() => {
    if (!active) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const key = keys.block(eventId, blockId);

    const tick = async () => {
      const cached = qc.getQueryData<BlockSeatsFull>(key);
      let waitMs = (cached?.poll_interval_seconds ?? 5) * 1000;
      if (cached) {
        const since = cached.marker;
        try {
          const answer = await fetchBlockSeats(eventId, blockId, since);
          if (cancelled) return;
          qc.setQueryData<BlockSeatsFull>(key, (current) => {
            // A delta only makes sense on top of the answer it was asked against. If the
            // block was reloaded while this request was in the air, drop the delta; the next
            // poll starts from the new marker.
            if (answer.kind === "delta" && current?.marker !== since) return current;
            return mergePoll(current, answer);
          });
          waitMs = answer.poll_interval_seconds * 1000;
          setFailures(0);
        } catch (error) {
          if (cancelled) return;
          setFailures((n) => n + 1);
          if (error instanceof ApiError && error.retryAfterSeconds) waitMs = Math.max(waitMs, error.retryAfterSeconds * 1000);
        }
      }
      if (!cancelled) timer = setTimeout(tick, Math.max(1000, waitMs));
    };

    void tick();
    return () => {
      cancelled = true;
      if (timer) clearTimeout(timer);
    };
  }, [active, eventId, blockId, qc]);

  return failures >= 2;
}
