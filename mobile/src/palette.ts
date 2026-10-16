// Colours and spacing with no React Native import, so pure logic (and its tests) can use them.
import { colors } from "@talanta/shared";

/** Mixes two `#RRGGBB` colours: `amount` of `a`, the rest `b`. Same idea as CSS `color-mix`. */
export function mix(a: string, b: string, amount: number): string {
  const part = (hex: string, at: number) => parseInt(hex.slice(at, at + 2), 16);
  const out = [1, 3, 5].map((at) => Math.round(part(a, at) * amount + part(b, at) * (1 - amount)));
  return `#${out.map((n) => n.toString(16).padStart(2, "0")).join("")}`;
}

/** `#RRGGBB` plus an alpha from 0 to 1. */
export function alpha(hex: string, a: number): string {
  return `${hex}${Math.round(a * 255).toString(16).padStart(2, "0")}`;
}

const WHITE = "#ffffff";
const BLACK = "#000000";

// The two brand colours are fixed (packages/shared/src/theme.ts). Everything else is mixed
// from them, with the same recipes as the admin site's CSS, so both apps look like one product.
export const palette = {
  emeraldInk: colors.emeraldInk,
  champagne: colors.champagne,

  inkDeep: mix(colors.emeraldInk, BLACK, 0.78),
  inkSoft: mix(colors.emeraldInk, WHITE, 0.82),
  ink400: mix(colors.emeraldInk, WHITE, 0.55),
  ink200: mix(colors.emeraldInk, WHITE, 0.22),
  ink100: mix(colors.emeraldInk, WHITE, 0.1),
  ink50: mix(colors.emeraldInk, WHITE, 0.05),
  champagneDeep: mix(colors.champagne, BLACK, 0.58),
  champagne300: mix(colors.champagne, WHITE, 0.7),
  champagne100: mix(colors.champagne, WHITE, 0.38),
  champagne50: mix(colors.champagne, WHITE, 0.2),

  grey900: "#1d2321",
  grey700: "#46504c",
  grey600: "#5b6561",
  grey400: "#9aa39f",
  grey300: "#cdd3d0",
  grey200: "#e4e8e5",
  grey100: "#f2f4f2",
  white: WHITE,

  // State colours: accents only (messages, chips, outlines).
  error: "#b42318",
  errorBg: "#fdf0ee",
  errorBorder: "#f0c4bd",
  warning: "#92500a",
  warningBg: "#fdf4e3",
  warningBorder: "#f0d49b",
  /** Gate mode's full-screen results: "let them in" and "turn them away". White text passes AA on both. */
  admit: "#15803d",
  refuse: "#b91c1c",
  /** The "held by someone else" outline on the seat map (§5.3). */
  amber: "#d97706",
} as const;

export const radius = { sm: 6, md: 12, lg: 18, pill: 999 } as const;
export const space = { xs: 4, sm: 8, md: 12, lg: 16, xl: 24, xxl: 32 } as const;

