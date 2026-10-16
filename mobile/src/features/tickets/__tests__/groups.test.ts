import type { components } from "@talanta/shared";
import { describe, expect, it } from "vitest";

import { groupTickets } from "../groups";

type Ticket = components["schemas"]["Ticket"];

const NOW = Date.parse("2026-10-16T12:00:00Z");

function ticket(id: string, eventId: number, kickoff: string, status: Ticket["event"]["status"] = "ON_SALE"): Ticket {
  return { id, event: { id: eventId, title: `Event ${eventId}`, kickoff_at: kickoff, status } } as unknown as Ticket;
}

describe("groupTickets", () => {
  it("groups tickets by event and lists the soonest match first", () => {
    const { upcoming, past } = groupTickets(
      [ticket("a", 2, "2026-11-20T13:00:00Z"), ticket("b", 1, "2026-11-01T13:00:00Z"), ticket("c", 2, "2026-11-20T13:00:00Z")],
      NOW,
    );
    expect(upcoming.map((g) => g.event.id)).toEqual([1, 2]);
    expect(upcoming[1]!.tickets.map((t) => t.id)).toEqual(["a", "c"]);
    expect(past).toEqual([]);
  });

  it("keeps a match under upcoming until four hours after kickoff", () => {
    const { upcoming, past } = groupTickets([ticket("a", 1, "2026-10-16T09:00:00Z"), ticket("b", 2, "2026-10-16T07:30:00Z")], NOW);
    expect(upcoming.map((g) => g.event.id)).toEqual([1]);
    expect(past.map((g) => g.event.id)).toEqual([2]);
  });

  it("puts a completed event under past, most recent first", () => {
    const { upcoming, past } = groupTickets(
      [ticket("a", 1, "2026-09-01T13:00:00Z"), ticket("b", 2, "2026-10-01T13:00:00Z"), ticket("c", 3, "2026-10-16T11:00:00Z", "COMPLETED")],
      NOW,
    );
    expect(upcoming).toEqual([]);
    expect(past.map((g) => g.event.id)).toEqual([3, 2, 1]);
  });
});
