import { Redirect, Tabs } from "expo-router";
import { StyleSheet, type ColorValue } from "react-native";

import { isGateRole } from "../../src/features/gate/roles";
import { useSession } from "../../src/session";
import { headerOptions, palette, type } from "../../src/theme";
import { Icon, type IconName } from "../../src/ui";

const tabIcon = (name: IconName) =>
  function TabIcon({ color, size }: { color: ColorValue; size: number }) {
    // The tab bar only ever passes the two tint strings set below.
    return <Icon name={name} color={typeof color === "string" ? color : palette.emeraldInk} size={size} />;
  };

export default function TabsLayout() {
  const { status, user } = useSession();
  if (status === "signedIn" && isGateRole(user?.role)) return <Redirect href="/gate" />;

  return (
    <Tabs
      screenOptions={{
        ...headerOptions,
        tabBarStyle: { backgroundColor: palette.white, borderTopColor: palette.grey200, borderTopWidth: StyleSheet.hairlineWidth },
        tabBarActiveTintColor: palette.emeraldInk,
        tabBarInactiveTintColor: palette.grey600,
        tabBarLabelStyle: type.tab,
        tabBarAllowFontScaling: false,
        sceneStyle: { backgroundColor: palette.champagne50 },
      }}
    >
      {/* Home draws its own large title instead of a header bar. */}
      <Tabs.Screen name="events" options={{ title: "Home", headerShown: false, tabBarIcon: tabIcon("home") }} />
      <Tabs.Screen name="tickets" options={{ title: "My tickets", tabBarIcon: tabIcon("ticket") }} />
      <Tabs.Screen name="profile" options={{ title: "Profile", tabBarIcon: tabIcon("user") }} />
    </Tabs>
  );
}
