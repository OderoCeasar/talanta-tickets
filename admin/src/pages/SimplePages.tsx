import { unwrap } from "@talanta/shared";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { StatusChip } from "../components/EventBits";
import { AlertIcon, CalendarIcon, ChartIcon, ShieldIcon, StadiumIcon, TeamIcon, UserIcon } from "../components/icons";
import { Alert, Card, LoadFailed, SkeletonRows, StateBlock } from "../components/ui";
import { api } from "../lib/api";
import { describeError } from "../lib/errors";
import { formatDateTime } from "../lib/format";
import { formatCount, formatKes } from "../lib/money";
import { isPublished, keys, useAdminEvents, type AdminEvent } from "../lib/queries";
import { useCurrentUser } from "../lib/session";

interface Tile {
  to: string;
  title: string;
  text: string;
  icon: ReactNode;
  adminOnly?: boolean;
}

const TILES: Tile[] = [
  { to: "/events", title: "Events", text: "Create events, set prices and put them on sale.", icon: <CalendarIcon /> },
  { to: "/layout", title: "Venue layout", text: "Place blocks, generate seats and keep templates.", icon: <StadiumIcon /> },
  { to: "/sales", title: "Sales", text: "Revenue and seats sold, as it happens.", icon: <ChartIcon /> },
  { to: "/team", title: "Team", text: "Invite people and see who has access.", icon: <TeamIcon />, adminOnly: true },
  { to: "/account", title: "Your account", text: "Change your password and set up two-step sign-in.", icon: <UserIcon /> },
];

/** How many upcoming events the home page shows; the rest are one click away under Events. */
const UPCOMING_LIMIT = 6;

export function HomePage() {
  const user = useCurrentUser();
  const firstName = user.name?.trim().split(/\s+/)[0];
  const tiles = TILES.filter((tile) => !tile.adminOnly || user.role === "ADMIN");

  return (
    <div className="page">
      <header className="page__header">
        <h1>{firstName ? `Welcome, ${firstName}` : "Welcome"}</h1>
        <p>This is the organiser site for Talanta Stadium.</p>
      </header>

      {!user.totp_enabled && (
        <Alert tone="info" title="Make your account harder to break into">
          Two-step sign-in asks for a code from your phone as well as your password.{" "}
          <Link to="/account">Set it up on your account page</Link>.
        </Alert>
      )}

      <UpcomingEvents />

      <div className="tiles">
        {tiles.map((tile) => (
          <Link key={tile.to} to={tile.to} className="card tile">
            <span className="tile__icon">{tile.icon}</span>
            <span className="tile__title">{tile.title}</span>
            <span className="small muted">{tile.text}</span>
          </Link>
        ))}
      </div>
    </div>
  );
}

function UpcomingEvents() {
  // The list is latest kickoff first; upcoming events are wanted soonest first.
  const events = useAdminEvents("ALL", 100);
  const now = Date.now();
  const upcoming = (events.data?.pages.flatMap((page) => page.items) ?? [])
    .filter((event) => new Date(event.kickoff_at).getTime() >= now && event.status !== "CANCELLED" && event.status !== "COMPLETED")
    .sort((a, b) => a.kickoff_at.localeCompare(b.kickoff_at));
  const shown = upcoming.slice(0, UPCOMING_LIMIT);

  return (
    <section className="stack" aria-labelledby="upcoming-heading">
      <div className="row" style={{ justifyContent: "space-between" }}>
        <h2 id="upcoming-heading">Upcoming events</h2>
        <Link to="/events" className="small">
          All events
        </Link>
      </div>
      {events.isPending ? (
        <Card>
          <div className="card__body card__body--flush">
            <SkeletonRows label="Loading upcoming events" />
          </div>
        </Card>
      ) : events.isError ? (
        <Card>
          <LoadFailed message={describeError(events.error)} onRetry={() => events.refetch()} retrying={events.isFetching} />
        </Card>
      ) : shown.length === 0 ? (
        <Card>
          <StateBlock
            icon={<CalendarIcon />}
            title="Nothing coming up"
            action={
              <Link to="/events/new" className="btn btn--secondary btn--small">
                Create an event
              </Link>
            }
          >
            <p>Create an event from your saved venue template, set its prices and publish it.</p>
          </StateBlock>
        </Card>
      ) : (
        <>
          <div className="eventcards">
            {shown.map((event) => (
              <EventCard key={event.id} event={event} />
            ))}
          </div>
          {upcoming.length > shown.length && (
            <p className="small muted">
              And {upcoming.length - shown.length} more under <Link to="/events">Events</Link>.
            </p>
          )}
        </>
      )}
    </section>
  );
}

function EventCard({ event }: { event: AdminEvent }) {
  const published = isPublished(event.status);
  const sales = useQuery({
    queryKey: [...keys.sales(event.id), "day"],
    queryFn: () => unwrap(api.GET("/admin/events/{eventId}/sales", { params: { path: { eventId: event.id }, query: { bucket: "day" } } })),
    enabled: published,
    refetchInterval: 30_000,
  });

  return (
    <Link to={published ? `/events/${event.id}/sales` : `/events/${event.id}`} className="card eventcard">
      <span className="eventcard__top">
        <StatusChip status={event.status} />
        <time className="small muted" dateTime={event.kickoff_at}>
          {formatDateTime(event.kickoff_at)}
        </time>
      </span>
      <span className="eventcard__title">{event.title}</span>
      {published ? (
        <span className="eventcard__figures">
          <span>
            <span className="eventcard__number">{sales.data ? formatKes(sales.data.revenue_cents) : sales.isError ? "—" : "…"}</span>
            <span className="eventcard__label">Revenue</span>
          </span>
          <span>
            <span className="eventcard__number">{sales.data ? formatCount(sales.data.seats.sold) : sales.isError ? "—" : "…"}</span>
            <span className="eventcard__label">Sold</span>
          </span>
          <span>
            <span className="eventcard__number">{formatCount(event.seats_available)}</span>
            <span className="eventcard__label">Available</span>
          </span>
        </span>
      ) : (
        <span className="small muted">Draft: not on sale yet. Open it to set prices, paint seats and publish.</span>
      )}
      {sales.data && sales.data.refund_required_count > 0 && (
        <span className="small eventcard__warn">
          {formatCount(sales.data.refund_required_count)} {sales.data.refund_required_count === 1 ? "booking needs" : "bookings need"} a refund
        </span>
      )}
      {sales.isError && <span className="small muted">Sales figures did not load.</span>}
    </Link>
  );
}

export function ForbiddenPage() {
  return (
    <div className="page page--narrow">
      <Card>
        <StateBlock
          icon={<ShieldIcon />}
          title="This page is for admins"
          action={
            <Link className="btn btn--secondary" to="/">
              Back to home
            </Link>
          }
        >
          <p>
            Your account is an organiser account, which cannot invite people or see the team list. If you need this, ask
            a site admin.
          </p>
          <p className="small">Error 403</p>
        </StateBlock>
      </Card>
    </div>
  );
}

export function NotFoundPage() {
  return (
    <div className="page page--narrow">
      <Card>
        <StateBlock
          icon={<AlertIcon />}
          title="There is no page here"
          action={
            <Link className="btn btn--secondary" to="/">
              Back to home
            </Link>
          }
        >
          <p>The address may be mistyped, or the page may have moved.</p>
        </StateBlock>
      </Card>
    </div>
  );
}
