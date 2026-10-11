import { unwrap } from "@talanta/shared";
import { useQuery } from "@tanstack/react-query";
import { useMemo, useState } from "react";
import { AlertIcon, ChartIcon } from "./icons";
import { StadiumPlan } from "./StadiumPlan";
import { Alert, Card, LoadFailed, SkeletonRows, StateBlock, Swatch } from "./ui";
import { api, type Schemas } from "../lib/api";
import { describeError } from "../lib/errors";
import { formatBucket, formatClock, formatDateTime } from "../lib/format";
import { HEAT_LEGEND_STOPS, heatColour, soldShare } from "../lib/heat";
import { formatCount, formatKes, formatKesCompact } from "../lib/money";
import { isPublished, keys, useVenue, type AdminEvent, type SalesBucket, type SalesSummary } from "../lib/queries";

/** How often the figures refresh. The server caches seat counts for 3 seconds, so faster buys nothing. */
const REFRESH_MS = 5_000;
/** The hourly chart shows this many of the most recent hours: enough bars to read, and no more. */
const HOURLY_LOOKBACK_HOURS = 48;

function useSales(eventId: number, bucket: SalesBucket, enabled: boolean) {
  return useQuery({
    queryKey: [...keys.sales(eventId), bucket],
    queryFn: () => {
      // The hourly view is "the last 48 hours", so it sends its own start. (Left out, the server
      // would answer with every hour since the first sale, up to 2,000 bars.) The daily view
      // takes the server's default range, from the first paid booking. `bucket` is always sent,
      // so the chart is drawn in the unit the organiser picked.
      const from =
        bucket === "hour"
          ? new Date(Math.floor(Date.now() / 3_600_000) * 3_600_000 - (HOURLY_LOOKBACK_HOURS - 1) * 3_600_000)
              .toISOString()
              .replace(".000Z", "Z")
          : undefined;
      return unwrap(api.GET("/admin/events/{eventId}/sales", { params: { path: { eventId }, query: { bucket, from } } }));
    },
    enabled,
    staleTime: 0,
    // Paused by TanStack Query while the tab is hidden (refetchIntervalInBackground is off).
    refetchInterval: REFRESH_MS,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: true,
    placeholderData: (previous) => previous,
  });
}

