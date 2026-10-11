import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type CSSProperties } from "react";
import { Link } from "react-router-dom";
import { Alert, Button, Card, ConfirmDialog, LoadFailed, SkeletonRows, useElapsedSeconds } from "../../components/ui";
import { describeError, isCode } from "../../lib/errors";
import { formatDateTime } from "../../lib/format";
import { formatCount } from "../../lib/money";
import { isPublished, keys, STATUS_LABELS, useReadiness, type ReadinessProblem } from "../../lib/queries";
import { PUBLISH_TIMEOUT_MS, useEventContext } from "./EventPage";

/** Where each kind of problem is fixed. */
const FIX: Record<ReadinessProblem["code"], { to: string; label: string }> = {
  NO_SEATS: { to: "/layout", label: "Open Venue layout" },
  NO_CATEGORIES: { to: "../pricing", label: "Add categories" },
  SEATS_WITHOUT_CATEGORY: { to: "../painting", label: "Open Seat painting" },
  CATEGORY_WITHOUT_PRICE: { to: "../pricing", label: "Set prices" },
  SALES_WINDOW_MISSING: { to: "..", label: "Set the sales dates" },
  SALES_WINDOW_INVALID: { to: "..", label: "Fix the sales dates" },
  KICKOFF_IN_PAST: { to: "..", label: "Change the kickoff" },
};

