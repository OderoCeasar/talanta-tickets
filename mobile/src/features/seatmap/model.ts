// The level-3 block as the canvas needs it: flat arrays instead of one object per seat, the
// block turned to face up, and a grid index so a tap finds its seat without a handler per
// seat. No React and no native modules in this file, so it is unit-tested directly.

import type { components } from "@talanta/shared";

type Schemas = components["schemas"];
export type BlockSeatsFull = Schemas["BlockSeatsFull"];
export type BlockSeatsDelta = Schemas["BlockSeatsDelta"];
export type BlockSeats = Schemas["BlockSeats"];
export type SeatStatusCode = Schemas["SeatStatusCode"];
export type MapCategory = Schemas["MapCategory"];

// Positions inside a `SeatTuple` (docs/API_NOTES.md).
export const T_ID = 0;
export const T_NUMBER = 1;
export const T_X = 2;
export const T_Y = 3;
export const T_TYPE = 4;
export const T_CATEGORY = 5;
export const T_STATUS = 6;

export const SEAT_STANDARD = 0;
export const SEAT_ACCESSIBLE = 1;
export const SEAT_COMPANION = 2;
export const SEAT_RESTRICTED = 3;

/** Above this many seats the block is drawn with Skia instead of SVG (ARCHITECTURE §5.2). */
export const SVG_SEAT_LIMIT = 1200;

export function pickRenderer(seatCount: number): "svg" | "skia" {
  return seatCount > SVG_SEAT_LIMIT ? "skia" : "svg";
}

export interface SeatGeometry {
  count: number;
  /** Event seat IDs, in the order of the API answer (row by row). */
  ids: number[];
  /** Seat centres in content coordinates: the block turned to face up, origin at the top left. */
  x: Float32Array;
  y: Float32Array;
  type: Uint8Array;
  /** Index into `categories`, or -1. */
  category: Int8Array;
  number: Uint16Array;
  /** Index into `rowLabels`. */
  row: Uint16Array;
  rowLabels: string[];
  /** Where each row's label is drawn (left of its first seat), content coordinates. */
  rowLabelX: Float32Array;
  rowLabelY: Float32Array;
  /** Content size in plan units. */
  width: number;
  height: number;
  pitchX: number;
  pitchY: number;
  /** Drawn seat size; a little smaller than the pitch so neighbours do not touch. */
  seatW: number;
  seatH: number;
  indexById: Map<number, number>;
  grid: SeatGrid;
}

export interface SeatGrid {
  cell: number;
  cols: number;
  rows: number;
  /** CSR layout: seats of cell `c` are `items[offsets[c] .. offsets[c + 1])`. */
  offsets: Int32Array;
  items: Int32Array;
}

/**
 * Turns a plan point into the block's own frame. The plan places a block by rotating its
 * local frame clockwise by `rotationDeg` (y grows downwards), so this is the inverse turn
 * about `(cx, cy)`. After it, rows run left to right and the pitch is above the first row.
 */
export function planToLocal(px: number, py: number, cx: number, cy: number, rotationDeg: number): [number, number] {
  const rad = (rotationDeg * Math.PI) / 180;
  const sin = Math.sin(rad);
  const cos = Math.cos(rad);
  const dx = px - cx;
  const dy = py - cy;
  return [dx * cos + dy * sin, -dx * sin + dy * cos];
}

