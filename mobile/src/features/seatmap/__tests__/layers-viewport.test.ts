import { describe, expect, it } from "vitest";

import { buildScene, dotsPath, isSelectable, rectsPath, seatLook, SEAT_COLORS } from "../layers";
import { buildGeometry, seatStatuses, T_ID } from "../model";
import { boundsOf, labelPoint, pointInPolygon, priceTints, SOLD_OUT_FILL, textOn } from "../overview";
import { clampScale, clampTranslate, fitScale, MAX_SEAT_POINTS, scaleLimits, toContent, zoomAbout } from "../viewport";
import { curvedBlock, tinyBlock } from "./fixtures";

describe("seatLook (§5.3)", () => {
  it("maps status and selection to a look", () => {
    expect(seatLook("A", false, false)).toBe("available");
    expect(seatLook("A", true, false)).toBe("selected");
    expect(seatLook("M", false, false)).toBe("mine");
    expect(seatLook("H", false, false)).toBe("held");
    expect(seatLook("S", false, false)).toBe("sold");
    expect(seatLook("B", false, false)).toBe("blocked");
  });

  it("greys out a seat the server just refused, whatever the cache still says", () => {
    expect(seatLook("A", false, true)).toBe("sold");
    expect(seatLook("H", false, true)).toBe("sold");
    expect(seatLook("M", false, true)).toBe("mine");
  });

  it("lets only free seats change the selection", () => {
    expect(isSelectable("available")).toBe(true);
    expect(isSelectable("selected")).toBe(true);
    for (const look of ["sold", "held", "blocked", "mine"] as const) expect(isSelectable(look)).toBe(false);
  });
});

describe("buildScene", () => {
  function scene() {
    const full = tinyBlock();
    const row = full.rows[0]!.seats;
    row[0]![6] = "S";
    row[1]![6] = "H";
    row[2]![6] = "B";
    row[3]![4] = 1; // accessible
    full.rows[1]!.seats[0]![5] = 1; // second category
    full.rows[1]!.seats[1]![4] = 3; // restricted view
    const geometry = buildGeometry(full);
    const selectedId = full.rows[2]!.seats[0]![T_ID];
    const lostId = full.rows[2]!.seats[1]![T_ID];
    const built = buildScene({ geometry, statuses: seatStatuses(full), categories: full.categories, selected: new Set([selectedId]), lost: new Set([lostId]) });
    return { full, geometry, ...built };
  }

  it("groups seats that look the same into one batch each", () => {
    const { batches, geometry } = scene();
    const byKey = new Map(batches.map((b) => [b.key, b]));
    expect([...byKey.keys()].sort()).toEqual(["blocked", "blocked-slash", "cat-0", "cat-1", "chosen", "chosen-tick", "glyph-fill", "glyph-stroke", "held", "sold"]);
    // Far fewer draw batches than seats, and it stays that way on the largest block.
    expect(batches.length).toBeLessThan(geometry.count);
    const big = curvedBlock();
    const bigScene = buildScene({ geometry: buildGeometry(big), statuses: seatStatuses(big), categories: big.categories, selected: new Set(), lost: new Set() });
    expect(bigScene.batches).toHaveLength(1);
    expect(bigScene.looks).toHaveLength(1312);
  });

  it("colours free seats by their category and the rest by state, never by category", () => {
    const { batches, full } = scene();
    const find = (key: string) => batches.find((b) => b.key === key)!;
    expect(find("cat-0")).toMatchObject({ kind: "rects", fill: full.categories[0]!.color_hex });
    expect(find("cat-1")).toMatchObject({ kind: "rects", fill: full.categories[1]!.color_hex, seats: [4] });
    expect(find("sold")).toMatchObject({ kind: "dots", fill: SEAT_COLORS.sold });
    expect(find("held")).toMatchObject({ kind: "rects", fill: SEAT_COLORS.heldFill, stroke: SEAT_COLORS.heldStroke });
    expect(find("chosen")).toMatchObject({ kind: "rects", fill: SEAT_COLORS.selected, seats: [8] });
  });

  it("gives sold, held, blocked and chosen seats different shapes, not only different colours", () => {
    const { batches } = scene();
    const kinds = Object.fromEntries(batches.map((b) => [b.key, b.kind]));
    expect(kinds.sold).toBe("dots"); // a dot
    expect(kinds.held).toBe("rects"); // a hollow outline
    expect(batches.find((b) => b.key === "held")).toMatchObject({ strokeWidth: expect.any(Number) });
    expect(kinds["blocked-slash"]).toBe("path"); // a stroke through it
    expect(kinds["chosen-tick"]).toBe("path"); // a tick
  });

  it("reports each seat's look for the tap handler, including the lost seat as sold", () => {
    const { looks } = scene();
    expect(looks.slice(0, 4)).toEqual(["sold", "held", "blocked", "available"]);
    expect(looks[8]).toBe("selected");
    expect(looks[9]).toBe("sold");
  });

  it("draws the wheelchair and restricted-view marks only on free seats", () => {
    const { batches } = scene();
    expect(batches.find((b) => b.key === "glyph-stroke")).toBeDefined();
    const full = tinyBlock();
    full.rows[0]!.seats[0]![4] = 1;
    full.rows[0]!.seats[0]![6] = "S";
    const sold = buildScene({ geometry: buildGeometry(full), statuses: seatStatuses(full), categories: full.categories, selected: new Set(), lost: new Set() });
    expect(sold.batches.find((b) => b.key.startsWith("glyph"))).toBeUndefined();
  });

  it("falls back to a neutral colour when a seat's category is missing from the answer", () => {
    const full = tinyBlock();
    full.rows[0]!.seats[0]![5] = 9;
    const s = buildScene({ geometry: buildGeometry(full), statuses: seatStatuses(full), categories: full.categories, selected: new Set(), lost: new Set() });
    expect(s.batches.find((b) => b.key === "cat-9")).toMatchObject({ fill: SEAT_COLORS.unknownCategory });
  });

  it("writes one closed sub-path per seat", () => {
    const g = buildGeometry(tinyBlock());
    expect(rectsPath(g, [0, 1, 2], 1).match(/z/g)).toHaveLength(3);
    expect(dotsPath(g, [0, 1], 0.3).match(/z/g)).toHaveLength(2);
    expect(rectsPath(g, [], 1)).toBe("");
  });
});

