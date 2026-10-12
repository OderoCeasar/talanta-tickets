import { forwardRef, useCallback, useImperativeHandle, useMemo, useState } from "react";
import { Pressable, StyleSheet, Text, View, type LayoutChangeEvent } from "react-native";
import { GestureDetector } from "react-native-gesture-handler";

import { palette, shadow, space, type } from "../../theme";
import { Icon } from "../../ui";
import type { SeatLook, SeatScene } from "./layers";
import { pickRenderer, seatAt, type SeatGeometry } from "./model";
import { SeatCanvasSkia } from "./SeatCanvasSkia";
import { SeatCanvasSvg } from "./SeatCanvasSvg";
import { usePanZoom } from "./usePanZoom";

interface Props {
  geometry: SeatGeometry;
  scene: SeatScene;
  /** Called with the seat's index in the geometry and how it is drawn. */
  onSeatPress: (index: number, look: SeatLook) => void;
}

export interface SeatCanvasHandle {
  /** Zooms to the given seats (geometry indexes), for "best available". */
  focusSeats: (indexes: readonly number[]) => void;
}

interface InnerProps extends Props {
  width: number;
  height: number;
}

/**
 * The level-3 canvas. The screen uses this one component; whether SVG or Skia draws the
 * seats is decided here from the seat count (ARCHITECTURE §5.2), and both share the same
 * gestures, the same scene and the same single tap handler.
 */
const Inner = forwardRef<SeatCanvasHandle, InnerProps>(function Inner({ geometry, scene, onSeatPress, width, height }, ref) {
  const renderer = pickRenderer(geometry.count);

  // The only touch handler for seats: a point in block coordinates, looked up in the grid.
  const onTap = useCallback(
    (x: number, y: number) => {
      const index = seatAt(geometry, x, y);
      if (index >= 0) onSeatPress(index, scene.looks[index]!);
    },
    [geometry, scene, onSeatPress],
  );

  const panZoom = usePanZoom({ viewW: width, viewH: height, contentW: geometry.width, contentH: geometry.height, pitch: geometry.pitchX, onTap });

  useImperativeHandle(
    ref,
    () => ({
      focusSeats: (indexes) => {
        if (indexes.length === 0) return;
        let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
        for (const k of indexes) {
          minX = Math.min(minX, geometry.x[k]!);
          maxX = Math.max(maxX, geometry.x[k]!);
          minY = Math.min(minY, geometry.y[k]!);
          maxY = Math.max(maxY, geometry.y[k]!);
        }
        panZoom.focus({ x: minX, y: minY, width: maxX - minX, height: maxY - minY });
      },
    }),
    [geometry, panZoom],
  );

  return (
    <>
      <GestureDetector gesture={panZoom.gesture}>
        <View style={styles.surface} collapsable={false} testID="seat-canvas" accessibilityLabel={`Seat map, ${geometry.count} seats. Pinch to zoom, drag to move, tap a seat to choose it.`}>
          {renderer === "skia" ? (
            <SeatCanvasSkia geometry={geometry} scene={scene} panZoom={panZoom} width={width} height={height} />
          ) : (
            <SeatCanvasSvg geometry={geometry} scene={scene} panZoom={panZoom} />
          )}
        </View>
      </GestureDetector>
      <View style={styles.controls}>
        <ZoomButton icon="plus" label="Zoom in" testID="zoom-in" onPress={() => panZoom.zoomBy(1.8)} />
        <ZoomButton icon="minus" label="Zoom out" testID="zoom-out" onPress={() => panZoom.zoomBy(1 / 1.8)} />
        <ZoomButton icon="fit" label="Show whole block" testID="zoom-fit" onPress={panZoom.reset} />
      </View>
    </>
  );
});

function ZoomButton({ icon, label, onPress, testID }: { icon: "plus" | "minus" | "fit"; label: string; onPress: () => void; testID: string }) {
  return (
    <Pressable onPress={onPress} accessibilityRole="button" accessibilityLabel={label} testID={testID} style={({ pressed }) => [styles.zoomButton, pressed && { backgroundColor: palette.ink100 }]}>
      <Icon name={icon} size={20} />
    </Pressable>
  );
}

export const SeatCanvas = forwardRef<SeatCanvasHandle, Props>(function SeatCanvas(props, ref) {
  const [size, setSize] = useState<{ width: number; height: number } | null>(null);
  const onLayout = useCallback((e: LayoutChangeEvent) => {
    const { width, height } = e.nativeEvent.layout;
    setSize((prev) => (prev && prev.width === width && prev.height === height ? prev : { width, height }));
  }, []);
  const ready = useMemo(() => size !== null && size.width > 0 && size.height > 0, [size]);

  // The strip sits above the canvas, not over it, so it never covers the front row.
  return (
    <View style={styles.root}>
      <View style={styles.strip}>
        <Text style={styles.pitchText} maxFontSizeMultiplier={1.3}>
          PITCH THIS WAY ↑
        </Text>
        {__DEV__ ? (
          <Text style={styles.renderer} testID="seat-renderer">
            {pickRenderer(props.geometry.count)} · {props.geometry.count} seats
          </Text>
        ) : null}
      </View>
      <View style={styles.view} onLayout={onLayout}>
        {ready && size ? <Inner ref={ref} {...props} width={size.width} height={size.height} /> : null}
      </View>
    </View>
  );
});

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: palette.white },
  view: { flex: 1, overflow: "hidden" },
  strip: { alignItems: "center", justifyContent: "center", paddingVertical: 5, backgroundColor: palette.ink50, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: palette.ink200 },
  surface: { position: "absolute", left: 0, right: 0, top: 0, bottom: 0, overflow: "hidden" },
  pitchText: { ...type.overline, color: palette.emeraldInk, fontSize: 11 },
  controls: { position: "absolute", right: space.md, bottom: space.md, gap: space.sm },
  zoomButton: {
    width: 46,
    height: 46,
    borderRadius: 23,
    backgroundColor: palette.white,
    alignItems: "center",
    justifyContent: "center",
    borderWidth: 1,
    borderColor: palette.grey300,
    ...shadow.card,
  },
  renderer: { ...type.small, position: "absolute", right: space.sm, fontSize: 10, lineHeight: 14, color: palette.grey400 },
});
