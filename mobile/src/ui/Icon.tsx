import Svg, { Circle, Path, Rect } from "react-native-svg";

import { palette } from "../theme";

export type IconName =
  | "calendar" | "ticket" | "user" | "chevron" | "check" | "warning" | "info" | "wheelchair" | "clock"
  | "pin" | "phone" | "plus" | "minus" | "fit" | "close" | "offline" | "seat" | "sparkle" | "gate" | "lock"
  | "arrow" | "back" | "help" | "logout" | "shield" | "map" | "home";

interface Props {
  name: IconName;
  size?: number;
  color?: string;
}

/** Line icons drawn in code, so the app ships no icon font and the tint is always a palette colour. */
export function Icon({ name, size = 20, color = palette.emeraldInk }: Props) {
  const stroke = { stroke: color, strokeWidth: 1.8, strokeLinecap: "round" as const, strokeLinejoin: "round" as const, fill: "none" };
  return (
    <Svg width={size} height={size} viewBox="0 0 24 24" accessibilityElementsHidden importantForAccessibility="no-hide-descendants">
      {name === "calendar" && (<><Rect x="3.5" y="5" width="17" height="15" rx="2.5" {...stroke} /><Path d="M3.5 10h17M8 3v4M16 3v4" {...stroke} /></>)}
      {name === "ticket" && (<><Path d="M4 8.5a2 2 0 0 1 2-2h12a2 2 0 0 1 2 2v1.6a2 2 0 0 0 0 3.8v1.6a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2v-1.6a2 2 0 0 0 0-3.8z" {...stroke} /><Path d="M14.5 7v2M14.5 11v2M14.5 15v2" {...stroke} /></>)}
      {name === "user" && (<><Circle cx="12" cy="8.5" r="3.5" {...stroke} /><Path d="M5 19.5c1-3.4 3.8-5 7-5s6 1.6 7 5" {...stroke} /></>)}
      {name === "chevron" && <Path d="M9 5.5l6.5 6.5L9 18.5" {...stroke} />}
      {name === "check" && <Path d="M5 12.5l4.5 4.5L19 7.5" {...stroke} strokeWidth={2.2} />}
      {name === "warning" && (<><Path d="M12 4.2 2.8 19.5h18.4z" {...stroke} /><Path d="M12 10v4.5M12 17v.3" {...stroke} /></>)}
      {name === "info" && (<><Circle cx="12" cy="12" r="8.5" {...stroke} /><Path d="M12 11v5M12 7.8v.3" {...stroke} /></>)}
      {name === "wheelchair" && (<><Circle cx="10.5" cy="5" r="1.6" fill={color} /><Path d="M10.5 8v5.5h5l2 4.5" {...stroke} /><Path d="M7.6 11.2a5 5 0 1 0 6.9 6.3" {...stroke} /></>)}
      {name === "clock" && (<><Circle cx="12" cy="12" r="8.5" {...stroke} /><Path d="M12 7.5V12l3 2" {...stroke} /></>)}
      {name === "pin" && (<><Path d="M12 21s6.5-5.6 6.5-11a6.5 6.5 0 1 0-13 0c0 5.4 6.5 11 6.5 11z" {...stroke} /><Circle cx="12" cy="10" r="2.3" {...stroke} /></>)}
      {name === "phone" && (<><Rect x="7" y="3" width="10" height="18" rx="2.5" {...stroke} /><Path d="M11 17.5h2" {...stroke} /></>)}
      {name === "plus" && <Path d="M12 5.500v13M5.5 12h13" {...stroke} strokeWidth={2.2} />}
      {name === "minus" && <Path d="M5.5 12h13" {...stroke} strokeWidth={2.2} />}
      {name === "fit" && <Path d="M4 9V4h5M20 9V4h-5M4 15v5h5M20 15v5h-5" {...stroke} />}
      {name === "close" && <Path d="M6 6l12 12M18 6 6 18" {...stroke} />}
      {name === "offline" && (<><Path d="M4 4l16 16" {...stroke} /><Path d="M8.5 13.500a5 5 0 0 1 2-1.200M5.5 10.500a9 9 0 0 1 2.4-1.700M11 7.100a9 9 0 0 1 7.5 3.400M14.5 12.800a5 5 0 0 1 1 .7M12 17.500v.3" {...stroke} /></>)}
      {name === "seat" && (<><Rect x="6" y="4" width="12" height="10" rx="2.5" {...stroke} /><Path d="M4.5 12v5.500h15V12M7 17.500V20M17 17.500V20" {...stroke} /></>)}
      {name === "sparkle" && <Path d="M12 3.500l1.8 5.2 5.2 1.8-5.2 1.800L12 17.500l-1.8-5.2-5.2-1.8 5.2-1.800zM18.5 16l.7 1.8 1.8.7-1.8.7-.7 1.8-.7-1.8-1.8-.7 1.8-.7z" {...stroke} />}
      {name === "gate" && (<><Path d="M4 20V8l8-4.500L20 8v12" {...stroke} /><Path d="M9 20v-7h6v7M4 20h16" {...stroke} /></>)}
      {name === "arrow" && <Path d="M4.5 12h15M13.5 6l6 6-6 6" {...stroke} strokeWidth={2} />}
      {name === "back" && <Path d="M19.5 12h-15M10.5 6l-6 6 6 6" {...stroke} strokeWidth={2} />}
      {name === "help" && (<><Circle cx="12" cy="12" r="8.5" {...stroke} /><Path d="M9.6 9.5a2.5 2.5 0 1 1 3.4 2.3c-.6.3-1 .8-1 1.5v.4M12 16.8v.3" {...stroke} /></>)}
      {name === "logout" && (<><Path d="M14 4.5H7a2 2 0 0 0-2 2v11a2 2 0 0 0 2 2h7" {...stroke} /><Path d="M10.5 12h10M17 8.5l3.5 3.5-3.5 3.5" {...stroke} /></>)}
      {name === "shield" && (<><Path d="M12 3.5 5 6.200v5.300c0 4.300 3 7.700 7 9 4-1.300 7-4.700 7-9V6.200z" {...stroke} /><Path d="M9 12l2.200 2.200L15.200 10" {...stroke} /></>)}
      {name === "map" && (<><Path d="M3.5 6.500 9 4.500l6 2 5.500-2v13l-5.500 2-6-2-5.500 2z" {...stroke} /><Path d="M9 4.500v13M15 6.500v13" {...stroke} /></>)}
      {name === "home" && (<><Path d="M4 10.500 12 4l8 6.500V19a1.500 1.500 0 0 1-1.500 1.500H5.500A1.500 1.500 0 0 1 4 19z" {...stroke} /><Path d="M9.500 20.500v-6h5v6" {...stroke} /></>)}
      {name === "lock" && (<><Rect x="5" y="10.5" width="14" height="9.5" rx="2" {...stroke} /><Path d="M8 10.500V8a4 4 0 0 1 8 0v2.5" {...stroke} /></>)}
    </Svg>
  );
}
