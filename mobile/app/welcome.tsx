import { router, useLocalSearchParams } from "expo-router";
import { StatusBar } from "expo-status-bar";
import { useRef, useState, type ComponentType } from "react";
import { Pressable, StyleSheet, Text, useWindowDimensions, View, type NativeScrollEvent, type NativeSyntheticEvent } from "react-native";
import Animated, { Extrapolation, interpolate, useAnimatedScrollHandler, useAnimatedStyle, useReducedMotion, useSharedValue, type SharedValue } from "react-native-reanimated";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import Svg, { Ellipse, Path } from "react-native-svg";

import { writeWelcomeSeen } from "../src/api/storage";
import { PayArt, SeatsArt, StadiumArt, TicketArt } from "../src/features/welcome/Illustrations";
import { alpha, font, palette, space, type } from "../src/theme";
import { Button } from "../src/ui";

interface Slide {
  title: string;
  body: string;
  Art: ComponentType<{ width: number; height: number }>;
}

// What the app does, in the order a fan does it.
const SLIDES: Slide[] = [
  {
    title: "Every match at Talanta",
    body: "See what is on at Talanta Stadium and get your tickets in a few taps.",
    Art: StadiumArt,
  },
  {
    title: "Pick your exact seat",
    body: "Choose a stand, zoom in and tap the seats you want. Or let us find the best seats together.",
    Art: SeatsArt,
  },
  {
    title: "Pay safely with M-Pesa",
    body: "Your seats are held for 10 minutes while you pay. You enter your PIN on the M-Pesa prompt, never in this app.",
    Art: PayArt,
  },
  {
    title: "Your ticket, even offline",
    body: "Tickets are saved on your phone. Show the QR code at your gate, no internet needed.",
    Art: TicketArt,
  },
];

export default function WelcomeScreen() {
  // From Profile ("How Talanta Tickets works"): the same slides, closed with Back instead.
  const { replay } = useLocalSearchParams<{ replay?: string }>();
  const replaying = replay === "1";
  const { width, height } = useWindowDimensions();
  const insets = useSafeAreaInsets();
  const reduceMotion = useReducedMotion();
  const scrollRef = useRef<Animated.ScrollView>(null);
  const scrollX = useSharedValue(0);
  const [index, setIndex] = useState(0);
  const last = index === SLIDES.length - 1;

  const onScroll = useAnimatedScrollHandler((e) => {
    scrollX.value = e.contentOffset.x;
  });
  const onSettle = (e: NativeSyntheticEvent<NativeScrollEvent>) => setIndex(Math.round(e.nativeEvent.contentOffset.x / width));

  const goTo = (i: number) => {
    scrollRef.current?.scrollTo({ x: i * width, animated: !reduceMotion });
    setIndex(i);
  };

  const finish = (then: "events" | "signIn") => {
    void writeWelcomeSeen();
    if (replaying) {
      router.back();
      return;
    }
    if (then === "signIn") {
      // "I already have tickets": land on My tickets with sign-in on top of it.
      router.replace("/tickets");
      router.push("/sign-in");
    } else {
      router.replace("/events");
    }
  };

  const artHeight = Math.min(300, height * 0.36);
  const artWidth = Math.min(width - 2 * space.xl, artHeight * (320 / 260));

  return (
    <View style={[styles.root, { paddingTop: insets.top + space.sm, paddingBottom: Math.max(insets.bottom, space.lg) }]}>
      <StatusBar style="light" />

      <View style={styles.top}>
        <View style={styles.brand}>
          <Mark />
          <Text style={styles.brandText}>Talanta Tickets</Text>
        </View>
        {last ? null : (
          <Pressable onPress={() => finish("events")} accessibilityRole="button" accessibilityLabel={replaying ? "Close" : "Skip the introduction"} hitSlop={12} style={styles.skip} testID="welcome-skip">
            <Text style={styles.skipText}>{replaying ? "Close" : "Skip"}</Text>
          </Pressable>
        )}
      </View>

      <Animated.ScrollView
        ref={scrollRef}
        horizontal
        pagingEnabled
        showsHorizontalScrollIndicator={false}
        onScroll={onScroll}
        scrollEventThrottle={16}
        onMomentumScrollEnd={onSettle}
        style={styles.pager}
        testID="welcome-pager"
      >
        {SLIDES.map((slide, i) => (
          <Page key={slide.title} slide={slide} i={i} width={width} scrollX={scrollX} still={reduceMotion} current={i === index} artWidth={artWidth} artHeight={artHeight} />
        ))}
      </Animated.ScrollView>

      <View style={styles.bottom}>
        <View style={styles.dots} accessible accessibilityLabel={`Page ${index + 1} of ${SLIDES.length}`}>
          {SLIDES.map((s, i) => (
            <View key={s.title} style={[styles.dot, i === index && styles.dotOn]} />
          ))}
        </View>
        {last ? (
          <>
            <Button title={replaying ? "Done" : "Find a match"} variant="light" icon={replaying ? undefined : "arrow"} onPress={() => finish("events")} testID="welcome-start" />
            {replaying ? (
              <View style={styles.secondary} />
            ) : (
              <Pressable onPress={() => finish("signIn")} accessibilityRole="button" hitSlop={8} style={styles.secondary} testID="welcome-sign-in">
                <Text style={styles.secondaryText}>
                  I already have tickets · <Text style={styles.secondaryStrong}>Sign in</Text>
                </Text>
              </Pressable>
            )}
          </>
        ) : (
          <>
            <Button title="Next" variant="light" onPress={() => goTo(index + 1)} testID="welcome-next" />
            {/* Keeps the button where it is on every page, so the thumb does not have to follow it. */}
            <View style={styles.secondary} />
          </>
        )}
      </View>
    </View>
  );
}

