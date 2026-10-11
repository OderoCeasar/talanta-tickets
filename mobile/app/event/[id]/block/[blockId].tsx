import { ApiError } from "@talanta/shared";
import * as Crypto from "expo-crypto";
import { router, Stack, useIsFocused, useLocalSearchParams } from "expo-router";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Pressable, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";

import { useBlockSeats, useCreateHold, useEvent, useMyHolds } from "../../../../src/api/queries";
import { writeStoredCheckout } from "../../../../src/api/storage";
import { Legend } from "../../../../src/features/seatmap/Legend";
import { buildScene, isSelectable, type SeatLook } from "../../../../src/features/seatmap/layers";
import { buildGeometry, SEAT_ACCESSIBLE, SEAT_COMPANION, SEAT_RESTRICTED, seatLabel, seatStatuses } from "../../../../src/features/seatmap/model";
import { SeatCanvas, type SeatCanvasHandle } from "../../../../src/features/seatmap/SeatCanvas";
import { useBlockPolling } from "../../../../src/features/seatmap/useBlockPolling";
import { formatCountdown, remainingMs } from "../../../../src/lib/countdown";
import { errorMessage, unavailableSeatIds } from "../../../../src/lib/errors";
import { formatKes, sumCents } from "../../../../src/lib/money";
import { useAppActive } from "../../../../src/lib/useAppActive";
import { useNow } from "../../../../src/lib/useNow";
import { useSession } from "../../../../src/session";
import { useBookingFlow } from "../../../../src/store/bookingFlow";
import { palette, radius, space, type } from "../../../../src/theme";
import { Banner, Button, ErrorState, Icon, Loading, Sheet } from "../../../../src/ui";

interface Notice {
  tone: "info" | "warning" | "error";
  text: string;
}

