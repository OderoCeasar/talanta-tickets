import { colors } from "@talanta/shared";
import { StyleSheet } from "react-native";

import { font } from "./fonts";
import { palette } from "./palette";

export * from "./fonts";
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

/**
 * Every text style in the app. Inter for reading and controls, Plus Jakarta Sans for
 * headings and headline numbers. A style names a family (one per weight, see src/fonts.ts)
 * and never `fontWeight`. Sizes are in scaled points: the system font size still applies.
 */
export const type = StyleSheet.create({
  /** Hero titles on a dark panel: teams, "You are going!". */
  display: { fontFamily: font.headingExtraBold, fontSize: 26, lineHeight: 32, color: palette.white },
  h1: { fontFamily: font.headingBold, fontSize: 26, lineHeight: 32, color: palette.emeraldInk },
  h2: { fontFamily: font.headingBold, fontSize: 20, lineHeight: 26, color: palette.emeraldInk },
  /** Card and section titles. */
  h3: { fontFamily: font.heading, fontSize: 17, lineHeight: 23, color: palette.emeraldInk },
  /** A price or number shown as a headline: totals, seat labels, gate. */
  price: { fontFamily: font.headingExtraBold, fontSize: 20, lineHeight: 26, color: palette.emeraldInk },
  /** Countdowns and clocks: digits keep their width so the text does not jump each second. */
  clock: { fontFamily: font.headingExtraBold, fontSize: 28, lineHeight: 34, fontVariant: ["tabular-nums"], color: palette.emeraldInk },
  body: { fontFamily: font.body, fontSize: 16, lineHeight: 23, color: palette.grey900 },
  muted: { fontFamily: font.body, fontSize: 15, lineHeight: 21, color: palette.grey600 },
  small: { fontFamily: font.body, fontSize: 13, lineHeight: 18, color: palette.grey600 },
  label: { fontFamily: font.bodySemiBold, fontSize: 14, lineHeight: 19, color: palette.grey900 },
  overline: { fontFamily: font.bodySemiBold, fontSize: 12, lineHeight: 16, letterSpacing: 1, color: palette.grey600, textTransform: "uppercase" },
  button: { fontFamily: font.bodySemiBold, fontSize: 16, lineHeight: 22, textAlign: "center" },
  chip: { fontFamily: font.bodySemiBold, fontSize: 12, lineHeight: 16 },
  input: { fontFamily: font.body, fontSize: 17, color: palette.grey900 },
  tab: { fontFamily: font.bodyMedium, fontSize: 12 },
  /** Codes the fan reads out or types (the sign-in code): the system's fixed-width font. */
  mono: { fontFamily: "monospace", fontSize: 15, color: palette.grey900 },
});

/** Header options shared by every stack and tab navigator. */
export const headerOptions = {
  headerStyle: { backgroundColor: palette.emeraldInk },
  headerTintColor: palette.champagne,
  headerTitleStyle: { fontFamily: font.headingBold, color: palette.champagne },
  headerShadowVisible: false,
};