export function SalesDashboard({ event }: { event: AdminEvent }) {
  const [bucket, setBucket] = useState<SalesBucket>("hour");
  const hasInventory = isPublished(event.status) || event.status === "CANCELLED" || event.status === "COMPLETED";
  const live = isPublished(event.status);
  const sales = useSales(event.id, bucket, hasInventory);
  const venue = useVenue(hasInventory ? event.venue_id : undefined);

  if (!hasInventory) {
    return (
      <Card>
        <StateBlock icon={<ChartIcon />} title="No sales yet: this event is a draft">
          <p>Sales figures appear here once the event is published and fans start buying.</p>
        </StateBlock>
      </Card>
    );
  }
  if (sales.isPending) {
    return (
      <Card>
        <div className="card__body card__body--flush">
          <SkeletonRows label="Loading sales" rows={5} />
        </div>
      </Card>
    );
  }
  if (sales.isError && !sales.data) {
    return (
      <Card>
        <LoadFailed message={describeError(sales.error)} onRetry={() => sales.refetch()} retrying={sales.isFetching} />
      </Card>
    );
  }
  const summary = sales.data!;

  return (
    <>
      <RefundsCard eventId={event.id} summary={summary} />

      {sales.isError && (
        <Alert tone="warning" title="The figures have stopped updating">
          {describeError(sales.error)} These are the figures from {formatClock(summary.generated_at)}; we keep trying.
        </Alert>
      )}

      <section className="kpis" aria-label="Headline figures">
        <Kpi label="Revenue" value={formatKes(summary.revenue_cents)} note="From paid bookings" hero />
        <Kpi label="Sold" value={formatCount(summary.seats.sold)} note={`${percent(summary.seats.sold, summary.seats.total - summary.seats.blocked)} of seats for sale`} />
        <Kpi label="Being paid for" value={formatCount(summary.seats.held)} note="Held for up to 10 minutes" />
        <Kpi
          label="Available"
          value={formatCount(summary.seats.available)}
          note={summary.seats.blocked > 0 ? `${formatCount(summary.seats.blocked)} more are blocked` : `of ${formatCount(summary.seats.total)} seats`}
        />
      </section>
      <p className="small muted livenote" role="status">
        <span className={live && !sales.isError ? "livedot" : "livedot livedot--off"} aria-hidden="true" />
        <span>
          Figures from <time dateTime={summary.generated_at}>{formatClock(summary.generated_at)}</time> Nairobi time.{" "}
          {live ? "They refresh every 5 seconds while this tab is open." : "This event is closed, so they no longer change."}
        </span>
      </p>

      <Card>
        <div className="card__header">
          <h2>By category</h2>
          <ul className="legend legend--inline" aria-hidden="true">
            <li>
              <span className="keymark keymark--sold" /> Sold
            </li>
            <li>
              <span className="keymark keymark--held" /> Being paid for
            </li>
            <li>
              <span className="keymark keymark--free" /> Available
            </li>
          </ul>
        </div>
        <div className="card__body card__body--flush">
          {summary.categories.length === 0 ? (
            <StateBlock title="This event has no categories" />
          ) : (
            <div className="table-wrap">
              <table className="table">
                <thead>
                  <tr>
                    <th scope="col">Category</th>
                    <th scope="col">Sold</th>
                    <th scope="col">Being paid for</th>
                    <th scope="col">Available</th>
                    <th scope="col">Revenue</th>
                    <th scope="col">Share sold</th>
                  </tr>
                </thead>
                <tbody>
                  {summary.categories.map((category) => {
                    const sellable = category.total - category.blocked;
                    return (
                      <tr key={category.category_id}>
                        <td className="table__primary" data-label="Category">
                          <span className="catname">
                            <Swatch color={category.color_hex} />
                            {category.name}
                          </span>
                          <span className="table__secondary">{formatKes(category.price_cents)} a ticket</span>
                        </td>
                        <td data-label="Sold">{formatCount(category.sold)}</td>
                        <td data-label="Being paid for">{formatCount(category.held)}</td>
                        <td data-label="Available">{formatCount(category.available)}</td>
                        <td data-label="Revenue">{formatKes(category.revenue_cents)}</td>
                        <td data-label="Share sold">
                          <span className="splitbar" role="img" aria-label={`${percent(category.sold, sellable)} sold, ${percent(category.held, sellable)} being paid for`}>
                            <span className="splitbar__sold" style={{ flexGrow: category.sold }} />
                            <span className="splitbar__held" style={{ flexGrow: category.held }} />
                            <span className="splitbar__free" style={{ flexGrow: Math.max(category.available, sellable === 0 ? 1 : 0) }} />
                          </span>
                          <span className="small muted"> {percent(category.sold, sellable)}</span>
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      </Card>

      <SalesChart summary={summary} bucket={bucket} onBucket={setBucket} switching={sales.isPlaceholderData || summary.series.bucket !== bucket} />

      <Card>
        <div className="card__header">
          <h2>Where the stadium is selling</h2>
          <HeatLegend />
        </div>
        <div className="card__body">
          {venue.isPending ? (
            <SkeletonRows label="Loading the stadium plan" />
          ) : venue.isError ? (
            <LoadFailed message={describeError(venue.error)} onRetry={() => venue.refetch()} retrying={venue.isFetching} />
          ) : (
            <Heatmap venue={venue.data} summary={summary} />
          )}
        </div>
      </Card>
    </>
  );
}

function percent(part: number, whole: number): string {
  if (!(whole > 0)) return "0%";
  const value = (part / whole) * 100;
  if (value > 0 && value < 1) return "under 1%";
  return `${Math.round(value)}%`;
}

function Kpi({ label, value, note, hero }: { label: string; value: string; note: string; hero?: boolean }) {
  return (
    <div className={hero ? "kpi kpi--hero" : "kpi"}>
      <span className="kpi__label">{label}</span>
      <span className="kpi__value" data-kpi={label}>
        {value}
      </span>
      <span className="kpi__note">{note}</span>
    </div>
  );
}

function RefundsCard({ eventId, summary }: { eventId: number; summary: SalesSummary }) {
  const count = summary.refund_required_count;
  const bookings = useQuery({
    // The count is part of the key, so the list reloads the moment the dashboard sees a new one.
    queryKey: [...keys.refundBookings(eventId), count],
    queryFn: () =>
      unwrap(
        api.GET("/admin/events/{eventId}/bookings", {
          params: { path: { eventId }, query: { status: "REFUND_REQUIRED", limit: 50 } },
        }),
      ),
    enabled: count > 0,
    placeholderData: (previous) => previous,
  });

  if (count === 0) {
    return (
      <p className="small muted calm">
        <span className="calm__mark" aria-hidden="true">
          ✓
        </span>
        No bookings are waiting for a refund.
      </p>
    );
  }
  const rows = bookings.data?.items ?? [];
  return (
    <Card className="card--attention">
      <div className="card__header">
        <h2>
          <AlertIcon /> Bookings that need a refund <span className="badge">{formatCount(count)}</span>
        </h2>
        <span className="small">
          <strong>{formatKes(summary.refund_required_cents)}</strong> owed back
        </span>
      </div>
      <div className="card__body stack">
        <p className="small">
          These people paid, but did not get (or no longer have) their seats. Someone has to send each of them their money
          back. Sending and ticking off refunds from this page comes with the payments milestone; for now this is the list
          to work from.
        </p>
      </div>
      <div className="card__body card__body--flush">
        {bookings.isPending ? (
          <SkeletonRows label="Loading bookings that need a refund" />
        ) : bookings.isError && rows.length === 0 ? (
          <LoadFailed message={describeError(bookings.error)} onRetry={() => bookings.refetch()} retrying={bookings.isFetching} />
        ) : (
          <div className="table-wrap">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">Booking</th>
                  <th scope="col">Fan</th>
                  <th scope="col">Seats</th>
                  <th scope="col">Amount</th>
                  <th scope="col">Why</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((booking) => (
                  <RefundRow key={booking.id} booking={booking} />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      {bookings.data?.next_cursor && (
        <div className="card__footer small muted">Showing the newest {rows.length} of {formatCount(count)}.</div>
      )}
    </Card>
  );
}

function RefundRow({ booking }: { booking: Schemas["AdminBooking"] }) {
  const amount = booking.refund?.amount_cents ?? booking.total_cents;
  return (
    <tr>
      <td className="table__primary" data-label="Booking">
        <span className="mono">{booking.reference}</span>
        <span className="table__secondary">
          Booked <time dateTime={booking.created_at}>{formatDateTime(booking.created_at)}</time>
        </span>
      </td>
      <td data-label="Fan">
        {booking.user.name ?? <span className="muted">No name</span>}
        <span className="table__secondary">{booking.refund?.phone ?? booking.user.phone ?? "No phone number"}</span>
      </td>
      <td className="table__wrap" data-label="Seats">
        {booking.seat_labels.slice(0, 4).join(", ")}
        {booking.seat_labels.length > 4 ? ` and ${booking.seat_labels.length - 4} more` : ""}
      </td>
      <td data-label="Amount">
        <strong>{formatKes(amount)}</strong>
        {booking.latest_payment?.mpesa_receipt && <span className="table__secondary mono">{booking.latest_payment.mpesa_receipt}</span>}
      </td>
      <td className="table__wrap" data-label="Why">
        {booking.refund?.reason ?? "Refund not queued yet"}
      </td>
    </tr>
  );
}

const CHART = { width: 720, height: 240, left: 64, right: 12, top: 14, bottom: 30 };

/** A round number at or above `value` to end the axis on: 1, 2 or 5 times a power of ten. */
function niceCeiling(value: number): number {
  if (!(value > 0)) return 1;
  const power = 10 ** Math.floor(Math.log10(value));
  for (const step of [1, 2, 5, 10]) if (step * power >= value) return step * power;
  return 10 * power;
}

function SalesChart({
  summary,
  bucket,
  onBucket,
  switching,
}: {
  summary: SalesSummary;
  bucket: SalesBucket;
  onBucket: (bucket: SalesBucket) => void;
  switching: boolean;
}) {
  const [hover, setHover] = useState<number | null>(null);
  const [asTable, setAsTable] = useState(false);
  const shownBucket = summary.series.bucket;
  const points = summary.series.points;
  const top = useMemo(() => niceCeiling(Math.max(0, ...points.map((point) => point.revenue_cents)) / 100) * 100, [points]);
  const total = points.reduce((sum, point) => sum + point.revenue_cents, 0);
  const innerWidth = CHART.width - CHART.left - CHART.right;
  const innerHeight = CHART.height - CHART.top - CHART.bottom;
  const slot = points.length > 0 ? innerWidth / points.length : innerWidth;
  const barWidth = Math.max(1.5, Math.min(28, slot - 2));
  const labelEvery = Math.max(1, Math.ceil(points.length / 6));
  const active = hover !== null ? points[hover] : undefined;

  return (
    <Card>
      <div className="card__header">
        <h2>Revenue over time</h2>
        <div className="row" style={{ gap: "0.5rem" }}>
          <div className="filter" role="group" aria-label="Group sales by">
            <button type="button" className="filter__option" aria-pressed={bucket === "hour"} onClick={() => onBucket("hour")}>
              By hour
            </button>
            <button type="button" className="filter__option" aria-pressed={bucket === "day"} onClick={() => onBucket("day")}>
              By day
            </button>
          </div>
          <button type="button" className="filter__option filter__option--lone" aria-pressed={asTable} onClick={() => setAsTable((on) => !on)}>
            Table
          </button>
        </div>
      </div>
      <div className="card__body stack" style={{ opacity: switching ? 0.55 : 1 }}>
        <p className="small muted">
          {shownBucket === "hour" ? `Paid bookings in the last ${HOURLY_LOOKBACK_HOURS} hours, by hour.` : "Paid bookings since the first sale, by Nairobi day."}{" "}
          Nairobi time.
        </p>
        {points.length === 0 || total === 0 ? (
          <StateBlock icon={<ChartIcon />} title={shownBucket === "hour" ? "No paid bookings in the last 48 hours" : "No paid bookings yet"}>
            <p>{shownBucket === "hour" && summary.revenue_cents > 0 ? "Switch to By day to see earlier sales." : "The chart fills in as fans pay."}</p>
          </StateBlock>
        ) : asTable ? (
          <div className="table-wrap chart-table">
            <table className="table">
              <thead>
                <tr>
                  <th scope="col">{shownBucket === "hour" ? "Hour starting" : "Day"}</th>
                  <th scope="col">Bookings</th>
                  <th scope="col">Tickets</th>
                  <th scope="col">Revenue</th>
                </tr>
              </thead>
              <tbody>
                {points
                  .filter((point) => point.bookings > 0)
                  .map((point) => (
                    <tr key={point.bucket_start}>
                      <td data-label="When">{formatBucket(point.bucket_start, shownBucket)}</td>
                      <td data-label="Bookings">{formatCount(point.bookings)}</td>
                      <td data-label="Tickets">{formatCount(point.tickets)}</td>
                      <td data-label="Revenue">{formatKes(point.revenue_cents)}</td>
                    </tr>
                  ))}
              </tbody>
            </table>
          </div>
        ) : (
          <div className="chart" onMouseLeave={() => setHover(null)}>
            <svg viewBox={`0 0 ${CHART.width} ${CHART.height}`} role="img" aria-label={`Bar chart of revenue ${shownBucket === "hour" ? "per hour" : "per day"}. Total ${formatKes(total)}. Use the Table button for the numbers.`}>
              {[0, 0.5, 1].map((fraction) => {
                const y = CHART.top + innerHeight * (1 - fraction);
                return (
                  <g key={fraction}>
                    <line className={fraction === 0 ? "chart__axis" : "chart__grid"} x1={CHART.left} x2={CHART.width - CHART.right} y1={y} y2={y} />
                    <text className="chart__tick" x={CHART.left - 8} y={y + 4} textAnchor="end">
                      {formatKesCompact(top * fraction)}
                    </text>
                  </g>
                );
              })}
              {points.map((point, index) => {
                const x = CHART.left + slot * index + (slot - barWidth) / 2;
                const height = top > 0 ? (point.revenue_cents / top) * innerHeight : 0;
                const y = CHART.top + innerHeight - height;
                const r = Math.min(4, barWidth / 2, height);
                return (
                  <g key={point.bucket_start}>
                    {height > 0 && (
                      <path
                        className={hover === index ? "chart__bar chart__bar--active" : "chart__bar"}
                        d={`M${x} ${y + height}V${y + r}Q${x} ${y} ${x + r} ${y}H${x + barWidth - r}Q${x + barWidth} ${y} ${x + barWidth} ${y + r}V${y + height}Z`}
                      />
                    )}
                    {index % labelEvery === 0 && (
                      <text className="chart__tick" x={x + barWidth / 2} y={CHART.height - 10} textAnchor={index === 0 ? "start" : "middle"}>
                        {formatBucket(point.bucket_start, shownBucket)}
                      </text>
                    )}
                    <rect
                      className="chart__hit"
                      x={CHART.left + slot * index}
                      y={CHART.top}
                      width={slot}
                      height={innerHeight}
                      onMouseEnter={() => setHover(index)}
                      onFocus={() => setHover(index)}
                      onBlur={() => setHover(null)}
                      tabIndex={point.bookings > 0 ? 0 : undefined}
                      aria-label={`${formatBucket(point.bucket_start, shownBucket)}: ${formatKes(point.revenue_cents)} from ${point.tickets} tickets`}
                    />
                  </g>
                );
              })}
            </svg>
            <p className="chart__readout" aria-live="off">
              {active ? (
                <>
                  <strong>{formatBucket(active.bucket_start, shownBucket)}</strong> · {formatKes(active.revenue_cents)} ·{" "}
                  {formatCount(active.tickets)} {active.tickets === 1 ? "ticket" : "tickets"} in {formatCount(active.bookings)}{" "}
                  {active.bookings === 1 ? "booking" : "bookings"}
                </>
              ) : (
                <span className="muted">Point at a bar for its figures. Total shown: {formatKes(total)}.</span>
              )}
            </p>
          </div>
        )}
      </div>
    </Card>
  );
}

function HeatLegend() {
  return (
    <div className="heatlegend" aria-label="Colour key: share of each block that is sold">
      <span className="small muted">Sold:</span>
      {HEAT_LEGEND_STOPS.map((stop) => (
        <span key={stop} className="heatlegend__stop">
          <span className="heatlegend__chip" style={{ background: heatColour(stop) }} />
          <span className="small muted">{stop === 0 ? "none" : stop === 0.01 ? "some" : `${stop * 100}%`}</span>
        </span>
      ))}
    </div>
  );
}

function Heatmap({ venue, summary }: { venue: Schemas["VenueDetail"]; summary: SalesSummary }) {
  const byBlock = useMemo(() => new Map(summary.blocks.map((block) => [block.block_id, block])), [summary.blocks]);
  const busiest = useMemo(
    () =>
      summary.blocks
        .filter((block) => block.sold > 0)
        .sort((a, b) => soldShare(b.sold, b.total) - soldShare(a.sold, a.total))
        .slice(0, 5),
    [summary.blocks],
  );
  const codes = useMemo(() => new Map(venue.stands.flatMap((stand) => stand.blocks.map((block) => [block.id, block.code] as const))), [venue]);
  return (
    <div className="stack">
      <div className="planwrap">
        <StadiumPlan
          venue={venue}
          label={`Heatmap of ${venue.name}: each block is darker the more of it is sold.`}
          look={(block, stand) => {
            const figures = byBlock.get(block.id);
            const share = figures ? soldShare(figures.sold, figures.total) : 0;
            return {
              fill: figures ? heatColour(share) : "#ffffff",
              title: figures
                ? `Block ${block.code}, ${stand.name} ${stand.tier.toLowerCase()}: ${formatCount(figures.sold)} of ${formatCount(figures.total)} sold (${percent(figures.sold, figures.total)})${figures.held > 0 ? `, ${formatCount(figures.held)} being paid for` : ""}`
                : `Block ${block.code}: not part of this event`,
            };
          }}
        />
      </div>
      {busiest.length > 0 ? (
        <p className="small" data-testid="busiest">
          <strong>Selling fastest:</strong>{" "}
          {busiest
            .map((block) => `${codes.get(block.block_id) ?? `#${block.block_id}`} ${percent(block.sold, block.total)} (${formatCount(block.sold)} of ${formatCount(block.total)})`)
            .join(" · ")}
        </p>
      ) : (
        <p className="small muted">Nothing is sold yet. Blocks darken as their seats sell. Point at a block for its figures.</p>
      )}
    </div>
  );
}
