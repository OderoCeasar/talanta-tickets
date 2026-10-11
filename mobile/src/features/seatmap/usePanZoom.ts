import { useCallback, useEffect, useMemo, useRef } from "react";
import { Gesture } from "react-native-gesture-handler";
import { cancelAnimation, useSharedValue, withDecay, withTiming, type SharedValue } from "react-native-reanimated";
import { scheduleOnRN, scheduleOnUI } from "react-native-worklets";

import { clampScale, clampTranslate, MIN_TAPPABLE_POINTS, scaleLimits, TAP_ZOOM_POINTS, toContent, zoomAbout } from "./viewport";

export interface PanZoom {
  /** Screen points per plan unit. */
  scale: SharedValue<number>;
  translateX: SharedValue<number>;
  translateY: SharedValue<number>;
  gesture: ReturnType<typeof Gesture.Simultaneous>;
  minScale: number;
  maxScale: number;
  zoomBy: (factor: number) => void;
  reset: () => void;
  /** Animates so the given content rectangle is centred and comfortably large. */
  focus: (rect: { x: number; y: number; width: number; height: number }) => void;
}

interface Options {
  viewW: number;
  viewH: number;
  contentW: number;
  contentH: number;
  /** Seat pitch in plan units; sets how far the fan may zoom in. */
  pitch: number;
  /** Called on the JS thread with the tapped point in content coordinates. */
  onTap: (x: number, y: number) => void;
}

const ANIM = { duration: 220 };

/**
 * Pinch, pan and tap for the block canvas. Every handler is a worklet, so zooming and
 * panning run on the UI thread and never wait for JS; only a completed tap crosses over.
 * There is one tap handler for the whole canvas: it reports a point, not a seat.
 */
