// Pan and zoom arithmetic for the block canvas. Each function is a worklet so the gesture
// handlers can call it on the UI thread, and plain enough to be unit-tested in Node.
//
// The transform is: screen = translate + content × scale. `scale` is screen points per plan
// unit, so it is the same number for the SVG and the Skia renderer.

export interface ViewportLimits {
  viewW: number;
  viewH: number;
  contentW: number;
  contentH: number;
  minScale: number;
  maxScale: number;
}

/** A seat pitch this many points wide is comfortable to tap. Taps below it zoom in instead. */
export const TAP_ZOOM_POINTS = 30;
/** Below this many points per seat pitch a tap cannot reliably hit one seat. */
export const MIN_TAPPABLE_POINTS = 17;
/** The most a seat pitch may grow to. */
export const MAX_SEAT_POINTS = 52;

/** The scale at which the whole block fits the view. */
export function fitScale(viewW: number, viewH: number, contentW: number, contentH: number): number {
  "worklet";
  if (viewW <= 0 || viewH <= 0 || contentW <= 0 || contentH <= 0) return 1;
  return Math.min(viewW / contentW, viewH / contentH);
}

export function scaleLimits(viewW: number, viewH: number, contentW: number, contentH: number, pitch: number): { minScale: number; maxScale: number } {
  "worklet";
  const fit = fitScale(viewW, viewH, contentW, contentH);
  // A small block already fits with large seats; it still gets some room to zoom.
  return { minScale: fit, maxScale: Math.max(fit * 1.5, MAX_SEAT_POINTS / Math.max(pitch, 0.0001)) };
}

export function clampScale(scale: number, minScale: number, maxScale: number): number {
  "worklet";
  return Math.min(maxScale, Math.max(minScale, scale));
}

/**
 * Keeps the block on screen along one axis: centred when it is smaller than the view,
 * otherwise never dragged so far that a gap opens at either edge.
 */
export function clampTranslate(translate: number, viewSize: number, contentSize: number, scale: number): number {
  "worklet";
  const scaled = contentSize * scale;
  if (scaled <= viewSize) return (viewSize - scaled) / 2;
  return Math.min(0, Math.max(viewSize - scaled, translate));
}

/** The translate that keeps the content point under `focal` fixed while the scale changes. */
export function zoomAbout(focal: number, translate: number, scale: number, nextScale: number): number {
  "worklet";
  return focal - ((focal - translate) / scale) * nextScale;
}

/** Screen point to content (plan-unit) coordinate. */
export function toContent(screen: number, translate: number, scale: number): number {
  "worklet";
  return (screen - translate) / scale;
}
