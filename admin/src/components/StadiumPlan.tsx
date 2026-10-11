import { useId, useMemo, useRef, useState, type KeyboardEvent, type PointerEvent, type ReactNode } from "react";
import { boundsOf, boxFromCorners, polygonPath, type Box, type Point } from "../lib/geometry";
import type { Block, Stand, VenueDetail } from "../lib/queries";

/** Pixels the pointer must travel before a press becomes a drag box instead of a click. */
const DRAG_THRESHOLD = 5;

export interface PlanBlockLook {
  /** A CSS colour, or null to draw the block hatched as "nothing assigned". */
  fill: string | null;
  /** Spoken and shown on hover: what this block is and what state it is in. */
  title: string;
  /** Draws a small dot on the block (for example "has row overrides"). */
  marked?: boolean;
}

interface StadiumPlanProps {
  venue: VenueDetail;
  /** Names the drawing for screen readers. */
  label: string;
  look: (block: Block, stand: Stand) => PlanBlockLook;
  selected?: ReadonlySet<number>;
  /** Makes blocks focusable buttons. `additive` is true with Shift, Ctrl or ⌘ held. */
  onBlockPress?: (block: Block, additive: boolean) => void;
  /** Enables the drag box. Called with the box in plan coordinates when the drag ends. */
  onBoxDrag?: (box: Box, additive: boolean) => void;
  /** A mouse always drags a box. A finger scrolls the page unless this is on. */
  touchDrag?: boolean;
  /** Zoom to this part of the plan. Null shows all of it. */
  view?: Box | null;
  /** Extra drawing on top of the blocks, in plan coordinates. */
  children?: ReactNode;
}

/** Where to write a stand's name: just outside the stand, on the side of the plan it sits on. */
function standLabels(venue: VenueDetail) {
  const groups = new Map<string, Point[]>();
  for (const stand of venue.stands) {
    groups.set(stand.name, [...(groups.get(stand.name) ?? []), ...stand.polygon]);
  }
  const all = boundsOf(venue.stands.flatMap((stand) => stand.polygon));
  if (!all) return [];
  const cx = venue.plan_width / 2;
  const cy = venue.plan_height / 2;
  return [...groups.entries()].flatMap(([name, points]) => {
    const box = boundsOf(points);
    if (!box) return [];
    const dx = ((box.minX + box.maxX) / 2 - cx) / venue.plan_width;
    const dy = ((box.minY + box.maxY) / 2 - cy) / venue.plan_height;
    if (Math.abs(dy) >= Math.abs(dx)) {
      return [{ name, x: cx, y: dy < 0 ? all.minY - 28 : all.maxY + 64, rotate: 0 }];
    }
    return [{ name, x: dx < 0 ? all.minX - 36 : all.maxX + 36, y: cy, rotate: dx < 0 ? -90 : 90 }];
  });
}

/**
 * The stadium from above: stands and block outlines only, never seats. One SVG path per
 * block (96 for Talanta), so it stays light however many seats the venue has.
 */
