import { router, Stack, useLocalSearchParams } from "expo-router";
import { useMemo } from "react";
import { RefreshControl, StyleSheet, Text, View } from "react-native";
import Svg, { Polygon, Text as SvgText } from "react-native-svg";

import { useStandBlocks } from "../../../../src/api/queries";
import { boundsOf, labelPoint, pointsAttr, SOLD_OUT_FILL, SOLD_OUT_STROKE, textOn } from "../../../../src/features/seatmap/overview";
import { formatCount, formatKes } from "../../../../src/lib/money";
import { standTitle } from "../../../../src/lib/labels";
import { font, mix, palette, space, type } from "../../../../src/theme";
import { Card, Chip, EmptyState, ErrorState, FlowSteps, Icon, Loading, Screen } from "../../../../src/ui";

/** Level 2: one stand. Block outlines coloured by price tier, with availability and a price badge. */
export default function StandScreen() {
  const { id, standId } = useLocalSearchParams<{ id: string; standId: string }>();
  const eventId = Number(id);
  const stand = useStandBlocks(eventId, Number(standId));

  const view = useMemo(() => {
    const blocks = stand.data?.blocks ?? [];
    const box = boundsOf(blocks.map((b) => b.outline), 14);
    const categoryById = new Map((stand.data?.categories ?? []).map((c) => [c.id, c]));
    return { box, categoryById };
  }, [stand.data]);

  if (stand.isPending) return <Loading label="Loading the stand…" />;
  if (stand.isError) return <ErrorState error={stand.error} onRetry={() => void stand.refetch()} title="We could not load this stand" />;

  const { blocks, categories } = stand.data;
  const { box, categoryById } = view;
  const open = (blockId: number) => router.push({ pathname: "/event/[id]/block/[blockId]", params: { id, blockId: String(blockId) } });

  /** The block's colour is its cheapest category that still has seats: the colour is the price tier. */
  const fillOf = (b: (typeof blocks)[number]) => {
    if (b.seats_available === 0) return SOLD_OUT_FILL;
    const badge = b.badges.find((x) => x.seats_available > 0) ?? b.badges[0];
    const color = (badge && categoryById.get(badge.category_id)?.color_hex) ?? palette.ink400;
    // Nearly full blocks are paler, so a glance shows where there is room.
    return b.seats_available * 4 < b.seats_total ? mix(color, palette.white, 0.55) : color;
  };
  const fontSize = Math.max(box.width, box.height) / 38;
  // On the plan a block shows its number only ("205"): the stand letter is in the title and
  // the full code would not fit a narrow block. The list below gives the full name.
  const standCode = stand.data.stand.code;
  const shortCode = (code: string) => (code.startsWith(standCode) && code.length > standCode.length ? code.slice(standCode.length) : code);

  return (
    <Screen refreshControl={<RefreshControl refreshing={stand.isRefetching} onRefresh={() => void stand.refetch()} tintColor={palette.emeraldInk} colors={[palette.emeraldInk]} />}>
      <Stack.Screen options={{ title: standTitle(stand.data.stand.name, stand.data.stand.tier) }} />
      <FlowSteps step={1} />
      <View>
        <Text style={type.h2}>Pick a block</Text>
        <Text style={type.muted}>{standTitle(stand.data.stand.name, stand.data.stand.tier)}, entry by gate {stand.data.stand.gate_group}. Paler blocks are nearly full.</Text>
      </View>

      {blocks.length === 0 ? (
        <EmptyState icon="seat" title="No blocks in this stand" body="Go back and pick another stand." />
      ) : (
        <>
          <Card style={styles.mapCard}>
            <Svg style={{ width: "100%", aspectRatio: box.width / box.height, maxHeight: 420 }} viewBox={`${box.x} ${box.y} ${box.width} ${box.height}`} testID="stand-map">
              {blocks.map((b) => {
                const soldOut = b.seats_available === 0;
                return (
                  <Polygon
                    key={b.id}
                    points={pointsAttr(b.outline)}
                    fill={fillOf(b)}
                    stroke={soldOut ? SOLD_OUT_STROKE : palette.white}
                    strokeWidth={fontSize / 8}
                    strokeDasharray={soldOut ? `${fontSize / 2} ${fontSize / 3}` : undefined}
                    onPress={soldOut ? undefined : () => open(b.id)}
                    accessibilityLabel={`${b.label}${soldOut ? ", sold out" : ""}`}
                  />
                );
              })}
              {blocks.map((b) => {
                const [cx, cy] = labelPoint(b.outline);
                return (
                  <SvgText key={b.id} x={cx} y={cy + fontSize * 0.36} fontSize={fontSize} fontFamily={font.bodySemiBold} fill={b.seats_available === 0 ? palette.grey600 : textOn(fillOf(b))} textAnchor="middle" pointerEvents="none">
                    {shortCode(b.code)}
                  </SvgText>
                );
              })}
            </Svg>
            <View style={styles.legend}>
              {categories.map((c) => (
                <View key={c.id} style={styles.legendItem}>
                  <View style={[styles.dot, { backgroundColor: c.color_hex }]} />
                  <Text style={type.small}>
                    {c.name} · {formatKes(c.price_cents)}
                  </Text>
                </View>
              ))}
              <View style={styles.legendItem}>
                <View style={[styles.dot, { backgroundColor: SOLD_OUT_FILL, borderWidth: 1, borderColor: SOLD_OUT_STROKE }]} />
                <Text style={type.small}>Sold out</Text>
              </View>
            </View>
          </Card>

          <View style={styles.list}>
            {blocks.map((b) => {
              const soldOut = b.seats_available === 0;
              return (
                <Card key={b.id} onPress={soldOut ? undefined : () => open(b.id)} style={[styles.row, soldOut && styles.rowOff]} accessibilityLabel={`${b.label}, ${soldOut ? "sold out" : `${b.seats_available} seats left, from ${formatKes(b.price_from_cents ?? 0)}`}`}>
                  <View style={[styles.swatch, { backgroundColor: fillOf(b), borderColor: soldOut ? SOLD_OUT_STROKE : fillOf(b) }]} />
                  <View style={styles.fill}>
                    <Text style={type.h3}>{b.label}</Text>
                    <Text style={type.small}>{soldOut ? "No seats left" : `${formatCount(b.seats_available)} of ${formatCount(b.seats_total)} seats left`}</Text>
                  </View>
                  {soldOut ? <Chip label="Sold out" tone="muted" /> : <Chip label={`From ${formatKes(b.price_from_cents ?? 0)}`} />}
                  {soldOut ? null : <Icon name="chevron" size={18} color={palette.grey400} />}
                </Card>
              );
            })}
          </View>
        </>
      )}
    </Screen>
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1 },
  mapCard: { padding: space.sm, alignItems: "center" },
  legend: { flexDirection: "row", flexWrap: "wrap", gap: space.md, paddingHorizontal: space.sm, paddingBottom: space.xs, alignSelf: "stretch" },
  legendItem: { flexDirection: "row", alignItems: "center", gap: 6 },
  dot: { width: 12, height: 12, borderRadius: 3 },
  list: { gap: space.sm },
  row: { flexDirection: "row", alignItems: "center", gap: space.md, paddingVertical: space.md },
  rowOff: { backgroundColor: palette.grey100 },
  swatch: { width: 22, height: 22, borderRadius: 6, borderWidth: 1 },
});