export function buildGeometry(full: BlockSeatsFull): SeatGeometry {
  const { block, rows } = full;
  let count = 0;
  for (const r of rows) count += r.seats.length;

  const ids: number[] = new Array(count);
  const x = new Float32Array(count);
  const y = new Float32Array(count);
  const type = new Uint8Array(count);
  const category = new Int8Array(count);
  const number = new Uint16Array(count);
  const row = new Uint16Array(count);
  const rowLabels = rows.map((r) => r.label);
  const rowLabelX = new Float32Array(rows.length);
  const rowLabelY = new Float32Array(rows.length);
  const indexById = new Map<number, number>();

  const cx = (block.bounds.min_x + block.bounds.max_x) / 2;
  const cy = (block.bounds.min_y + block.bounds.max_y) / 2;
  const pitchX = block.seat_pitch_x > 0 ? block.seat_pitch_x : 1;
  const pitchY = block.seat_pitch_y > 0 ? block.seat_pitch_y : pitchX;

  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  let i = 0;
  for (let r = 0; r < rows.length; r++) {
    for (const seat of rows[r]!.seats) {
      const [lx, ly] = planToLocal(seat[T_X], seat[T_Y], cx, cy, block.rotation_deg);
      ids[i] = seat[T_ID];
      x[i] = lx;
      y[i] = ly;
      type[i] = seat[T_TYPE];
      category[i] = seat[T_CATEGORY];
      number[i] = seat[T_NUMBER];
      row[i] = r;
      indexById.set(seat[T_ID], i);
      if (lx < minX) minX = lx;
      if (lx > maxX) maxX = lx;
      if (ly < minY) minY = ly;
      if (ly > maxY) maxY = ly;
      i++;
    }
  }
  if (count === 0) {
    minX = minY = 0;
    maxX = maxY = 0;
  }

  // Room round the seats: a full pitch at the sides for the row labels, a little above and below.
  const padX = pitchX * 2.2;
  const padY = pitchY * 0.9;
  for (let k = 0; k < count; k++) {
    x[k] = x[k]! - minX + padX;
    y[k] = y[k]! - minY + padY;
  }
  const width = maxX - minX + 2 * padX;
  const height = maxY - minY + 2 * padY;

  // Row labels sit one and a half pitches before the row's first seat, along the row's own
  // direction, so they follow a curved block's arc instead of a straight left margin.
  i = 0;
  for (let r = 0; r < rows.length; r++) {
    const n = rows[r]!.seats.length;
    if (n === 0) continue;
    let dirX = -1;
    let dirY = 0;
    if (n > 1) {
      const ddx = x[i]! - x[i + 1]!;
      const ddy = y[i]! - y[i + 1]!;
      const len = Math.hypot(ddx, ddy) || 1;
      dirX = ddx / len;
      dirY = ddy / len;
    }
    rowLabelX[r] = x[i]! + dirX * pitchX * 1.3;
    rowLabelY[r] = y[i]! + dirY * pitchX * 1.3;
    i += n;
  }

  const seatW = pitchX * 0.8;
  const seatH = Math.min(pitchY * 0.8, seatW * 1.3);

  const geometry: SeatGeometry = {
    count, ids, x, y, type, category, number, row, rowLabels, rowLabelX, rowLabelY,
    width, height, pitchX, pitchY, seatW, seatH, indexById,
    grid: buildGrid(x, y, count, width, height, Math.max(pitchX, pitchY)),
  };
  return geometry;
}

/** Buckets seat centres into square cells so a lookup reads at most nine cells. */
export function buildGrid(x: Float32Array, y: Float32Array, count: number, width: number, height: number, cell: number): SeatGrid {
  const cols = Math.max(1, Math.ceil(width / cell));
  const rows = Math.max(1, Math.ceil(height / cell));
  const cellOf = (k: number) => {
    const c = Math.min(cols - 1, Math.max(0, Math.floor(x[k]! / cell)));
    const r = Math.min(rows - 1, Math.max(0, Math.floor(y[k]! / cell)));
    return r * cols + c;
  };
  const offsets = new Int32Array(cols * rows + 1);
  for (let k = 0; k < count; k++) offsets[cellOf(k) + 1]!++;
  for (let c = 0; c < cols * rows; c++) offsets[c + 1] = offsets[c + 1]! + offsets[c]!;
  const cursor = offsets.slice(0, cols * rows);
  const items = new Int32Array(count);
  for (let k = 0; k < count; k++) {
    const c = cellOf(k);
    items[cursor[c]!] = k;
    cursor[c] = cursor[c]! + 1;
  }
  return { cell, cols, rows, offsets, items };
}

