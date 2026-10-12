import { useEffect, useState, type ReactNode } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router-dom";
import { badgeText, countPhrase } from "../lib/attention";
import { initials, roleLabel } from "../lib/format";
import { useOpenAlertCount, usePendingRefundCount } from "../lib/queries";
import { useCurrentUser, useSession } from "../lib/session";
import {
  BellIcon,
  BrandMark,
  CalendarIcon,
  ChartIcon,
  CloseIcon,
  HomeIcon,
  MenuIcon,
  RefundIcon,
  SignOutIcon,
  StadiumIcon,
  TeamIcon,
  UserIcon,
} from "./icons";

interface NavItem {
  to: string;
  label: string;
  icon: ReactNode;
  adminOnly?: boolean;
  /** Which count of things waiting for staff to show next to the label. */
  count?: "alerts" | "refunds";
}

const NAV: NavItem[] = [
  { to: "/", label: "Home", icon: <HomeIcon /> },
  { to: "/events", label: "Events", icon: <CalendarIcon /> },
  { to: "/layout", label: "Venue layout", icon: <StadiumIcon /> },
  { to: "/sales", label: "Sales", icon: <ChartIcon /> },
  { to: "/alerts", label: "Alerts", icon: <BellIcon />, count: "alerts" },
  { to: "/refunds", label: "Refunds", icon: <RefundIcon />, count: "refunds" },
  { to: "/team", label: "Team", icon: <TeamIcon />, adminOnly: true },
];

/** The signed-in frame: brand, navigation, who is signed in, and the page itself. */
export function AppLayout() {
  const user = useCurrentUser();
  const { signOut } = useSession();
  const location = useLocation();
  const [menuOpen, setMenuOpen] = useState(false);
  const [signingOut, setSigningOut] = useState(false);

  // On a phone the menu covers the page, so close it once a link has been followed.
  useEffect(() => setMenuOpen(false), [location.pathname]);

  async function handleSignOut() {
    setSigningOut(true);
    // The session provider clears this browser first; the route guard then shows /login.
    await signOut();
  }

  const items = NAV.filter((item) => !item.adminOnly || user.role === "ADMIN");
  const openAlerts = useOpenAlertCount().data;
  const pendingRefunds = usePendingRefundCount().data;
  const counts = {
    alerts: openAlerts && { ...openAlerts, words: countPhrase(openAlerts.count, openAlerts.more, "open alert", "open alerts") },
    refunds: pendingRefunds && { ...pendingRefunds, words: countPhrase(pendingRefunds.count, pendingRefunds.more, "refund waiting", "refunds waiting") },
  };
  const waiting = (openAlerts?.count ?? 0) + (pendingRefunds?.count ?? 0);
  const displayName = user.name?.trim() || user.email || "Signed in";

  return (
    <div className="shell">
      <header className="sidebar">
        <div className="sidebar__top">
          <Link to="/" className="brand">
            <BrandMark />
            <span>
              <span className="brand__name">Talanta Tickets</span>
              <span className="brand__tag">Organiser site</span>
            </span>
          </Link>
          <button
            type="button"
            className="sidebar__menu-button"
            aria-expanded={menuOpen}
            aria-controls="site-menu"
            onClick={() => setMenuOpen((open) => !open)}
          >
            {menuOpen ? <CloseIcon /> : <MenuIcon />}
            {menuOpen ? "Close" : "Menu"}
            {/* On a phone the menu is shut, so the button itself has to say something is waiting. */}
            {!menuOpen && waiting > 0 && (
              <>
                <span className="menudot" aria-hidden="true" />
                <span className="visually-hidden">, alerts or refunds are waiting</span>
              </>
            )}
          </button>
        </div>

        <div className="sidebar__panel" id="site-menu" data-open={menuOpen}>
          <nav className="nav" aria-label="Main">
            {items.map((item) => {
              const count = item.count ? counts[item.count] : undefined;
              const badge = count ? badgeText(count.count, count.more) : null;
              return (
                <NavLink key={item.to} to={item.to} end={item.to === "/"} className="nav__link">
                  {item.icon}
                  {item.label}
                  {badge && count && (
                    <span className="badge nav__badge" data-testid={`nav-${item.count}-badge`}>
                      <span aria-hidden="true">{badge}</span>
                      <span className="visually-hidden">, {count.words}</span>
                    </span>
                  )}
                </NavLink>
              );
            })}
          </nav>

          <div className="sidebar__user">
            <div className="usercard">
              <span className="avatar" aria-hidden="true">
                {initials(user.name, user.email)}
              </span>
              <div style={{ minWidth: 0 }}>
                <div className="usercard__name" title={displayName}>
                  {displayName}
                </div>
                <div className="usercard__role">{roleLabel(user.role)}</div>
              </div>
            </div>
            <div className="sidebar__actions">
              <NavLink to="/account" className="sidebar__action">
                <UserIcon />
                Account
              </NavLink>
              <button type="button" className="sidebar__action" onClick={handleSignOut} disabled={signingOut}>
                <SignOutIcon />
                Sign out
              </button>
            </div>
          </div>
        </div>
      </header>

      <main className="main">
        <Outlet />
      </main>
    </div>
  );
}
