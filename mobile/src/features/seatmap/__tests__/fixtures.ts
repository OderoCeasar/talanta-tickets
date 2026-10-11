// Builds level-3 answers the way the backend's seatmap package lays a block out
// (backend/internal/seatmap/seatmap.go), so the tests run on real rotated and curved geometry.

import type { BlockSeatsFull } from "../model";

interface Params {
  originX: number;
  originY: number;
  rotationDeg: number;
  /** Radius of the front row; 0 for a straight block. */
  arcRadius: number;
  rows: number;
  seatsPerRow: number;
  pitchX?: number;
  pitchY?: number;
}

const round2 = (v: number) => Math.round(v * 100) / 100;

function rowLabel(index: number): string {
  let out = "";
  for (let n = index + 1; n > 0; n = Math.floor((n - 1) / 26)) out = String.fromCharCode(65 + ((n - 1) % 26)) + out;
  return out;
}

export function makeBlock(p: Params, id = 116): BlockSeatsFull {
  const pitchX = p.pitchX ?? 5;
  const pitchY = p.pitchY ?? 8;
  const rad = (p.rotationDeg * Math.PI) / 180;
  const sin = Math.sin(rad);
  const cos = Math.cos(rad);
  const toPlan = (lx: number, ly: number): [number, number] => [p.originX + lx * cos - ly * sin, p.originY + lx * sin + ly * cos];
  const mid = ((p.seatsPerRow - 1) * pitchX) / 2;
  const place = (lx: number, ly: number): [number, number] => {
    if (p.arcRadius === 0) return toPlan(lx, ly);
    const theta = (lx - mid) / p.arcRadius;
    const rho = p.arcRadius + ly;
    return toPlan(mid + rho * Math.sin(theta), rho * Math.cos(theta) - p.arcRadius);
  };

  let seatId = id * 10_000;
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  const rows: BlockSeatsFull["rows"] = [];
  for (let r = 0; r < p.rows; r++) {
    const seats: BlockSeatsFull["rows"][number]["seats"] = [];
    for (let i = 0; i < p.seatsPerRow; i++) {
      const [x, y] = place(i * pitchX, r * pitchY);
      minX = Math.min(minX, x); maxX = Math.max(maxX, x); minY = Math.min(minY, y); maxY = Math.max(maxY, y);
      seats.push([++seatId, i + 1, round2(x), round2(y), 0, 0, "A"]);
    }
    rows.push({ label: rowLabel(r), seats });
  }
  return {
    kind: "full",
    marker: "m1",
    poll_interval_seconds: 5,
    block: {
      id, stand_id: 2, code: "N202", label: "Block N202", rotation_deg: p.rotationDeg, seat_pitch_x: pitchX, seat_pitch_y: pitchY,
      bounds: { min_x: round2(minX - 4), min_y: round2(minY - 4), max_x: round2(maxX + 4), max_y: round2(maxY + 4) },
    },
    categories: [
      { id: 11, name: "Terrace", price_cents: 50_000, color_hex: "#DB2777" },
      { id: 12, name: "Regular", price_cents: 100_000, color_hex: "#059669" },
    ],
    rows,
  };
}

/** The seeded venue's largest block: an upper-tier corner wedge, 82 seats × 16 rows = 1,312, bent round a 534-unit radius and turned 202.5°. */
export const curvedBlock = () => makeBlock({ originX: 1820.6, originY: 233.9, rotationDeg: 202.5, arcRadius: 534, rows: 16, seatsPerRow: 82 });

/** An East lower-tier block: straight, 18 × 28 = 504 seats, turned 270°. */
export const straightBlock = () => makeBlock({ originX: 1550, originY: 1534.2, rotationDeg: 270, arcRadius: 0, rows: 28, seatsPerRow: 18 }, 131);

/** A small upright block, easy to reason about by hand. */
export const tinyBlock = () => makeBlock({ originX: 100, originY: 100, rotationDeg: 0, arcRadius: 0, rows: 3, seatsPerRow: 4 }, 7);
