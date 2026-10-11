// Plan geometry for the stadium drawings. Pure functions, no DOM.

export type Point = readonly [number, number];

export interface Box {
  minX: number;
  minY: number;
  maxX: number;
  maxY: number;
}

/** The box between two drag corners, whichever way the drag went. */
export function boxFromCorners(a: Point, b: Point): Box {
  return {
    minX: Math.min(a[0], b[0]),
    minY: Math.min(a[1], b[1]),
    maxX: Math.max(a[0], b[0]),
    maxY: Math.max(a[1], b[1]),
  };
}

export function boundsOf(points: readonly Point[]): Box | null {
  if (points.length === 0) return null;
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  for (const [x, y] of points) {
    if (x < minX) minX = x;
    if (y < minY) minY = y;
    if (x > maxX) maxX = x;
    if (y > maxY) maxY = y;
  }
  return { minX, minY, maxX, maxY };
}

export function centreOf(points: readonly Point[]): Point | null {
  const box = boundsOf(points);
  return box ? [(box.minX + box.maxX) / 2, (box.minY + box.maxY) / 2] : null;
}

/** SVG path data for a closed polygon. */
export function polygonPath(points: readonly Point[]): string {
  if (points.length === 0) return "";
  return `M${points.map(([x, y]) => `${x} ${y}`).join("L")}Z`;
}

export function pointInBox([x, y]: Point, box: Box): boolean {
  return x >= box.minX && x <= box.maxX && y >= box.minY && y <= box.maxY;
}

/** Ray casting; a point exactly on an edge may land either side, which is fine for a drag. */
export function pointInPolygon([x, y]: Point, polygon: readonly Point[]): boolean {
  let inside = false;
  for (let i = 0, j = polygon.length - 1; i < polygon.length; j = i++) {
    const [xi, yi] = polygon[i]!;
    const [xj, yj] = polygon[j]!;
    if (yi > y !== yj > y && x < ((xj - xi) * (y - yi)) / (yj - yi) + xi) inside = !inside;
  }
  return inside;
}

function orientation(a: Point, b: Point, c: Point): number {
  return Math.sign((b[0] - a[0]) * (c[1] - a[1]) - (b[1] - a[1]) * (c[0] - a[0]));
}

function segmentsCross(a: Point, b: Point, c: Point, d: Point): boolean {
  return orientation(a, b, c) !== orientation(a, b, d) && orientation(c, d, a) !== orientation(c, d, b);
}

/**
 * True when the polygon and the box share any area: a corner of one is inside the other, or
 * their edges cross. Touching a block with the drag box is enough to select it, which is
 * what people expect from a marquee on a plan of thin, curved blocks.
 */
export function polygonTouchesBox(polygon: readonly Point[], box: Box): boolean {
  if (polygon.length === 0) return false;
  if (polygon.some((point) => pointInBox(point, box))) return true;
  const corners: Point[] = [
    [box.minX, box.minY],
    [box.maxX, box.minY],
    [box.maxX, box.maxY],
    [box.minX, box.maxY],
  ];
  if (corners.some((corner) => pointInPolygon(corner, polygon))) return true;
  for (let i = 0; i < polygon.length; i++) {
    const a = polygon[i]!;
    const b = polygon[(i + 1) % polygon.length]!;
    for (let k = 0; k < 4; k++) {
      if (segmentsCross(a, b, corners[k]!, corners[(k + 1) % 4]!)) return true;
    }
  }
  return false;
}

/** IDs of the blocks a drag box touches, in the order given. Blocks with no outline have no seats and are skipped. */
export function blocksInBox<T extends { id: number; outline: readonly Point[] }>(blocks: readonly T[], box: Box): number[] {
  return blocks.filter((block) => polygonTouchesBox(block.outline, box)).map((block) => block.id);
}

/** Indexes of the rows that have at least one seat centre inside the box. */
export function rowsInBox(rows: readonly { seats: readonly Point[] }[], box: Box): number[] {
  const hit: number[] = [];
  rows.forEach((row, index) => {
    if (row.seats.some((seat) => pointInBox(seat, box))) hit.push(index);
  });
  return hit;
}

/**
 * Turns a rectangle dragged on the plan into a block placement the server understands: the
 * origin is the first seat of the front row (half a pitch in from the corner) and the size
 * becomes whole rows and seats. Valid for an unrotated block; the server's preview is the
 * truth once rotation or a curve is set.
 */
export function placementFromBox(box: Box, pitchX: number, pitchY: number) {
  const round2 = (value: number) => Math.round(value * 100) / 100;
  return {
    originX: round2(box.minX + pitchX / 2),
    originY: round2(box.minY + pitchY / 2),
    seatsPerRow: Math.max(1, Math.round((box.maxX - box.minX) / pitchX)),
    rows: Math.max(1, Math.round((box.maxY - box.minY) / pitchY)),
  };
}