export function usePanZoom({ viewW, viewH, contentW, contentH, pitch, onTap }: Options): PanZoom {
  const { minScale, maxScale } = useMemo(() => scaleLimits(viewW, viewH, contentW, contentH, pitch), [viewW, viewH, contentW, contentH, pitch]);

  // The tap callback changes with every selection; the gesture must not be rebuilt for that.
  const tapRef = useRef(onTap);
  tapRef.current = onTap;
  const handleTap = useCallback((x: number, y: number) => tapRef.current(x, y), []);

  const scale = useSharedValue(minScale);
  const translateX = useSharedValue(clampTranslate(0, viewW, contentW, minScale));
  const translateY = useSharedValue(clampTranslate(0, viewH, contentH, minScale));

  // Values at the start of the gesture in progress.
  const startScale = useSharedValue(1);
  const startX = useSharedValue(0);
  const startY = useSharedValue(0);
  const startFocalX = useSharedValue(0);
  const startFocalY = useSharedValue(0);

  // A new block shows whole. A change in the view's size alone (a message appearing under
  // the map, the keyboard, a rotated screen) keeps the fan's zoom and only re-applies the limits.
  const shownContent = useRef({ w: contentW, h: contentH });
  useEffect(() => {
    const sameBlock = shownContent.current.w === contentW && shownContent.current.h === contentH;
    shownContent.current = { w: contentW, h: contentH };
    cancelAnimation(scale);
    cancelAnimation(translateX);
    cancelAnimation(translateY);
    const s = sameBlock ? clampScale(scale.value, minScale, maxScale) : minScale;
    scale.value = s;
    translateX.value = clampTranslate(sameBlock ? translateX.value : 0, viewW, contentW, s);
    translateY.value = clampTranslate(sameBlock ? translateY.value : 0, viewH, contentH, s);
  }, [minScale, maxScale, viewW, viewH, contentW, contentH, scale, translateX, translateY]);

  const gesture = useMemo(() => {
    const animateTo = (nextScale: number, focalX: number, focalY: number) => {
      "worklet";
      const s = clampScale(nextScale, minScale, maxScale);
      const x = clampTranslate(zoomAbout(focalX, translateX.value, scale.value, s), viewW, contentW, s);
      const y = clampTranslate(zoomAbout(focalY, translateY.value, scale.value, s), viewH, contentH, s);
      scale.value = withTiming(s, ANIM);
      translateX.value = withTiming(x, ANIM);
      translateY.value = withTiming(y, ANIM);
    };

    const pinch = Gesture.Pinch()
      .onStart((e) => {
        cancelAnimation(scale);
        cancelAnimation(translateX);
        cancelAnimation(translateY);
        startScale.value = scale.value;
        startX.value = translateX.value;
        startY.value = translateY.value;
        startFocalX.value = e.focalX;
        startFocalY.value = e.focalY;
      })
      .onUpdate((e) => {
        const s = clampScale(startScale.value * e.scale, minScale, maxScale);
        // The content point that was under the fingers at the start stays under them, so
        // moving both fingers also pans.
        const x = e.focalX - ((startFocalX.value - startX.value) / startScale.value) * s;
        const y = e.focalY - ((startFocalY.value - startY.value) / startScale.value) * s;
        scale.value = s;
        translateX.value = clampTranslate(x, viewW, contentW, s);
        translateY.value = clampTranslate(y, viewH, contentH, s);
      });

    const pan = Gesture.Pan()
      .maxPointers(1)
      .minDistance(6)
      .onStart(() => {
        cancelAnimation(translateX);
        cancelAnimation(translateY);
        startX.value = translateX.value;
        startY.value = translateY.value;
      })
      .onUpdate((e) => {
        translateX.value = clampTranslate(startX.value + e.translationX, viewW, contentW, scale.value);
        translateY.value = clampTranslate(startY.value + e.translationY, viewH, contentH, scale.value);
      })
      .onEnd((e) => {
        const s = scale.value;
        // Glide on, but only where there is somewhere to go.
        if (contentW * s > viewW) translateX.value = withDecay({ velocity: e.velocityX, clamp: [viewW - contentW * s, 0] });
        if (contentH * s > viewH) translateY.value = withDecay({ velocity: e.velocityY, clamp: [viewH - contentH * s, 0] });
      });

    const tap = Gesture.Tap()
      .maxDuration(300)
      .maxDistance(12)
      .onEnd((e, success) => {
        if (!success) return;
        // Zoomed out, a fingertip covers several seats: the first tap zooms in on the spot
        // instead of guessing which seat was meant.
        if (scale.value * pitch < MIN_TAPPABLE_POINTS) {
          animateTo(TAP_ZOOM_POINTS / pitch, e.x, e.y);
          return;
        }
        scheduleOnRN(handleTap, toContent(e.x, translateX.value, scale.value), toContent(e.y, translateY.value, scale.value));
      });

    return Gesture.Simultaneous(pinch, pan, tap);
  }, [minScale, maxScale, viewW, viewH, contentW, contentH, pitch, handleTap, scale, translateX, translateY, startScale, startX, startY, startFocalX, startFocalY]);

  const zoomBy = useCallback(
    (factor: number) => {
      scheduleOnUI(() => {
        "worklet";
        const s = clampScale(scale.value * factor, minScale, maxScale);
        const x = clampTranslate(zoomAbout(viewW / 2, translateX.value, scale.value, s), viewW, contentW, s);
        const y = clampTranslate(zoomAbout(viewH / 2, translateY.value, scale.value, s), viewH, contentH, s);
        scale.value = withTiming(s, ANIM);
        translateX.value = withTiming(x, ANIM);
        translateY.value = withTiming(y, ANIM);
      });
    },
    [minScale, maxScale, viewW, viewH, contentW, contentH, scale, translateX, translateY],
  );

  const reset = useCallback(() => {
    scheduleOnUI(() => {
      "worklet";
      scale.value = withTiming(minScale, ANIM);
      translateX.value = withTiming(clampTranslate(0, viewW, contentW, minScale), ANIM);
      translateY.value = withTiming(clampTranslate(0, viewH, contentH, minScale), ANIM);
    });
  }, [minScale, viewW, viewH, contentW, contentH, scale, translateX, translateY]);

  const focus = useCallback(
    (rect: { x: number; y: number; width: number; height: number }) => {
      scheduleOnUI(() => {
        "worklet";
        // Large enough to tap, but never so close that the group does not fit the view.
        const fitGroup = Math.min(viewW / (rect.width + 6 * pitch), viewH / (rect.height + 6 * pitch));
        const s = clampScale(Math.min(TAP_ZOOM_POINTS / pitch, fitGroup), minScale, maxScale);
        const x = clampTranslate(viewW / 2 - (rect.x + rect.width / 2) * s, viewW, contentW, s);
        const y = clampTranslate(viewH / 2 - (rect.y + rect.height / 2) * s, viewH, contentH, s);
        scale.value = withTiming(s, ANIM);
        translateX.value = withTiming(x, ANIM);
        translateY.value = withTiming(y, ANIM);
      });
    },
    [minScale, maxScale, viewW, viewH, contentW, contentH, pitch, scale, translateX, translateY],
  );

  return { scale, translateX, translateY, gesture, minScale, maxScale, zoomBy, reset, focus };
}
