import { memo, useMemo } from "react";
import { PixelRatio, StyleSheet } from "react-native";
import Animated, { useAnimatedStyle } from "react-native-reanimated";
import Svg, { Path, Text as SvgText } from "react-native-svg";

import { font, palette } from "../../theme";
import { dotsPath, rectsPath, type SeatBatch, type SeatScene } from "./layers";
import type { SeatGeometry } from "./model";
import type { PanZoom } from "./usePanZoom";

interface Props {
  geometry: SeatGeometry;
  scene: SeatScene;
  panZoom: PanZoom;
}

// react-native-svg paints into a bitmap the size of the view, so a pinch scales that bitmap
// on the compositor and never re-renders the SVG. To stay sharp when zoomed in, the bitmap is
// drawn larger than the fitted size; the cap keeps it well inside texture and memory limits.
const MAX_BITMAP_EDGE_PX = 2600;
const OVERSAMPLE = 2.5;

function BatchPath({ batch, geometry }: { batch: SeatBatch; geometry: SeatGeometry }) {
  const w = geometry.seatW;
  if (batch.kind === "path") {
    return <Path d={batch.d} fill={batch.fill ?? "none"} stroke={batch.stroke ?? "none"} strokeWidth={batch.strokeWidth} strokeLinecap="round" strokeLinejoin="round" />;
  }
  if (batch.kind === "dots") return <Path d={dotsPath(geometry, batch.seats, batch.radius)} fill={batch.fill} />;
  const stroke = batch.strokeWidth * w;
  // A stroked square is inset by its stroke so the outline stays inside the seat's slot.
  return <Path d={rectsPath(geometry, batch.seats, batch.scale, stroke)} fill={batch.fill ?? "none"} stroke={batch.stroke ?? "none"} strokeWidth={stroke} />;
}

// One <Path> per batch: about a dozen SVG nodes for the whole block, however many seats.
const Batch = memo(BatchPath, (a, b) => a.geometry === b.geometry && a.batch.key === b.batch.key && sameBatch(a.batch, b.batch));

function sameBatch(a: SeatBatch, b: SeatBatch): boolean {
  if (a.kind === "path" && b.kind === "path") return a.d === b.d;
  if (a.kind !== "path" && b.kind !== "path") return a.seats.length === b.seats.length && a.seats.every((s, i) => s === b.seats[i]);
  return false;
}

export function SeatCanvasSvg({ geometry, scene, panZoom }: Props) {
  const base = useMemo(() => {
    const edge = Math.max(geometry.width, geometry.height);
    const cap = MAX_BITMAP_EDGE_PX / PixelRatio.get() / edge;
    return Math.min(panZoom.minScale * OVERSAMPLE, panZoom.maxScale, cap);
  }, [geometry.width, geometry.height, panZoom.minScale, panZoom.maxScale]);

  const { scale, translateX, translateY } = panZoom;
  const style = useAnimatedStyle(() => ({
    transform: [{ translateX: translateX.value }, { translateY: translateY.value }, { scale: scale.value / base }],
  }));

  const width = geometry.width * base;
  const height = geometry.height * base;
  const fontSize = geometry.pitchX * 0.62;

  return (
    <Animated.View style={[styles.layer, { width, height }, style]}>
      <Svg width={width} height={height} viewBox={`0 0 ${geometry.width} ${geometry.height}`}>
        {geometry.rowLabels.map((label, r) => (
          <SvgText key={r} x={geometry.rowLabelX[r]} y={geometry.rowLabelY[r]! + fontSize * 0.36} fontSize={fontSize} fontFamily={font.bodySemiBold} fill={palette.grey600} textAnchor="middle">
            {label}
          </SvgText>
        ))}
        {scene.batches.map((batch) => (
          <Batch key={batch.key} batch={batch} geometry={geometry} />
        ))}
      </Svg>
    </Animated.View>
  );
}

const styles = StyleSheet.create({
  layer: { position: "absolute", left: 0, top: 0, transformOrigin: "top left" },
});
