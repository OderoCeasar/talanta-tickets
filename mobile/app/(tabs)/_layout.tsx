import { Redirect, Tabs } from "expo-router";
import type { ColorValue } from "react-native";

import { useSession } from "../../src/session";
import { alpha, headerOptions, palette, type } from "../../src/theme";
import { Icon, type IconName } from "../../src/ui";

const tabIcon = (name: IconName) =>
  function TabIcon({ color, size }: { color: ColorValue; size: number }) {
    // The tab bar only ever passes the two tint strings set below.
    return <Icon name={name} color={typeof color === "string" ? color : palette.champagne} size={size} />;
  };

export default function TabsLayout() {
  const { status, user } = useSession();
  if (status === "signedIn" && user?.role === "GATE_STAFF") return <Redirect href="/gate" />;

  return (
    <Tabs
      screenOptions={{
        ...headerOptions,
        tabBarStyle: { backgroundColor: palette.emeraldInk, borderTopColor: palette.inkDeep },
        tabBarActiveTintColor: palette.champagne,
        tabBarInactiveTintColor: alpha(palette.champagne, 0.6),
        tabBarLabelStyle: type.tab,
        tabBarAllowFontScaling: false,
        sceneStyle: { backgroundColor: palette.champagne50 },
      }}
    >
      <Tabs.Screen name="events" options={{ title: "Events", headerTitle: "Talanta Tickets", tabBarIcon: tabIcon("calendar") }} />
      <Tabs.Screen name="tickets" options={{ title: "My tickets", tabBarIcon: tabIcon("ticket") }} />
      <Tabs.Screen name="profile" options={{ title: "Profile", tabBarIcon: tabIcon("user") }} />
    </Tabs>
  );
}
