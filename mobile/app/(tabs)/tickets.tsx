import { router } from "expo-router";
import { useCallback, useMemo, useState } from "react";
import { RefreshControl, StyleSheet, Text, View } from "react-native";

import { useOfflineTickets, useTickets } from "../../src/api/queries";
import { ResumeBanner } from "../../src/features/booking/ResumeBanner";
import { groupTickets, type TicketGroup } from "../../src/features/tickets/groups";
import { TicketRow } from "../../src/features/tickets/TicketRow";
import { isSignedOutError } from "../../src/lib/errors";
import { dateTile, formatDateTime, formatTime } from "../../src/lib/time";
import { useSession } from "../../src/session";
import { font, palette, radius, space, type } from "../../src/theme";
import { Banner, EmptyState, ErrorState, Loading, Screen, SectionTitle } from "../../src/ui";

function EventGroup({ group, past }: { group: TicketGroup; past?: boolean }) {
  const e = group.event;
  const tile = dateTile(e.kickoff_at);
  const n = group.tickets.length;
  return (
    <View style={[styles.group, past && styles.past]}>
      <View style={styles.groupHead}>
        <View style={[styles.tile, past && styles.tilePast]}>
          <Text style={styles.tileMonth} maxFontSizeMultiplier={1.3}>{tile.month}</Text>
          <Text style={styles.tileDay} maxFontSizeMultiplier={1.3}>{tile.day}</Text>
        </View>
        <View style={styles.fill}>
          <Text style={type.h3}>{e.title}</Text>
          <Text style={type.small}>
            {formatTime(e.kickoff_at)} · {e.venue_name} · {n} ticket{n === 1 ? "" : "s"}
          </Text>
          {e.status === "CANCELLED" ? <Text style={[type.label, { color: palette.error }]}>This event was cancelled.</Text> : null}
        </View>
      </View>
      {group.tickets.map((t) => (
        <TicketRow key={t.id} ticket={t} />
      ))}
    </View>
  );
}

export default function TicketsScreen() {
  const { status, user } = useSession();
  const userId = status === "signedIn" ? (user?.id ?? null) : null;
  const live = useTickets(userId);
  const offline = useOfflineTickets(userId);
  const [refreshing, setRefreshing] = useState(false);

  const { refetch } = live;
  const onRefresh = useCallback(async () => {
    setRefreshing(true);
    await refetch();
    setRefreshing(false);
  }, [refetch]);

  // The server's answer when there is one; otherwise the copy saved on the phone.
  const usingOffline = !live.data && !!offline.data;
  const tickets = live.data ?? offline.data?.items;
  const { upcoming, past } = useMemo(() => groupTickets(tickets ?? [], Date.now()), [tickets]);

  if (status === "loading") return <Loading />;
  if (status === "signedOut") {
    return (
      <Screen scroll={false}>
        <EmptyState icon="ticket" title="Your tickets live here" body="Sign in with your phone number to see the tickets you have bought." action="Sign in" onAction={() => router.push("/sign-in")} />
      </Screen>
    );
  }
  if (!tickets) {
    if (live.isError && !isSignedOutError(live.error)) return <ErrorState error={live.error} onRetry={() => void refetch()} title="We could not load your tickets" />;
    return <Loading label="Loading your tickets…" />;
  }

  return (
    <Screen refreshControl={<RefreshControl refreshing={refreshing} onRefresh={() => void onRefresh()} tintColor={palette.emeraldInk} colors={[palette.emeraldInk]} />}>
      <ResumeBanner />
      {usingOffline && offline.data ? (
        <Banner tone="warning" title="Offline copy" testID="offline-copy" action={live.isFetching ? undefined : "Try again"} onAction={() => void refetch()}>
          {`${live.isFetching ? "Checking for updates… " : "We could not reach the server. "}These tickets were saved on this phone on ${formatDateTime(offline.data.savedAt)} and still work at the gate.`}
        </Banner>
      ) : null}

      {upcoming.length === 0 && past.length === 0 ? (
        <EmptyState icon="ticket" title="No tickets yet" body="When you buy tickets they appear here, and stay on this phone so they open without internet." action="Find a match" onAction={() => router.navigate("/events")} />
      ) : (
        <>
          {upcoming.length > 0 ? (
            <Text style={styles.hint}>Tap a ticket to show its QR code at the gate. Each person needs their own.</Text>
          ) : (
            <Banner title="No upcoming matches">Your past tickets are below.</Banner>
          )}
          {upcoming.map((g) => (
            <EventGroup key={g.event.id} group={g} />
          ))}
          {past.length > 0 ? <SectionTitle title="Past events" /> : null}
          {past.map((g) => (
            <EventGroup key={g.event.id} group={g} past />
          ))}
        </>
      )}
    </Screen>
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1 },
  hint: { ...type.muted },
  group: { gap: space.sm },
  past: { opacity: 0.7 },
  groupHead: { flexDirection: "row", alignItems: "center", gap: space.md, marginTop: space.xs },
  tile: { width: 48, borderRadius: radius.md, backgroundColor: palette.emeraldInk, alignItems: "center", paddingVertical: 6 },
  tilePast: { backgroundColor: palette.grey600 },
  tileMonth: { fontFamily: font.bodySemiBold, fontSize: 10, letterSpacing: 1, color: palette.champagne },
  tileDay: { fontFamily: font.headingExtraBold, fontSize: 20, lineHeight: 24, color: palette.white },
});
