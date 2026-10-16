import { router, Stack, useLocalSearchParams } from "expo-router";
import { RefreshControl, StyleSheet, Text, View } from "react-native";

import { useCategories, useEvent } from "../../../src/api/queries";
import { ResumeBanner } from "../../../src/features/booking/ResumeBanner";
import { formatCount, formatKes } from "../../../src/lib/money";
import { formatDate, formatDateTime, formatTime } from "../../../src/lib/time";
import { font, palette, radius, space, type } from "../../../src/theme";
import { Banner, Button, Card, Chip, ErrorState, Icon, Loading, Screen, SectionTitle } from "../../../src/ui";

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
            <Button title="Choose your seats" icon="seat" onPress={() => router.push({ pathname: "/event/[id]/map", params: { id } })} testID="choose-seats" />
            <Button title="Or let us pick the best seats" icon="sparkle" variant="ghost" onPress={() => router.push({ pathname: "/event/[id]/best", params: { id } })} testID="best-available" style={styles.quiet} />
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
        <Text style={styles.heroWhen}>{formatDate(e.kickoff_at)} · {formatTime(e.kickoff_at)}</Text>
        <View style={styles.heroChips}>
          {soldOut ? <Chip label="Sold out" tone="muted" /> : e.status === "PUBLISHED" ? <Chip label="On sale soon" tone="warning" /> : e.status === "CANCELLED" ? <Chip label="Cancelled" tone="error" /> : e.status === "COMPLETED" ? <Chip label="Finished" tone="muted" /> : <Chip label="On sale" />}
        </View>
      </View>

      <Card style={styles.facts}>
        <Fact icon="clock" label="Kickoff" value={`${formatTime(e.kickoff_at)} EAT`} />
        <Fact icon="gate" label="Gates open" value={e.gates_open_at ? formatTime(e.gates_open_at) : "To be confirmed"} />
        <Fact icon="pin" label="Venue" value={e.venue_name} />
        <Fact icon="ticket" label="Limit" value={`${e.max_tickets_per_user} per person`} />
      </Card>

      {e.status === "PUBLISHED" ? (
        <Banner tone="warning" title="Not on sale yet">
          {e.sales_open_at ? `Ticket sales open on ${formatDateTime(e.sales_open_at)}.` : "Ticket sales have not opened yet."}
        </Banner>
      ) : null}
      {soldOut ? <Banner title="Sold out">Every seat for this event has been taken.</Banner> : null}
      {e.status === "CANCELLED" ? <Banner tone="error" title="This event was cancelled">If you bought tickets, you will be refunded.</Banner> : null}
      {onSale && e.sales_close_at ? <Text style={type.small}>Sales close {formatDateTime(e.sales_close_at)}.</Text> : null}

      <SectionTitle title="Prices" />
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

      {onSale ? (
        <>
          <SectionTitle title="How buying works" />
          <Card>
            <HowStep n={1} title="Choose your seats" body="Pick a stand, then tap the seats you want." />
            <HowStep n={2} title="Pay with M-Pesa" body="Your seats are held for 10 minutes while you pay." />
            <HowStep n={3} title="Show your ticket" body="It is saved on your phone and works without internet." last />
          </Card>
        </>
      ) : null}
    </Screen>
  );
}

function HowStep({ n, title, body, last }: { n: number; title: string; body: string; last?: boolean }) {
  return (
    <View style={styles.how}>
      <View style={styles.howRail}>
        <View style={styles.howDot}>
          <Text style={styles.howNumber}>{n}</Text>
        </View>
        {last ? null : <View style={styles.howLine} />}
      </View>
      <View style={[styles.fill, !last && styles.howBody]}>
        <Text style={type.label}>{title}</Text>
        <Text style={type.small}>{body}</Text>
      </View>
    </View>
  );
}

function Fact({ icon, label, value }: { icon: "gate" | "clock" | "pin" | "ticket"; label: string; value: string }) {
  return (
    <View style={styles.fact}>
      <View style={styles.factIcon}>
        <Icon name={icon} size={16} />
      </View>
      <View style={styles.fill}>
        <Text style={type.small}>{label}</Text>
        <Text style={type.label}>{value}</Text>
      </View>
    </View>
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1 },
  hero: { backgroundColor: palette.emeraldInk, borderRadius: radius.lg, padding: space.xl, gap: space.sm, alignItems: "center" },
  heroOver: { ...type.overline, color: palette.champagne, textAlign: "center" },
  teams: { alignItems: "center", gap: 2 },
  team: { ...type.display, textAlign: "center" },
  vs: { fontFamily: font.bodySemiBold, fontSize: 14, color: palette.champagne, letterSpacing: 2, textTransform: "uppercase" },
  heroWhen: { ...type.body, color: palette.champagne, textAlign: "center", marginTop: space.xs },
  heroChips: { marginTop: space.xs },
  facts: { flexDirection: "row", flexWrap: "wrap", rowGap: space.md, gap: 0 },
  fact: { width: "50%", flexDirection: "row", gap: space.sm, alignItems: "center", paddingRight: space.sm },
  factIcon: { width: 32, height: 32, borderRadius: 16, backgroundColor: palette.champagne, alignItems: "center", justifyContent: "center" },
  quiet: { minHeight: 44, paddingVertical: space.xs },
  how: { flexDirection: "row", gap: space.md },
  howRail: { alignItems: "center", width: 26 },
  howDot: { width: 26, height: 26, borderRadius: 13, backgroundColor: palette.emeraldInk, alignItems: "center", justifyContent: "center" },
  howNumber: { fontFamily: font.bodySemiBold, fontSize: 13, color: palette.champagne },
  howLine: { flex: 1, width: 2, backgroundColor: palette.ink200, marginVertical: 2 },
  howBody: { paddingBottom: space.md },
  categories: { gap: 0, paddingVertical: space.xs },
  category: { flexDirection: "row", alignItems: "center", gap: space.md, paddingVertical: space.md },
  categoryBorder: { borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: palette.grey300 },
  swatch: { width: 18, height: 18, borderRadius: 5 },
  struck: { color: palette.grey400, textDecorationLine: "line-through" },
});