/** Level 3: the seats of one block. */
export default function BlockScreen() {
  const { id, blockId: blockParam, expired, found } = useLocalSearchParams<{ id: string; blockId: string; expired?: string; found?: string }>();
  const eventId = Number(id);
  const blockId = Number(blockParam);
  const insets = useSafeAreaInsets();
  const { status: sessionStatus, user } = useSession();
  const signedIn = sessionStatus === "signedIn";

  const block = useBlockSeats(eventId, blockId);
  const event = useEvent(eventId);
  const holds = useMyHolds(signedIn && user?.role === "FAN");
  const createHold = useCreateHold();

  const isFocused = useIsFocused();
  const appActive = useAppActive();
  // Polling runs only while this screen is the one in view and the app is in the foreground.
  const pollingPaused = useBlockPolling(eventId, blockId, isFocused && appActive && block.isSuccess);

  const openBlock = useBookingFlow((s) => s.openBlock);
  const selectedIds = useBookingFlow((s) => (s.eventId === eventId && s.blockId === blockId ? s.seatIds : EMPTY));
  const lostIds = useBookingFlow((s) => (s.eventId === eventId && s.blockId === blockId ? s.lostSeatIds : EMPTY));
  const toggleSeat = useBookingFlow((s) => s.toggleSeat);
  const removeSeats = useBookingFlow((s) => s.removeSeats);
  const markLost = useBookingFlow((s) => s.markLost);
  const unmarkLost = useBookingFlow((s) => s.unmarkLost);
  const clearSelection = useBookingFlow((s) => s.clearSelection);
  const syncHold = useBookingFlow((s) => s.syncHold);
  const holdExpiresAt = useBookingFlow((s) => s.holdExpiresAt);
  const serverOffset = useBookingFlow((s) => s.serverOffsetMs);

  useEffect(() => openBlock(eventId, blockId), [openBlock, eventId, blockId]);

  const [notice, setNotice] = useState<Notice | null>(null);
  const [legendOpen, setLegendOpen] = useState(false);
  const [confirmSeat, setConfirmSeat] = useState<number | null>(null);
  const [cooldownUntil, setCooldownUntil] = useState(0);
  const noticeTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const say = useCallback((next: Notice, sticky = false) => {
    if (noticeTimer.current) clearTimeout(noticeTimer.current);
    setNotice(next);
    if (!sticky) noticeTimer.current = setTimeout(() => setNotice(null), 5000);
  }, []);
  useEffect(() => () => clearTimeout(noticeTimer.current), []);
  // Checkout sends the fan back here with `expired` set when the hold ran out.
  useEffect(() => {
    if (expired) say({ tone: "warning", text: "Your time to pay ran out, so the seats were released. Choose your seats again." }, true);
  }, [expired, say]);

  // Geometry depends only on where seats are. A poll that changes statuses keeps the same
  // `block` object and seat count (see applyDelta), so the grid index is not rebuilt.
  const data = block.data;
  const geometryKey = data?.block;
  const seatCount = useMemo(() => data?.rows.reduce((n, r) => n + r.seats.length, 0) ?? 0, [data?.rows]);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  const geometry = useMemo(() => (data ? buildGeometry(data) : null), [geometryKey, seatCount]);
  const statuses = useMemo(() => (data ? seatStatuses(data) : []), [data]);
  const selectedSet = useMemo(() => new Set(selectedIds), [selectedIds]);
  const lostSet = useMemo(() => new Set(lostIds), [lostIds]);
  const scene = useMemo(
    () => (geometry && data ? buildScene({ geometry, statuses, categories: data.categories, selected: selectedSet, lost: lostSet }) : null),
    [geometry, data, statuses, selectedSet, lostSet],
  );

  // A seat the fan had picked that the poll now reports as taken leaves the selection, with a word why.
  useEffect(() => {
    if (!geometry || selectedIds.length === 0) return;
    const gone = selectedIds.filter((seatId) => {
      const k = geometry.indexById.get(seatId);
      return k !== undefined && statuses[k] !== "A";
    });
    if (gone.length === 0) return;
    const labels = gone.map((seatId) => seatLabel(geometry, geometry.indexById.get(seatId)!)).join(", ");
    removeSeats(gone);
    say({ tone: "warning", text: `${gone.length === 1 ? `Seat ${labels} was` : `Seats ${labels} were`} just taken by someone else.` });
  }, [geometry, statuses, selectedIds, removeSeats, say]);

  // A lost seat stays greyed until the server says it is free again (the other fan's hold
  // lapsed). Only statuses loaded after the refusal count; the cache at that moment still
  // said "available", which is why the hold was tried.
  const [lostSettled, setLostSettled] = useState(false);
  useEffect(() => {
    if (!geometry || !lostSettled || lostIds.length === 0) return;
    const back = lostIds.filter((seatId) => {
      const k = geometry.indexById.get(seatId);
      return k === undefined || statuses[k] === "A";
    });
    if (back.length > 0) unmarkLost(back);
  }, [geometry, statuses, lostIds, lostSettled, unmarkLost]);

  // Seats this fan already holds for the event (an unfinished checkout, or "best available").
  const activeHold = holds.data?.items.find((h) => h.event_id === eventId) ?? null;
  useEffect(() => {
    if (activeHold) syncHold(activeHold.expires_at, activeHold.remaining_seconds);
  }, [activeHold, syncHold]);
  const now = useNow(1000, activeHold !== null || cooldownUntil > 0);
  const holdLeft = activeHold ? remainingMs(holdExpiresAt ?? activeHold.expires_at, now, serverOffset) : 0;

  // After "best available": bring the seats the server chose into view.
  const canvasRef = useRef<SeatCanvasHandle>(null);
  const focused = useRef(false);
  useEffect(() => {
    if (!found || focused.current || !geometry || !activeHold) return;
    const indexes = activeHold.seats.map((s) => geometry.indexById.get(s.event_seat_id)).filter((k): k is number => k !== undefined);
    if (indexes.length === 0) return;
    focused.current = true;
    const timer = setTimeout(() => canvasRef.current?.focusSeats(indexes), 350);
    return () => clearTimeout(timer);
  }, [found, geometry, activeHold]);

  const maxSeats = event.data?.max_tickets_per_user ?? null;

  const onSeatPress = useCallback(
    (index: number, look: SeatLook) => {
      if (!geometry) return;
      const label = seatLabel(geometry, index);
      if (!isSelectable(look)) {
        const why: Record<Exclude<SeatLook, "available" | "selected">, string> = {
          sold: `Seat ${label} is sold.`,
          held: `Someone else is paying for seat ${label} right now. It may come back.`,
          blocked: `Seat ${label} is not for sale.`,
          mine: `You are already holding seat ${label}.`,
        };
        say({ tone: "info", text: why[look as keyof typeof why] });
        return;
      }
      const seatId = geometry.ids[index]!;
      if (look === "selected") {
        toggleSeat(seatId);
        return;
      }
      if (activeHold) {
        say({ tone: "warning", text: "You are already holding seats for this event. Pay for them, or release them, before choosing others." });
        return;
      }
      if (maxSeats !== null && selectedIds.length >= maxSeats) {
        say({ tone: "warning", text: `You can choose up to ${maxSeats} seats for this event.` });
        return;
      }
      // A restricted view is confirmed before the seat joins the selection (§5.3).
      if (geometry.type[index] === SEAT_RESTRICTED) {
        setConfirmSeat(index);
        return;
      }
      toggleSeat(seatId);
      if (geometry.type[index] === SEAT_ACCESSIBLE) say({ tone: "info", text: `Seat ${label} is a wheelchair space.` });
      else if (geometry.type[index] === SEAT_COMPANION) say({ tone: "info", text: `Seat ${label} is a companion seat next to a wheelchair space.` });
    },
    [geometry, activeHold, maxSeats, selectedIds.length, toggleSeat, say],
  );

  const selection = useMemo(() => {
    if (!geometry || !data) return { labels: [] as string[], total: 0 };
    const indexes = selectedIds.map((seatId) => geometry.indexById.get(seatId)).filter((k): k is number => k !== undefined);
    return {
      labels: indexes.map((k) => seatLabel(geometry, k)),
      total: sumCents(indexes.map((k) => data.categories[geometry.category[k]!]?.price_cents ?? 0)),
    };
  }, [geometry, data, selectedIds]);

  const refetchBlock = block.refetch;
  const hold = useCallback(() => {
    if (selectedIds.length === 0) return;
    createHold.mutate(
      { event_id: eventId, seat_ids: selectedIds },
      {
        onSuccess: async (h) => {
          syncHold(h.expires_at, h.remaining_seconds);
          clearSelection();
          // The idempotency key for this checkout is made here, once, and kept with the hold.
          await writeStoredCheckout({ holdId: h.id, eventId, idempotencyKey: Crypto.randomUUID(), bookingReference: null });
          router.push({ pathname: "/checkout/[holdId]", params: { holdId: String(h.id) } });
        },
        onError: (error) => {
          const lost = unavailableSeatIds(error);
          if (lost.length > 0) {
            // Grey out what was lost, keep the rest selected, and load the block's real state.
            setLostSettled(false);
            markLost(lost);
            void refetchBlock().then((r) => setLostSettled(r.isSuccess));
          }
          if (error instanceof ApiError && error.status === 429) setCooldownUntil(Date.now() + (error.retryAfterSeconds ?? 10) * 1000);
          say({ tone: "error", text: errorMessage(error) }, true);
        },
      },
    );
  }, [selectedIds, createHold, eventId, syncHold, clearSelection, markLost, refetchBlock, say]);

  // Signing in is asked for only at Continue. When the fan comes back signed in, carry on.
  const continueAfterSignIn = useRef(false);
  useEffect(() => {
    if (signedIn && isFocused && continueAfterSignIn.current) {
      continueAfterSignIn.current = false;
      hold();
    }
  }, [signedIn, isFocused, hold]);

  const onContinue = () => {
    if (!signedIn) {
      continueAfterSignIn.current = true;
      router.push("/sign-in");
      return;
    }
    hold();
  };

  if (block.isPending) return <Loading label="Loading seats…" />;
  if (block.isError || !geometry || !scene || !data) {
    return <ErrorState error={block.error} onRetry={() => void block.refetch()} title="We could not load the seats" />;
  }

  const cooldown = Math.max(0, Math.ceil((cooldownUntil - now) / 1000));
  const count = selectedIds.length;

  return (
    <View style={styles.root}>
      <Stack.Screen options={{ title: data.block.label }} />

      <View style={styles.top}>
        <View style={styles.categories}>
          {data.categories.map((c) => (
            <View key={c.id} style={styles.category}>
              <View style={[styles.swatch, { backgroundColor: c.color_hex }]} />
              <Text style={type.small} maxFontSizeMultiplier={1.4}>
                {c.name} {formatKes(c.price_cents)}
              </Text>
            </View>
          ))}
        </View>
        <Pressable onPress={() => setLegendOpen(true)} accessibilityRole="button" hitSlop={10} style={styles.legendButton} testID="open-legend">
          <Icon name="info" size={16} />
          <Text style={[type.label, { color: palette.emeraldInk }]} maxFontSizeMultiplier={1.4}>Key</Text>
        </Pressable>
      </View>

      <SeatCanvas ref={canvasRef} geometry={geometry} scene={scene} onSeatPress={onSeatPress} />

      <View style={[styles.bottom, { paddingBottom: Math.max(insets.bottom, space.md) }]}>
        {pollingPaused ? <Text style={[type.small, { color: palette.warning }]}>Live updates are paused. Seats shown may be out of date.</Text> : null}
        {notice ? (
          <Banner tone={notice.tone} testID="seat-notice">
            {notice.text}
          </Banner>
        ) : null}

        {activeHold ? (
          <>
            <View style={styles.summary}>
              <View style={styles.fill}>
                <Text style={type.h3} testID="held-summary">
                  You are holding {activeHold.seats.length} seat{activeHold.seats.length === 1 ? "" : "s"}
                </Text>
                <Text style={type.muted}>
                  {activeHold.seats.map((s) => s.location.seat_label).join(", ")} in {activeHold.seats[0]?.location.block_label} · {formatKes(activeHold.subtotal_cents)}
                </Text>
              </View>
              <Text style={styles.timer} maxFontSizeMultiplier={1.3}>{formatCountdown(holdLeft)}</Text>
            </View>
            <Button title="Continue to checkout" onPress={() => router.push({ pathname: "/checkout/[holdId]", params: { holdId: String(activeHold.id) } })} testID="continue-held" />
          </>
        ) : (
          <>
            <View style={styles.summary}>
              <View style={styles.fill}>
                <Text style={type.h3} testID="selection-count">
                  {count === 0 ? "No seats chosen" : `${count} seat${count === 1 ? "" : "s"} chosen`}
                </Text>
                <Text style={type.muted} testID="selection-labels">
                  {count === 0 ? `Tap a seat to choose it${maxSeats ? ` (up to ${maxSeats})` : ""}.` : selection.labels.join(", ")}
                </Text>
              </View>
              {count > 0 ? (
                <Text style={styles.total} testID="selection-total" maxFontSizeMultiplier={1.4}>
                  {formatKes(selection.total)}
                </Text>
              ) : null}
            </View>
            <Button
              title={cooldown > 0 ? `Try again in ${cooldown}s` : signedIn ? "Continue" : "Sign in to continue"}
              onPress={onContinue}
              disabled={count === 0 || cooldown > 0}
              loading={createHold.isPending}
              testID="continue"
            />
          </>
        )}
      </View>

      <Legend visible={legendOpen} onClose={() => setLegendOpen(false)} categories={data.categories} />

      <Sheet visible={confirmSeat !== null} onClose={() => setConfirmSeat(null)} title="Restricted view">
        <Banner tone="warning">
          {`From seat ${confirmSeat !== null ? seatLabel(geometry, confirmSeat) : ""} part of the pitch is hidden, for example by the tier above or a pillar. It is sold at the same price and cannot be swapped on the day.`}
        </Banner>
        <Button
          title="I understand, choose this seat"
          testID="confirm-restricted"
          onPress={() => {
            if (confirmSeat !== null) toggleSeat(geometry.ids[confirmSeat]!);
            setConfirmSeat(null);
          }}
        />
        <Button title="Pick a different seat" variant="secondary" onPress={() => setConfirmSeat(null)} />
      </Sheet>
    </View>
  );
}

