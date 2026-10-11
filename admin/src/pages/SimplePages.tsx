import type { ReactNode } from "react";
import { Link } from "react-router-dom";
import { AlertIcon, CalendarIcon, ChartIcon, ShieldIcon, StadiumIcon, TeamIcon, UserIcon } from "../components/icons";
import { Alert, Card, StateBlock } from "../components/ui";
import { useCurrentUser } from "../lib/session";

interface Tile {
  to: string;
  title: string;
  text: string;
  icon: ReactNode;
  soon?: boolean;
  adminOnly?: boolean;
}

const TILES: Tile[] = [
  { to: "/events", title: "Events", text: "Create events, set prices and put them on sale.", icon: <CalendarIcon />, soon: true },
  { to: "/layout", title: "Venue layout", text: "Place the stands, generate seats and paint categories.", icon: <StadiumIcon />, soon: true },
  { to: "/sales", title: "Sales", text: "Revenue and seats sold, as it happens.", icon: <ChartIcon />, soon: true },
  { to: "/team", title: "Team", text: "Invite people and see who has access.", icon: <TeamIcon />, adminOnly: true },
  { to: "/account", title: "Your account", text: "Change your password and set up two-step sign-in.", icon: <UserIcon /> },
];

export function HomePage() {
  const user = useCurrentUser();
  const firstName = user.name?.trim().split(/\s+/)[0];
  const tiles = TILES.filter((tile) => !tile.adminOnly || user.role === "ADMIN");

  return (
    <div className="page">
      <header className="page__header">
        <h1>{firstName ? `Welcome, ${firstName}` : "Welcome"}</h1>
        <p>This is the organiser site for Talanta Stadium. Pick where you want to go.</p>
      </header>

      {!user.totp_enabled && (
        <Alert tone="info" title="Make your account harder to break into">
          Two-step sign-in asks for a code from your phone as well as your password.{" "}
          <Link to="/account">Set it up on your account page</Link>.
        </Alert>
      )}

      <div className="tiles">
        {tiles.map((tile) => (
          <Link key={tile.to} to={tile.to} className="card tile">
            <span className="tile__icon">{tile.icon}</span>
            <span className="tile__title">
              {tile.title}
              {tile.soon && <span className="chip chip--plain chip--soon">Coming soon</span>}
            </span>
            <span className="small muted">{tile.text}</span>
          </Link>
        ))}
      </div>
    </div>
  );
}

/** Stands in for a section that a later slice builds, so the navigation is already real. */
export function ComingSoonPage({ title, text, icon }: { title: string; text: string; icon: ReactNode }) {
  return (
    <div className="page">
      <header className="page__header">
        <h1>{title}</h1>
      </header>
      <Card>
        <StateBlock icon={icon} title={`${title} is coming soon`}>
          <p>{text}</p>
        </StateBlock>
      </Card>
    </div>
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
