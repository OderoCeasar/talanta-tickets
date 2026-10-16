import Svg, { Circle, Ellipse, G, Path, Rect, Text as SvgText } from "react-native-svg";

import { alpha, font, palette } from "../../theme";

// Line drawings for the welcome slides, in Champagne on Emerald Ink like the app icon. Drawn
// in code so they stay sharp at any size and the app ships no image files for them.

const LINE = palette.champagne;
const SOFT = alpha(palette.champagne, 0.16);
const FAINT = alpha(palette.champagne, 0.08);

interface Props {
  width: number;
  height: number;
}

const box = (p: Props) => ({ width: p.width, height: p.height, viewBox: "0 0 320 260", accessibilityElementsHidden: true, importantForAccessibility: "no-hide-descendants" as const });

/** The stadium from above, with a match card: "every match at Talanta". */
export function StadiumArt(p: Props) {
  return (
    <Svg {...box(p)}>
      <Circle cx={160} cy={124} r={118} fill={FAINT} />
      <Ellipse cx={160} cy={124} rx={128} ry={88} fill="none" stroke={LINE} strokeWidth={14} />
      <Ellipse cx={160} cy={124} rx={94} ry={60} fill={SOFT} stroke={LINE} strokeWidth={6} />
      <Rect x={112} y={96} width={96} height={56} rx={8} fill={LINE} />
      <Path d="M160 96v56" stroke={palette.emeraldInk} strokeWidth={3} />
      <Circle cx={160} cy={124} r={11} fill="none" stroke={palette.emeraldInk} strokeWidth={3} />
      {/* floodlights */}
      {[[38, 40], [282, 40], [38, 208], [282, 208]].map(([x, y]) => (
        <G key={`${x}-${y}`}>
          <Circle cx={x} cy={y} r={7} fill={LINE} />
          <Circle cx={x} cy={y} r={14} fill={SOFT} />
        </G>
      ))}
      {/* the match card */}
      <G transform="translate(196 176)">
        <Rect width={112} height={62} rx={12} fill={palette.white} />
        <Rect x={8} y={8} width={34} height={46} rx={8} fill={palette.emeraldInk} />
        <SvgText x={25} y={24} fontSize={9} fontFamily={font.bodySemiBold} fill={palette.champagne} textAnchor="middle">NOV</SvgText>
        <SvgText x={25} y={44} fontSize={18} fontFamily={font.headingExtraBold} fill={palette.white} textAnchor="middle">01</SvgText>
        <Rect x={50} y={14} width={52} height={7} rx={3.5} fill={palette.emeraldInk} />
        <Rect x={50} y={28} width={38} height={6} rx={3} fill={palette.grey300} />
        <Rect x={50} y={41} width={44} height={10} rx={5} fill={palette.champagne} />
      </G>
    </Svg>
  );
}

/** A block of seats with three chosen: "pick your exact seat". */
export function SeatsArt(p: Props) {
  const rows = 5;
  const cols = 8;
  const chosen = new Set(["2-3", "2-4", "2-5"]);
  const sold = new Set(["0-1", "0-2", "1-6", "3-0", "3-1", "4-5", "4-6", "1-0"]);
  const seat = 26;
  const gap = 8;
  const width = cols * seat + (cols - 1) * gap;
  const left = (320 - width) / 2;
  return (
    <Svg {...box(p)}>
      {/* the pitch edge */}
      <Rect x={40} y={14} width={240} height={34} rx={10} fill={SOFT} stroke={LINE} strokeWidth={2} />
      <SvgText x={160} y={36} fontSize={11} letterSpacing={3} fontFamily={font.bodySemiBold} fill={LINE} textAnchor="middle">PITCH</SvgText>
      {Array.from({ length: rows }, (_, r) =>
        Array.from({ length: cols }, (_, c) => {
          const key = `${r}-${c}`;
          const x = left + c * (seat + gap);
          const y = 68 + r * (seat + gap);
          if (chosen.has(key)) {
            return (
              <G key={key}>
                <Rect x={x} y={y} width={seat} height={seat} rx={7} fill={palette.champagne} />
                <Path d={`M${x + 7} ${y + 13.5}l4.5 4.5 8-9`} stroke={palette.emeraldInk} strokeWidth={2.8} fill="none" strokeLinecap="round" strokeLinejoin="round" />
              </G>
            );
          }
          if (sold.has(key)) return <Circle key={key} cx={x + seat / 2} cy={y + seat / 2} r={6} fill={alpha(palette.champagne, 0.3)} />;
          return <Rect key={key} x={x} y={y} width={seat} height={seat} rx={7} fill="none" stroke={alpha(palette.champagne, 0.65)} strokeWidth={2} />;
        }),
      )}
      {/* a finger tapping the last chosen seat */}
      <Circle cx={left + 5 * (seat + gap) + seat / 2} cy={68 + 2 * (seat + gap) + seat / 2} r={24} fill="none" stroke={LINE} strokeWidth={2} strokeDasharray="4 5" />
    </Svg>
  );
}

