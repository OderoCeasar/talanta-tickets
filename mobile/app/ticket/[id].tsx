import { unwrap } from "@talanta/shared";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import * as Brightness from "expo-brightness";
import { Stack, useFocusEffect, useLocalSearchParams } from "expo-router";
import { useCallback, useEffect, useMemo } from "react";
import { Platform, StyleSheet, Text, useWindowDimensions, View } from "react-native";
import Animated, { Easing, useAnimatedStyle, useSharedValue, withRepeat, withSequence, withTiming } from "react-native-reanimated";

import { api, type Schemas } from "../../src/api/client";
import { useOfflineTickets, useTickets } from "../../src/api/queries";
import { QrCode } from "../../src/features/tickets/QrCode";
import { formatPhone } from "../../src/lib/phone";
import { formatClock, formatDate, formatTime } from "../../src/lib/time";
import { useNow } from "../../src/lib/useNow";
import { useSession } from "../../src/session";
import { standTitle } from "../../src/lib/labels";
import { font, palette, radius, space, type } from "../../src/theme";
import { Banner, ErrorState, Icon, Loading, Screen } from "../../src/ui";

type Ticket = Schemas["Ticket"];

/** Full brightness while the ticket is in view, so the QR scans; the old level comes back on leaving. */
function useFullBrightness() {
  useFocusEffect(
    useCallback(() => {
      let previous: number | null = null;
      let left = false;
      (async () => {
        try {
          previous = await Brightness.getBrightnessAsync();
          if (!left) await Brightness.setBrightnessAsync(1);
        } catch {
          // No brightness control on this device: the ticket still shows.
        }
      })();
      return () => {
        left = true;
        // Android: hand brightness back to the system setting (auto-brightness included).
        // iOS has no such call, so the level read on arrival is put back.
        const restore = Platform.OS === "android" ? Brightness.restoreSystemBrightnessAsync() : previous !== null ? Brightness.setBrightnessAsync(previous) : Promise.resolve();
        restore.catch(() => undefined);
      };
    }, []),
  );
}

/** A ticking clock and a moving stripe: a screenshot of this ticket shows a stopped clock. */
function LiveStrip() {
  const now = useNow(1000);
  const sweep = useSharedValue(0);
  const pulse = useSharedValue(1);
  useEffect(() => {
    sweep.value = withRepeat(withTiming(1, { duration: 2600, easing: Easing.inOut(Easing.quad) }), -1, true);
    pulse.value = withRepeat(withSequence(withTiming(0.25, { duration: 700 }), withTiming(1, { duration: 700 })), -1);
  }, [sweep, pulse]);
  const { width } = useWindowDimensions();
  const sweepStyle = useAnimatedStyle(() => ({ transform: [{ translateX: sweep.value * (width - 2 * space.lg - 90) }] }));
  const pulseStyle = useAnimatedStyle(() => ({ opacity: pulse.value }));

  return (
    <View style={styles.live} accessibilityLabel={`Live ticket. Time now ${formatClock(now)}`}>
      <Animated.View style={[styles.sweep, sweepStyle]} />
      <Animated.View style={[styles.liveDot, pulseStyle]} />
      <Text style={styles.liveText} maxFontSizeMultiplier={1.3}>LIVE</Text>
      <Text style={styles.liveClock} testID="live-clock" maxFontSizeMultiplier={1.3}>{formatClock(now)}</Text>
      <Text style={styles.liveText} maxFontSizeMultiplier={1.3}>{formatDate(now)}</Text>
    </View>
  );
}

