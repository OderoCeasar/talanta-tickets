import { colors } from "@talanta/shared";
import { Tabs } from "expo-router";

export default function TabsLayout() {
  return (
    <Tabs
      screenOptions={{
        headerStyle: { backgroundColor: colors.emeraldInk },
        headerTintColor: colors.champagne,
        tabBarStyle: { backgroundColor: colors.emeraldInk },
        tabBarActiveTintColor: colors.champagne,
        tabBarInactiveTintColor: `${colors.champagne}99`,
        sceneStyle: { backgroundColor: colors.champagne },
      }}
    >
      <Tabs.Screen name="events" options={{ title: "Events" }} />
      <Tabs.Screen name="tickets" options={{ title: "My tickets" }} />
      <Tabs.Screen name="profile" options={{ title: "Profile" }} />
    </Tabs>
  );
}
