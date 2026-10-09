import { colors } from "@talanta/shared";
import { StyleSheet } from "react-native";

/** Styles every screen shares, so the palette is applied in one place. */
export const screen = StyleSheet.create({
  container: { flex: 1, padding: 24, gap: 12, backgroundColor: colors.champagne },
  title: { fontSize: 22, fontWeight: "600", color: colors.emeraldInk },
  body: { color: colors.emeraldInk },
});
