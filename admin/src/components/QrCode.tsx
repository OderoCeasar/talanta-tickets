import qrcode from "qrcode-generator";
import { useMemo } from "react";

const QUIET_ZONE = 4;

/** Draws `value` as a QR code in SVG. Dark modules use the brand ink on a white ground. */
export function QrCode({ value, label }: { value: string; label: string }) {
  const { size, path } = useMemo(() => {
    // Type 0 lets the library pick the smallest code that fits; "M" survives a smudged screen.
    const qr = qrcode(0, "M");
    qr.addData(value);
    qr.make();
    const count = qr.getModuleCount();
    let d = "";
    for (let row = 0; row < count; row += 1) {
      for (let col = 0; col < count; col += 1) {
        if (qr.isDark(row, col)) d += `M${col + QUIET_ZONE} ${row + QUIET_ZONE}h1v1h-1z`;
      }
    }
    return { size: count + QUIET_ZONE * 2, path: d };
  }, [value]);

  return (
    <svg className="qr" viewBox={`0 0 ${size} ${size}`} role="img" aria-label={label} shapeRendering="crispEdges">
      <rect width={size} height={size} fill="#ffffff" />
      <path d={path} fill="var(--emerald-ink)" />
    </svg>
  );
}
