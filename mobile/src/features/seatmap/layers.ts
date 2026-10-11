// Decides how every seat is drawn (ARCHITECTURE §5.3) and groups seats that look the same,
// so both renderers draw a dozen batches instead of a thousand nodes. Pure: no React, no
// native modules. The SVG and Skia canvases read the same output, so they cannot disagree.
//
// States differ by shape as well as colour, for fans who cannot tell the colours apart:
//   available  filled square in the category colour (the colour is the price tier)
//   selected   larger dark square with a tick
//   sold       small grey dot
//   held       hollow square with an amber outline
//   blocked    pale square with a diagonal stroke through it
// Accessible seats carry a wheelchair mark, companion seats a dot, restricted-view seats "!".

import { palette } from "../../palette";
import {
  SEAT_ACCESSIBLE,
  SEAT_COMPANION,
  SEAT_RESTRICTED,
  type MapCategory,
  type SeatGeometry,
  type SeatStatusCode,
} from "./model";

export type SeatLook = "available" | "selected" | "mine" | "sold" | "held" | "blocked";

export const SEAT_COLORS = {
  selected: palette.emeraldInk,
  tick: palette.champagne,
  sold: palette.grey300,
  heldStroke: palette.amber,
  heldFill: palette.white,
  blockedFill: palette.grey100,
  blockedStroke: palette.grey400,
  glyph: palette.white,
  /** A seat whose category the answer does not name is drawn in this, never left invisible. */
  unknownCategory: palette.ink400,
} as const;

/** How one seat looks, from its server status and the fan's own selection. */
export function seatLook(status: SeatStatusCode, selected: boolean, lost: boolean): SeatLook {
  // A seat the server refused on the last hold attempt is greyed out at once, before the
  // next poll says who has it. The screen drops the mark when the seat is free again.
  if (lost && status !== "M") return "sold";
  switch (status) {
    case "A":
      return selected ? "selected" : "available";
    case "M":
      return "mine";
    case "H":
      return "held";
    case "S":
      return "sold";
    case "B":
      return "blocked";
  }
}

/** Whether tapping a seat with this look may change the selection. */
export function isSelectable(look: SeatLook): boolean {
  return look === "available" || look === "selected";
}

export interface RectBatch {
  kind: "rects";
  key: string;
  /** Seat indexes into the geometry. */
  seats: number[];
  /** Size as a multiple of the geometry's seat size. */
  scale: number;
  fill: string | null;
  stroke: string | null;
  /** Stroke width as a fraction of the seat width. */
  strokeWidth: number;
}

export interface DotBatch {
  kind: "dots";
  key: string;
  seats: number[];
  /** Radius as a fraction of the seat width. */
  radius: number;
  fill: string;
}

export interface PathBatch {
  kind: "path";
  key: string;
  /** SVG path data in content coordinates; both renderers parse the same string. */
  d: string;
  stroke: string | null;
  fill: string | null;
  strokeWidth: number;
}

export type SeatBatch = RectBatch | DotBatch | PathBatch;

const n = (v: number) => (Math.round(v * 100) / 100).toString();

/** Rounded-square path data for a set of seats; used by the SVG renderer and by tests. */
export function rectsPath(g: SeatGeometry, seats: readonly number[], scale: number, inset = 0): string {
  const w = g.seatW * scale - inset;
  const h = g.seatH * scale - inset;
  const r = Math.min(w, h) * 0.24;
  let d = "";
  for (const k of seats) {
    const x0 = g.x[k]! - w / 2;
    const y0 = g.y[k]! - h / 2;
    d += `M${n(x0 + r)} ${n(y0)}h${n(w - 2 * r)}a${n(r)} ${n(r)} 0 0 1 ${n(r)} ${n(r)}v${n(h - 2 * r)}a${n(r)} ${n(r)} 0 0 1 ${n(-r)} ${n(r)}h${n(-(w - 2 * r))}a${n(r)} ${n(r)} 0 0 1 ${n(-r)} ${n(-r)}v${n(-(h - 2 * r))}a${n(r)} ${n(r)} 0 0 1 ${n(r)} ${n(-r)}z`;
  }
  return d;
}

function circle(cx: number, cy: number, r: number): string {
  return `M${n(cx - r)} ${n(cy)}a${n(r)} ${n(r)} 0 1 0 ${n(2 * r)} 0a${n(r)} ${n(r)} 0 1 0 ${n(-2 * r)} 0z`;
}

/** Dot path data for a set of seats (SVG renderer). */
export function dotsPath(g: SeatGeometry, seats: readonly number[], radius: number): string {
  let d = "";
  for (const k of seats) d += circle(g.x[k]!, g.y[k]!, g.seatW * radius);
  return d;
}

export interface BuildInput {
  geometry: SeatGeometry;
  statuses: readonly SeatStatusCode[];
  categories: readonly MapCategory[];
  selected: ReadonlySet<number>;
  lost: ReadonlySet<number>;
}

