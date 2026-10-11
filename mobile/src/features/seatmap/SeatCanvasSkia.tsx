import { Canvas, Group, matchFont, PaintStyle, Picture, Skia, StrokeCap, StrokeJoin } from "@shopify/react-native-skia";
import { useMemo } from "react";
import { Platform, StyleSheet } from "react-native";
import { useDerivedValue } from "react-native-reanimated";

import { palette } from "../../theme";
import type { SeatScene } from "./layers";
import type { SeatGeometry } from "./model";
import type { PanZoom } from "./usePanZoom";

interface Props {
  geometry: SeatGeometry;
  scene: SeatScene;
  panZoom: PanZoom;
  width: number;
  height: number;
}

/**
 * Draws every seat into one recorded picture. React sees a single <Picture>: there is no
 * component, node or handler per seat. The picture is re-recorded only when a seat's look
 * changes; zooming and panning replay it under a transform on the UI thread.
 */
function record(geometry: SeatGeometry, scene: SeatScene) {
  const recorder = Skia.PictureRecorder();
  const canvas = recorder.beginRecording(Skia.XYWHRect(0, 0, geometry.width, geometry.height));
  const w = geometry.seatW;
  const h = geometry.seatH;

  const fill = Skia.Paint();
  fill.setAntiAlias(true);
  fill.setStyle(PaintStyle.Fill);
  const stroke = Skia.Paint();
  stroke.setAntiAlias(true);
  stroke.setStyle(PaintStyle.Stroke);
  stroke.setStrokeCap(StrokeCap.Round);
  stroke.setStrokeJoin(StrokeJoin.Round);

  const font = matchFont({ fontFamily: Platform.select({ ios: "Helvetica", default: "sans-serif" }), fontSize: geometry.pitchX * 0.62, fontWeight: "bold" });
  fill.setColor(Skia.Color(palette.grey600));
  for (let r = 0; r < geometry.rowLabels.length; r++) {
    const label = geometry.rowLabels[r]!;
    const textWidth = font.measureText(label).width;
    canvas.drawText(label, geometry.rowLabelX[r]! - textWidth / 2, geometry.rowLabelY[r]! + geometry.pitchX * 0.22, fill, font);
  }

  for (const batch of scene.batches) {
    if (batch.kind === "path") {
      const path = Skia.Path.MakeFromSVGString(batch.d);
      if (!path) continue;
      if (batch.fill) {
        fill.setColor(Skia.Color(batch.fill));
        canvas.drawPath(path, fill);
      }
      if (batch.stroke) {
        stroke.setColor(Skia.Color(batch.stroke));
        stroke.setStrokeWidth(batch.strokeWidth);
        canvas.drawPath(path, stroke);
      }
    } else if (batch.kind === "dots") {
      fill.setColor(Skia.Color(batch.fill));
      const radius = w * batch.radius;
      for (const k of batch.seats) canvas.drawCircle(geometry.x[k]!, geometry.y[k]!, radius, fill);
    } else {
      const strokeWidth = batch.strokeWidth * w;
      const bw = w * batch.scale - strokeWidth;
      const bh = h * batch.scale - strokeWidth;
      const corner = Math.min(bw, bh) * 0.24;
      if (batch.fill) fill.setColor(Skia.Color(batch.fill));
      if (batch.stroke) {
        stroke.setColor(Skia.Color(batch.stroke));
        stroke.setStrokeWidth(strokeWidth);
      }
      for (const k of batch.seats) {
        const rrect = Skia.RRectXY(Skia.XYWHRect(geometry.x[k]! - bw / 2, geometry.y[k]! - bh / 2, bw, bh), corner, corner);
        if (batch.fill) canvas.drawRRect(rrect, fill);
        if (batch.stroke) canvas.drawRRect(rrect, stroke);
      }
    }
  }
  return recorder.finishRecordingAsPicture();
}

export function SeatCanvasSkia({ geometry, scene, panZoom, width, height }: Props) {
  const picture = useMemo(() => record(geometry, scene), [geometry, scene]);
  const { scale, translateX, translateY } = panZoom;
  const transform = useDerivedValue(() => [{ translateX: translateX.value }, { translateY: translateY.value }, { scale: scale.value }]);

  return (
    <Canvas style={[styles.canvas, { width, height }]}>
      <Group transform={transform}>
        <Picture picture={picture} />
      </Group>
    </Canvas>
  );
}

const styles = StyleSheet.create({
  canvas: { position: "absolute", left: 0, top: 0 },
});
