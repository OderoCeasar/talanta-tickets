import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { Link, useLocation } from "react-router-dom";
import { DeleteEventDialog } from "../components/DeleteEventDialog";
import { StatusChip } from "../components/EventBits";
import { CalendarIcon } from "../components/icons";
import { Alert, Card, LoadFailed, SkeletonRows, StateBlock, Button } from "../components/ui";
import { describeError } from "../lib/errors";
import { formatDateTime } from "../lib/format";
import { formatCount, formatKes } from "../lib/money";
import { isPublished, keys, STATUS_LABELS, useAdminEvents, usePublishInFlight, type AdminEvent, type EventStatus } from "../lib/queries";

const FILTERS: (EventStatus | "ALL")[] = ["ALL", "DRAFT", "PUBLISHED", "ON_SALE", "SOLD_OUT", "CANCELLED", "COMPLETED"];

export function EventsPage() {
  const [status, setStatus] = useState<EventStatus | "ALL">("ALL");
  const events = useAdminEvents(status);
  const rows = events.data?.pages.flatMap((page) => page.items) ?? [];
  const queryClient = useQueryClient();
  const location = useLocation();
  // Set by the event page when a draft was deleted there and the organiser was sent back here.
  const cameFromDelete = (location.state as { deleted?: Deleted } | null)?.deleted ?? null;
  const [toDelete, setToDelete] = useState<AdminEvent | null>(null);
  const [deletedHere, setDeletedHere] = useState<Deleted | null>(null);
  const deleted = deletedHere ?? cameFromDelete;
  // A publish started in this tab keeps running when the organiser comes back to the list.
  const publishing = usePublishInFlight();

  // Drop what is cached about a deleted event, so nothing of it is shown if its address is opened again.
  useEffect(() => {
    if (deleted) queryClient.removeQueries({ queryKey: keys.event(deleted.id) });
  }, [deleted, queryClient]);

  return (
    <div className="page">
      <header className="page__header page__header--split">
        <div>
          <h1>Events</h1>
          <p>Every match and its status. Open one to set prices, paint seats, publish it or watch its sales.</p>
        </div>
        <Link to="/events/new" className="btn btn--primary">
          New event
        </Link>
      </header>

      {deleted && (
        <Alert tone="success" title="Draft deleted">
          “{deleted.title}” and its categories and painting are gone.
        </Alert>
      )}

      <Card>
        <div className="card__header">
          <div className="filter" role="group" aria-label="Show events with status">
            {FILTERS.map((value) => (
              <button
                key={value}
                type="button"
                className="filter__option"
                aria-pressed={status === value}
                onClick={() => setStatus(value)}
              >
                {value === "ALL" ? "All" : STATUS_LABELS[value]}
              </button>
            ))}
          </div>
          <span className="small muted">Latest kickoff first · Nairobi time</span>
        </div>
        <div className="card__body card__body--flush">
          {events.isPending ? (
            <SkeletonRows label="Loading events" rows={4} />
          ) : events.isError ? (
            <LoadFailed message={describeError(events.error)} onRetry={() => events.refetch()} retrying={events.isFetching} />
          ) : rows.length === 0 ? (
            <StateBlock
              icon={<CalendarIcon />}
              title={status === "ALL" ? "No events yet" : `No ${STATUS_LABELS[status].toLowerCase()} events`}
              action={
                status === "ALL" ? (
                  <Link to="/events/new" className="btn btn--secondary btn--small">
                    Create the first event
                  </Link>
                ) : undefined
              }
            >
              <p>
                {status === "ALL"
                  ? "Create an event from your saved venue template, set its prices and publish it."
                  : "Pick another status above to see other events."}
              </p>
            </StateBlock>
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th scope="col">Event</th>
                    <th scope="col">Kickoff</th>
                    <th scope="col">Status</th>
                    <th scope="col">Seats left</th>
                    <th scope="col">From</th>
                    <th scope="col">
                      <span className="visually-hidden">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((event) => (
                    <tr key={event.id}>
                      <td className="table__primary" data-label="Event">
                        <Link to={`/events/${event.id}`}>{event.title}</Link>
                        <span className="table__secondary">
                          {[event.competition, event.venue_name].filter(Boolean).join(" · ")}
                        </span>
                      </td>
                      <td data-label="Kickoff">
                        <time dateTime={event.kickoff_at}>{formatDateTime(event.kickoff_at)}</time>
                      </td>
                      <td data-label="Status">
                        <StatusChip status={event.status} />
                      </td>
                      <td data-label="Seats left">
                        {isPublished(event.status) ? (
                          `${formatCount(event.seats_available)} of ${formatCount(event.seats_total)}`
                        ) : (
                          <span className="muted">—</span>
                        )}
                      </td>
                      <td data-label="From">
                        {event.price_from_cents === null ? <span className="muted">—</span> : formatKes(event.price_from_cents)}
                      </td>
                      {/* Only a draft can be deleted; the cell stays empty for every other status. */}
                      <td className="table__actions-cell" data-label="Actions">
                        {event.status === "DRAFT" && (
                          <div className="table__actions">
                            <Button
                              variant="danger"
                              size="small"
                              disabled={publishing}
                              onClick={() => setToDelete(event)}
                              aria-label={`Delete the draft ${event.title}`}
                            >
                              Delete
                            </Button>
                          </div>
                        )}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </div>
        {events.hasNextPage && (
          <div className="card__footer">
            <Button variant="secondary" size="small" loading={events.isFetchingNextPage} onClick={() => events.fetchNextPage()}>
              Show more
            </Button>
          </div>
        )}
      </Card>

      <DeleteEventDialog
        event={toDelete}
        onClose={() => setToDelete(null)}
        onDeleted={(gone) => {
          setToDelete(null);
          setDeletedHere({ id: gone.id, title: gone.title });
        }}
      />
    </div>
  );
}

interface Deleted {
  id: number;
  title: string;
}
