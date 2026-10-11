import { ApiError, unwrap } from "@talanta/shared";
import { useMutation, useQueryClient, type UseMutationResult } from "@tanstack/react-query";
import { useState } from "react";
import { Link, NavLink, Outlet, useNavigate, useOutletContext, useParams } from "react-router-dom";
import { EventForm } from "../../components/EventForm";
import { StatusChip, teamsLine } from "../../components/EventBits";
import { Alert, Button, Card, ConfirmDialog, LoadFailed, SkeletonRows } from "../../components/ui";
import { api, type Schemas } from "../../lib/api";
import { describeError, isCode } from "../../lib/errors";
import { formatDateTime } from "../../lib/format";
import { formatCount, formatKes } from "../../lib/money";
import { isPublished, keys, STATUS_HINTS, STATUS_LABELS, useEvent, usePublishInFlight, type AdminEvent } from "../../lib/queries";
import { NotFoundPage } from "../SimplePages";

/** The contract asks clients to give publish 60 seconds; the server gives up at 55. */
export const PUBLISH_TIMEOUT_MS = 65_000;

type PublishResult = Schemas["PublishResult"];

export interface EventContext {
  event: AdminEvent;
  /** True while this browser waits for the publish answer: every editing action is off. */
  publishing: boolean;
  publish: UseMutationResult<PublishResult, unknown, void>;
  /** When the running publish was started, for the progress display. */
  publishStartedAt: number | null;
}

export function useEventContext(): EventContext {
  return useOutletContext<EventContext>();
}

/** One sentence for why editing is off, or null when it is on. */
export function editLock(context: EventContext, what: "details" | "prices" | "painting"): string | null {
  const { event, publishing } = context;
  if (publishing) return "This event is being published. Editing is paused until that finishes (up to a minute).";
  if (event.status === "CANCELLED") return "This event is cancelled, so it can no longer be changed.";
  if (event.status === "COMPLETED") return "This event has been played, so it can no longer be changed.";
  if (what === "painting" && event.status !== "DRAFT") {
    return `This event is ${STATUS_LABELS[event.status].toLowerCase()}: its seats are on the market with the categories below, so painting is closed. Prices can still be changed under Pricing & categories.`;
  }
  return null;
}

const TABS = [
  { to: "", label: "Details", end: true },
  { to: "pricing", label: "Pricing & categories" },
  { to: "painting", label: "Seat painting" },
  { to: "publish", label: "Publish" },
  { to: "sales", label: "Sales" },
];

export function EventPage() {
  const params = useParams();
  const eventId = Number(params.eventId);
  if (!Number.isSafeInteger(eventId) || eventId <= 0) return <NotFoundPage />;
  return <EventPageInner key={eventId} eventId={eventId} />;
}

