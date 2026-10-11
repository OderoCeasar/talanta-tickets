import { router } from "expo-router";
import { useCallback, useMemo, useState } from "react";
import { ActivityIndicator, FlatList, RefreshControl, StyleSheet, Text, View } from "react-native";

import type { Schemas } from "../../src/api/client";
import { useEvents } from "../../src/api/queries";
import { ResumeBanner } from "../../src/features/booking/ResumeBanner";
import { errorMessage } from "../../src/lib/errors";
import { formatKes } from "../../src/lib/money";
import { dateTile, daysUntil, formatTime } from "../../src/lib/time";
import { palette, radius, space, type } from "../../src/theme";
import { Banner, Button, Card, Chip, EmptyState, ErrorState, Icon, Loading } from "../../src/ui";

type Event = Schemas["Event"];

function EventCard({ event }: { event: Event }) {
  const tile = dateTile(event.kickoff_at);
  const soldOut = event.status === "SOLD_OUT" || (event.status === "ON_SALE" && event.seats_available === 0);
  const notYet = event.status === "PUBLISHED";
  const when = daysUntil(event.kickoff_at, Date.now());
  return (
    <Card
      onPress={() => router.push({ pathname: "/event/[id]", params: { id: String(event.id) } })}
      accessibilityLabel={`${event.title}, ${tile.weekday} ${tile.day} ${tile.month}, ${formatTime(event.kickoff_at)}`}
      style={styles.card}
    >
      <View style={styles.tile}>
        <Text style={styles.tileMonth} maxFontSizeMultiplier={1.3}>{tile.month}</Text>
        <Text style={styles.tileDay} maxFontSizeMultiplier={1.3}>{tile.day}</Text>
        <Text style={styles.tileWeekday} maxFontSizeMultiplier={1.3}>{tile.weekday}</Text>
      </View>
      <View style={styles.cardBody}>
        {event.competition ? <Text style={type.overline} numberOfLines={2}>{event.competition}</Text> : null}
        <Text style={type.h3}>{event.title}</Text>
        <Text style={type.muted}>
          {formatTime(event.kickoff_at)} · {event.venue_name}
          {when ? ` · ${when}` : ""}
        </Text>
        <View style={styles.cardFoot}>
          {soldOut ? (
            <Chip label="Sold out" tone="muted" />
          ) : notYet ? (
            <Chip label="On sale soon" tone="warning" />
          ) : event.price_from_cents !== null ? (
            <Text style={type.label}>From {formatKes(event.price_from_cents)}</Text>
          ) : (
            <Chip label="On sale" />
          )}
          <Icon name="chevron" size={18} color={palette.grey400} />
        </View>
      </View>
    </Card>
  );
}

export default function EventsScreen() {
  const events = useEvents();
  const [refreshing, setRefreshing] = useState(false);
  const items = useMemo(() => events.data?.pages.flatMap((p) => p.items) ?? [], [events.data]);

  const { refetch, fetchNextPage, hasNextPage, isFetchingNextPage } = events;
  const onRefresh = useCallback(async () => {
    setRefreshing(true);
    await refetch();
    setRefreshing(false);
  }, [refetch]);

  if (events.isPending) return <Loading label="Loading events…" />;
  if (events.isError && items.length === 0) return <ErrorState error={events.error} onRetry={() => void refetch()} title="We could not load the events" />;

  return (
    <FlatList
      testID="events-list"
      data={items}
      keyExtractor={(e) => String(e.id)}
      renderItem={({ item }) => <EventCard event={item} />}
      contentContainerStyle={styles.list}
      refreshControl={<RefreshControl refreshing={refreshing} onRefresh={() => void onRefresh()} tintColor={palette.emeraldInk} colors={[palette.emeraldInk]} />}
      onEndReachedThreshold={0.4}
      onEndReached={() => {
        if (hasNextPage && !isFetchingNextPage) void fetchNextPage();
      }}
      ListHeaderComponent={
        <View style={styles.header}>
          <ResumeBanner />
          {events.isError ? <Banner tone="error" action="Try again" onAction={() => void refetch()}>{errorMessage(events.error)}</Banner> : null}
          <Text style={type.h1}>Upcoming at Talanta</Text>
          <Text style={type.muted}>Pick a match, choose your seats and pay with M-Pesa.</Text>
        </View>
      }
      ListEmptyComponent={<EmptyState icon="calendar" title="No events yet" body="When a match or event goes on sale it will be listed here. Pull down to check again." />}
      ListFooterComponent={
        isFetchingNextPage ? (
          <ActivityIndicator style={styles.more} color={palette.emeraldInk} />
        ) : events.isFetchNextPageError ? (
          <Button title="Load more events" variant="secondary" onPress={() => void fetchNextPage()} />
        ) : !hasNextPage && items.length > 0 ? (
          <Text style={[type.small, styles.end]}>That is everything on sale for now.</Text>
        ) : null
      }
    />
  );
}

const styles = StyleSheet.create({
  list: { padding: space.lg, gap: space.md, flexGrow: 1 },
  header: { gap: space.sm, marginBottom: space.xs },
  card: { flexDirection: "row", gap: space.md, alignItems: "flex-start" },
  cardBody: { flex: 1, gap: 4 },
  cardFoot: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", marginTop: space.xs, gap: space.sm },
  tile: { minWidth: 58, borderRadius: radius.md, backgroundColor: palette.emeraldInk, alignItems: "center", paddingVertical: space.sm, paddingHorizontal: space.sm },
  tileMonth: { fontSize: 11, fontWeight: "700", letterSpacing: 1, color: palette.champagne },
  tileDay: { fontSize: 24, lineHeight: 28, fontWeight: "800", color: palette.white },
  tileWeekday: { fontSize: 11, fontWeight: "600", color: palette.champagne300 },
  more: { padding: space.lg },
  end: { textAlign: "center", padding: space.md },
});
