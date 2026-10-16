import { router } from "expo-router";
import { useCallback, useMemo, useState } from "react";
import { ActivityIndicator, FlatList, RefreshControl, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import type { Schemas } from "../../src/api/client";
import { useEvents } from "../../src/api/queries";
import { ResumeBanner } from "../../src/features/booking/ResumeBanner";
import { errorMessage } from "../../src/lib/errors";
import { formatKes } from "../../src/lib/money";
import { dateTile, daysUntil, formatDate, formatTime } from "../../src/lib/time";
import { useSession } from "../../src/session";
import { alpha, font, palette, radius, space, type } from "../../src/theme";
import { Banner, Button, Card, Chip, EmptyState, ErrorState, Icon, Loading, SectionTitle } from "../../src/ui";

type Event = Schemas["Event"];

const openEvent = (event: Event) => router.push({ pathname: "/event/[id]", params: { id: String(event.id) } });
const isSoldOut = (e: Event) => e.status === "SOLD_OUT" || (e.status === "ON_SALE" && e.seats_available === 0);
const isBuyable = (e: Event) => e.status === "ON_SALE" && e.seats_available > 0;

/** The next match you can buy for, large, with one button. Most fans come for exactly this. */
function Featured({ event }: { event: Event }) {
  const when = daysUntil(event.kickoff_at, Date.now());
  const teams = event.home_team && event.away_team;
  return (
    <View style={styles.featured} testID="featured-event">
      <View style={styles.featuredTop}>
        <Text style={styles.featuredOver} numberOfLines={1}>{event.competition ?? "Next at Talanta"}</Text>
        {when ? <Text style={styles.featuredWhen}>{when}</Text> : null}
      </View>
      {teams ? (
        <View style={styles.teams}>
          <Text style={styles.team} numberOfLines={2}>{event.home_team}</Text>
          <Text style={styles.vs}>vs</Text>
          <Text style={styles.team} numberOfLines={2}>{event.away_team}</Text>
        </View>
      ) : (
        <Text style={styles.team}>{event.title}</Text>
      )}
      <View style={styles.featuredFacts}>
        <View style={styles.featuredFact}>
          <Icon name="calendar" size={16} color={palette.champagne} />
          <Text style={styles.featuredFactText}>{formatDate(event.kickoff_at)} · {formatTime(event.kickoff_at)}</Text>
        </View>
        <View style={styles.featuredFact}>
          <Icon name="pin" size={16} color={palette.champagne} />
          <Text style={styles.featuredFactText}>{event.venue_name}</Text>
        </View>
      </View>
      <Button
        title={event.price_from_cents !== null ? `Get tickets · from ${formatKes(event.price_from_cents)}` : "Get tickets"}
        variant="light"
        icon="ticket"
        onPress={() => openEvent(event)}
        testID="featured-get-tickets"
      />
    </View>
  );
}

function EventCard({ event }: { event: Event }) {
  const tile = dateTile(event.kickoff_at);
  const when = daysUntil(event.kickoff_at, Date.now());
  return (
    <Card
      onPress={() => openEvent(event)}
      accessibilityLabel={`${event.title}, ${tile.weekday} ${tile.day} ${tile.month}, ${formatTime(event.kickoff_at)}`}
      style={styles.card}
    >
      <View style={styles.tile}>
        <Text style={styles.tileMonth} maxFontSizeMultiplier={1.3}>{tile.month}</Text>
        <Text style={styles.tileDay} maxFontSizeMultiplier={1.3}>{tile.day}</Text>
        <Text style={styles.tileWeekday} maxFontSizeMultiplier={1.3}>{tile.weekday}</Text>
      </View>
      <View style={styles.cardBody}>
        {event.competition ? <Text style={type.overline} numberOfLines={1}>{event.competition}</Text> : null}
        <Text style={type.h3} numberOfLines={2}>{event.title}</Text>
        <Text style={type.small}>
          {formatTime(event.kickoff_at)}
          {when ? ` · ${when}` : ""}
        </Text>
        <View style={styles.cardFoot}>
          {isSoldOut(event) ? (
            <Chip label="Sold out" tone="muted" />
          ) : event.status === "PUBLISHED" ? (
            <Chip label="On sale soon" tone="warning" />
          ) : event.price_from_cents !== null ? (
            <Text style={styles.price}>From {formatKes(event.price_from_cents)}</Text>
          ) : (
            <Chip label="On sale" />
          )}
        </View>
      </View>
      <Icon name="chevron" size={18} color={palette.grey400} />
    </Card>
  );
}

export default function EventsScreen() {
  const insets = useSafeAreaInsets();
  const { status, user } = useSession();
  const events = useEvents();
  const [refreshing, setRefreshing] = useState(false);
  const items = useMemo(() => events.data?.pages.flatMap((p) => p.items) ?? [], [events.data]);
  const featured = useMemo(() => items.find(isBuyable) ?? null, [items]);
  const rest = useMemo(() => items.filter((e) => e !== featured), [items, featured]);

  const { refetch, fetchNextPage, hasNextPage, isFetchingNextPage } = events;
  const onRefresh = useCallback(async () => {
    setRefreshing(true);
    await refetch();
    setRefreshing(false);
  }, [refetch]);

  const firstName = status === "signedIn" ? user?.name?.trim().split(/\s+/)[0] : undefined;
  const top = (
    <View style={[styles.top, { paddingTop: insets.top + space.lg }]}>
      <Text style={type.overline}>{firstName ? `Hi ${firstName}` : "Talanta Stadium · Nairobi"}</Text>
      <Text style={type.hero} accessibilityRole="header">Find your next match</Text>
    </View>
  );

  if (events.isPending) {
    return (
      <View style={[styles.fill, styles.padded]}>
        {top}
        <Loading label="Loading events…" />
      </View>
    );
  }
  if (events.isError && items.length === 0) {
    return (
      <View style={[styles.fill, styles.padded]}>
        {top}
        <ErrorState error={events.error} onRetry={() => void refetch()} title="We could not load the events" />
      </View>
    );
  }

  return (
    <FlatList
      testID="events-list"
      style={styles.fill}
      data={rest}
      keyExtractor={(e) => String(e.id)}
      renderItem={({ item }) => <EventCard event={item} />}
      contentContainerStyle={[styles.list, { paddingBottom: space.xl }]}
      refreshControl={<RefreshControl refreshing={refreshing} onRefresh={() => void onRefresh()} tintColor={palette.emeraldInk} colors={[palette.emeraldInk]} progressViewOffset={insets.top} />}
      onEndReachedThreshold={0.4}
      onEndReached={() => {
        if (hasNextPage && !isFetchingNextPage) void fetchNextPage();
      }}
      ListHeaderComponent={
        <View style={styles.header}>
          {top}
          <ResumeBanner />
          {events.isError ? <Banner tone="error" action="Try again" onAction={() => void refetch()}>{errorMessage(events.error)}</Banner> : null}
          {featured ? <Featured event={featured} /> : null}
          {featured && rest.length > 0 ? <SectionTitle title="More at Talanta" /> : null}
        </View>
      }
      ListEmptyComponent={featured ? null : <EmptyState icon="calendar" title="No events yet" body="When a match or event goes on sale it will be listed here. Pull down to check again." />}
      ListFooterComponent={
        isFetchingNextPage ? (
          <ActivityIndicator style={styles.more} color={palette.emeraldInk} />
        ) : events.isFetchNextPageError ? (
          <Button title="Load more events" variant="secondary" onPress={() => void fetchNextPage()} />
        ) : !hasNextPage && items.length > 1 ? (
          <Text style={[type.small, styles.end]}>That is everything on sale for now.</Text>
        ) : null
      }
    />
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1, backgroundColor: palette.champagne50 },
  top: { gap: 2 },
  padded: { paddingHorizontal: space.lg },
  list: { paddingHorizontal: space.lg, gap: space.md, flexGrow: 1 },
  header: { gap: space.lg },
  featured: { backgroundColor: palette.emeraldInk, borderRadius: radius.lg, padding: space.xl, gap: space.lg },
  featuredTop: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", gap: space.sm },
  featuredOver: { ...type.overline, color: palette.champagne, flex: 1 },
  featuredWhen: { fontFamily: font.bodySemiBold, fontSize: 12, color: palette.emeraldInk, backgroundColor: palette.champagne, paddingHorizontal: 10, paddingVertical: 3, borderRadius: radius.pill, overflow: "hidden" },
  teams: { gap: 2 },
  team: { ...type.display, fontSize: 28, lineHeight: 34 },
  vs: { fontFamily: font.bodySemiBold, fontSize: 13, color: alpha(palette.champagne, 0.75), letterSpacing: 2, textTransform: "uppercase" },
  featuredFacts: { gap: 6 },
  featuredFact: { flexDirection: "row", alignItems: "center", gap: space.sm },
  featuredFactText: { ...type.body, fontSize: 15, color: palette.champagne, flex: 1 },
  card: { flexDirection: "row", gap: space.md, alignItems: "center" },
  cardBody: { flex: 1, gap: 2 },
  cardFoot: { flexDirection: "row", alignItems: "center", marginTop: space.xs },
  price: { fontFamily: font.bodySemiBold, fontSize: 14, color: palette.emeraldInk },
  tile: { width: 58, borderRadius: radius.md, backgroundColor: palette.champagne, alignItems: "center", paddingVertical: space.sm },
  tileMonth: { fontFamily: font.bodySemiBold, fontSize: 11, letterSpacing: 1, color: palette.champagneDeep },
  tileDay: { fontFamily: font.headingExtraBold, fontSize: 24, lineHeight: 28, color: palette.emeraldInk },
  tileWeekday: { fontFamily: font.bodySemiBold, fontSize: 11, color: palette.champagneDeep },
  more: { padding: space.lg },
  end: { textAlign: "center", padding: space.md },
});