const EMPTY: number[] = [];

const styles = StyleSheet.create({
  root: { flex: 1, backgroundColor: palette.champagne50 },
  fill: { flex: 1 },
  top: {
    flexDirection: "row",
    alignItems: "center",
    gap: space.sm,
    paddingHorizontal: space.lg,
    paddingVertical: space.sm,
    backgroundColor: palette.white,
    borderBottomWidth: StyleSheet.hairlineWidth,
    borderBottomColor: palette.grey300,
  },
  categories: { flex: 1, flexDirection: "row", flexWrap: "wrap", columnGap: space.md, rowGap: 2 },
  category: { flexDirection: "row", alignItems: "center", gap: 6 },
  swatch: { width: 12, height: 12, borderRadius: 3 },
  legendButton: { flexDirection: "row", alignItems: "center", gap: 4, minHeight: 36, paddingHorizontal: space.sm, borderRadius: radius.pill, borderWidth: 1, borderColor: palette.ink200 },
  bottom: {
    paddingHorizontal: space.lg,
    paddingTop: space.md,
    gap: space.sm,
    backgroundColor: palette.white,
    borderTopWidth: StyleSheet.hairlineWidth,
    borderTopColor: palette.grey300,
  },
  summary: { flexDirection: "row", alignItems: "center", gap: space.md },
  total: { fontSize: 18, fontWeight: "800", color: palette.emeraldInk },
  timer: { fontSize: 22, fontWeight: "800", color: palette.warning, fontVariant: ["tabular-nums"] },
});
