import { focusManager, QueryClientProvider } from "@tanstack/react-query";
import { Stack } from "expo-router";
import { StatusBar } from "expo-status-bar";
import { useEffect } from "react";
import { AppState } from "react-native";
import { GestureHandlerRootView } from "react-native-gesture-handler";
import { SafeAreaProvider } from "react-native-safe-area-context";

import { queryClient } from "../src/api/client";
import { SessionProvider } from "../src/session";
import { headerOptions, palette } from "../src/theme";

export default function RootLayout() {
  // TanStack Query's "window focus" is the app being in the foreground: timers that poll
  // (payment status) pause in the background and stale data is refreshed on return.
  useEffect(() => {
    const sub = AppState.addEventListener("change", (state) => focusManager.setFocused(state === "active"));
    return () => sub.remove();
  }, []);

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
