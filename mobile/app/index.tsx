import { Redirect } from "expo-router";

import { useSession } from "../src/session";
import { Loading, Screen } from "../src/ui";

export default function Index() {
  const { status, user } = useSession();
  if (status === "loading") {
    return (
      <Screen scroll={false}>
        <Loading label="Opening Talanta Tickets…" />
      </Screen>
    );
  }
  // Gate staff use the same app but never see the fan screens.
  if (status === "signedIn" && user?.role === "GATE_STAFF") return <Redirect href="/gate" />;
  return <Redirect href="/events" />;
}
