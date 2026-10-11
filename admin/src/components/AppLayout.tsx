import { useEffect, useState, type ReactNode } from "react";
import { Link, NavLink, Outlet, useLocation } from "react-router-dom";
import { initials, roleLabel } from "../lib/format";
import { useCurrentUser, useSession } from "../lib/session";
import {
  BrandMark,
  CalendarIcon,
  ChartIcon,
  CloseIcon,
  HomeIcon,
  MenuIcon,
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
}

const NAV: NavItem[] = [
  { to: "/", label: "Home", icon: <HomeIcon /> },
  { to: "/events", label: "Events", icon: <CalendarIcon /> },
  { to: "/layout", label: "Venue layout", icon: <StadiumIcon /> },
  { to: "/sales", label: "Sales", icon: <ChartIcon /> },
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
          </button>
        </div>

        <div className="sidebar__panel" id="site-menu" data-open={menuOpen}>
          <nav className="nav" aria-label="Main">
            {items.map((item) => (
              <NavLink key={item.to} to={item.to} end={item.to === "/"} className="nav__link">
                {item.icon}
                {item.label}
              </NavLink>
            ))}
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