export function StadiumPlan({ venue, label, look, selected, onBlockPress, onBoxDrag, touchDrag, view, children }: StadiumPlanProps) {
  const svgRef = useRef<SVGSVGElement>(null);
  const hatchId = useId();
  const press = useRef<{ id: number; x: number; y: number; start: Point; dragging: boolean } | null>(null);
  const suppressClick = useRef(false);
  const [dragBox, setDragBox] = useState<Box | null>(null);
  const labels = useMemo(() => standLabels(venue), [venue]);

  const viewBox = view
    ? `${view.minX} ${view.minY} ${Math.max(1, view.maxX - view.minX)} ${Math.max(1, view.maxY - view.minY)}`
    : `0 0 ${venue.plan_width} ${venue.plan_height}`;

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
    if (!onBoxDrag || event.button !== 0) return;
    if (event.pointerType !== "mouse" && !touchDrag) return;
    press.current = {
      id: event.pointerId,
      x: event.clientX,
      y: event.clientY,
      start: toPlan(event.clientX, event.clientY),
      dragging: false,
    };
  }

  function pointerMove(event: PointerEvent<SVGSVGElement>) {
    const current = press.current;
    if (!current || current.id !== event.pointerId) return;
    if (!current.dragging) {
      if (Math.hypot(event.clientX - current.x, event.clientY - current.y) < DRAG_THRESHOLD) return;
      current.dragging = true;
      // Captured only once it is a drag, so that a plain click still reaches the block under it.
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
    // The browser sends a click after the drag ends; it must not toggle the block under it.
    suppressClick.current = true;
    window.setTimeout(() => (suppressClick.current = false), 0);
    if (!cancelled) {
      onBoxDrag?.(
        boxFromCorners(current.start, toPlan(event.clientX, event.clientY)),
        event.shiftKey || event.ctrlKey || event.metaKey,
      );
    }
  }

  return (
    <svg
      ref={svgRef}
      className="plan"
      viewBox={viewBox}
      role="group"
      aria-label={label}
      style={{ touchAction: onBoxDrag && touchDrag ? "none" : undefined, cursor: onBoxDrag ? "crosshair" : undefined }}
      onPointerDown={pointerDown}
      onPointerMove={pointerMove}
      onPointerUp={(event) => pointerEnd(event, false)}
      onPointerCancel={(event) => pointerEnd(event, true)}
    >
      <defs>
        <pattern id={hatchId} width="14" height="14" patternUnits="userSpaceOnUse" patternTransform="rotate(45)">
          <rect width="14" height="14" className="plan__hatch-bg" />
          <line x1="0" y1="0" x2="0" y2="14" className="plan__hatch-line" />
        </pattern>
      </defs>

      {venue.stands.map((stand) => (
        <path key={stand.id} className="plan__stand" d={polygonPath(stand.polygon)} />
      ))}
      {labels.map((item) => (
        <text
          key={item.name}
          className="plan__stand-label"
          x={item.x}
          y={item.y}
          textAnchor="middle"
          transform={item.rotate ? `rotate(${item.rotate} ${item.x} ${item.y})` : undefined}
        >
          {item.name}
        </text>
      ))}

      {venue.stands.map((stand) =>
        stand.blocks.map((block) => {
          const { fill, title, marked } = look(block, stand);
          const isSelected = selected?.has(block.id) ?? false;
          const interactive = onBlockPress
            ? {
                tabIndex: 0,
                role: "button",
                "aria-pressed": selected ? isSelected : undefined,
                "aria-label": title,
                onClick: (event: { shiftKey: boolean; ctrlKey: boolean; metaKey: boolean }) => {
                  if (suppressClick.current) return;
                  onBlockPress(block, event.shiftKey || event.ctrlKey || event.metaKey);
                },
                onKeyDown: (event: KeyboardEvent<SVGElement>) => {
                  if (event.key !== "Enter" && event.key !== " ") return;
                  event.preventDefault();
                  onBlockPress(block, event.shiftKey || event.ctrlKey || event.metaKey);
                },
              }
            : {};
          const className = ["plan__block", onBlockPress && "plan__block--button", isSelected && "plan__block--selected"]
            .filter(Boolean)
            .join(" ");
          if (block.outline.length === 0) {
            // Placed but no seats yet: there is no outline, so mark where its first seat will be.
            return (
              <g key={block.id} className={className} {...interactive}>
                <title>{title}</title>
                <circle cx={block.origin_x} cy={block.origin_y} r="5" className="plan__origin" />
                <circle cx={block.origin_x} cy={block.origin_y} r="14" className="plan__origin-ring" />
              </g>
            );
          }
          const centre = marked ? boundsOf(block.outline) : null;
          return (
            <g key={block.id} className={className} {...interactive}>
              <title>{title}</title>
              <path d={polygonPath(block.outline)} fill={fill ?? `url(#${hatchId})`} />
              {centre && (
                <circle
                  className="plan__mark"
                  cx={(centre.minX + centre.maxX) / 2}
                  cy={(centre.minY + centre.maxY) / 2}
                  r="9"
                />
              )}
            </g>
          );
        }),
      )}

      {children}

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

/** A box around some points with room to spare, for zooming the plan onto one block. */
export function zoomBox(points: readonly Point[], margin: number, limits: { width: number; height: number }): Box | null {
  const box = boundsOf(points);
  if (!box) return null;
  // Keep the plan's proportions so the drawing does not jump in height when zooming.
  const aspect = limits.width / limits.height;
  let width = box.maxX - box.minX + margin * 2;
  let height = box.maxY - box.minY + margin * 2;
  if (width / height > aspect) height = width / aspect;
  else width = height * aspect;
  const cx = (box.minX + box.maxX) / 2;
  const cy = (box.minY + box.maxY) / 2;
  return { minX: cx - width / 2, minY: cy - height / 2, maxX: cx + width / 2, maxY: cy + height / 2 };
}
