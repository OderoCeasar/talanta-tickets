import { describe, expect, it } from "vitest";

import {
  applyDelta, buildGeometry, joinSeatLabel, mergePoll, pickRenderer, planToLocal, seatAt, seatLabel, seatStatuses, SVG_SEAT_LIMIT, T_ID, T_STATUS, T_X, T_Y,
  type BlockSeatsDelta, type BlockSeatsFull,
} from "../model";
import { curvedBlock, straightBlock, tinyBlock } from "./fixtures";

/** Where a plan point lands in content coordinates, worked out independently of buildGeometry's arrays. */
function contentPoint(full: BlockSeatsFull, px: number, py: number): [number, number] {
  const g = buildGeometry(full);
  const b = full.block.bounds;
  const cx = (b.min_x + b.max_x) / 2;
  const cy = (b.min_y + b.max_y) / 2;
  const first = full.rows[0]!.seats[0]!;
  const [fx, fy] = planToLocal(first[T_X], first[T_Y], cx, cy, full.block.rotation_deg);
  const [lx, ly] = planToLocal(px, py, cx, cy, full.block.rotation_deg);
  return [lx - fx + g.x[0]!, ly - fy + g.y[0]!];
}

describe("pickRenderer", () => {
  it("uses SVG up to the limit and Skia above it", () => {
    expect(SVG_SEAT_LIMIT).toBe(1200);
    expect(pickRenderer(504)).toBe("svg");
    expect(pickRenderer(1200)).toBe("svg");
    expect(pickRenderer(1201)).toBe("skia");
    expect(pickRenderer(1312)).toBe("skia");
  });
});

describe("buildGeometry", () => {
  it("keeps every seat, in order, with its ID, number and row", () => {
    const full = tinyBlock();
    const g = buildGeometry(full);
    expect(g.count).toBe(12);
    expect(g.ids).toEqual(full.rows.flatMap((r) => r.seats.map((s) => s[T_ID])));
    expect(g.rowLabels).toEqual(["A", "B", "C"]);
    expect(seatLabel(g, 0)).toBe("A1");
    expect(seatLabel(g, 11)).toBe("C4");
    expect(g.indexById.get(g.ids[7]!)).toBe(7);
  });

  it("turns a rotated block to face up: rows level, front row at the top, seat 1 on the left", () => {
    const g = buildGeometry(straightBlock());
    expect(g.count).toBe(504);
    // Seats 0..17 are row A.
    for (let i = 1; i < 18; i++) {
      expect(g.y[i]).toBeCloseTo(g.y[0]!, 1);
      expect(g.x[i]! - g.x[i - 1]!).toBeCloseTo(5, 1);
    }
    // Row B is one row pitch below row A.
    expect(g.y[18]! - g.y[0]!).toBeCloseTo(8, 1);
    expect(g.x[18]).toBeCloseTo(g.x[0]!, 1);
  });

  it("sizes the content to hold every seat with a margin", () => {
    for (const full of [tinyBlock(), straightBlock(), curvedBlock()]) {
      const g = buildGeometry(full);
      for (let k = 0; k < g.count; k++) {
        expect(g.x[k]).toBeGreaterThan(g.seatW / 2);
        expect(g.x[k]).toBeLessThan(g.width - g.seatW / 2);
        expect(g.y[k]).toBeGreaterThan(g.seatH / 2);
        expect(g.y[k]).toBeLessThan(g.height - g.seatH / 2);
      }
    }
  });

  it("opens a curved block symmetrically, with the shorter front row at the top", () => {
    const g = buildGeometry(curvedBlock());
    expect(g.count).toBe(1312);
    const rowA = { first: 0, last: 81 };
    const rowP = { first: 15 * 82, last: 15 * 82 + 81 };
    // Both ends of a row are at the same height, and the row is centred in the content.
    expect(g.y[rowA.first]).toBeCloseTo(g.y[rowA.last]!, 0);
    expect((g.x[rowA.first]! + g.x[rowA.last]!) / 2).toBeCloseTo(g.width / 2, 0);
    // The back row is wider than the front row and further down the screen.
    expect(g.x[rowP.last]! - g.x[rowP.first]!).toBeGreaterThan(g.x[rowA.last]! - g.x[rowA.first]!);
    expect(g.y[rowP.first + 41]).toBeGreaterThan(g.y[rowA.first + 41]!);
  });

  it("puts each row label beside its first seat, outside the seats", () => {
    const g = buildGeometry(curvedBlock());
    for (let r = 0; r < g.rowLabels.length; r++) {
      const first = r * 82;
      expect(Math.hypot(g.rowLabelX[r]! - g.x[first]!, g.rowLabelY[r]! - g.y[first]!)).toBeCloseTo(g.pitchX * 1.3, 1);
      expect(seatAt(g, g.rowLabelX[r]!, g.rowLabelY[r]!)).toBe(-1);
    }
  });

  it("survives an empty block", () => {
    const full = tinyBlock();
    const g = buildGeometry({ ...full, rows: [] });
    expect(g.count).toBe(0);
    expect(seatAt(g, 1, 1)).toBe(-1);
  });
});

