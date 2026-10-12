// The app's font families: the ONE place their names are written.
//
// React Native on Android picks a bundled font by family name only; `fontWeight` on a custom
// family either does nothing or smears a fake bold over the regular cut. So each weight is
// its own family, and text styles (src/theme.ts) name a family and never set `fontWeight`.
//
// The names are the file names of the .ttf files in the @expo-google-fonts packages, because
// that is the name Android gives a font embedded by the expo-font config plugin (app.json);
// `useFonts` in app/_layout.tsx registers the same names at run time, which is what Expo Go
// and iOS use. No React Native import here, so pure code and its tests can read the names.

export const font = {
  /** Inter: body text, labels, buttons, inputs, the tab bar. */
  body: "Inter_400Regular",
  bodyMedium: "Inter_500Medium",
  bodySemiBold: "Inter_600SemiBold",
  /** Plus Jakarta Sans: screen titles, headers, card titles, headline prices. */
  heading: "PlusJakartaSans_600SemiBold",
  headingBold: "PlusJakartaSans_700Bold",
  headingExtraBold: "PlusJakartaSans_800ExtraBold",
} as const;

export type FontFamily = (typeof font)[keyof typeof font];
