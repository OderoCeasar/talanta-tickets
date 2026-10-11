import { useQueryClient } from "@tanstack/react-query";
import { router, useFocusEffect } from "expo-router";
import { useCallback, useEffect, useRef, useState } from "react";

import { isLivePayment, keys, useBooking, useMyHolds } from "../../api/queries";
import { clearStoredCheckout, readStoredCheckout, type StoredCheckout } from "../../api/storage";
import { formatCountdown, remainingMs } from "../../lib/countdown";
import { useAppActive } from "../../lib/useAppActive";
import { useNow } from "../../lib/useNow";
import { useSession } from "../../session";
import { useBookingFlow } from "../../store/bookingFlow";
import { Banner } from "../../ui";

// The app was mid-payment when it was last closed: go straight back to checkout, once.
let autoResumed = false;

/**
 * Picks an unfinished purchase back up after the app was closed or sent to the background.
 * Everything shown comes from the server: `GET /me/holds` for seats still held, and
 * `GET /bookings/{reference}` for a booking whose payment finished while the app was away.
 * The phone only remembers which booking to ask about.
 */
export function ResumeBanner() {
  const { status, user } = useSession();
  const isFan = status === "signedIn" && user?.role === "FAN";
  const qc = useQueryClient();
  const appActive = useAppActive();
  const holds = useMyHolds(isFan);
  const [stored, setStored] = useState<StoredCheckout | null>(null);
  const syncHold = useBookingFlow((s) => s.syncHold);
  const expiresAt = useBookingFlow((s) => s.holdExpiresAt);
  const offset = useBookingFlow((s) => s.serverOffsetMs);

  const refetchHolds = holds.refetch;
  useFocusEffect(
    useCallback(() => {
      if (!isFan || !appActive) return;
      void readStoredCheckout().then(setStored);
      void refetchHolds();
    }, [isFan, appActive, refetchHolds]),
  );

  const hold = holds.data?.items[0] ?? null;
  const reference = hold?.booking_reference ?? stored?.bookingReference ?? null;
  const booking = useBooking(isFan ? reference : null);

  useEffect(() => {
    if (hold) syncHold(hold.expires_at, hold.remaining_seconds);
  }, [hold, syncHold]);

  const now = useNow(1000, hold !== null);
  const left = hold ? remainingMs(expiresAt ?? hold.expires_at, now, offset) : 0;

  const livePayment = booking.data?.latest_payment ? isLivePayment(booking.data.latest_payment.status) : false;
  const holdId = hold?.id;
  const resumed = useRef(false);
  useEffect(() => {
    if (!holdId || !livePayment || autoResumed || resumed.current) return;
    autoResumed = true;
    resumed.current = true;
    router.push({ pathname: "/checkout/[holdId]", params: { holdId: String(holdId) } });
  }, [holdId, livePayment]);

  // A stored pointer to a booking that ended without tickets and without a refund owed has
  // nothing left to say.
  const ended = booking.data && !hold && ["EXPIRED", "CANCELLED", "FAILED"].includes(booking.data.status);
  useEffect(() => {
    if (ended) void clearStoredCheckout().then(() => setStored(null));
  }, [ended]);

  if (!isFan) return null;

  if (hold && left > 0) {
    const n = hold.seats.length;
    return (
      <Banner
        tone="warning"
        testID="resume-hold"
        title={livePayment ? "Your payment is in progress" : `You have ${n} seat${n === 1 ? "" : "s"} on hold`}
        action="Continue to checkout"
        onAction={() => router.push({ pathname: "/checkout/[holdId]", params: { holdId: String(hold.id) } })}
      >
        {`${formatCountdown(left)} left to pay before the seats are released.`}
      </Banner>
    );
  }

  if (stored && booking.data && !hold) {
    const b = booking.data;
    const dismiss = () => {
      void clearStoredCheckout().then(() => setStored(null));
      void qc.invalidateQueries({ queryKey: keys.tickets });
    };
    if (b.status === "PAID") {
      return (
        <Banner
          tone="success"
          testID="resume-paid"
          title="Payment received"
          action="See your tickets"
          onAction={() => {
            dismiss();
            router.push({ pathname: "/booking/[reference]", params: { reference: b.reference } });
          }}
        >
          {`Booking ${b.reference} is paid. Your ${b.tickets.length === 1 ? "ticket is" : `${b.tickets.length} tickets are`} ready.`}
        </Banner>
      );
    }
    if (b.status === "REFUND_REQUIRED" || b.status === "REFUNDED") {
      return (
        <Banner tone="error" testID="resume-refund" title={b.status === "REFUNDED" ? "Your money was refunded" : "A refund is on its way"} action="OK" onAction={dismiss}>
          {`We received your payment for booking ${b.reference} but could not give you the seats. ${b.status === "REFUNDED" ? "The money has been sent back." : "You do not need to do anything; the money will be sent back to your M-Pesa."}`}
        </Banner>
      );
    }
  }
  return null;
}