describe("seatAt (tap hit-testing through the grid index)", () => {
  it.each([
    ["upright", tinyBlock],
    ["rotated 270°", straightBlock],
    ["curved and rotated 202.5°", curvedBlock],
  ])("finds every seat from its own centre: %s", (_name, make) => {
    const g = buildGeometry(make());
    for (let k = 0; k < g.count; k++) expect(seatAt(g, g.x[k]!, g.y[k]!)).toBe(k);
  });

  it.each([
    ["rotated 270°", straightBlock],
    ["curved and rotated 202.5°", curvedBlock],
  ])("still finds the seat when the finger is a little off centre: %s", (_name, make) => {
    const g = buildGeometry(make());
    const off = [[1.2, 0], [-1.2, 0], [0, 2], [0, -2], [1, 1.5], [-1, -1.5]] as const;
    for (let k = 0; k < g.count; k += 7) {
      for (const [dx, dy] of off) expect(seatAt(g, g.x[k]! + dx, g.y[k]! + dy)).toBe(k);
    }
  });

  it("maps a tap given in venue plan coordinates back to the right seat on a curved, rotated block", () => {
    const full = curvedBlock();
    const g = buildGeometry(full);
    // Row H, seat 60: take its plan position from the API tuple and convert independently.
    const tuple = full.rows[7]!.seats[59]!;
    const [x, y] = contentPoint(full, tuple[T_X], tuple[T_Y]);
    const hit = seatAt(g, x, y);
    expect(g.ids[hit]).toBe(tuple[T_ID]);
    expect(seatLabel(g, hit)).toBe("H60");
  });

  it("returns nothing for a tap in an aisle or outside the block", () => {
    const g = buildGeometry(straightBlock());
    // Seat slots reach 3 units sideways; 4 units left of seat 1 is the aisle.
    expect(seatAt(g, g.x[0]! - 4, g.y[0]!)).toBe(-1);
    expect(seatAt(g, g.x[17]! + 4, g.y[17]!)).toBe(-1);
    // Above the front row and below the back row.
    expect(seatAt(g, g.x[0]!, g.y[0]! - 4.5)).toBe(-1);
    expect(seatAt(g, g.x[503]!, g.y[503]! + 4.5)).toBe(-1);
    expect(seatAt(g, -50, -50)).toBe(-1);
    expect(seatAt(g, g.width + 50, g.height + 50)).toBe(-1);
  });

  it("gives a tap between two rows to the nearer row", () => {
    const g = buildGeometry(straightBlock());
    expect(seatAt(g, g.x[0]!, g.y[0]! + 3.5)).toBe(0);
    expect(seatAt(g, g.x[0]!, g.y[0]! + 4.5)).toBe(18);
  });

  it("gives a tap between two neighbours to the nearer one", () => {
    const g = buildGeometry(tinyBlock());
    const midX = (g.x[0]! + g.x[1]!) / 2;
    expect(seatAt(g, midX - 0.2, g.y[0]!)).toBe(0);
    expect(seatAt(g, midX + 0.2, g.y[0]!)).toBe(1);
  });

  it("reads only the cells round the tap, not every seat", () => {
    const g = buildGeometry(curvedBlock());
    const { grid } = g;
    let largest = 0;
    for (let c = 0; c < grid.cols * grid.rows; c++) largest = Math.max(largest, grid.offsets[c + 1]! - grid.offsets[c]!);
    // A cell is one row pitch square, so it can hold only a few seats; nine cells are read per tap.
    expect(largest).toBeLessThanOrEqual(4);
    expect(grid.offsets[grid.cols * grid.rows]).toBe(g.count);
    expect([...grid.items].sort((a, b) => a - b)).toEqual(Array.from({ length: g.count }, (_, i) => i));
  });
});

