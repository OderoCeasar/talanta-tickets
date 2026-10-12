import type { components } from "@talanta/shared";
import { describe, expect, it } from "vitest";

import { bookingPollMs, checkoutAmounts, paymentPollMs } from "../checkout";

type Schemas = components["schemas"];

const location = (seat: number): Schemas["SeatLocation"] => ({
  stand_id: 2,
  stand_code: "N",
  stand_name: "North Stand",
  tier: "UPPER",
  gate_group: "N-UPPER",
  block_id: 16,
  block_code: "N202",
  block_label: "Block N202",
  row_label: "A",
  seat_number: seat,
  seat_label: `A${seat}`,
  seat_type: "STANDARD",
});

const holdSeat = (id: number, price: number): Schemas["Hold"]["seats"][number] => ({
  event_seat_id: id,
  location: location(id),
  category_id: 4,
  category_name: "Terrace",
  price_cents: price,
});

const activeHold = {
  status: "ACTIVE" as const,
  subtotal_cents: 200_000,
  fee_cents: 5_000,
  total_cents: 205_000,
  seats: [holdSeat(1, 50_000), holdSeat(2, 50_000), holdSeat(3, 50_000), holdSeat(4, 50_000)],
};

describe("checkoutAmounts", () => {
  it("shows the hold's total before any booking exists", () => {
    const a = checkoutAmounts(activeHold, null);
    expect(a.source).toBe("hold");
    expect(a).toMatchObject({ subtotalCents: 200_000, feeCents: 5_000, totalCents: 205_000 });
    expect(a.lines.map((l) => [l.eventSeatId, l.priceCents, l.location.seat_label])).toEqual([
      [1, 50_000, "A1"],
      [2, 50_000, "A2"],
      [3, 50_000, "A3"],
      [4, 50_000, "A4"],
    ]);
  });

  it("takes the fee and total from the server, never by adding up seat prices", () => {
    // The hold says 205,000 with its fee; the seats add up to 200,000.
    expect(checkoutAmounts(activeHold, null).totalCents).toBe(205_000);
  });

  it("uses the booking's binding prices once the booking exists", () => {
    const booking = {
      subtotal_cents: 240_000,
      fee_cents: 0,
      total_cents: 240_000,
      items: [1, 2, 3, 4].map((id) => ({ event_seat_id: id, location: location(id), category_id: 4, category_name: "Terrace", unit_price_cents: 60_000 })),
    };
    const a = checkoutAmounts(activeHold, booking);
    expect(a.source).toBe("booking");
    expect(a.totalCents).toBe(240_000);
    expect(a.lines.every((l) => l.priceCents === 60_000)).toBe(true);
  });

  it("keeps showing the booking after the hold has ended with no seats", () => {
    const ended = { status: "EXPIRED" as const, subtotal_cents: 0, fee_cents: 0, total_cents: 0, seats: [] };
    const booking = { subtotal_cents: 100_000, fee_cents: 0, total_cents: 100_000, items: [{ event_seat_id: 1, location: location(1), category_id: 4, category_name: "Terrace", unit_price_cents: 100_000 }] };
    expect(checkoutAmounts(ended, booking)).toMatchObject({ source: "booking", totalCents: 100_000 });
  });

  it("an ended hold is nothing to pay, not a total of zero", () => {
    for (const status of ["EXPIRED", "RELEASED"] as const) {
      const a = checkoutAmounts({ status, subtotal_cents: 0, fee_cents: 0, total_cents: 0, seats: [] }, null);
      expect(a.source).toBe("none");
      expect(a.lines).toEqual([]);
    }
    expect(checkoutAmounts(undefined, undefined).source).toBe("none");
  });
});

describe("paymentPollMs", () => {
  it("polls a live payment every two seconds", () => {
    expect(paymentPollMs(undefined)).toBe(2000);
    expect(paymentPollMs("INITIATED")).toBe(2000);
    expect(paymentPollMs("PENDING")).toBe(2000);
  });

  it("keeps asking about a TIMEOUT, which M-Pesa can still settle", () => {
    expect(paymentPollMs("TIMEOUT")).toBe(10_000);
  });

  it("stops on a final answer", () => {
    expect(paymentPollMs("SUCCESS")).toBe(false);
    expect(paymentPollMs("FAILED")).toBe(false);
    expect(paymentPollMs("REVERSED")).toBe(false);
  });
});

describe("bookingPollMs", () => {
  it("keeps reading an unpaid booking after a failed or unanswered payment, more slowly", () => {
    expect(bookingPollMs("PENDING")).toBe(5000);
    expect(bookingPollMs("TIMEOUT")).toBe(10_000);
    expect(bookingPollMs("FAILED")).toBe(30_000);
  });

  it("stops once the payment succeeded or was reversed", () => {
    expect(bookingPollMs("SUCCESS")).toBe(false);
    expect(bookingPollMs("REVERSED")).toBe(false);
  });
});
