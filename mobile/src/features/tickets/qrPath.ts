import qrcode from "qrcode-generator";

const QUIET_ZONE = 4;

/** Module matrix of a QR code as one SVG path. Pure, so it is unit-tested. */
export function qrPath(value: string): { size: number; path: string } {
  // Type 0 lets the library pick the smallest code that fits; "M" survives a scratched screen.
  const qr = qrcode(0, "M");
  qr.addData(value);
  qr.make();
  const count = qr.getModuleCount();
  let d = "";
  for (let row = 0; row < count; row += 1) {
    // Runs of dark modules in a row become one rectangle, which keeps the path short.
    let start = -1;
    for (let col = 0; col <= count; col += 1) {
      const dark = col < count && qr.isDark(row, col);
      if (dark && start < 0) start = col;
      if (!dark && start >= 0) {
        d += `M${start + QUIET_ZONE} ${row + QUIET_ZONE}h${col - start}v1h${-(col - start)}z`;
        start = -1;
      }
    }
  }
  return { size: count + QUIET_ZONE * 2, path: d };
}
