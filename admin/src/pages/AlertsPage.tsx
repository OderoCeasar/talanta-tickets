import { unwrap } from "@talanta/shared";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router-dom";
import { EventFilter, useEventFilter } from "../components/EventFilter";
import { CheckIcon } from "../components/icons";
import { Alert as Notice, Button, Card, LoadFailed, SkeletonRows, StateBlock } from "../components/ui";
import { api } from "../lib/api";
import { alertHasRefund, alertKindLabel, alertNextStep, type Alert } from "../lib/attention";
import { describeError } from "../lib/errors";
import { formatDateTime } from "../lib/format";
import { keys, useAlerts } from "../lib/queries";

/** Payments that went wrong, for staff to look at: the open ones first, then the ones already seen. */
export function AlertsPage() {
  const filter = useEventFilter();
  const [showSeen, setShowSeen] = useState(false);

  return (
    <div className="page">
      <header className="page__header">
        <h1>Alerts</h1>
        <p>
          Payments that went wrong and need a person to look at them. Acknowledging an alert only marks it as seen, so
          the rest of the team knows someone has it. It does not refund anyone: that is done from{" "}
          <Link to={filter.eventId === null ? "/refunds" : `/refunds?event=${filter.eventId}`}>Refunds</Link>.
        </p>
      </header>

      <div className="pickbar">
        <EventFilter filter={filter} />
      </div>

      <AlertList key={`open-${filter.eventId}`} acknowledged={false} filter={filter} />

      {showSeen ? (
        <AlertList key={`seen-${filter.eventId}`} acknowledged filter={filter} />
      ) : (
        <p>
          <Button variant="secondary" size="small" onClick={() => setShowSeen(true)}>
            Show acknowledged alerts
          </Button>
        </p>
      )}
    </div>
  );
}

function AlertList({ acknowledged, filter }: { acknowledged: boolean; filter: ReturnType<typeof useEventFilter> }) {
  const queryClient = useQueryClient();
  const alerts = useAlerts(acknowledged, filter.eventId);
  const rows = alerts.data?.pages.flatMap((page) => page.items) ?? [];
  const [failed, setFailed] = useState<{ id: number; message: string } | null>(null);

  const ack = useMutation({
    mutationFn: (alert: Alert) => unwrap(api.POST("/admin/alerts/{alertId}/ack", { params: { path: { alertId: alert.id } } })),
    onMutate: () => setFailed(null),
    onError: (error, alert) => setFailed({ id: alert.id, message: describeError(error, { NOT_FOUND: "This alert no longer exists." }) }),
    onSettled: (_data, _error, alert) => {
      // Both lists, both badges, and the event's dashboard count all change with one acknowledgement.
      void queryClient.invalidateQueries({ queryKey: keys.alerts });
      void queryClient.invalidateQueries({ queryKey: keys.sales(alert.event_id) });
    },
  });

  const heading = acknowledged ? "Acknowledged" : "Open";

  return (
    <Card className={!acknowledged && rows.length > 0 ? "card--attention" : undefined}>
      <div className="card__header">
        <h2>
          {heading}
          {!acknowledged && rows.length > 0 && (
            <span className="badge" data-testid="open-alerts-badge">
              {rows.length}
              {alerts.hasNextPage ? "+" : ""}
            </span>
          )}
        </h2>
        <span className="small muted">Newest first. Times are Nairobi time.</span>
      </div>
      <div className="card__body card__body--flush">
        {alerts.isPending ? (
          <SkeletonRows label={`Loading ${heading.toLowerCase()} alerts`} />
        ) : alerts.isError && rows.length === 0 ? (
          <LoadFailed message={describeError(alerts.error)} onRetry={() => alerts.refetch()} retrying={alerts.isFetching} />
        ) : rows.length === 0 ? (
          acknowledged ? (
            <StateBlock title="No acknowledged alerts">
              <p>Alerts move here once someone acknowledges them.</p>
            </StateBlock>
          ) : (
            <StateBlock icon={<CheckIcon />} title="Nothing needs attention">
              <p>
                {filter.eventId === null
                  ? "No payment has gone wrong since the last alert was acknowledged."
                  : "This event has no open alerts."}{" "}
                This list checks for new ones every 30 seconds.
              </p>
            </StateBlock>
          )
        ) : (
          <>
            {alerts.isError && (
              <div className="card__inset">
                <Notice tone="warning" title="This list has stopped updating">
                  {describeError(alerts.error)} We keep trying.
                </Notice>
              </div>
            )}
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th scope="col">What happened</th>
                    <th scope="col">Event and booking</th>
                    <th scope="col">When</th>
                    <th scope="col">
                      <span className="visually-hidden">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {rows.map((alert) => (
                    <AlertRow
                      key={alert.id}
                      alert={alert}
                      eventTitle={filter.titleOf(alert.event_id)}
                      busy={ack.isPending && ack.variables?.id === alert.id}
                      error={failed?.id === alert.id ? failed.message : null}
                      onAcknowledge={() => ack.mutate(alert)}
                    />
                  ))}
                </tbody>
              </table>
            </div>
          </>
        )}
      </div>
      {alerts.hasNextPage && (
        <div className="card__footer">
          <Button variant="secondary" size="small" loading={alerts.isFetchingNextPage} onClick={() => alerts.fetchNextPage()}>
            Show more
          </Button>
        </div>
      )}
    </Card>
  );
}

function AlertRow({
  alert,
  eventTitle,
  busy,
  error,
  onAcknowledge,
}: {
  alert: Alert;
  eventTitle: string;
  busy: boolean;
  error: string | null;
  onAcknowledge: () => void;
}) {
  const kind = alertKindLabel(alert.kind);
  return (
    <tr data-alert={alert.id}>
      <td className="table__primary" data-label="What happened">
        <span className={`chip chip--alert-${alertHasRefund(alert.kind) ? "refund" : "check"}`}>{kind}</span>
        <span className="alertrow__message">{alert.message}</span>
        <span className="table__secondary">{alertNextStep(alert.kind)}</span>
      </td>
      <td className="table__wrap" data-label="Event and booking">
        <span className="cellstack alertrow__where">
          <Link to={`/events/${alert.event_id}/sales`}>{eventTitle}</Link>
          {alert.booking_reference ? (
            <span className="mono">{alert.booking_reference}</span>
          ) : (
            <span className="muted small">No booking</span>
          )}
        </span>
      </td>
      <td data-label="When">
        <time dateTime={alert.created_at}>{formatDateTime(alert.created_at)}</time>
        {alert.acknowledged_at && (
          <span className="table__secondary">
            Acknowledged <time dateTime={alert.acknowledged_at}>{formatDateTime(alert.acknowledged_at)}</time>
          </span>
        )}
      </td>
      <td className="table__actions-cell">
        <div className="table__actions table__actions--stack">
          {alertHasRefund(alert.kind) && (
            <Link className="btn btn--secondary btn--small" to={`/refunds?event=${alert.event_id}`} aria-label={`Open the refund queue for ${eventTitle}`}>
              Refund queue
            </Link>
          )}
          {!alert.acknowledged_at && (
            <Button
              size="small"
              loading={busy}
              onClick={onAcknowledge}
              aria-label={`Acknowledge: ${kind}${alert.booking_reference ? `, booking ${alert.booking_reference}` : ""}`}
            >
              Acknowledge
            </Button>
          )}
        </div>
        {error && (
          <p className="field__error" role="alert">
            {error}
          </p>
        )}
      </td>
    </tr>
  );
}