export function PublishTab() {
  const { event, publishing, cancelling, publish, publishStartedAt } = useEventContext();
  const queryClient = useQueryClient();
  const draft = event.status === "DRAFT";
  const readiness = useReadiness(event.id, draft && !publishing && !cancelling);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const elapsed = useElapsedSeconds(publishing ? publishStartedAt : null);

  // The server refused with its own list: show it, and refresh the readiness card to match.
  const notReady = isCode(publish.error, "EVENT_NOT_READY")
    ? ((publish.error.details as { problems?: ReadinessProblem[] } | null)?.problems ?? [])
    : null;
  useEffect(() => {
    if (notReady) void queryClient.invalidateQueries({ queryKey: keys.readiness(event.id) });
    // Only when a new refusal arrives.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [publish.error]);

  if (publishing) {
    const limit = PUBLISH_TIMEOUT_MS / 1000;
    return (
      <Card>
        <div className="card__body stack" role="status" aria-live="polite">
          <h2>Putting the seats on sale…</h2>
          <p className="muted">
            The server is creating one sellable seat for every seat in the stadium. For the full venue this usually takes
            30 to 45 seconds and can take up to a minute. Keep this tab open; editing is paused meanwhile.
          </p>
          <div className="progress" aria-hidden="true">
            <div className="progress__bar" style={{ width: `${Math.min(97, (elapsed / limit) * 100)}%` }} />
          </div>
          <p className="small muted">
            {elapsed} {elapsed === 1 ? "second" : "seconds"} so far
            {elapsed > 55 ? ". Still waiting — checking with the server whether it went through." : "."}
          </p>
        </div>
      </Card>
    );
  }

  if (!draft) {
    const justNow = publish.data && !publish.data.already_published;
    return (
      <Card>
        <div className="card__body stack">
          {isPublished(event.status) ? (
            <>
              <Alert tone="success" title={justNow ? "Published" : "This event is published"}>
                {publish.data
                  ? `${formatCount(publish.data.seats_on_sale)} seats are on sale.`
                  : `${formatCount(event.seats_total)} seats were put on sale`}
                {event.published_at && !publish.data ? ` on ${formatDateTime(event.published_at)} (Nairobi time).` : ""}{" "}
                {event.status === "ON_SALE"
                  ? "Fans can buy tickets now."
                  : event.sales_open_at
                    ? `Fans can see it; ticket sales open ${formatDateTime(event.sales_open_at)} and close ${event.sales_close_at ? formatDateTime(event.sales_close_at) : "at kickoff"}.`
                    : "Fans can see it."}
              </Alert>
              <div className="form-actions">
                <Link to="../sales" relative="path" className="btn btn--primary">
                  Watch the sales
                </Link>
                <span className="small muted">There is nothing more to do here. To stop the event, use Cancel event at the top.</span>
              </div>
            </>
          ) : (
            <Alert tone="info">
              This event is {STATUS_LABELS[event.status].toLowerCase()}, so it cannot be published.
            </Alert>
          )}
        </div>
      </Card>
    );
  }

  const report = readiness.data;
  const publishError =
    publish.error && !notReady
      ? describeError(publish.error, {
          INVALID_STATE_TRANSITION: "This event is cancelled or already played, so it cannot be published.",
          NETWORK_ERROR:
            "We did not get an answer in time, and when we checked, the event was still a draft. Nothing is on sale. Check your connection and try again; publishing twice is safe.",
          UNEXPECTED_RESPONSE:
            "The server did not give a clear answer, and when we checked, the event was still a draft. Nothing is on sale. Try again in a minute; publishing twice is safe.",
          INTERNAL:
            "The server hit a problem, and when we checked, the event was still a draft. Nothing is on sale. Try again in a minute; publishing twice is safe.",
        })
      : null;

  return (
    <>
      {publishError && (
        <Alert tone="error" title="The event was not published">
          {publishError}
        </Alert>
      )}
      {notReady && (
        <Alert tone="error" title="The server refused to publish: something is still missing">
          {notReady.length === 0 ? "Check the list below." : `${notReady.length} ${notReady.length === 1 ? "thing needs" : "things need"} fixing. They are listed below.`}
        </Alert>
      )}

      <Card>
        <div className="card__header">
          <h2>Ready to publish?</h2>
          {report && (
            <Button variant="ghost" size="small" loading={readiness.isFetching} onClick={() => readiness.refetch()}>
              Check again
            </Button>
          )}
        </div>
        {readiness.isPending ? (
          <div className="card__body card__body--flush">
            <SkeletonRows label="Checking the event" />
          </div>
        ) : readiness.isError ? (
          <LoadFailed message={describeError(readiness.error)} onRetry={() => readiness.refetch()} retrying={readiness.isFetching} />
        ) : (
          report && (
            <div className="card__body stack">
              <div className="meter-row">
                <div
                  className="meter"
                  role="img"
                  aria-label={`${formatCount(report.seats_with_category)} of ${formatCount(report.seat_total)} seats have a category`}
                >
                  <span
                    className="meter__fill"
                    style={{ width: `${report.seat_total ? (report.seats_with_category / report.seat_total) * 100 : 0}%` }}
                  />
                </div>
                <span className="small">
                  <strong>{formatCount(report.seats_with_category)}</strong> of {formatCount(report.seat_total)} seats have a
                  category
                </span>
              </div>

              {report.ready ? (
                <Alert tone="success" title="Everything is in place">
                  Every seat has a category, every category has a price, and the sales dates are set.
                </Alert>
              ) : (
                <ProblemList problems={report.problems} />
              )}

              <div className="form-actions">
                <Button disabled={!report.ready || cancelling} onClick={() => { publish.reset(); setConfirmOpen(true); }}>
                  Publish event…
                </Button>
                {cancelling ? (
                  <span className="small muted">This event is being cancelled.</span>
                ) : (
                  !report.ready && <span className="small muted">Fix the list above first.</span>
                )}
              </div>
            </div>
          )
        )}
      </Card>

      <ConfirmDialog
        open={confirmOpen}
        title="Publish this event?"
        confirmLabel={`Put ${formatCount(report?.seat_total ?? 0)} seats on sale`}
        cancelLabel="Not yet"
        onConfirm={() => {
          setConfirmOpen(false);
          publish.mutate();
        }}
        onCancel={() => setConfirmOpen(false)}
      >
        <p>
          This puts <strong>{formatCount(report?.seat_total ?? 0)} seats on sale</strong> for <strong>{event.title}</strong> at the
          prices you set.
        </p>
        <ul className="plainlist">
          <li>
            Fans will see the event straight away
            {event.sales_open_at ? `, and can buy from ${formatDateTime(event.sales_open_at)} (Nairobi time).` : "."}
          </li>
          <li>Seat painting closes for good. Prices can still be changed, with a warning.</li>
          <li>It takes up to a minute, and editing is paused while it runs.</li>
        </ul>
      </ConfirmDialog>
    </>
  );
}

function ProblemList({ problems }: { problems: ReadinessProblem[] }) {
  const blocks = problems.filter((problem) => problem.code === "SEATS_WITHOUT_CATEGORY");
  const others = problems.filter((problem) => problem.code !== "SEATS_WITHOUT_CATEGORY");
  const unpainted = blocks.reduce((sum, problem) => sum + (problem.seat_count ?? 0), 0);
  return (
    <div className="stack" style={{ "--gap": "0.75rem" } as CSSProperties}>
      <p className="problems__title">
        {problems.length === 1 ? "1 thing is missing" : `${problems.length} things are missing`}
      </p>
      <ul className="problems">
        {others.map((problem, index) => (
          <li key={`${problem.code}-${problem.category_id ?? index}`} className="problems__item">
            <span>{problem.message}</span>
            <Link to={FIX[problem.code].to} relative="path" className="small">
              {FIX[problem.code].label}
            </Link>
          </li>
        ))}
        {blocks.length > 0 && (
          <li className="problems__item problems__item--stack">
            <span>
              {formatCount(unpainted)} seats in {blocks.length} {blocks.length === 1 ? "block have" : "blocks have"} no category.{" "}
              <Link to={FIX.SEATS_WITHOUT_CATEGORY.to} relative="path" className="small">
                {FIX.SEATS_WITHOUT_CATEGORY.label}
              </Link>
            </span>
            <ul className="blockchips" aria-label="Blocks with seats that have no category">
              {blocks.map((problem) => (
                <li key={problem.block_id} title={problem.message}>
                  <strong>{problem.block_code}</strong> {formatCount(problem.seat_count ?? 0)}
                </li>
              ))}
            </ul>
          </li>
        )}
      </ul>
    </div>
  );
}
