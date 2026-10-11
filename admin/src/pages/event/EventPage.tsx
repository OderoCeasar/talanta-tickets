import { ApiError, unwrap } from "@talanta/shared";
import { useMutation, useQueryClient, type UseMutationResult } from "@tanstack/react-query";
import { useState, type CSSProperties } from "react";
import { Link, NavLink, Outlet, useNavigate, useOutletContext, useParams } from "react-router-dom";
import { DeleteEventDialog } from "../../components/DeleteEventDialog";
import { EventForm } from "../../components/EventForm";
import { StatusChip, teamsLine } from "../../components/EventBits";
import { Alert, Button, Card, ConfirmDialog, LoadFailed, SkeletonRows, useElapsedSeconds } from "../../components/ui";
import { api, type Schemas } from "../../lib/api";
import { describeError, isCode } from "../../lib/errors";
import { formatDateTime } from "../../lib/format";
import { formatCount, formatKes } from "../../lib/money";
import { isPublished, keys, STATUS_HINTS, STATUS_LABELS, useCancelInFlight, useEvent, usePublishInFlight, type AdminEvent } from "../../lib/queries";
import { NotFoundPage } from "../SimplePages";

/** The contract asks clients to give publish and cancel 60 seconds; the server gives up at 55. */
export const PUBLISH_TIMEOUT_MS = 65_000;
const CANCEL_TIMEOUT_MS = PUBLISH_TIMEOUT_MS;

type PublishResult = Schemas["PublishResult"];
type CancelResult = Schemas["CancelResult"];

/** How a cancel ended. `counts` is null when the answer was lost and only a re-check showed the event cancelled. */
interface CancelOutcome {
  event: AdminEvent;
  counts: Omit<CancelResult, "event"> | null;
}

/** No clear answer: a timeout, a dropped connection or a fault page. The call may still have gone through. */
function isUnclear(error: unknown): error is ApiError {
  return error instanceof ApiError && (error.status === 0 || error.status >= 500);
}

export interface EventContext {
  event: AdminEvent;
  /** True while this browser waits for the publish answer: every editing action is off. */
  publishing: boolean;
  publish: UseMutationResult<PublishResult, unknown, void>;
  /** When the running publish was started, for the progress display. */
  publishStartedAt: number | null;
  /** True while this browser waits for the cancel answer: every editing action is off. */
  cancelling: boolean;
}

export function useEventContext(): EventContext {
  return useOutletContext<EventContext>();
}