/**
 * The seat under a point in content coordinates, or -1.
 *
 * A point counts as "on" a seat when it is inside that seat's own slot: within 0.6 of a
 * seat pitch sideways and half a row pitch up or down. That is a little larger than the
 * drawn square, so a fingertip does not have to be exact, but small enough that a tap in an
 * aisle hits nothing. When two slots overlap (the inside of a curved row) the nearer centre
 * wins.
 */
export function seatAt(g: SeatGeometry, px: number, py: number): number {
  const { grid } = g;
  const reachX = g.pitchX * 0.6;
  const reachY = g.pitchY * 0.5;
  const c0 = Math.floor(px / grid.cell);
  const r0 = Math.floor(py / grid.cell);
  let best = -1;
  let bestDist = Infinity;
  for (let r = r0 - 1; r <= r0 + 1; r++) {
    if (r < 0 || r >= grid.rows) continue;
    for (let c = c0 - 1; c <= c0 + 1; c++) {
      if (c < 0 || c >= grid.cols) continue;
      const cell = r * grid.cols + c;
      for (let at = grid.offsets[cell]!; at < grid.offsets[cell + 1]!; at++) {
        const k = grid.items[at]!;
        const dx = Math.abs(px - g.x[k]!);
        const dy = Math.abs(py - g.y[k]!);
        if (dx > reachX || dy > reachY) continue;
        const dist = dx * dx + dy * dy;
        if (dist < bestDist) {
          bestDist = dist;
          best = k;
        }
      }
    }
  }
  return best;
}

/** `C14`: the row label followed by the seat number. */
export function seatLabel(g: SeatGeometry, index: number): string {
  return `${g.rowLabels[g.row[index]!] ?? ""}${g.number[index]}`;
}

/** Statuses in the same order as the geometry's arrays. */
export function seatStatuses(full: BlockSeatsFull): SeatStatusCode[] {
  const out: SeatStatusCode[] = [];
  for (const r of full.rows) for (const seat of r.seats) out.push(seat[T_STATUS]);
  return out;
}

/**
 * Applies a `kind: "delta"` answer to the cached full block and returns a new object.
 * Rows that did not change keep their identity; the block and category objects are kept,
 * so geometry built from them stays valid. Unknown seat IDs are ignored (the seat belongs
 * to another block, or the layout changed and a full answer will follow).
 */
export function applyDelta(full: BlockSeatsFull, delta: BlockSeatsDelta): BlockSeatsFull {
  const base = { ...full, marker: delta.marker, poll_interval_seconds: delta.poll_interval_seconds };
  if (delta.changes.length === 0) return base;
  const next = new Map<number, SeatStatusCode>();
  for (const [id, status] of delta.changes) next.set(id, status);

  let touched = false;
  const rows = full.rows.map((row) => {
    let seats: typeof row.seats | null = null;
    for (let i = 0; i < row.seats.length; i++) {
      const seat = row.seats[i]!;
      const status = next.get(seat[T_ID]);
      if (status === undefined || status === seat[T_STATUS]) continue;
      if (!seats) seats = row.seats.slice();
      seats[i] = [seat[0], seat[1], seat[2], seat[3], seat[4], seat[5], status];
    }
    if (!seats) return row;
    touched = true;
    return { ...row, seats };
  });
  return touched ? { ...base, rows } : base;
}

/**
 * What the cache should hold after a poll: a `full` answer replaces it (the server sends one
 * when the marker is too old or something other than status changed), a `delta` patches it.
 */
export function mergePoll(cached: BlockSeatsFull | undefined, answer: BlockSeats): BlockSeatsFull | undefined {
  if (answer.kind === "full") return answer;
  return cached ? applyDelta(cached, answer) : cached;
}
