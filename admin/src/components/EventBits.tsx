import { STATUS_LABELS, type EventStatus } from "../lib/queries";

export function StatusChip({ status }: { status: EventStatus }) {
  return <span className={`chip chip--event-${status.toLowerCase()}`}>{STATUS_LABELS[status]}</span>;
}

/** "Harambee Stars vs Uganda Cranes", or nothing when the teams are not both set. */
export function teamsLine(event: { home_team: string | null; away_team: string | null }): string | null {
  return event.home_team && event.away_team ? `${event.home_team} vs ${event.away_team}` : null;
}
