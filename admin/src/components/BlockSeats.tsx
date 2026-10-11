import { useMemo, useRef, useState, type PointerEvent } from "react";
import { boxFromCorners, rowsInBox, type Box, type Point } from "../lib/geometry";
import type { BlockLayout } from "../lib/queries";

const DRAG_THRESHOLD = 5;

interface BlockSeatsProps {
  layout: BlockLayout;
  label: string;
  /** Colour of a row's seats, or null for "no category". */
  rowFill: (rowIndex: number) => string | null;
  selectedRows?: ReadonlySet<number>;
  /** Called with row indexes when rows are clicked or boxed. Omit for a read-only drawing. */
  onSelectRows?: (rowIndexes: number[], mode: "toggle" | "replace" | "add") => void;
  touchDrag?: boolean;
}

/**
 * One open block, seat by seat. This is the only place seats are drawn, and only ever for
 * one block (at most about 1,300 circles).
 */
export function BlockSeats({ layout, label, rowFill, selectedRows, onSelectRows, touchDrag }: BlockSeatsProps) {
  const svgRef = useRef<SVGSVGElement>(null);
  const press = useRef<{ id: number; x: number; y: number; start: Point; dragging: boolean } | null>(null);
  const suppressClick = useRef(false);
  const [dragBox, setDragBox] = useState<Box | null>(null);

  const rows = useMemo(
    () => layout.rows.map((row) => ({ seats: row.seats.map((seat): Point => [seat[2], seat[3]]) })),
    [layout],
  );
  // Seat size from the real spacing: the gap between the first two seats of the first full row.
  const radius = useMemo(() => {
    const row = rows.find((candidate) => candidate.seats.length > 1);
    if (!row) return 2;
    const [a, b] = [row.seats[0]!, row.seats[1]!];
    return Math.max(0.6, Math.hypot(b[0] - a[0], b[1] - a[1]) * 0.36);
  }, [rows]);

  const bounds = layout.bounds;
  if (!bounds || layout.seat_count === 0) return null;
  const pad = radius * 4;
  const viewBox = `${bounds.min_x - pad} ${bounds.min_y - pad} ${bounds.max_x - bounds.min_x + pad * 2} ${bounds.max_y - bounds.min_y + pad * 2}`;

  function toPlan(clientX: number, clientY: number): Point {
    const svg = svgRef.current;
    const matrix = svg?.getScreenCTM();
    if (!svg || !matrix) return [0, 0];
    const point = svg.createSVGPoint();
    point.x = clientX;
    point.y = clientY;
    const mapped = point.matrixTransform(matrix.inverse());
    return [mapped.x, mapped.y];
  }

  function pointerDown(event: PointerEvent<SVGSVGElement>) {
    if (!onSelectRows || event.button !== 0) return;
    if (event.pointerType !== "mouse" && !touchDrag) return;
    press.current = { id: event.pointerId, x: event.clientX, y: event.clientY, start: toPlan(event.clientX, event.clientY), dragging: false };
  }

  function pointerMove(event: PointerEvent<SVGSVGElement>) {
    const current = press.current;
    if (!current || current.id !== event.pointerId) return;
    if (!current.dragging) {
      if (Math.hypot(event.clientX - current.x, event.clientY - current.y) < DRAG_THRESHOLD) return;
      current.dragging = true;
      event.currentTarget.setPointerCapture(event.pointerId);
    }
    setDragBox(boxFromCorners(current.start, toPlan(event.clientX, event.clientY)));
  }

  function pointerEnd(event: PointerEvent<SVGSVGElement>, cancelled: boolean) {
    const current = press.current;
    if (!current || current.id !== event.pointerId) return;
    press.current = null;
    if (!current.dragging) return;
    setDragBox(null);
    suppressClick.current = true;
    window.setTimeout(() => (suppressClick.current = false), 0);
    if (cancelled) return;
    const box = boxFromCorners(current.start, toPlan(event.clientX, event.clientY));
    onSelectRows?.(rowsInBox(rows, box), event.shiftKey || event.ctrlKey || event.metaKey ? "add" : "replace");
  }

  return (
    <svg
      ref={svgRef}
      className="seatplan"
      viewBox={viewBox}
      role="img"
      aria-label={label}
      style={{ touchAction: onSelectRows && touchDrag ? "none" : undefined, cursor: onSelectRows ? "crosshair" : undefined }}
      onPointerDown={pointerDown}
      onPointerMove={pointerMove}
      onPointerUp={(event) => pointerEnd(event, false)}
      onPointerCancel={(event) => pointerEnd(event, true)}
    >
      {layout.rows.map((row, rowIndex) => {
        const fill = rowFill(rowIndex);
        const isSelected = selectedRows?.has(rowIndex) ?? false;
        return (
          <g
            key={row.row_index}
            className={["seatplan__row", isSelected && "seatplan__row--selected", fill === null && "seatplan__row--none"]
              .filter(Boolean)
              .join(" ")}
            fill={fill ?? undefined}
            onClick={() => {
              if (suppressClick.current || !onSelectRows) return;
              onSelectRows([rowIndex], "toggle");
            }}
          >
            <title>{`Row ${row.label}, ${row.seats.length} seats`}</title>
            {row.seats.map((seat) => (
              <circle key={seat[1]} cx={seat[2]} cy={seat[3]} r={radius} opacity={seat[5] === 1 ? 1 : 0.25} />
            ))}
          </g>
        );
      })}
      {dragBox && (
        <rect
          className="plan__dragbox"
          x={dragBox.minX}
          y={dragBox.minY}
          width={dragBox.maxX - dragBox.minX}
          height={dragBox.maxY - dragBox.minY}
        />
      )}
    </svg>
  );
}
