import type { ReactNode } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";
import { AppLayout } from "./components/AppLayout";
import { Button, Spinner, StateBlock } from "./components/ui";
import { useCurrentUser, useSession } from "./lib/session";
import { AcceptInvitePage } from "./pages/AcceptInvitePage";
import { AccountPage } from "./pages/AccountPage";
import { AlertsPage } from "./pages/AlertsPage";
import { LoginPage } from "./pages/LoginPage";
import { EventsPage } from "./pages/EventsPage";
import { EventCreatePage, EventDetailsTab, EventPage } from "./pages/event/EventPage";
import { PaintingTab } from "./pages/event/PaintingTab";
import { PricingTab } from "./pages/event/PricingTab";
import { PublishTab } from "./pages/event/PublishTab";
import { RefundsPage } from "./pages/RefundsPage";
import { SalesPage, SalesTab } from "./pages/SalesPage";
import { ForbiddenPage, HomePage, NotFoundPage } from "./pages/SimplePages";
import { VenuePage } from "./pages/VenuePage";
import { TeamPage } from "./pages/TeamPage";

/** Everything inside needs a signed-in organiser or admin. Anyone else is sent to /login. */
function RequireSession() {
  const { state, retryRestore } = useSession();
  const location = useLocation();

  if (state.status === "restoring") {
    return (
      <div className="fullscreen">
        <Spinner label="Signing you back in" />
      </div>
    );
  }
  if (state.status === "unreachable") {
    return (
      <div className="fullscreen">
        <StateBlock
          title="We could not reach the server"
          action={
            <Button variant="secondary" onClick={retryRestore}>
              Try again
            </Button>
          }
        >
          <p>You are still signed in on this computer. Check your internet connection and try again.</p>
        </StateBlock>
      </div>
    );
  }
  if (state.status === "signedOut") {
    // Signed out by the server: remember where they were so signing in takes them back.
    // Signed out by choice: remember nothing, so the next person to use this browser
    // starts at Home and not on the last person's page.
    const from = state.byChoice ? undefined : { from: location.pathname + location.search };
    return <Navigate to="/login" replace state={from} />;
  }
  return <AppLayout />;
}

/** The server enforces this too (403); this keeps organisers from seeing a page that cannot load. */
function AdminOnly({ children }: { children: ReactNode }) {
  const user = useCurrentUser();
  return user.role === "ADMIN" ? <>{children}</> : <ForbiddenPage />;
}

export function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      {/* The API builds invitation links as <ADMIN_BASE_URL>/invite/<token>, so this path is fixed. */}
      <Route path="/invite/:token" element={<AcceptInvitePage />} />

      <Route element={<RequireSession />}>
        <Route index element={<HomePage />} />
        <Route path="events" element={<EventsPage />} />
        <Route path="events/new" element={<EventCreatePage />} />
        <Route path="events/:eventId" element={<EventPage />}>
          <Route index element={<EventDetailsTab />} />
          <Route path="pricing" element={<PricingTab />} />
          <Route path="painting" element={<PaintingTab />} />
          <Route path="publish" element={<PublishTab />} />
          <Route path="sales" element={<SalesTab />} />
        </Route>
        <Route path="layout" element={<VenuePage />} />
        <Route path="sales" element={<SalesPage />} />
        <Route path="alerts" element={<AlertsPage />} />
        <Route path="refunds" element={<RefundsPage />} />
        <Route
          path="team"
          element={
            <AdminOnly>
              <TeamPage />
            </AdminOnly>
          }
        />
        <Route path="account" element={<AccountPage />} />
        <Route path="*" element={<NotFoundPage />} />
      </Route>
    </Routes>
  );
}