describe("applyDelta", () => {
  const delta = (changes: BlockSeatsDelta["changes"], marker = "m2"): BlockSeatsDelta => ({ kind: "delta", marker, poll_interval_seconds: 7, changes });

  it("changes only the named seats and takes the new marker", () => {
    const full = tinyBlock();
    const a2 = full.rows[0]!.seats[1]![T_ID];
    const c4 = full.rows[2]!.seats[3]![T_ID];
    const next = applyDelta(full, delta([[a2, "H"], [c4, "S"]]));
    expect(next.marker).toBe("m2");
    expect(next.poll_interval_seconds).toBe(7);
    const statuses = seatStatuses(next);
    expect(statuses[1]).toBe("H");
    expect(statuses[11]).toBe("S");
    expect(statuses.filter((s) => s === "A")).toHaveLength(10);
  });

  it("does not mutate the cached block", () => {
    const full = tinyBlock();
    const before = JSON.stringify(full);
    applyDelta(full, delta([[full.rows[0]!.seats[0]![T_ID], "S"]]));
    expect(JSON.stringify(full)).toBe(before);
  });

  it("keeps untouched rows and the block object, so geometry is not rebuilt", () => {
    const full = tinyBlock();
    const next = applyDelta(full, delta([[full.rows[1]!.seats[2]![T_ID], "H"]]));
    expect(next.block).toBe(full.block);
    expect(next.categories).toBe(full.categories);
    expect(next.rows[0]).toBe(full.rows[0]);
    expect(next.rows[2]).toBe(full.rows[2]);
    expect(next.rows[1]).not.toBe(full.rows[1]);
    // Everything but the status of the changed seat is the same.
    expect(next.rows[1]!.seats[2]!.slice(0, 6)).toEqual(full.rows[1]!.seats[2]!.slice(0, 6));
  });

  it("with no changes only moves the marker", () => {
    const full = tinyBlock();
    const next = applyDelta(full, delta([]));
    expect(next.rows).toBe(full.rows);
    expect(next.marker).toBe("m2");
  });

  it("ignores a seat that is not in this block", () => {
    const full = tinyBlock();
    const next = applyDelta(full, delta([[999_999_999, "S"]]));
    expect(next.rows).toBe(full.rows);
  });

  it("applies the last word when a seat appears twice, and a change back to available", () => {
    const full = tinyBlock();
    const id = full.rows[0]!.seats[0]![T_ID];
    const held = applyDelta(full, delta([[id, "H"]]));
    expect(held.rows[0]!.seats[0]![T_STATUS]).toBe("H");
    const freed = applyDelta(held, delta([[id, "S"], [id, "A"]], "m3"));
    expect(freed.rows[0]!.seats[0]![T_STATUS]).toBe("A");
  });
});

describe("mergePoll", () => {
  it("replaces the cache when the server answers a `since` request with a full block", () => {
    const cached = tinyBlock();
    const fresh = { ...straightBlock(), marker: "m9" };
    expect(mergePoll(cached, fresh)).toBe(fresh);
  });

  it("patches the cache with a delta", () => {
    const cached = tinyBlock();
    const id = cached.rows[0]!.seats[0]![T_ID];
    const merged = mergePoll(cached, { kind: "delta", marker: "m2", poll_interval_seconds: 5, changes: [[id, "S"]] });
    expect(merged?.rows[0]!.seats[0]![T_STATUS]).toBe("S");
  });

  it("leaves an empty cache empty on a delta (there is nothing to patch)", () => {
    expect(mergePoll(undefined, { kind: "delta", marker: "m2", poll_interval_seconds: 5, changes: [] })).toBeUndefined();
  });
});

describe("joinSeatLabel", () => {
  it("writes lettered rows as the server does", () => {
    expect(joinSeatLabel("C", 14)).toBe("C14");
    expect(joinSeatLabel("AA", 3)).toBe("AA3");
  });

  it("puts a dash between a numbered row and the seat, so row 12 seat 14 is not 1214", () => {
    expect(joinSeatLabel("12", 14)).toBe("12-14");
    expect(joinSeatLabel("1", 1)).toBe("1-1");
  });
});
