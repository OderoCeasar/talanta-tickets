import { Redirect, Stack } from "expo-router";

import { GateProvider } from "../../src/features/gate/GateProvider";
import { isGateRole } from "../../src/features/gate/roles";
import { useSession } from "../../src/session";
import { headerOptions, palette } from "../../src/theme";
import { Loading, Screen } from "../../src/ui";

/**
 * Everything under /gate. The role is checked here, once, for every gate screen: a fan (or
 * anyone signed out) is sent back to the events and never sees a scanner.
 */
export default function GateLayout() {
  const { status, user } = useSession();
  if (status === "signedOut") return <Redirect href="/events" />;
  // The saved user is read with the session, so this is brief and needs no network.
  if (status === "loading" || !user) {
    return (
      <Screen scroll={false}>
        <Loading label="Opening gate mode…" />
      </Screen>
    );
  }
  if (!isGateRole(user.role)) return <Redirect href="/events" />;

  return (
    <GateProvider>
      <Stack screenOptions={{ ...headerOptions, headerBackButtonDisplayMode: "minimal", contentStyle: { backgroundColor: palette.champagne50 } }}>
        <Stack.Screen name="index" options={{ title: "Gate mode", headerBackVisible: false }} />
        <Stack.Screen name="scan" options={{ title: "Scan tickets" }} />
        <Stack.Screen name="history" options={{ title: "Scans on this phone" }} />
      </Stack>
    </GateProvider>
  );
}
