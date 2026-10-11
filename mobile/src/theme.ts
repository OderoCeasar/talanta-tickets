import { colors } from "@talanta/shared";
import { StyleSheet } from "react-native";

import { palette } from "./palette";

export * from "./palette";

export const shadow = {
  card: {
    shadowColor: colors.emeraldInk,
    shadowOpacity: 0.12,
    shadowRadius: 12,
    shadowOffset: { width: 0, height: 4 },
    elevation: 2,
  },
} as const;

export const type = StyleSheet.create({
  h1: { fontSize: 26, lineHeight: 32, fontWeight: "700", color: palette.emeraldInk },
  h2: { fontSize: 20, lineHeight: 26, fontWeight: "700", color: palette.emeraldInk },
  h3: { fontSize: 17, lineHeight: 23, fontWeight: "700", color: palette.emeraldInk },
  body: { fontSize: 16, lineHeight: 23, color: palette.grey900 },
  muted: { fontSize: 15, lineHeight: 21, color: palette.grey600 },
  small: { fontSize: 13, lineHeight: 18, color: palette.grey600 },
  label: { fontSize: 14, lineHeight: 19, fontWeight: "600", color: palette.grey900 },
  overline: { fontSize: 12, lineHeight: 16, fontWeight: "700", letterSpacing: 1, color: palette.grey600, textTransform: "uppercase" },
  mono: { fontFamily: "monospace", fontSize: 15, color: palette.grey900 },
});

/** Header options shared by every stack and tab navigator. */
export const headerOptions = {
  headerStyle: { backgroundColor: palette.emeraldInk },
  headerTintColor: palette.champagne,
  headerTitleStyle: { fontWeight: "700" as const, color: palette.champagne },
  headerShadowVisible: false,
};