function Page({ slide, i, width, scrollX, still, current, artWidth, artHeight }: { slide: Slide; i: number; width: number; scrollX: SharedValue<number>; still: boolean; current: boolean; artWidth: number; artHeight: number }) {
  const range = [(i - 1) * width, i * width, (i + 1) * width];
  // The drawing moves slower than the page and fades at the edges, so a swipe has depth.
  const artStyle = useAnimatedStyle(() =>
    still
      ? {}
      : {
          opacity: interpolate(scrollX.value, range, [0.2, 1, 0.2], Extrapolation.CLAMP),
          transform: [
            { translateX: interpolate(scrollX.value, range, [width * 0.35, 0, -width * 0.35], Extrapolation.CLAMP) },
            { scale: interpolate(scrollX.value, range, [0.85, 1, 0.85], Extrapolation.CLAMP) },
          ],
        },
  );
  const { Art } = slide;
  return (
    // Pages off screen are hidden from screen readers, which would otherwise read all four.
    <View style={[styles.page, { width }]} importantForAccessibility={current ? "auto" : "no-hide-descendants"} accessibilityElementsHidden={!current}>
      <Animated.View style={[styles.art, artStyle]}>
        <Art width={artWidth} height={artHeight} />
      </Animated.View>
      <Text style={styles.title} accessibilityRole="header">{slide.title}</Text>
      <Text style={styles.body}>{slide.body}</Text>
    </View>
  );
}

/** The stadium-bowl mark from the app icon. */
function Mark() {
  return (
    <Svg width={30} height={30} viewBox="-150 -150 300 300">
      <Ellipse rx={130} ry={94} fill="none" stroke={palette.champagne} strokeWidth={24} />
      <Path fill={palette.champagne} fillRule="evenodd" d="M-60-44h54v88h-54a14 14 0 0 1-14-14v-60a14 14 0 0 1 14-14ZM6-44h54a14 14 0 0 1 14 14v60a14 14 0 0 1-14 14h-54Z" />
    </Svg>
  );
}

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: palette.emeraldInk },
  top: { flexDirection: "row", alignItems: "center", justifyContent: "space-between", paddingHorizontal: space.lg, minHeight: 48 },
  brand: { flexDirection: "row", alignItems: "center", gap: space.sm },
  brandText: { fontFamily: font.headingBold, fontSize: 17, color: palette.champagne },
  skip: { minHeight: 44, minWidth: 44, justifyContent: "center", alignItems: "flex-end", paddingHorizontal: space.xs },
  skipText: { fontFamily: font.bodySemiBold, fontSize: 16, color: palette.champagne },
  pager: { flex: 1 },
  page: { flex: 1, alignItems: "center", justifyContent: "center", paddingHorizontal: space.xl, gap: space.md },
  art: { marginBottom: space.lg },
  title: { ...type.display, fontSize: 28, lineHeight: 34, textAlign: "center" },
  body: { ...type.body, fontSize: 17, lineHeight: 25, color: alpha(palette.champagne, 0.85), textAlign: "center", maxWidth: 360 },
  bottom: { paddingHorizontal: space.lg, gap: space.md },
  dots: { flexDirection: "row", justifyContent: "center", alignItems: "center", gap: 8, minHeight: 20 },
  // Driven by the page number, not the scroll position: a width animated from scroll events
  // was seen stuck on the old page after a finger swipe on Android.
  dot: { width: 8, height: 8, borderRadius: 4, backgroundColor: alpha(palette.champagne, 0.35) },
  dotOn: { width: 26, backgroundColor: palette.champagne },
  secondary: { minHeight: 44, alignItems: "center", justifyContent: "center" },
  secondaryText: { ...type.body, color: alpha(palette.champagne, 0.85), textAlign: "center" },
  secondaryStrong: { fontFamily: font.bodySemiBold, color: palette.champagne, textDecorationLine: "underline" },
});
