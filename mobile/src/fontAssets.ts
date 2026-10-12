// The font files, bundled with the app (nothing is fetched at run time). Each is imported
// from its own folder: the packages' index files would pull every weight into the bundle.
import { Inter_400Regular } from "@expo-google-fonts/inter/400Regular";
import { Inter_500Medium } from "@expo-google-fonts/inter/500Medium";
import { Inter_600SemiBold } from "@expo-google-fonts/inter/600SemiBold";
import { PlusJakartaSans_600SemiBold } from "@expo-google-fonts/plus-jakarta-sans/600SemiBold";
import { PlusJakartaSans_700Bold } from "@expo-google-fonts/plus-jakarta-sans/700Bold";
import { PlusJakartaSans_800ExtraBold } from "@expo-google-fonts/plus-jakarta-sans/800ExtraBold";

import { font, type FontFamily } from "./fonts";

/** Family name to font file, for `useFonts`. Every family in `font` must be here. */
export const fontAssets: Record<FontFamily, number> = {
  [font.body]: Inter_400Regular,
  [font.bodyMedium]: Inter_500Medium,
  [font.bodySemiBold]: Inter_600SemiBold,
  [font.heading]: PlusJakartaSans_600SemiBold,
  [font.headingBold]: PlusJakartaSans_700Bold,
  [font.headingExtraBold]: PlusJakartaSans_800ExtraBold,
};

/** The file Skia draws seat-map row labels with (it needs the file, not a family name). */
export const seatMapLabelFont = Inter_600SemiBold;
