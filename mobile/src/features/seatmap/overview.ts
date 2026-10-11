// Geometry helpers for levels 1 and 2 (stadium and stand), where a handful of polygons are
// drawn with react-native-svg. Pure, so the price tinting and bounds are unit-tested.

import type { components } from "@talanta/shared";

import { mix, palette } from "../../palette";

type Polygon = components["schemas"]["Polygon"];

export interface Box {
  x: number;
  y: number;
  width: number;
  height: number;
}

/** The box round a set of polygons, grown by `pad` on every side. */
export function boundsOf(polygons: readonly Polygon[], pad = 0): Box {
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  for (const polygon of polygons) {
    for (const [x, y] of polygon) {
      if (x < minX) minX = x;
      if (x > maxX) maxX = x;
      if (y < minY) minY = y;
      if (y > maxY) maxY = y;
    }
  }
  if (!Number.isFinite(minX)) return { x: 0, y: 0, width: 1, height: 1 };
  return { x: minX - pad, y: minY - pad, width: maxX - minX + 2 * pad, height: maxY - minY + 2 * pad };
}

export function pointsAttr(polygon: Polygon): string {
  return polygon.map(([x, y]) => `${x},${y}`).join(" ");
}

/** Area-weighted centre of a polygon; falls back to the mean of its points when it has no area. */
export function centroid(polygon: Polygon): [number, number] {
  let area = 0;
  let cx = 0;
  let cy = 0;
  for (let i = 0; i < polygon.length; i++) {
    const [x0, y0] = polygon[i]!;
    const [x1, y1] = polygon[(i + 1) % polygon.length]!;
    const cross = x0 * y1 - x1 * y0;
    area += cross;
    cx += (x0 + x1) * cross;
    cy += (y0 + y1) * cross;
  }
  if (Math.abs(area) < 1e-9) {
    const n = Math.max(1, polygon.length);
    return [polygon.reduce((s, p) => s + p[0], 0) / n, polygon.reduce((s, p) => s + p[1], 0) / n];
  }
  return [cx / (3 * area), cy / (3 * area)];
}

export const SOLD_OUT_FILL = palette.grey200;
export const SOLD_OUT_STROKE = palette.grey400;

/**
 * The fill for a stand on the stadium overview: a tint of the brand ink, light for the
 * cheapest "from" price and dark for the dearest. A stand with nothing left is grey.
 * Prices are ranked, not scaled, so one very expensive stand does not wash out the rest.
 */
export function priceTints(pricesFrom: readonly (number | null)[]): string[] {
  const distinct = [...new Set(pricesFrom.filter((p): p is number => p !== null))].sort((a, b) => a - b);
  return pricesFrom.map((price) => {
    if (price === null) return SOLD_OUT_FILL;
    const rank = distinct.indexOf(price);
    const t = distinct.length <= 1 ? 0.5 : rank / (distinct.length - 1);
    return mix(palette.emeraldInk, palette.white, 0.28 + 0.62 * t);
  });
}

/** Whether white or dark text reads better on a `#RRGGBB` fill. */
export function textOn(fill: string): string {
  const r = parseInt(fill.slice(1, 3), 16);
  const g = parseInt(fill.slice(3, 5), 16);
  const b = parseInt(fill.slice(5, 7), 16);
  return (r * 299 + g * 587 + b * 114) / 1000 > 150 ? palette.grey900 : palette.white;
}

export function pointInPolygon(x: number, y: number, polygon: Polygon): boolean {
  let inside = false;
  for (let i = 0, j = polygon.length - 1; i < polygon.length; j = i++) {
    const [xi, yi] = polygon[i]!;
    const [xj, yj] = polygon[j]!;
    if (yi > y !== yj > y && x < ((xj - xi) * (y - yi)) / (yj - yi) + xi) inside = !inside;
  }
  return inside;
}

function distanceToEdges(x: number, y: number, polygon: Polygon): number {
  let best = Infinity;
  for (let i = 0, j = polygon.length - 1; i < polygon.length; j = i++) {
    const [x1, y1] = polygon[j]!;
    const [x2, y2] = polygon[i]!;
    const dx = x2 - x1;
    const dy = y2 - y1;
    const len2 = dx * dx + dy * dy;
    const t = len2 === 0 ? 0 : Math.max(0, Math.min(1, ((x - x1) * dx + (y - y1) * dy) / len2));
    best = Math.min(best, Math.hypot(x - (x1 + t * dx), y - (y1 + t * dy)));
  }
  return best;
}

/**
 * Where to put a label so it sits inside the shape. A stand that wraps round two corners is
 * a "C", and the centre of a C is outside it, so the centroid is used only when it is inside.
 * Otherwise this takes the sampled inside point furthest from every edge.
 */
export function labelPoint(polygon: Polygon): [number, number] {
  const c = centroid(polygon);
  const box = boundsOf([polygon]);
  let best: [number, number] = c;
  let bestScore = pointInPolygon(c[0], c[1], polygon) ? distanceToEdges(c[0], c[1], polygon) * 1.5 : -1;
  const steps = 16;
  for (let i = 1; i < steps; i++) {
    for (let j = 1; j < steps; j++) {
      const x = box.x + (box.width * i) / steps;
      const y = box.y + (box.height * j) / steps;
      if (!pointInPolygon(x, y, polygon)) continue;
      const score = distanceToEdges(x, y, polygon);
      if (score > bestScore) {
        bestScore = score;
        best = [x, y];
      }
    }
  }
  return best;
}