export interface SeatScene {
  batches: SeatBatch[];
  /** The look of every seat, in geometry order: the tap handler reads this. */
  looks: SeatLook[];
}

export function buildScene(input: BuildInput): SeatScene {
  const { geometry: g, statuses, categories, selected, lost } = input;
  const byCategory = new Map<number, number[]>();
  const sold: number[] = [];
  const held: number[] = [];
  const blocked: number[] = [];
  const chosen: number[] = [];
  const looks: SeatLook[] = new Array(g.count);

  let ticks = "";
  let slashes = "";
  let glyphStrokes = "";
  let glyphFills = "";
  const w = g.seatW;

  for (let k = 0; k < g.count; k++) {
    const id = g.ids[k]!;
    const look = seatLook(statuses[k] ?? "B", selected.has(id), lost.has(id));
    looks[k] = look;
    const cx = g.x[k]!;
    const cy = g.y[k]!;
    switch (look) {
      case "available": {
        const c = g.category[k]!;
        let list = byCategory.get(c);
        if (!list) byCategory.set(c, (list = []));
        list.push(k);
        const t = g.type[k];
        if (t === SEAT_ACCESSIBLE) {
          // Wheelchair mark: head, back and seat, and a wheel.
          glyphFills += circle(cx - 0.06 * w, cy - 0.3 * w, 0.09 * w);
          glyphStrokes += `M${n(cx - 0.06 * w)} ${n(cy - 0.17 * w)}L${n(cx - 0.06 * w)} ${n(cy + 0.06 * w)}L${n(cx + 0.18 * w)} ${n(cy + 0.06 * w)}L${n(cx + 0.26 * w)} ${n(cy + 0.3 * w)}`;
          glyphStrokes += `M${n(cx - 0.2 * w)} ${n(cy)}A${n(0.2 * w)} ${n(0.2 * w)} 0 1 0 ${n(cx + 0.12 * w)} ${n(cy + 0.26 * w)}`;
        } else if (t === SEAT_COMPANION) {
          glyphFills += circle(cx, cy, 0.14 * w);
        } else if (t === SEAT_RESTRICTED) {
          glyphStrokes += `M${n(cx)} ${n(cy - 0.3 * w)}L${n(cx)} ${n(cy + 0.08 * w)}`;
          glyphFills += circle(cx, cy + 0.28 * w, 0.07 * w);
        }
        break;
      }
      case "selected":
      case "mine":
        chosen.push(k);
        ticks += `M${n(cx - 0.3 * w)} ${n(cy + 0.02 * w)}L${n(cx - 0.08 * w)} ${n(cy + 0.24 * w)}L${n(cx + 0.32 * w)} ${n(cy - 0.24 * w)}`;
        break;
      case "sold":
        sold.push(k);
        break;
      case "held":
        held.push(k);
        break;
      case "blocked":
        blocked.push(k);
        slashes += `M${n(cx - 0.4 * w)} ${n(cy + 0.4 * w)}L${n(cx + 0.4 * w)} ${n(cy - 0.4 * w)}`;
        break;
    }
  }

  const batches: SeatBatch[] = [];
  if (sold.length) batches.push({ kind: "dots", key: "sold", seats: sold, radius: 0.3, fill: SEAT_COLORS.sold });
  if (blocked.length) {
    batches.push({ kind: "rects", key: "blocked", seats: blocked, scale: 1, fill: SEAT_COLORS.blockedFill, stroke: SEAT_COLORS.blockedStroke, strokeWidth: 0.06 });
    batches.push({ kind: "path", key: "blocked-slash", d: slashes, stroke: SEAT_COLORS.blockedStroke, fill: null, strokeWidth: 0.1 * w });
  }
  for (const [c, seats] of [...byCategory.entries()].sort((a, b) => a[0] - b[0])) {
    const color = categories[c]?.color_hex ?? SEAT_COLORS.unknownCategory;
    batches.push({ kind: "rects", key: `cat-${c}`, seats, scale: 1, fill: color, stroke: null, strokeWidth: 0 });
  }
  if (held.length) {
    batches.push({ kind: "rects", key: "held", seats: held, scale: 0.92, fill: SEAT_COLORS.heldFill, stroke: SEAT_COLORS.heldStroke, strokeWidth: 0.16 });
  }
  if (glyphStrokes) batches.push({ kind: "path", key: "glyph-stroke", d: glyphStrokes, stroke: SEAT_COLORS.glyph, fill: null, strokeWidth: 0.11 * w });
  if (glyphFills) batches.push({ kind: "path", key: "glyph-fill", d: glyphFills, stroke: null, fill: SEAT_COLORS.glyph, strokeWidth: 0 });
  if (chosen.length) {
    batches.push({ kind: "rects", key: "chosen", seats: chosen, scale: 1.16, fill: SEAT_COLORS.selected, stroke: null, strokeWidth: 0 });
    batches.push({ kind: "path", key: "chosen-tick", d: ticks, stroke: SEAT_COLORS.tick, fill: null, strokeWidth: 0.15 * w });
  }
  return { batches, looks };
}
