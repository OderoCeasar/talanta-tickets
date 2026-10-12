import { useMemo } from "react";
import { useSearchParams } from "react-router-dom";
import { formatDate } from "../lib/format";
import { useAdminEvents } from "../lib/queries";
import { SelectField } from "./ui";

/**
 * The "which event" choice shared by the Alerts and Refunds pages. The choice lives in the
 * address (`?event=12`), so a link from an event's Sales tab opens the list already narrowed.
 */
export function useEventFilter() {
  const [params, setParams] = useSearchParams();
  const events = useAdminEvents("ALL", 100);
  const all = useMemo(() => events.data?.pages.flatMap((page) => page.items) ?? [], [events.data]);
  const titles = useMemo(() => new Map(all.map((event) => [event.id, event.title])), [all]);
  const raw = Number(params.get("event"));
  const eventId = Number.isInteger(raw) && raw > 0 ? raw : null;

  function setEventId(next: number | null) {
    const copy = new URLSearchParams(params);
    if (next === null) copy.delete("event");
    else copy.set("event", String(next));
    setParams(copy, { replace: true });
  }

  return {
    eventId,
    setEventId,
    /** Events that can have payments: anything that was ever published. */
    choices: all.filter((event) => event.status !== "DRAFT"),
    /** The event's title, or "Event 12" while the list of events is loading or does not hold it. */
    titleOf: (id: number) => titles.get(id) ?? `Event ${id}`,
  };
}

export function EventFilter({ filter }: { filter: ReturnType<typeof useEventFilter> }) {
  const known = filter.eventId === null || filter.choices.some((event) => event.id === filter.eventId);
  return (
    <SelectField
      label="Event"
      name="event"
      value={filter.eventId ?? ""}
      onChange={(e) => filter.setEventId(e.target.value === "" ? null : Number(e.target.value))}
    >
      <option value="">All events</option>
      {!known && <option value={filter.eventId!}>{filter.titleOf(filter.eventId!)}</option>}
      {filter.choices.map((event) => (
        <option key={event.id} value={event.id}>
          {event.title} · {formatDate(event.kickoff_at)}
        </option>
      ))}
    </SelectField>
  );
}
