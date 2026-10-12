import { router, useLocalSearchParams } from "expo-router";
import { useMemo } from "react";
import { RefreshControl, StyleSheet, Text, View } from "react-native";
import Svg, { Polygon, Rect, Text as SvgText } from "react-native-svg";

import { useEvent, useStadiumMap } from "../../../src/api/queries";
import { boundsOf, labelPoint, pointsAttr, priceTints, SOLD_OUT_STROKE, textOn } from "../../../src/features/seatmap/overview";
import { formatCount, formatKes } from "../../../src/lib/money";
import { standTitle } from "../../../src/lib/labels";
import { font, palette, space, type } from "../../../src/theme";
import { Card, Chip, EmptyState, ErrorState, Icon, Loading, Screen } from "../../../src/ui";

/** Level 1: the stadium. Eight or so stand polygons, tinted by cheapest available price. */
export default function StadiumScreen() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const eventId = Number(id);
  const map = useStadiumMap(eventId);
  const event = useEvent(eventId);

  const tints = useMemo(() => priceTints(map.data?.stands.map((s) => s.price_from_cents) ?? []), [map.data]);
  const pitch = useMemo(() => {
    // The contract carries no pitch outline; this is drawn only so the plan reads as a stadium.
    const box = boundsOf(map.data?.stands.map((s) => s.polygon) ?? []);
    const w = box.width * 0.22;
    const h = box.height * 0.36;
    return { x: box.x + (box.width - w) / 2, y: box.y + (box.height - h) / 2, w, h };
  }, [map.data]);

  if (map.isPending) return <Loading label="Loading the stadium…" />;
  if (map.isError) return <ErrorState error={map.error} onRetry={() => void map.refetch()} title="We could not load the stadium" />;

  const { venue, stands } = map.data;
  const open = (standId: number) => router.push({ pathname: "/event/[id]/stand/[standId]", params: { id, standId: String(standId) } });

  return (
    <Screen refreshControl={<RefreshControl refreshing={map.isRefetching} onRefresh={() => void map.refetch()} tintColor={palette.emeraldInk} colors={[palette.emeraldInk]} />}>
      <View>
        <Text style={type.h2}>{event.data?.title ?? venue.name}</Text>
        <Text style={type.muted}>Tap a stand to see its blocks. Darker stands cost more; grey stands are sold out.</Text>
      </View>

      {stands.length === 0 ? (
        <EmptyState icon="seat" title="No stands to show" body="The seating for this event has not been published yet." />
      ) : (
        <>
          <Card style={styles.mapCard}>
            <Svg style={{ width: "100%", aspectRatio: venue.plan_width / venue.plan_height }} viewBox={`0 0 ${venue.plan_width} ${venue.plan_height}`} testID="stadium-map">
              <Rect x={pitch.x} y={pitch.y} width={pitch.w} height={pitch.h} rx={12} fill={palette.ink100} stroke={palette.ink200} strokeWidth={6} />
              <SvgText x={pitch.x + pitch.w / 2} y={pitch.y + pitch.h / 2 + 22} fontSize={64} fontFamily={font.headingBold} fill={palette.ink400} textAnchor="middle">
                PITCH
              </SvgText>
              {stands.map((s, i) => {
                const soldOut = s.seats_available === 0;
                return (
                  <Polygon
                    key={s.id}
                    points={pointsAttr(s.polygon)}
                    fill={tints[i]}
                    stroke={soldOut ? SOLD_OUT_STROKE : palette.white}
                    strokeWidth={8}
                    strokeDasharray={soldOut ? "24 16" : undefined}
                    onPress={soldOut ? undefined : () => open(s.id)}
                    accessibilityLabel={`${standTitle(s.name, s.tier)}${soldOut ? ", sold out" : ""}`}
                  />
                );
              })}
              {stands.map((s, i) => {
                const [cx, cy] = labelPoint(s.polygon);
                return (
                  <SvgText key={s.id} x={cx} y={cy + 24} fontSize={68} fontFamily={font.headingBold} fill={s.seats_available === 0 ? palette.grey600 : textOn(tints[i]!)} textAnchor="middle" pointerEvents="none">
                    {s.code}
                  </SvgText>
                );
              })}
            </Svg>
          </Card>

          <View style={styles.list}>
            {stands.map((s, i) => {
              const soldOut = s.seats_available === 0;
              return (
                <Card key={s.id} onPress={soldOut ? undefined : () => open(s.id)} style={[styles.row, soldOut && styles.rowOff]} accessibilityLabel={`${standTitle(s.name, s.tier)}, ${soldOut ? "sold out" : `from ${formatKes(s.price_from_cents ?? 0)}`}`}>
                  <View style={[styles.swatch, { backgroundColor: tints[i], borderColor: soldOut ? SOLD_OUT_STROKE : tints[i] }]} />
                  <View style={styles.fill}>
                    <Text style={type.h3}>{standTitle(s.name, s.tier)}</Text>
                    <Text style={type.small}>
                      {soldOut ? "No seats left" : `${formatCount(s.seats_available)} of ${formatCount(s.seats_total)} seats left`} · Gate {s.gate_group}
                    </Text>
                  </View>
                  {soldOut ? <Chip label="Sold out" tone="muted" /> : <Text style={type.label}>From {formatKes(s.price_from_cents ?? 0)}</Text>}
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
  mapCard: { padding: space.sm },
  list: { gap: space.sm },
  row: { flexDirection: "row", alignItems: "center", gap: space.md, paddingVertical: space.md },
  rowOff: { backgroundColor: palette.grey100 },
  swatch: { width: 22, height: 22, borderRadius: 6, borderWidth: 1 },
});