describe("viewport", () => {
  it("fits the whole block in the view", () => {
    expect(fitScale(400, 600, 200, 100)).toBe(2);
    expect(fitScale(400, 100, 200, 100)).toBe(1);
    expect(fitScale(0, 0, 200, 100)).toBe(1);
  });

  it("limits zoom between 'whole block' and a comfortable seat size", () => {
    const { minScale, maxScale } = scaleLimits(400, 500, 437, 150, 5);
    expect(minScale).toBeCloseTo(400 / 437);
    expect(maxScale).toBeCloseTo(MAX_SEAT_POINTS / 5);
    expect(clampScale(0.1, minScale, maxScale)).toBe(minScale);
    expect(clampScale(99, minScale, maxScale)).toBe(maxScale);
    expect(clampScale(3, minScale, maxScale)).toBe(3);
  });

  it("centres content smaller than the view and never lets a gap open at an edge", () => {
    expect(clampTranslate(-999, 400, 100, 2)).toBe(100);
    expect(clampTranslate(50, 400, 300, 2)).toBe(0);
    expect(clampTranslate(-500, 400, 300, 2)).toBe(-200);
    expect(clampTranslate(-120, 400, 300, 2)).toBe(-120);
  });

  it("keeps the point under the fingers fixed while zooming", () => {
    const focal = 180;
    const translate = -40;
    const before = toContent(focal, translate, 2);
    const next = zoomAbout(focal, translate, 2, 5);
    expect(toContent(focal, next, 5)).toBeCloseTo(before);
  });
});

describe("overview helpers (levels 1 and 2)", () => {
  it("tints stands by price rank and greys a sold-out stand", () => {
    const tints = priceTints([100_000, 50_000, null, 1_000_000, 100_000]);
    expect(tints[2]).toBe(SOLD_OUT_FILL);
    expect(tints[0]).toBe(tints[4]);
    const lightness = (hex: string) => parseInt(hex.slice(1, 3), 16) + parseInt(hex.slice(3, 5), 16) + parseInt(hex.slice(5, 7), 16);
    // Cheapest is lightest, dearest is darkest.
    expect(lightness(tints[1]!)).toBeGreaterThan(lightness(tints[0]!));
    expect(lightness(tints[0]!)).toBeGreaterThan(lightness(tints[3]!));
    expect(textOn(tints[3]!)).toBe("#ffffff");
  });

  it("puts a label inside a C-shaped stand, where the centroid is not", () => {
    // A band that runs up one side, across the top and down the other.
    const c: [number, number][] = [[0, 0], [100, 0], [100, 100], [80, 100], [80, 20], [20, 20], [20, 100], [0, 100]];
    expect(pointInPolygon(50, 60, c)).toBe(false);
    const [x, y] = labelPoint(c);
    expect(pointInPolygon(x, y, c)).toBe(true);
  });

  it("measures the box round polygons", () => {
    expect(boundsOf([[[10, 20], [30, 5]], [[0, 40], [15, 15]]], 2)).toEqual({ x: -2, y: 3, width: 34, height: 39 });
    expect(boundsOf([])).toEqual({ x: 0, y: 0, width: 1, height: 1 });
  });
});
