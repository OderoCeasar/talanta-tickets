import { create } from "zustand";

import { serverOffsetMs } from "../lib/countdown";

// Local UI state only: which seats the fan has tapped, and when the current hold runs out.
// Everything the server owns (seat status, prices, holds, bookings) stays in TanStack Query;
// copying it here is how seat maps go stale (ARCHITECTURE §12).
interface BookingFlowState {
  /** The block the selection belongs to. A selection never spans two blocks. */
  eventId: number | null;
  blockId: number | null;
  /** Event seat IDs, in the order they were tapped. */
  seatIds: number[];
  /** Seats the server refused on the last hold attempt; drawn greyed until the block refreshes. */
  lostSeatIds: number[];

  /** `expires_at` of the hold being paid for, as the server sent it. */
  holdExpiresAt: string | null;
  /** Server clock minus phone clock, so the countdown is right on a phone with a wrong clock. */
  serverOffsetMs: number;

  /** Starts (or keeps) a selection for a block; a different block clears the old selection. */
  openBlock: (eventId: number, blockId: number) => void;
  toggleSeat: (seatId: number) => void;
  removeSeats: (seatIds: readonly number[]) => void;
  markLost: (seatIds: readonly number[]) => void;
  unmarkLost: (seatIds: readonly number[]) => void;
  clearSelection: () => void;

  /** Records the expiry from a hold answer, with the server's own remaining time. */
  syncHold: (expiresAt: string, remainingSeconds: number) => void;
  /** Records a new expiry that came without `remaining_seconds` (a booking's `hold_expires_at`). */
  setHoldExpiry: (expiresAt: string | null) => void;
  clearHold: () => void;
  reset: () => void;
}

const empty = {
  eventId: null,
  blockId: null,
  seatIds: [] as number[],
  lostSeatIds: [] as number[],
  holdExpiresAt: null,
  serverOffsetMs: 0,
};

export const useBookingFlow = create<BookingFlowState>((set) => ({
  ...empty,
  openBlock: (eventId, blockId) =>
    set((s) => (s.eventId === eventId && s.blockId === blockId ? s : { eventId, blockId, seatIds: [], lostSeatIds: [] })),
  toggleSeat: (seatId) =>
    set((s) => ({ seatIds: s.seatIds.includes(seatId) ? s.seatIds.filter((id) => id !== seatId) : [...s.seatIds, seatId] })),
  removeSeats: (seatIds) => set((s) => ({ seatIds: s.seatIds.filter((id) => !seatIds.includes(id)) })),
  markLost: (seatIds) =>
    set((s) => ({
      seatIds: s.seatIds.filter((id) => !seatIds.includes(id)),
      lostSeatIds: [...new Set([...s.lostSeatIds, ...seatIds])],
    })),
  unmarkLost: (seatIds) => set((s) => ({ lostSeatIds: s.lostSeatIds.filter((id) => !seatIds.includes(id)) })),
  clearSelection: () => set({ seatIds: [], lostSeatIds: [] }),
  syncHold: (expiresAt, remainingSeconds) =>
    set({ holdExpiresAt: expiresAt, serverOffsetMs: serverOffsetMs(expiresAt, remainingSeconds, Date.now()) }),
  setHoldExpiry: (expiresAt) => set({ holdExpiresAt: expiresAt }),
  clearHold: () => set({ holdExpiresAt: null }),
  reset: () => set({ ...empty }),
}));
