import type { components } from "@talanta/shared";

type Ticket = components["schemas"]["Ticket"];

export interface TicketGroup {
  event: Ticket["event"];
  tickets: Ticket[];
}

// A match is "past" a while after kickoff, not at it: a fan still needs the ticket at half time.
const STILL_RELEVANT_MS = 4 * 60 * 60 * 1000;

/**
 * Tickets grouped by event and split into the ones still to use and the ones gone by.
 * Upcoming events come soonest first (the next match is what the fan opens this tab for);
 * past ones most recent first.
 */
export function groupTickets(tickets: readonly Ticket[], now: number): { upcoming: TicketGroup[]; past: TicketGroup[] } {
  const groups = new Map<number, TicketGroup>();
  for (const t of tickets) {
    let g = groups.get(t.event.id);
    if (!g) groups.set(t.event.id, (g = { event: t.event, tickets: [] }));
    g.tickets.push(t);
  }
  const kickoff = (g: TicketGroup) => Date.parse(g.event.kickoff_at);
  const upcoming: TicketGroup[] = [];
  const past: TicketGroup[] = [];
  for (const g of groups.values()) {
    const over = g.event.status === "COMPLETED" || kickoff(g) + STILL_RELEVANT_MS < now;
    (over ? past : upcoming).push(g);
  }
  upcoming.sort((a, b) => kickoff(a) - kickoff(b));
  past.sort((a, b) => kickoff(b) - kickoff(a));
  return { upcoming, past };
}