/** A phone showing a PIN prompt, with the hold timer: "pay safely with M-Pesa". */
export function PayArt(p: Props) {
  return (
    <Svg {...box(p)}>
      <Circle cx={160} cy={130} r={112} fill={FAINT} />
      <Rect x={106} y={18} width={108} height={214} rx={20} fill={palette.inkDeep} stroke={LINE} strokeWidth={5} />
      <Rect x={146} y={28} width={28} height={5} rx={2.5} fill={alpha(palette.champagne, 0.5)} />
      {/* the prompt */}
      <Rect x={118} y={70} width={84} height={104} rx={12} fill={palette.white} />
      <Rect x={130} y={82} width={44} height={7} rx={3.5} fill={palette.emeraldInk} />
      <Rect x={130} y={96} width={60} height={5} rx={2.5} fill={palette.grey300} />
      <Rect x={130} y={106} width={50} height={5} rx={2.5} fill={palette.grey300} />
      {[0, 1, 2, 3].map((i) => (
        <Circle key={i} cx={136 + i * 16} cy={130} r={5} fill={palette.emeraldInk} />
      ))}
      <Rect x={130} y={148} width={60} height={16} rx={8} fill={palette.emeraldInk} />
      {/* the lock */}
      <G transform="translate(48 120)">
        <Circle cx={22} cy={22} r={30} fill={palette.champagne} />
        <Rect x={9} y={20} width={26} height={20} rx={4} fill={palette.emeraldInk} />
        <Path d="M14 20v-5a8 8 0 0 1 16 0v5" stroke={palette.emeraldInk} strokeWidth={4} fill="none" />
      </G>
      {/* the hold timer */}
      <G transform="translate(226 52)">
        <Circle cx={30} cy={30} r={32} fill={palette.champagne} />
        <Circle cx={30} cy={30} r={24} fill="none" stroke={palette.emeraldInk} strokeWidth={3} />
        <Path d="M30 16v14l9 6" stroke={palette.emeraldInk} strokeWidth={3.5} strokeLinecap="round" fill="none" />
      </G>
    </Svg>
  );
}

/** A ticket with its QR and gate, and no signal needed: "your ticket, even offline". */
export function TicketArt(p: Props) {
  // A fixed pattern that reads as a QR code at a glance (it is not one).
  const bits = [
    "1110101", "1010010", "1110111", "0001000", "1011101", "0100101", "1101011",
  ];
  return (
    <Svg {...box(p)}>
      <Circle cx={160} cy={130} r={112} fill={FAINT} />
      <G transform="rotate(-6 160 130)">
        <Path
          d="M78 30h164a12 12 0 0 1 12 12v58a14 14 0 0 0 0 28v90a12 12 0 0 1-12 12H78a12 12 0 0 1-12-12v-90a14 14 0 0 0 0-28V42a12 12 0 0 1 12-12z"
          fill={palette.white}
        />
        <Rect x={66} y={30} width={188} height={46} rx={12} fill={palette.champagne} />
        <Rect x={66} y={58} width={188} height={18} fill={palette.champagne} />
        <Rect x={82} y={44} width={70} height={8} rx={4} fill={palette.emeraldInk} />
        <Rect x={82} y={58} width={46} height={6} rx={3} fill={palette.champagneDeep} />
        <Path d="M76 114h168" stroke={palette.grey300} strokeWidth={2} strokeDasharray="6 6" />
        <G transform="translate(96 132)">
          {bits.map((row, r) =>
            row.split("").map((b, c) => (b === "1" ? <Rect key={`${r}-${c}`} x={c * 11} y={r * 11} width={11} height={11} fill={palette.emeraldInk} /> : null)),
          )}
        </G>
        <Rect x={192} y={140} width={46} height={58} rx={10} fill={palette.emeraldInk} />
        <SvgText x={215} y={160} fontSize={9} fontFamily={font.bodySemiBold} fill={palette.champagne} textAnchor="middle">GATE</SvgText>
        <SvgText x={215} y={187} fontSize={22} fontFamily={font.headingExtraBold} fill={palette.white} textAnchor="middle">B</SvgText>
      </G>
      {/* no signal needed */}
      <G transform="translate(236 6)">
        <Circle cx={26} cy={26} r={26} fill={palette.champagne} />
        <Path d="M14 23a17 17 0 0 1 24 0M18.5 28a10.5 10.5 0 0 1 15 0" stroke={palette.emeraldInk} strokeWidth={3} strokeLinecap="round" fill="none" />
        <Circle cx={26} cy={34} r={2.6} fill={palette.emeraldInk} />
        <Path d="M13 13l26 26" stroke={palette.emeraldInk} strokeWidth={3} strokeLinecap="round" />
      </G>
    </Svg>
  );
}
