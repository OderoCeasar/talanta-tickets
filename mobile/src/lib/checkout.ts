// What checkout shows before and after the booking exists. No React Native imports, so the
// rules are unit tested.

import type { components } from "@talanta/shared";

type Schemas = components["schemas"];
type HoldAmounts = Pick<Schemas["Hold"], "status" | "subtotal_cents" | "fee_cents" | "total_cents" | "seats">;
type BookingAmounts = Pick<Schemas["Booking"], "subtotal_cents" | "fee_cents" | "total_cents" | "items">;

export interface CheckoutLine {
  eventSeatId: number;
  location: Schemas["SeatLocation"];
  categoryName: string;
  priceCents: number;
}

export interface CheckoutAmounts {
  /**
   * `booking`: the binding prices copied when the booking was made. `hold`: the hold's
   * estimate at today's prices, shown before the fan taps Pay. `none`: nothing to show (the
   * hold has ended and there is no booking; an ended hold answers with no seats and zeros).
   */
  source: "booking" | "hold" | "none";
  lines: CheckoutLine[];
  subtotalCents: number;
  feeCents: number;
  totalCents: number;
}

/**
 * The seats and the amount to show at checkout.
 *
 * Before Pay there is no booking, so the total comes from the hold. Once a booking exists
 * its amounts win, because they are what M-Pesa will be asked for. A hold that has ended
 * (`EXPIRED`, `RELEASED`) carries no seats and zero amounts: it is never shown as "KES 0".
 */
export function checkoutAmounts(hold: HoldAmounts | null | undefined, booking: BookingAmounts | null | undefined): CheckoutAmounts {
  if (booking) {
    return {
      source: "booking",
      lines: booking.items.map((i) => ({ eventSeatId: i.event_seat_id, location: i.location, categoryName: i.category_name, priceCents: i.unit_price_cents })),
      subtotalCents: booking.subtotal_cents,
      feeCents: booking.fee_cents,
      totalCents: booking.total_cents,
    };
  }
  if (hold && hold.seats.length > 0 && (hold.status === "ACTIVE" || hold.status === "CONVERTED")) {
    return {
      source: "hold",
      lines: hold.seats.map((s) => ({ eventSeatId: s.event_seat_id, location: s.location, categoryName: s.category_name, priceCents: s.price_cents })),
      subtotalCents: hold.subtotal_cents,
      feeCents: hold.fee_cents,
      totalCents: hold.total_cents,
    };
  }
  return { source: "none", lines: [], subtotalCents: 0, feeCents: 0, totalCents: 0 };
}

/**
 * How often to read an unpaid booking again, given its latest payment. The booking is the
 * truth a screen resumes from. Even a `FAILED` payment can turn into `SUCCESS` if M-Pesa
 * later confirms the money arrived, so an unpaid booking is never read just once.
 */
export function bookingPollMs(status: Schemas["PaymentStatus"]): number | false {
  if (status === "INITIATED" || status === "PENDING") return 5000;
  if (status === "TIMEOUT") return 10_000;
  if (status === "FAILED") return 30_000;
  return false;
}

/** How often to ask about a payment again, in milliseconds; false once nothing can change. */
export function paymentPollMs(status: Schemas["PaymentStatus"] | undefined): number | false {
  if (status === undefined || status === "INITIATED" || status === "PENDING") return 2000;
  // M-Pesa can still report late on a payment we gave up waiting for.
  if (status === "TIMEOUT") return 10_000;
  return false;
}
