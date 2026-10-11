import { router } from "expo-router";
import { useCallback, useMemo, useState } from "react";
import { RefreshControl, StyleSheet, Text, View } from "react-native";

import type { Schemas } from "../../src/api/client";
import { useOfflineTickets, useTickets } from "../../src/api/queries";
import { ResumeBanner } from "../../src/features/booking/ResumeBanner";
import { TicketRow } from "../../src/features/tickets/TicketRow";
import { isSignedOutError } from "../../src/lib/errors";
import { formatDateTime } from "../../src/lib/time";
import { useSession } from "../../src/session";
import { palette, space, type } from "../../src/theme";
import { Banner, EmptyState, ErrorState, Loading, Screen } from "../../src/ui";

type Ticket = Schemas["Ticket"];

function groupByEvent(tickets: readonly Ticket[]): { event: Ticket["event"]; tickets: Ticket[] }[] {
  const groups = new Map<number, { event: Ticket["event"]; tickets: Ticket[] }>();
  for (const t of tickets) {
    let g = groups.get(t.event.id);
    if (!g) groups.set(t.event.id, (g = { event: t.event, tickets: [] }));
    g.tickets.push(t);
  }
  return [...groups.values()];
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
  const groups = useMemo(() => groupByEvent(tickets ?? []), [tickets]);

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

      {groups.length === 0 ? (
        <EmptyState icon="ticket" title="No tickets yet" body="When you buy tickets they appear here, and stay on this phone so they open without internet." action="Browse events" onAction={() => router.navigate("/events")} />
      ) : (
        groups.map((g) => (
          <View key={g.event.id} style={styles.group}>
            <View>
              {g.event.competition ? <Text style={type.overline}>{g.event.competition}</Text> : null}
              <Text style={type.h2}>{g.event.title}</Text>
              <Text style={type.muted}>
                {formatDateTime(g.event.kickoff_at)} · {g.event.venue_name}
              </Text>
              {g.event.status === "CANCELLED" ? <Text style={[type.label, { color: palette.error }]}>This event was cancelled.</Text> : null}
            </View>
            {g.tickets.map((t) => (
              <TicketRow key={t.id} ticket={t} />
            ))}
          </View>
        ))
      )}
    </Screen>
  );
}

const styles = StyleSheet.create({
  group: { gap: space.sm },
});