function EventPageInner({ eventId }: { eventId: number }) {
  const queryClient = useQueryClient();
  const event = useEvent(eventId);
  const publishing = usePublishInFlight(eventId);
  const [publishStartedAt, setPublishStartedAt] = useState<number | null>(null);
  const [cancelOpen, setCancelOpen] = useState(false);

  const refreshEvent = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: keys.event(eventId) }),
      queryClient.invalidateQueries({ queryKey: ["events", "list"] }),
      queryClient.invalidateQueries({ queryKey: keys.venues }),
    ]);

  const publish = useMutation({
    mutationKey: keys.publish(eventId),
    mutationFn: async (): Promise<PublishResult> => {
      setPublishStartedAt(Date.now());
      try {
        return await unwrap(
          api.POST("/admin/events/{eventId}/publish", {
            params: { path: { eventId } },
            signal: AbortSignal.timeout(PUBLISH_TIMEOUT_MS),
          }),
        );
      } catch (error) {
        // No clear answer (timeout, dropped connection, a fault page): the publish may still
        // have gone through. Ask the server what state the event is in before saying it failed.
        const unclear = error instanceof ApiError && (error.status === 0 || error.status >= 500);
        if (!unclear) throw error;
        for (let attempt = 0; attempt < 8; attempt++) {
          await new Promise((resolve) => window.setTimeout(resolve, attempt === 0 ? 500 : 4000));
          try {
            const now = await unwrap(api.GET("/admin/events/{eventId}", { params: { path: { eventId } } }));
            if (isPublished(now.status)) return { event: now, seats_on_sale: now.seats_total, already_published: false };
            if (now.status !== "DRAFT") break;
          } catch {
            // Still unreachable; try again.
          }
        }
        throw error;
      }
    },
    onSuccess: (result) => {
      queryClient.setQueryData(keys.event(eventId), result.event);
    },
    onSettled: () => {
      setPublishStartedAt(null);
      return Promise.all([refreshEvent(), queryClient.invalidateQueries({ queryKey: ["events", eventId] })]);
    },
  });

  const cancel = useMutation({
    mutationFn: () => unwrap(api.POST("/admin/events/{eventId}/cancel", { params: { path: { eventId } } })),
    onSuccess: (result) => {
      queryClient.setQueryData(keys.event(eventId), result.event);
      setCancelOpen(false);
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ["events"] }),
  });

  if (event.isPending) {
    return (
      <div className="page">
        <Card>
          <div className="card__body card__body--flush">
            <SkeletonRows label="Loading event" rows={4} />
          </div>
        </Card>
      </div>
    );
  }
  if (event.isError) {
    if (isCode(event.error, "NOT_FOUND")) return <NotFoundPage />;
    return (
      <div className="page">
        <Card>
          <LoadFailed message={describeError(event.error)} onRetry={() => event.refetch()} retrying={event.isFetching} />
        </Card>
      </div>
    );
  }

  const data = event.data;
  const context: EventContext = { event: data, publishing, publish, publishStartedAt };
  const canCancel = data.status !== "CANCELLED" && data.status !== "COMPLETED";
  const cancelled = cancel.data;

  return (
    <div className="page">
      <header className="page__header">
        <Link to="/events" className="small backlink">
          ← All events
        </Link>
        <div className="eventhead">
          <div className="eventhead__title">
            <h1>{data.title}</h1>
            <StatusChip status={data.status} />
          </div>
          {canCancel && (
            <Button
              variant="danger"
              size="small"
              disabled={publishing}
              onClick={() => {
                cancel.reset();
                setCancelOpen(true);
              }}
            >
              Cancel event
            </Button>
          )}
        </div>
        <p>
          {[teamsLine(data), data.competition, data.venue_name].filter(Boolean).join(" · ")}
          {" · Kickoff "}
          <time dateTime={data.kickoff_at}>{formatDateTime(data.kickoff_at)}</time> (Nairobi time). {STATUS_HINTS[data.status]}
        </p>
      </header>

      {publishing && (
        <Alert tone="warning" title="Publishing this event…">
          Seats are being put on sale. This can take up to a minute. Editing is paused until it finishes; you can look
          around but keep this tab open.
        </Alert>
      )}

      {cancelled && (
        <Alert tone="success" title="The event is cancelled">
          {formatCount(cancelled.tickets_voided)} tickets voided, {formatCount(cancelled.refunds_queued)} refunds queued (
          {formatKes(cancelled.refund_total_cents)}), {formatCount(cancelled.bookings_cancelled)} unpaid bookings closed and{" "}
          {formatCount(cancelled.holds_released)} holds released.
        </Alert>
      )}

      <nav className="tabs" aria-label="Event sections">
        {TABS.map((tab) => (
          <NavLink key={tab.to} to={tab.to} end={tab.end} className="tabs__tab">
            {tab.label}
          </NavLink>
        ))}
      </nav>

      <Outlet context={context} />

      <ConfirmDialog
        open={cancelOpen}
        title="Cancel this event?"
        confirmLabel={cancel.isPending ? "Cancelling…" : "Cancel the event"}
        cancelLabel="Keep the event"
        danger
        busy={cancel.isPending}
        error={
          cancel.error
            ? describeError(cancel.error, {
                INVALID_STATE_TRANSITION: "This event has already been played, so it cannot be cancelled.",
              })
            : null
        }
        onConfirm={() => cancel.mutate()}
        onCancel={() => setCancelOpen(false)}
      >
        <p>
          <strong>{data.title}</strong> will be cancelled for everyone, straight away.
        </p>
        <ul className="plainlist">
          <li>Every ticket for it is voided and will be refused at the gate.</li>
          <li>A refund is queued for every paid booking. Someone still has to send each refund.</li>
          <li>Unpaid bookings are closed and held seats are released.</li>
        </ul>
        <p>
          <strong>This cannot be undone.</strong> A cancelled event cannot be edited or put back on sale.
        </p>
      </ConfirmDialog>
    </div>
  );
}

export function EventDetailsTab() {
  const context = useEventContext();
  return (
    <Card>
      <div className="card__header">
        <h2>Details</h2>
        {isPublished(context.event.status) && <span className="small muted">Changes show to fans straight away</span>}
      </div>
      <div className="card__body">
        <EventForm key={context.event.id} event={context.event} lockedReason={editLock(context, "details")} onSaved={() => undefined} />
      </div>
    </Card>
  );
}

export function EventCreatePage() {
  const navigate = useNavigate();
  return (
    <div className="page">
      <header className="page__header">
        <Link to="/events" className="small backlink">
          ← All events
        </Link>
        <h1>New event</h1>
        <p>This makes a draft. Fans see nothing until you publish it.</p>
      </header>
      <Card>
        <div className="card__body">
          <EventForm onSaved={(event) => navigate(`/events/${event.id}/pricing`, { state: { created: true } })} />
        </div>
      </Card>
    </div>
  );
}
