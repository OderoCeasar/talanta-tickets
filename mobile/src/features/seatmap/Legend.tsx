import { StyleSheet, Text, View } from "react-native";
import Svg, { Path } from "react-native-svg";

import { formatKes } from "../../lib/money";
import { palette, space, type } from "../../theme";
import { Button, Sheet } from "../../ui";
import { SEAT_COLORS } from "./layers";
import type { MapCategory } from "./model";

type Sample = "available" | "selected" | "sold" | "held" | "blocked" | "accessible" | "companion" | "restricted";

/** One seat drawn the way the canvas draws it, at legend size. */
function SeatSample({ kind, color = palette.ink400 }: { kind: Sample; color?: string }) {
  const square = "M5 2h14a3 3 0 0 1 3 3v14a3 3 0 0 1-3 3H5a3 3 0 0 1-3-3V5a3 3 0 0 1 3-3z";
  return (
    <Svg width={26} height={26} viewBox="0 0 24 24">
      {kind === "sold" ? <Path d="M12 5a7 7 0 1 0 0 14a7 7 0 1 0 0-14z" fill={SEAT_COLORS.sold} /> : null}
      {kind === "held" ? <Path d={square} fill={SEAT_COLORS.heldFill} stroke={SEAT_COLORS.heldStroke} strokeWidth={3} /> : null}
      {kind === "blocked" ? (<><Path d={square} fill={SEAT_COLORS.blockedFill} stroke={SEAT_COLORS.blockedStroke} strokeWidth={1.4} /><Path d="M4 20 20 4" stroke={SEAT_COLORS.blockedStroke} strokeWidth={2.2} strokeLinecap="round" /></>) : null}
      {kind === "selected" ? (<><Path d={square} fill={SEAT_COLORS.selected} /><Path d="M6 12.500l4 4 8-9" stroke={SEAT_COLORS.tick} strokeWidth={2.6} fill="none" strokeLinecap="round" strokeLinejoin="round" /></>) : null}
      {kind === "available" || kind === "accessible" || kind === "companion" || kind === "restricted" ? <Path d={square} fill={color} /> : null}
      {kind === "accessible" ? (<><Path d="M10.8 5.500a1.7 1.7 0 1 0 0 .01z" fill={SEAT_COLORS.glyph} /><Path d="M10.8 8.500v5h4.800l1.7 4.5" stroke={SEAT_COLORS.glyph} strokeWidth={1.8} fill="none" strokeLinecap="round" strokeLinejoin="round" /><Path d="M8 12a4.2 4.2 0 1 0 6.2 5" stroke={SEAT_COLORS.glyph} strokeWidth={1.8} fill="none" strokeLinecap="round" /></>) : null}
      {kind === "companion" ? <Path d="M12 9a3 3 0 1 0 0 6a3 3 0 1 0 0-6z" fill={SEAT_COLORS.glyph} /> : null}
      {kind === "restricted" ? (<><Path d="M12 6v8" stroke={SEAT_COLORS.glyph} strokeWidth={2.4} strokeLinecap="round" /><Path d="M12 17a1.3 1.3 0 1 0 0 2.600a1.3 1.3 0 1 0 0-2.600z" fill={SEAT_COLORS.glyph} /></>) : null}
    </Svg>
  );
}

function Line({ kind, color, title, body }: { kind: Sample; color?: string; title: string; body?: string }) {
  return (
    <View style={styles.line}>
      <SeatSample kind={kind} color={color} />
      <View style={styles.fill}>
        <Text style={type.label}>{title}</Text>
        {body ? <Text style={type.small}>{body}</Text> : null}
      </View>
    </View>
  );
}

export function Legend({ visible, onClose, categories }: { visible: boolean; onClose: () => void; categories: readonly MapCategory[] }) {
  const sample = categories[0]?.color_hex ?? palette.ink400;
  return (
    <Sheet visible={visible} onClose={onClose} title="What the seats mean">
      <Text style={type.overline}>Free seats: the colour is the price</Text>
      {categories.map((c) => (
        <Line key={c.id} kind="available" color={c.color_hex} title={`${c.name} · ${formatKes(c.price_cents)}`} />
      ))}
      <Text style={type.overline}>Other seats</Text>
      <Line kind="selected" title="Chosen by you" body="Dark square with a tick. Tap again to let it go." />
      <Line kind="held" title="Someone is paying for it" body="Hollow square with an amber outline. It may come back in a few minutes." />
      <Line kind="sold" title="Sold" body="Small grey dot." />
      <Line kind="blocked" title="Not for sale" body="Pale square with a line through it." />
      <Text style={type.overline}>Marks on a seat</Text>
      <Line kind="accessible" color={sample} title="Wheelchair space" />
      <Line kind="companion" color={sample} title="Companion seat" body="Next to a wheelchair space." />
      <Line kind="restricted" color={sample} title="Restricted view" body="Part of the pitch is hidden. We ask you to confirm before choosing it." />
      <Button title="Got it" variant="secondary" onPress={onClose} testID="close-legend" />
    </Sheet>
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1 },
  line: { flexDirection: "row", alignItems: "center", gap: space.md },
});
