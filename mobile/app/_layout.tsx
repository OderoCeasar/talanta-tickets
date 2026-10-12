import { focusManager, QueryClientProvider } from "@tanstack/react-query";
import { useFonts } from "expo-font";
import { Stack } from "expo-router";
import * as SplashScreen from "expo-splash-screen";
import { StatusBar } from "expo-status-bar";
import { useEffect } from "react";
import { AppState } from "react-native";
import { GestureHandlerRootView } from "react-native-gesture-handler";
import { SafeAreaProvider } from "react-native-safe-area-context";

import { queryClient } from "../src/api/client";
import { fontAssets } from "../src/fontAssets";
import { SessionProvider } from "../src/session";
import { headerOptions, palette } from "../src/theme";

// The splash stays up until the fonts are ready, so no screen is ever drawn in the system
// font and then redrawn. (Module scope: it must run before the first render.)
void SplashScreen.preventAutoHideAsync().catch(() => undefined);

export default function RootLayout() {
  // The font files are bundled, so this reads from the app, not the network. In a
  // development or release build the expo-font plugin has already embedded them and this
  // resolves at once; in Expo Go it is what makes them available at all.
  const [fontsLoaded, fontError] = useFonts(fontAssets);
  // A font that cannot be read must not lock the fan out of their tickets: carry on in the
  // system font.
  const fontsReady = fontsLoaded || fontError !== null;
  useEffect(() => {
    if (fontError) console.warn("Fonts did not load; using the system font.", fontError);
    else if (fontsLoaded && __DEV__) console.log("[fonts] useFonts loaded:", Object.keys(fontAssets).join(", "));
    if (fontsReady) void SplashScreen.hideAsync().catch(() => undefined);
  }, [fontsLoaded, fontsReady, fontError]);

  // TanStack Query's "window focus" is the app being in the foreground: timers that poll
  // (payment status) pause in the background and stale data is refreshed on return.
  useEffect(() => {
    const sub = AppState.addEventListener("change", (state) => focusManager.setFocused(state === "active"));
    return () => sub.remove();
  }, []);

  if (!fontsReady) return null;

  return (
    <GestureHandlerRootView style={{ flex: 1, backgroundColor: palette.champagne50 }}>
      <QueryClientProvider client={queryClient}>
        <SafeAreaProvider>
          <SessionProvider>
            {/* Light icons: every header sits on Emerald Ink. */}
            <StatusBar style="light" />
            <Stack screenOptions={{ ...headerOptions, headerBackButtonDisplayMode: "minimal", contentStyle: { backgroundColor: palette.champagne50 } }}>
              <Stack.Screen name="index" options={{ headerShown: false }} />
              <Stack.Screen name="(tabs)" options={{ headerShown: false }} />
              <Stack.Screen name="sign-in" options={{ title: "Sign in", presentation: "modal" }} />
              <Stack.Screen name="gate" options={{ title: "Gate mode", headerBackVisible: false }} />
              <Stack.Screen name="event/[id]/index" options={{ title: "Event" }} />
              <Stack.Screen name="event/[id]/map" options={{ title: "Choose a stand" }} />
              <Stack.Screen name="event/[id]/stand/[standId]" options={{ title: "Choose a block" }} />
              <Stack.Screen name="event/[id]/block/[blockId]" options={{ title: "Choose seats" }} />
              <Stack.Screen name="event/[id]/best" options={{ title: "Best available" }} />
              <Stack.Screen name="checkout/[holdId]" options={{ title: "Checkout", gestureEnabled: false }} />
              <Stack.Screen name="booking/[reference]" options={{ title: "Booking confirmed", headerBackVisible: false, gestureEnabled: false }} />
              <Stack.Screen name="ticket/[id]" options={{ title: "Ticket" }} />
            </Stack>
          </SessionProvider>
        </SafeAreaProvider>
      </QueryClientProvider>
    </GestureHandlerRootView>
  );
}
