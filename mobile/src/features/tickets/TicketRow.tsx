import { router } from "expo-router";
import { StyleSheet, Text, View } from "react-native";

import type { Schemas } from "../../api/client";
import { palette, radius, space, type } from "../../theme";
import { Card, Chip, Icon } from "../../ui";

const STATUS: Record<Schemas["TicketStatus"], { label: string; tone: "neutral" | "muted" | "error" } | null> = {
  VALID: null,
  USED: { label: "Used", tone: "muted" },
  VOID: { label: "Cancelled", tone: "error" },
  TRANSFERRED: { label: "Transferred", tone: "muted" },
};

export function TicketRow({ ticket }: { ticket: Schemas["Ticket"] }) {
  const l = ticket.location;
  const status = STATUS[ticket.status];
  return (
    <Card
      onPress={() => router.push({ pathname: "/ticket/[id]", params: { id: ticket.id } })}
      accessibilityLabel={`Ticket, ${l.block_label}, row ${l.row_label}, seat ${l.seat_number}`}
      style={styles.row}
    >
      <View style={styles.seat}>
        <Text style={styles.seatOver} maxFontSizeMultiplier={1.3}>SEAT</Text>
        <Text style={styles.seatLabel} maxFontSizeMultiplier={1.3}>{l.seat_label}</Text>
      </View>
      <View style={styles.fill}>
        <Text style={type.h3}>{l.block_label}</Text>
        <Text style={type.muted}>
          {l.stand_name} · Row {l.row_label} · {ticket.category_name}
        </Text>
        <Text style={type.small}>Gate {l.gate_group}</Text>
        {status ? <Chip label={status.label} tone={status.tone} /> : null}
      </View>
      <Icon name="chevron" size={18} color={palette.grey400} />
    </Card>
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1, gap: 2 },
  row: { flexDirection: "row", alignItems: "center", gap: space.md },
  seat: { minWidth: 64, alignItems: "center", backgroundColor: palette.champagne, borderRadius: radius.md, paddingVertical: space.sm, paddingHorizontal: space.sm },
  seatOver: { fontSize: 10, fontWeight: "700", letterSpacing: 1, color: palette.grey700 },
  seatLabel: { fontSize: 20, fontWeight: "800", color: palette.emeraldInk },
});
