// The sales heatmap's colour scale: one hue, light to dark, ending on the brand's Emerald Ink.

const EMPTY = "#eef0ee";
const LIGHT: readonly [number, number, number] = [0xdc, 0xe9, 0xe4];
const DARK: readonly [number, number, number] = [0x06, 0x4e, 0x3b];

/** Share of a block that is sold, 0 to 1. A block with nothing to sell counts as 0. */
export function soldShare(sold: number, total: number): number {
  if (!(total > 0) || !(sold > 0)) return 0;
  return Math.min(1, sold / total);
}

/**
 * Fill for a block that is `share` sold. Nothing sold has its own neutral grey so that the
 * first ticket is visible as a change of colour; from there the fill runs from a pale tint
 * to Emerald Ink at a full block.
 */
export function heatColour(share: number): string {
  if (!(share > 0)) return EMPTY;
  const t = Math.min(1, share);
  const channel = (i: 0 | 1 | 2) =>
    Math.round(LIGHT[i] + (DARK[i] - LIGHT[i]) * t)
      .toString(16)
      .padStart(2, "0");
  return `#${channel(0)}${channel(1)}${channel(2)}`;
}

export const HEAT_LEGEND_STOPS = [0, 0.01, 0.25, 0.5, 0.75, 1] as const;

/** Black or white, whichever reads better on the given #rrggbb fill. */
export function readableOn(hex: string): "#ffffff" | "#1d2321" {
  const value = /^#([0-9a-f]{6})$/i.exec(hex)?.[1];
  if (!value) return "#1d2321";
  const [r, g, b] = [0, 2, 4].map((at) => Number.parseInt(value.slice(at, at + 2), 16)) as [number, number, number];
  // Perceived brightness (ITU-R BT.601), 0 to 255.
  return (r * 299 + g * 587 + b * 114) / 1000 < 140 ? "#ffffff" : "#1d2321";
}
