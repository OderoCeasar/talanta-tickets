import { unwrap } from "@talanta/shared";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState, type CSSProperties } from "react";
import { Link } from "react-router-dom";
import { EventFilter, useEventFilter } from "../components/EventFilter";
import { CheckIcon } from "../components/icons";
import { Alert as Notice, Button, Card, ConfirmDialog, LoadFailed, SkeletonRows, StateBlock } from "../components/ui";
import { api, type Schemas } from "../lib/api";
import { refundStatusLabel, wasAlreadyCompleted, type Refund } from "../lib/attention";
import { describeError, isCode } from "../lib/errors";
import { formatDateTime } from "../lib/format";
import { formatKes } from "../lib/money";
import { keys, useRefunds } from "../lib/queries";

type RefundStatus = Schemas["RefundStatus"];

/** What the last "Mark as refunded" did, shown above the list once the dialog has closed. */
interface Outcome {
  refund: Refund;
  /** Someone else had already marked it; our request changed nothing. */
  already: boolean;
}

/**
 * The refund queue. Refunds are paid by hand in the M-Pesa portal; this page lists what to
 * pay and lets staff record that a refund has been paid. It never moves money.
 */
export function RefundsPage() {
  const filter = useEventFilter();
  const queryClient = useQueryClient();
  const [status, setStatus] = useState<RefundStatus>("PENDING");
  const [asking, setAsking] = useState<Refund | null>(null);
  const [outcome, setOutcome] = useState<Outcome | null>(null);
  const refunds = useRefunds(status, filter.eventId);
  const rows = refunds.data?.pages.flatMap((page) => page.items) ?? [];

  const complete = useMutation({
    mutationFn: async (refund: Refund) => {
      const sentAt = Date.now();
      const done = await unwrap(api.POST("/admin/refunds/{refundId}/complete", { params: { path: { refundId: refund.id } } }));
      return { refund: done, already: wasAlreadyCompleted(done, sentAt) } satisfies Outcome;
    },
    onSuccess: (result) => {
      setOutcome(result);
      setAsking(null);
    },
    onSettled: (_data, _error, refund) => {
      // The queue, its badge, and the event's dashboard (bookings waiting for a refund) all move.
      void queryClient.invalidateQueries({ queryKey: keys.refunds });
      void queryClient.invalidateQueries({ queryKey: keys.sales(refund.event_id) });
      void queryClient.invalidateQueries({ queryKey: keys.refundBookings(refund.event_id) });
    },
  });

  function ask(refund: Refund) {
    complete.reset();
    setOutcome(null);
    setAsking(refund);
  }

  const gone = isCode(complete.error, "NOT_FOUND");

  return (
    <div className="page">
      <header className="page__header">
        <h1>Refunds</h1>
        <p>
          Money that has to go back to a fan. This site does not send money: pay each refund by hand in the M-Pesa
          portal, to the number shown, then mark it as refunded here. The fan who has waited longest is at the top.
        </p>
      </header>

      <div className="pickbar">
        <EventFilter filter={filter} />
        <div className="filter" role="group" aria-label="Show refunds that are">
          <button type="button" className="filter__option" aria-pressed={status === "PENDING"} onClick={() => setStatus("PENDING")}>
            Waiting
          </button>
          <button type="button" className="filter__option" aria-pressed={status === "DONE"} onClick={() => setStatus("DONE")}>
            Paid back
          </button>
        </div>
      </div>

      {outcome && (
        <Notice tone={outcome.already ? "info" : "success"} title={outcome.already ? "This refund was already marked as refunded" : "Marked as refunded"}>
          <p data-testid="refund-outcome">
            {outcome.already ? (
              <>
                Someone had already marked the {formatKes(outcome.refund.amount_cents)} refund for booking{" "}
                <span className="mono">{outcome.refund.booking_reference}</span> as paid back
                {outcome.refund.completed_at ? ` on ${formatDateTime(outcome.refund.completed_at)}` : ""}. Nothing changed. If you also
                sent the money, it was sent twice: check the M-Pesa portal.
              </>
            ) : (
              <>
                The {formatKes(outcome.refund.amount_cents)} refund for booking <span className="mono">{outcome.refund.booking_reference}</span>{" "}
                is recorded as paid back to {outcome.refund.phone}. It is now under Paid back.
              </>
            )}
          </p>
        </Notice>
      )}

      <Card>
        <div className="card__header">
          <h2>
            {status === "PENDING" ? "Waiting to be paid back" : "Paid back"}
            {status === "PENDING" && rows.length > 0 && (
              <span className="badge" data-testid="pending-refunds-badge">
                {rows.length}
                {refunds.hasNextPage ? "+" : ""}
              </span>
            )}
          </h2>
          <span className="small muted">Oldest first. Times are Nairobi time.</span>
        </div>
        <div className="card__body card__body--flush">
          {refunds.isPending ? (
            <SkeletonRows label="Loading refunds" />
          ) : refunds.isError && rows.length === 0 ? (
            <LoadFailed message={describeError(refunds.error)} onRetry={() => refunds.refetch()} retrying={refunds.isFetching} />
          ) : rows.length === 0 ? (
            status === "PENDING" ? (
              <StateBlock icon={<CheckIcon />} title="No refunds are waiting">
                <p>{filter.eventId === null ? "Nobody is owed money back." : "Nobody is owed money back for this event."} This list checks for new ones every 30 seconds.</p>
              </StateBlock>
            ) : (
              <StateBlock title="No refunds have been paid back yet">
                <p>Refunds move here once someone marks them as refunded.</p>
              </StateBlock>
            )
          ) : (
            <>
              {refunds.isError && (
                <div className="card__inset">
                  <Notice tone="warning" title="This list has stopped updating">
                    {describeError(refunds.error)} We keep trying.
                  </Notice>
                </div>
              )}
              <div className="table-wrap">
                <table className="table">
                  <thead>
                    <tr>
                      <th scope="col">Booking</th>
                      <th scope="col">Amount</th>
                      <th scope="col">Pay it back to</th>
                      <th scope="col">Why</th>
                      <th scope="col">{status === "PENDING" ? "Waiting since" : "Paid back"}</th>
                      <th scope="col">
                        <span className="visually-hidden">Actions</span>
                      </th>
                    </tr>
                  </thead>
                  <tbody>
                    {rows.map((refund) => (
                      <tr key={refund.id} data-refund={refund.id}>
                        <td className="table__primary" data-label="Booking">
                          <span className="mono">{refund.booking_reference}</span>
                          <span className="table__secondary">
                            <Link to={`/events/${refund.event_id}/sales`}>{filter.titleOf(refund.event_id)}</Link>
                          </span>
                        </td>
                        <td data-label="Amount">
                          <strong>{formatKes(refund.amount_cents)}</strong>
                        </td>
                        <td data-label="Pay it back to">
                          <span className="cellstack">
                            <span className="nowrap">{refund.phone}</span>
                            <span className="table__secondary">
                              {refund.mpesa_receipt ? (
                                <>
                                  M-Pesa receipt <span className="mono">{refund.mpesa_receipt}</span>
                                </>
                              ) : (
                                "No M-Pesa receipt"
                              )}
                            </span>
                          </span>
                        </td>
                        <td className="table__wrap" data-label="Why">
                          {refund.reason}
                        </td>
                        <td data-label={status === "PENDING" ? "Waiting since" : "Paid back"}>
                          {refund.status === "DONE" && refund.completed_at ? (
                            <span className="cellstack">
                              <time dateTime={refund.completed_at}>{formatDateTime(refund.completed_at)}</time>
                              <span className="table__secondary">
                                Queued <time dateTime={refund.created_at}>{formatDateTime(refund.created_at)}</time>
                              </span>
                            </span>
                          ) : (
                            <time dateTime={refund.created_at}>{formatDateTime(refund.created_at)}</time>
                          )}
                        </td>
                        <td className="table__actions-cell">
                          {refund.status === "PENDING" ? (
                            <div className="table__actions">
                              <Button
                                size="small"
                                onClick={() => ask(refund)}
                                aria-label={`Mark as refunded: ${formatKes(refund.amount_cents)}, booking ${refund.booking_reference}`}
                              >
                                Mark as refunded…
                              </Button>
                            </div>
                          ) : (
                            <span className="chip chip--accepted">{refundStatusLabel(refund.status)}</span>
                          )}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}
        </div>
        {refunds.hasNextPage && (
          <div className="card__footer">
            <Button variant="secondary" size="small" loading={refunds.isFetchingNextPage} onClick={() => refunds.fetchNextPage()}>
              Show more
            </Button>
          </div>
        )}
      </Card>

      <ConfirmDialog
        open={asking !== null}
        title={asking ? `Mark ${formatKes(asking.amount_cents)} as refunded?` : "Mark as refunded?"}
        // A refused or lost request changed nothing (an event that is busy answers 409), and the
        // call is safe to repeat, so the same button sends the same thing again.
        confirmLabel={gone ? "Close" : complete.isError ? "Try again" : "Yes, I have sent the money"}
        cancelLabel={complete.isError ? "Close" : "Not yet"}
        busy={complete.isPending}
        error={complete.isError ? describeError(complete.error, { NOT_FOUND: "This refund no longer exists. Close this and check the list." }) : null}
        onConfirm={() => {
          if (!asking) return;
          if (gone) {
            setAsking(null);
            return;
          }
          complete.mutate(asking);
        }}
        onCancel={() => setAsking(null)}
      >
        {asking && (
          <div className="stack" style={{ "--gap": "0.625rem" } as CSSProperties}>
            <p>
              Only do this after you have sent <strong>{formatKes(asking.amount_cents)}</strong> back to <strong>{asking.phone}</strong> in
              the M-Pesa portal. This site does not send any money. It only records that you did.
            </p>
            <p>What marking it does, for booking <span className="mono">{asking.booking_reference}</span>:</p>
            <ul className="plainlist">
              <li>This refund is recorded as paid back and leaves the waiting list.</li>
              <li>
                If the booking is waiting for a refund and this is the last one it is owed, the booking becomes Refunded and
                any ticket it still has stops working.
              </li>
              <li>
                If the booking is paid and this refund is for a second payment made by mistake, the booking stays paid and
                keeps its tickets.
              </li>
              <li>No seat changes. Marking a refund never puts a seat back on sale.</li>
            </ul>
            <p>There is no button to undo this.</p>
          </div>
        )}
      </ConfirmDialog>
    </div>
  );
}
