import { Link, useSearchParams } from "react-router-dom";
import { StatusChip } from "../components/EventBits";
import { ChartIcon } from "../components/icons";
import { SalesDashboard } from "../components/SalesDashboard";
import { Card, LoadFailed, SelectField, SkeletonRows, StateBlock } from "../components/ui";
import { describeError } from "../lib/errors";
import { formatDate } from "../lib/format";
import { isPublished, STATUS_LABELS, useAdminEvents } from "../lib/queries";
import { useEventContext } from "./event/EventPage";

/** The Sales tab of one event. */
export function SalesTab() {
  const { event } = useEventContext();
  return <SalesDashboard event={event} />;
}

/** The Sales section: pick an event, see its dashboard. */
export function SalesPage() {
  const [params, setParams] = useSearchParams();
  const events = useAdminEvents("ALL", 100);
  const all = events.data?.pages.flatMap((page) => page.items) ?? [];
  const withSales = all.filter((event) => event.status !== "DRAFT");
  const wanted = Number(params.get("event"));
  // Without a choice, show what is selling now, else the next thing that will.
  const chosen =
    withSales.find((event) => event.id === wanted) ??
    withSales.find((event) => event.status === "ON_SALE") ??
    [...withSales].reverse().find((event) => isPublished(event.status)) ??
    withSales[0];

  return (
    <div className="page">
      <header className="page__header">
        <h1>Sales</h1>
        <p>Revenue and seats sold for one event, as it happens.</p>
      </header>

      {events.isPending ? (
        <Card>
          <div className="card__body card__body--flush">
            <SkeletonRows label="Loading events" />
          </div>
        </Card>
      ) : events.isError ? (
        <Card>
          <LoadFailed message={describeError(events.error)} onRetry={() => events.refetch()} retrying={events.isFetching} />
        </Card>
      ) : !chosen ? (
        <Card>
          <StateBlock
            icon={<ChartIcon />}
            title="No event has been published yet"
            action={
              <Link to="/events" className="btn btn--secondary btn--small">
                Go to events
              </Link>
            }
          >
            <p>Sales appear here once an event is published.</p>
          </StateBlock>
        </Card>
      ) : (
        <>
          <div className="pickbar">
            <SelectField label="Event" name="event" value={chosen.id} onChange={(e) => setParams({ event: e.target.value }, { replace: true })}>
              {withSales.map((event) => (
                <option key={event.id} value={event.id}>
                  {event.title} · {formatDate(event.kickoff_at)} · {STATUS_LABELS[event.status]}
                </option>
              ))}
            </SelectField>
            <div className="pickbar__meta">
              <StatusChip status={chosen.status} />
              <Link to={`/events/${chosen.id}`} className="small">
                Open this event
              </Link>
            </div>
          </div>
          <SalesDashboard key={chosen.id} event={chosen} />
        </>
      )}
    </div>
  );
}
