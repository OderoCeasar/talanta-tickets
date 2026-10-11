import { router, Stack, useLocalSearchParams } from "expo-router";
import { RefreshControl, StyleSheet, Text, View } from "react-native";

import { useCategories, useEvent } from "../../../src/api/queries";
import { ResumeBanner } from "../../../src/features/booking/ResumeBanner";
import { formatCount, formatKes } from "../../../src/lib/money";
import { formatDate, formatDateTime, formatTime } from "../../../src/lib/time";
import { palette, radius, space, type } from "../../../src/theme";
import { Banner, Button, Card, Chip, ErrorState, Icon, Loading, Screen } from "../../../src/ui";

export default function EventScreen() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const eventId = Number(id);
  const event = useEvent(eventId);
  const categories = useCategories(eventId);

  if (event.isPending) return <Loading label="Loading event…" />;
  if (event.isError) return <ErrorState error={event.error} onRetry={() => void event.refetch()} title="We could not load this event" />;

  const e = event.data;
  const onSale = e.status === "ON_SALE" && e.seats_available > 0;
  const soldOut = e.status === "SOLD_OUT" || (e.status === "ON_SALE" && e.seats_available === 0);
  const refresh = () => {
    void event.refetch();
    void categories.refetch();
  };

  return (
    <Screen
      refreshControl={<RefreshControl refreshing={event.isRefetching && !event.isPending} onRefresh={refresh} tintColor={palette.emeraldInk} colors={[palette.emeraldInk]} />}
      footer={
        onSale ? (
          <>
            <Button title="Choose seats on the map" icon="seat" onPress={() => router.push({ pathname: "/event/[id]/map", params: { id } })} testID="choose-seats" />
            <Button title="Best available" icon="sparkle" variant="secondary" onPress={() => router.push({ pathname: "/event/[id]/best", params: { id } })} testID="best-available" />
          </>
        ) : undefined
      }
    >
      <Stack.Screen options={{ title: e.home_team && e.away_team ? "Match" : "Event" }} />
      <ResumeBanner />
      <View style={styles.hero}>
        {e.competition ? <Text style={styles.heroOver}>{e.competition}</Text> : null}
        {e.home_team && e.away_team ? (
          <View style={styles.teams}>
            <Text style={styles.team}>{e.home_team}</Text>
            <Text style={styles.vs}>vs</Text>
            <Text style={styles.team}>{e.away_team}</Text>
          </View>
        ) : (
          <Text style={styles.team}>{e.title}</Text>
        )}
        <View style={styles.heroChips}>
          {soldOut ? <Chip label="Sold out" tone="muted" /> : e.status === "PUBLISHED" ? <Chip label="On sale soon" tone="warning" /> : e.status === "CANCELLED" ? <Chip label="Cancelled" tone="error" /> : e.status === "COMPLETED" ? <Chip label="Finished" tone="muted" /> : <Chip label="On sale" />}
        </View>
      </View>

      <Card>
        <Fact icon="calendar" label="Kickoff" value={`${formatDate(e.kickoff_at)}\n${formatTime(e.kickoff_at)} East Africa Time`} />
        {e.gates_open_at ? <Fact icon="clock" label="Gates open" value={formatTime(e.gates_open_at)} /> : null}
        <Fact icon="pin" label="Venue" value={e.venue_name} />
        <Fact icon="ticket" label="Tickets" value={`Up to ${e.max_tickets_per_user} per person`} />
      </Card>

      {e.status === "PUBLISHED" ? (
        <Banner tone="warning" title="Not on sale yet">
          {e.sales_open_at ? `Ticket sales open on ${formatDateTime(e.sales_open_at)}.` : "Ticket sales have not opened yet."}
        </Banner>
      ) : null}
      {soldOut ? <Banner title="Sold out">Every seat for this event has been taken.</Banner> : null}
      {e.status === "CANCELLED" ? <Banner tone="error" title="This event was cancelled">If you bought tickets, you will be refunded.</Banner> : null}
      {onSale && e.sales_close_at ? <Text style={type.small}>Sales close {formatDateTime(e.sales_close_at)}.</Text> : null}

      <Text style={type.h2}>Tickets and prices</Text>
      {categories.isPending ? (
        <Loading label="Loading prices…" />
      ) : categories.isError ? (
        <ErrorState error={categories.error} onRetry={() => void categories.refetch()} title="We could not load the prices" />
      ) : categories.data.items.length === 0 ? (
        <Banner>Prices have not been set yet.</Banner>
      ) : (
        <Card style={styles.categories}>
          {categories.data.items.map((c, i) => (
            <View key={c.id} style={[styles.category, i > 0 && styles.categoryBorder]}>
              <View style={[styles.swatch, { backgroundColor: c.color_hex }]} />
              <View style={styles.fill}>
                <Text style={type.h3}>{c.name}</Text>
                {c.description ? <Text style={type.muted}>{c.description}</Text> : null}
                <Text style={type.small}>{c.seats_available === 0 ? "Sold out" : `${formatCount(c.seats_available)} seats left`}</Text>
              </View>
              <Text style={[type.h3, c.seats_available === 0 && styles.struck]}>{formatKes(c.price_cents)}</Text>
            </View>
          ))}
        </Card>
      )}
    </Screen>
  );
}

function Fact({ icon, label, value }: { icon: "calendar" | "clock" | "pin" | "ticket"; label: string; value: string }) {
  return (
    <View style={styles.fact}>
      <View style={styles.factIcon}>
        <Icon name={icon} size={18} />
      </View>
      <View style={styles.fill}>
        <Text style={type.small}>{label}</Text>
        <Text style={type.body}>{value}</Text>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1 },
  hero: { backgroundColor: palette.emeraldInk, borderRadius: radius.lg, padding: space.xl, gap: space.sm, alignItems: "center" },
  heroOver: { ...type.overline, color: palette.champagne, textAlign: "center" },
  teams: { alignItems: "center", gap: 2 },
  team: { fontSize: 26, lineHeight: 32, fontWeight: "800", color: palette.white, textAlign: "center" },
  vs: { fontSize: 14, fontWeight: "700", color: palette.champagne, letterSpacing: 2, textTransform: "uppercase" },
  heroChips: { marginTop: space.xs },
  fact: { flexDirection: "row", gap: space.md, alignItems: "flex-start", paddingVertical: 4 },
  factIcon: { width: 34, height: 34, borderRadius: 17, backgroundColor: palette.champagne, alignItems: "center", justifyContent: "center" },
  categories: { gap: 0, paddingVertical: space.xs },
  category: { flexDirection: "row", alignItems: "center", gap: space.md, paddingVertical: space.md },
  categoryBorder: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: palette.grey300 },
  swatch: { width: 18, height: 18, borderRadius: 5 },
  struck: { color: palette.grey400, textDecorationLine: "line-through" },
});
