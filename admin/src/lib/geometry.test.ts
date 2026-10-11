import { describe, expect, it } from "vitest";
import { blocksInBox, boxFromCorners, placementFromBox, polygonPath, polygonTouchesBox, rowsInBox, type Point } from "./geometry";

const square = (x: number, y: number, size = 10): Point[] => [
  [x, y],
  [x + size, y],
  [x + size, y + size],
  [x, y + size],
];

const blocks = [
  { id: 1, outline: square(0, 0) },
  { id: 2, outline: square(20, 0) },
  { id: 3, outline: square(40, 0) },
  { id: 4, outline: [] as Point[] }, // placed but no seats yet
];

describe("drag box", () => {
  it("is the same box whichever way it is dragged", () => {
    expect(boxFromCorners([30, 8], [-1, -1])).toEqual({ minX: -1, minY: -1, maxX: 30, maxY: 8 });
    expect(boxFromCorners([-1, -1], [30, 8])).toEqual(boxFromCorners([30, 8], [-1, -1]));
  });

  it("selects the blocks it covers or touches and no others", () => {
    expect(blocksInBox(blocks, boxFromCorners([-1, -1], [31, 11]))).toEqual([1, 2]);
    expect(blocksInBox(blocks, boxFromCorners([5, 5], [25, 6]))).toEqual([1, 2]);
    expect(blocksInBox(blocks, boxFromCorners([11, 0], [19, 10]))).toEqual([]);
    expect(blocksInBox(blocks, boxFromCorners([100, 100], [200, 200]))).toEqual([]);
  });

  it("selects a block the box is wholly inside", () => {
    expect(blocksInBox(blocks, boxFromCorners([44, 4], [46, 6]))).toEqual([3]);
  });

  it("selects a block whose edges cross the box with no corner inside either", () => {
    // A tall thin box through the middle of a wide block: a plus sign.
    const wide: Point[] = [
      [0, 40],
      [100, 40],
      [100, 50],
      [0, 50],
    ];
    expect(polygonTouchesBox(wide, boxFromCorners([45, 0], [55, 100]))).toBe(true);
  });

  it("does not select across the gap of a curved block", () => {
    // An L-shaped block: the box sits in the notch, inside the bounding box but outside the shape.
    const lShape: Point[] = [
      [0, 0],
      [30, 0],
      [30, 10],
      [10, 10],
      [10, 30],
      [0, 30],
    ];
    expect(polygonTouchesBox(lShape, boxFromCorners([15, 15], [28, 28]))).toBe(false);
    expect(polygonTouchesBox(lShape, boxFromCorners([5, 15], [28, 28]))).toBe(true);
  });

  it("never selects a block with no outline", () => {
    expect(blocksInBox(blocks, boxFromCorners([-1000, -1000], [1000, 1000]))).toEqual([1, 2, 3]);
  });

  it("selects rows by seat centre", () => {
    const rows = [{ seats: [[0, 0], [5, 0]] as Point[] }, { seats: [[0, 8], [5, 8]] as Point[] }, { seats: [[0, 16]] as Point[] }];
    expect(rowsInBox(rows, boxFromCorners([4, -1], [6, 9]))).toEqual([0, 1]);
    expect(rowsInBox(rows, boxFromCorners([1, 1], [4, 7]))).toEqual([]);
  });
});

describe("plan drawing", () => {
  it("writes a closed path", () => {
    expect(polygonPath(square(0, 0, 2))).toBe("M0 0L2 0L2 2L0 2Z");
    expect(polygonPath([])).toBe("");
  });

  it("turns a dragged rectangle into an origin and whole rows and seats", () => {
    // The contract's own example: 2 seats at pitch 5 and 2 rows at pitch 8 fill 4010..4020 by 590..606.
    expect(placementFromBox({ minX: 4010, minY: 590, maxX: 4020, maxY: 606 }, 5, 8)).toEqual({
      originX: 4012.5,
      originY: 594,
      seatsPerRow: 2,
      rows: 2,
    });
    expect(placementFromBox({ minX: 0, minY: 0, maxX: 1, maxY: 1 }, 5, 8)).toMatchObject({ seatsPerRow: 1, rows: 1 });
  });
});