export default function TicketScreen() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const { status, user } = useSession();
  const userId = status === "signedIn" ? (user?.id ?? null) : null;
  const qc = useQueryClient();
  const live = useTickets(userId);
  const offline = useOfflineTickets(userId);
  const { width } = useWindowDimensions();
  useFullBrightness();

  const fromLive = live.data?.find((t) => t.id === id);
  const fromOffline = offline.data?.items.find((t) => t.id === id);
  // Straight after paying, the ticket is already in the booking answer.
  const fromBooking = useMemo(() => {
    if (fromLive) return undefined;
    for (const [, b] of qc.getQueriesData<Schemas["Booking"]>({ queryKey: ["bookings"] })) {
      const t = b?.tickets.find((x) => x.id === id);
      if (t) return t;
    }
    return undefined;
  }, [qc, id, fromLive]);

  const known = fromLive ?? fromBooking ?? fromOffline;
  const single = useQuery({
    queryKey: ["tickets", "one", id],
    enabled: !known && userId !== null && !live.isPending && !offline.isPending,
    queryFn: () => unwrap(api.GET("/tickets/{ticketId}", { params: { path: { ticketId: id } } })),
  });
  const ticket: Ticket | undefined = known ?? single.data;
  const isOfflineCopy = !fromLive && !fromBooking && !!fromOffline;

  if (!ticket) {
    if (single.isError) return <ErrorState error={single.error} onRetry={() => void single.refetch()} title="We could not load this ticket" />;
    return <Loading label="Loading your ticket…" />;
  }

  const l = ticket.location;
  const e = ticket.event;
  const qrSize = Math.min(width - 2 * space.lg - 2 * space.xl, 300);
  const valid = ticket.status === "VALID";

  return (
    <Screen>
      <Stack.Screen options={{ title: `Seat ${l.seat_label}` }} />
      {isOfflineCopy ? (
        <Banner tone="warning" title="Offline copy" testID="offline-copy">
          {offline.data ? `Saved on this phone on ${formatDate(offline.data.savedAt)} at ${formatTime(offline.data.savedAt)}. It still works at the gate.` : "Saved on this phone."}
        </Banner>
      ) : null}
      {!valid ? (
        <Banner tone="error" title={ticket.status === "USED" ? "This ticket has been used" : ticket.status === "VOID" ? "This ticket was cancelled" : "This ticket was transferred"}>
          {ticket.status === "USED" && ticket.used_at ? `It was scanned at the gate on ${formatDate(ticket.used_at)} at ${formatTime(ticket.used_at)}.` : "It will not open the gate."}
        </Banner>
      ) : null}

      <View style={styles.ticket}>
        <View style={styles.head}>
          {e.competition ? <Text style={styles.headOver}>{e.competition}</Text> : null}
          <Text style={styles.headTitle}>{e.title}</Text>
          <Text style={styles.headMeta}>
            {formatDate(e.kickoff_at)} · {formatTime(e.kickoff_at)}
          </Text>
          <Text style={styles.headMeta}>
            {e.venue_name}
            {e.gates_open_at ? ` · gates open ${formatTime(e.gates_open_at)}` : ""}
          </Text>
        </View>

        <LiveStrip />

        <View style={styles.qrWrap}>
          <View style={[styles.qr, !valid && styles.qrOff]}>
            <QrCode value={ticket.qr_token} size={qrSize} label="Ticket QR code. Show this at the gate." />
          </View>
          <Text style={[type.small, styles.center]}>Show this code at the gate. One person per ticket.</Text>
        </View>

        <View style={styles.gate}>
          <Icon name="gate" color={palette.champagne} />
          <View style={styles.fill}>
            <Text style={styles.gateOver}>ENTER BY GATE</Text>
            <Text style={styles.gateValue} testID="ticket-gate">{l.gate_group}</Text>
          </View>
        </View>

        <View style={styles.grid}>
          <Cell label="Stand" value={standTitle(l.stand_name, l.tier)} wide />
          <Cell label="Block" value={l.block_code} />
          <Cell label="Row" value={l.row_label} />
          <Cell label="Seat" value={String(l.seat_number)} testID="ticket-seat" />
          <Cell label="Category" value={ticket.category_name} wide />
          {l.seat_type !== "STANDARD" ? <Cell label="Seat type" value={l.seat_type === "ACCESSIBLE" ? "Wheelchair space" : l.seat_type === "COMPANION" ? "Companion seat" : "Restricted view"} wide /> : null}
          <Cell label="Name" value={ticket.holder_name ?? "Not given"} wide />
          {ticket.holder_phone ? <Cell label="Phone" value={formatPhone(ticket.holder_phone)} wide /> : null}
          <Cell label="Booking reference" value={ticket.booking_reference} wide testID="ticket-reference" />
        </View>
      </View>
    </Screen>
  );
}

function Cell({ label, value, wide, testID }: { label: string; value: string; wide?: boolean; testID?: string }) {
  return (
    <View style={[styles.cell, wide && styles.cellWide]}>
      <Text style={type.overline}>{label}</Text>
      <Text style={styles.cellValue} testID={testID}>{value}</Text>
    </View>
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1 },
  center: { textAlign: "center" },
  ticket: { backgroundColor: palette.white, borderRadius: radius.lg, overflow: "hidden", borderWidth: StyleSheet.hairlineWidth, borderColor: palette.grey300 },
  head: { backgroundColor: palette.emeraldInk, padding: space.lg, gap: 2 },
  headOver: { ...type.overline, color: palette.champagne },
  headTitle: { ...type.display, fontSize: 22, lineHeight: 28 },
  headMeta: { ...type.muted, color: palette.champagne300 },
  live: { flexDirection: "row", alignItems: "center", gap: space.sm, backgroundColor: palette.champagne, paddingHorizontal: space.lg, paddingVertical: space.sm, overflow: "hidden", flexWrap: "wrap" },
  sweep: { position: "absolute", left: 0, top: 0, bottom: 0, width: 90, backgroundColor: palette.champagne300, opacity: 0.9, transform: [{ skewX: "-20deg" }] },
  liveDot: { width: 10, height: 10, borderRadius: 5, backgroundColor: palette.emeraldInk },
  liveText: { fontFamily: font.bodySemiBold, fontSize: 12, letterSpacing: 1, color: palette.emeraldInk },
  liveClock: { ...type.clock, fontSize: 20, lineHeight: 26, marginLeft: "auto" },
  qrWrap: { alignItems: "center", padding: space.xl, gap: space.sm },
  qr: { padding: space.sm, backgroundColor: palette.white },
  qrOff: { opacity: 0.25 },
  gate: { flexDirection: "row", alignItems: "center", gap: space.md, backgroundColor: palette.emeraldInk, marginHorizontal: space.lg, borderRadius: radius.md, padding: space.md },
  gateOver: { fontFamily: font.bodySemiBold, fontSize: 11, letterSpacing: 1, color: palette.champagne300 },
  gateValue: { ...type.price, fontSize: 22, lineHeight: 28, color: palette.white },
  grid: { flexDirection: "row", flexWrap: "wrap", padding: space.lg, rowGap: space.md },
  cell: { width: "33.33%", gap: 2, paddingRight: space.sm },
  cellWide: { width: "100%" },
  cellValue: { fontFamily: font.headingBold, fontSize: 18, lineHeight: 24, color: palette.grey900 },
});