/** One sentence for why editing is off, or null when it is on. */
export function editLock(context: EventContext, what: "details" | "prices" | "painting"): string | null {
  const { event, publishing, cancelling } = context;
  if (cancelling) return "This event is being cancelled. Editing is paused until that finishes (up to a minute).";
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
  const cancelling = useCancelInFlight(eventId);
  const [cancelStartedAt, setCancelStartedAt] = useState<number | null>(null);
  const [cancelOpen, setCancelOpen] = useState(false);
  const cancelElapsed = useElapsedSeconds(cancelling ? cancelStartedAt : null);

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
        if (!isUnclear(error)) throw error;
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
    mutationKey: keys.cancel(eventId),
    mutationFn: async (): Promise<CancelOutcome> => {
      setCancelStartedAt(Date.now());
      try {
        const { event: cancelledEvent, ...counts } = await unwrap(
          api.POST("/admin/events/{eventId}/cancel", {
            params: { path: { eventId } },
            signal: AbortSignal.timeout(CANCEL_TIMEOUT_MS),
          }),
        );
        return { event: cancelledEvent, counts };
      } catch (error) {
        // No clear answer: the cancel may still have gone through, or still be running. Ask
        // the server what state the event is in before saying it failed. (Sending the cancel
        // again would be safe, but a repeat answers with zero counts, which says nothing.)
        if (!isUnclear(error)) throw error;
        for (let attempt = 0; attempt < 8; attempt++) {
          await new Promise((resolve) => window.setTimeout(resolve, attempt === 0 ? 500 : 4000));
          try {
            const now = await unwrap(api.GET("/admin/events/{eventId}", { params: { path: { eventId } } }));
            if (now.status === "CANCELLED") return { event: now, counts: null };
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
      setCancelStartedAt(null);
      return queryClient.invalidateQueries({ queryKey: ["events"] });
    },
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
  const context: EventContext = { event: data, publishing, publish, publishStartedAt, cancelling };
  const canCancel = data.status !== "CANCELLED" && data.status !== "COMPLETED";
  const cancelled = cancel.data;
  const cancelError = cancel.error
    ? describeError(cancel.error, {
        INVALID_STATE_TRANSITION: "This event has already been played, so it cannot be cancelled.",
        NETWORK_ERROR:
          "We did not get an answer in time, and when we checked, the event was not cancelled. Check your connection and try again; cancelling twice is safe.",
        UNEXPECTED_RESPONSE:
          "The server did not give a clear answer, and when we checked, the event was not cancelled. Try again in a minute; cancelling twice is safe.",
        INTERNAL:
          "The server hit a problem, and when we checked, the event was not cancelled. Try again in a minute; cancelling twice is safe.",
      })
    : null;

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
              loading={cancelling}
              onClick={() => {
                cancel.reset();
                setCancelOpen(true);
              }}
            >
              {cancelling ? "Cancelling…" : "Cancel event"}
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

      {cancelling && (
        <Alert tone="warning" title="Cancelling this event…">
          <div className="stack" style={{ "--gap": "0.5rem" } as CSSProperties}>
            <p>
              Tickets are being voided, refunds queued and held seats released. For a full stadium this usually takes 10
              to 20 seconds and can take up to a minute. Keep this tab open; editing is paused until it finishes.
            </p>
            <div className="progress" aria-hidden="true">
              <div className="progress__bar" style={{ width: `${Math.min(97, (cancelElapsed / (CANCEL_TIMEOUT_MS / 1000)) * 100)}%` }} />
            </div>
            <p className="small">
              {cancelElapsed} {cancelElapsed === 1 ? "second" : "seconds"} so far
              {cancelElapsed > 55 ? ". Still waiting — checking with the server whether it went through." : "."}
            </p>
          </div>
        </Alert>
      )}

      {cancelError && !cancelling && (
        <Alert tone="error" title="The event was not cancelled">
          <p>{cancelError}</p>
          {canCancel && (
            <p>
              <Button variant="secondary" size="small" onClick={() => cancel.mutate()}>
                Try cancelling again
              </Button>
            </p>
          )}
        </Alert>
      )}

      {cancelled && !cancelling && (
        <Alert tone="success" title="The event is cancelled">
          {cancelled.counts
            ? `${formatCount(cancelled.counts.tickets_voided)} tickets voided, ${formatCount(cancelled.counts.refunds_queued)} refunds queued (${formatKes(cancelled.counts.refund_total_cents)}), ${formatCount(cancelled.counts.bookings_cancelled)} unpaid bookings closed and ${formatCount(cancelled.counts.holds_released)} holds released.`
            : "We lost the server's answer, so the counts are not shown here, but we checked and the cancel went through: every ticket is void and a refund is queued for every paid booking. The Sales tab lists the refunds."}
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
        confirmLabel="Cancel the event"
        cancelLabel="Keep the event"
        danger
        onConfirm={() => {
          // The dialog closes and the page shows the progress, as it does for a publish.
          setCancelOpen(false);
          cancel.mutate();
        }}
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
          <strong>This cannot be undone.</strong> A cancelled event cannot be edited or put back on sale. It can take up
          to a minute, and editing is paused while it runs.
        </p>
      </ConfirmDialog>
    </div>
  );
}

export function EventDetailsTab() {
  const context = useEventContext();
  const navigate = useNavigate();
  const [deleting, setDeleting] = useState(false);
  const { event } = context;
  const waiting = context.publishing || context.cancelling;
  return (
    <>
      <Card>
        <div className="card__header">
          <h2>Details</h2>
          {isPublished(event.status) && <span className="small muted">Changes show to fans straight away</span>}
        </div>
        <div className="card__body">
          <EventForm key={event.id} event={event} lockedReason={editLock(context, "details")} onSaved={() => undefined} />
        </div>
      </Card>

      {/* Only a draft can be deleted; anything published is cancelled instead. */}
      {event.status === "DRAFT" && (
        <Card>
          <div className="card__header">
            <h2>Delete this draft</h2>
          </div>
          <div className="card__body form-actions">
            <Button variant="danger" size="small" disabled={waiting} onClick={() => setDeleting(true)}>
              Delete draft event…
            </Button>
            <span className="small muted">
              Removes the draft with its categories, prices and painting. Nothing is on sale yet, so no fan is affected.
            </span>
          </div>
        </Card>
      )}

      <DeleteEventDialog
        event={deleting ? event : null}
        onClose={() => setDeleting(false)}
        onDeleted={(gone) => navigate("/events", { state: { deleted: { id: gone.id, title: gone.title } } })}
      />
    </>
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
