import { useMemo } from "react";
import Svg, { Path, Rect } from "react-native-svg";

import { palette } from "../../theme";
import { qrPath } from "./qrPath";

/** Draws the ticket's signed token as a QR code. Dark on white, with a full quiet zone, so gate scanners read it. */
export function QrCode({ value, size, label }: { value: string; size: number; label: string }) {
  const qr = useMemo(() => qrPath(value), [value]);
  return (
    <Svg width={size} height={size} viewBox={`0 0 ${qr.size} ${qr.size}`} accessibilityLabel={label} testID="ticket-qr">
      <Rect width={qr.size} height={qr.size} fill={palette.white} />
      <Path d={qr.path} fill={palette.grey900} />
    </Svg>
  );
}
