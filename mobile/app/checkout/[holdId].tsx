import { ApiError } from "@talanta/shared";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import * as Crypto from "expo-crypto";
import { router, Stack, useFocusEffect, useLocalSearchParams } from "expo-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { ActivityIndicator, Alert, BackHandler, Pressable, StyleSheet, Text, View } from "react-native";

import type { Schemas } from "../../src/api/client";
import { createBooking, fetchBooking, isLivePayment, keys, startStkPush, useBooking, useHold, usePayment, useReleaseHold } from "../../src/api/queries";
import { clearStoredCheckout, readStoredCheckout, writeStoredCheckout, type StoredCheckout } from "../../src/api/storage";
import { formatCountdown, isUrgent, remainingMs } from "../../src/lib/countdown";
import { errorMessage, paymentOutcome } from "../../src/lib/errors";
import { formatKes } from "../../src/lib/money";
import { formatPhone, formatPhoneLocal, normalisePhone } from "../../src/lib/phone";
import { formatDateTime } from "../../src/lib/time";
import { useAppActive } from "../../src/lib/useAppActive";
import { useNow } from "../../src/lib/useNow";
import { useSession } from "../../src/session";
import { useBookingFlow } from "../../src/store/bookingFlow";
import { palette, radius, space, type } from "../../src/theme";
import { Banner, Button, Card, ErrorState, Field, Icon, Loading, Screen } from "../../src/ui";

type MockOutcome = Schemas["MockOutcome"];
const MOCK_OUTCOMES: MockOutcome[] = ["success", "insufficient_funds", "wrong_pin", "user_cancel", "timeout", "wrong_amount", "no_callback", "late_callback", "duplicate_callback"];

function has<K extends string>(details: unknown, key: K): details is Record<K, unknown> {
  return typeof details === "object" && details !== null && key in details;
}

export default function CheckoutScreen() {
  const { holdId: holdParam } = useLocalSearchParams<{ holdId: string }>();
  const holdId = Number(holdParam);
  const qc = useQueryClient();
  const { user } = useSession();
  const appActive = useAppActive();

  const hold = useHold(holdId);
  const release = useReleaseHold();
  const syncHold = useBookingFlow((s) => s.syncHold);
  const setHoldExpiry = useBookingFlow((s) => s.setHoldExpiry);
  const clearHold = useBookingFlow((s) => s.clearHold);
  const holdExpiresAt = useBookingFlow((s) => s.holdExpiresAt);
  const serverOffset = useBookingFlow((s) => s.serverOffsetMs);

  // ------------------------------------------------------------------ the checkout record
  // One idempotency key per checkout. It is normally made when the hold is created; if the
  // phone has none for this hold (storage was cleared), a new one is made here, once.
  const [checkout, setCheckout] = useState<StoredCheckout | null>(null);
  const eventId = hold.data?.event_id;
  useEffect(() => {
    if (eventId === undefined) return;
    let cancelled = false;
    void readStoredCheckout().then(async (stored) => {
      let record = stored && stored.holdId === holdId ? stored : null;
      if (!record) {
        record = { holdId, eventId, idempotencyKey: Crypto.randomUUID(), bookingReference: null };
        await writeStoredCheckout(record);
      }
      if (!cancelled) setCheckout(record);
    });
    return () => {
      cancelled = true;
    };
  }, [holdId, eventId]);

  // ------------------------------------------------------------------ hold and countdown
  const holdData = hold.data;
  useEffect(() => {
    if (holdData && holdData.status === "ACTIVE") syncHold(holdData.expires_at, holdData.remaining_seconds);
  }, [holdData, syncHold]);

  // Back in the foreground: ask the server how long is really left.
  const refetchHold = hold.refetch;
  const wasActive = useRef(appActive);
  useEffect(() => {
    if (appActive && !wasActive.current) void refetchHold();
    wasActive.current = appActive;
  }, [appActive, refetchHold]);

  // ------------------------------------------------------------------ booking
  const reference = holdData?.booking_reference ?? checkout?.bookingReference ?? null;
  const booking = useBooking(reference);

  const ensureBooking = useMutation({
    mutationFn: async (record: StoredCheckout) => {
      try {
        return await createBooking(record.holdId, record.idempotencyKey);
      } catch (error) {
        // The hold was booked under a key this phone no longer has: use that booking.
        if (error instanceof ApiError && error.code === "HOLD_ALREADY_BOOKED" && has(error.details, "booking_reference")) {
          return fetchBooking(String(error.details.booking_reference));
        }
        throw error;
      }
    },
    onSuccess: async (b, record) => {
      qc.setQueryData(keys.booking(b.reference), b);
      if (b.hold_expires_at) setHoldExpiry(b.hold_expires_at);
      const next = { ...record, bookingReference: b.reference };
      setCheckout(next);
      await writeStoredCheckout(next);
    },
  });

  // The booking is made as soon as checkout opens, because only a booking carries the fee
  // and the total; the fan must see the amount before being asked to pay it. The same key
  // is sent again by "Pay" if this first attempt did not get through.
  const autoBooked = useRef(false);
  const { mutate: bookNow } = ensureBooking;
  useEffect(() => {
    if (autoBooked.current || !checkout || !holdData || holdData.status !== "ACTIVE" || reference) return;
    autoBooked.current = true;
    bookNow(checkout);
  }, [checkout, holdData, reference, bookNow]);

  const bookingData = booking.data ?? (ensureBooking.data && ensureBooking.data.reference === reference ? ensureBooking.data : undefined);
  const bookingExpiry = bookingData?.hold_expires_at;
  useEffect(() => {
    if (bookingExpiry) setHoldExpiry(bookingExpiry);
  }, [bookingExpiry, setHoldExpiry]);

  // ------------------------------------------------------------------ payment
  const [paymentId, setPaymentId] = useState<number | null>(null);
  const latestPaymentId = bookingData?.latest_payment?.id ?? null;
  // Reopening the app mid-payment: the booking names its latest payment; carry on polling it.
  useEffect(() => {
    if (paymentId === null && latestPaymentId !== null) setPaymentId(latestPaymentId);
  }, [paymentId, latestPaymentId]);
  const payment = usePayment(paymentId);
  const paymentData = payment.data ?? (bookingData?.latest_payment?.id === paymentId ? bookingData?.latest_payment : undefined) ?? undefined;
  const paymentLive = paymentData ? isLivePayment(paymentData.status) : false;

  const [phoneInput, setPhoneInput] = useState("");
  const [phoneError, setPhoneError] = useState<string | null>(null);
  const [mock, setMock] = useState<MockOutcome>("success");
  const defaultPhone = user?.phone ?? null;
  useEffect(() => {
    if (defaultPhone) setPhoneInput((current) => (current === "" ? formatPhoneLocal(defaultPhone) : current));
  }, [defaultPhone]);

  const refetchBooking = booking.refetch;
  const pay = useMutation({
    mutationFn: async (phone: string) => {
      if (!checkout) throw new Error("Checkout is not ready");
      // Same key as before: if the booking already exists the server returns it (200).
      const b = bookingData ?? (await ensureBooking.mutateAsync(checkout));
      try {
        // The mock header is sent only by development builds, and only when it is not the default.
        return await startStkPush(b.id, phone, __DEV__ && mock !== "success" ? mock : undefined);
      } catch (error) {
        // A prompt is already out for this booking (a double tap, or a second phone): follow it.
        if (error instanceof ApiError && error.code === "PAYMENT_IN_PROGRESS" && has(error.details, "payment_id")) {
          return { id: Number(error.details.payment_id) } as Pick<Schemas["Payment"], "id">;
        }
        throw error;
      }
    },
    onSuccess: (p) => {
      if ("status" in p) qc.setQueryData(keys.payment(p.id), p);
      setPaymentId(p.id);
      // Starting a payment may push the hold's expiry out once; read the new time.
      void refetchBooking();
      void refetchHold();
    },
    onError: (error) => {
      if (error instanceof ApiError && (error.code === "HOLD_EXPIRED" || error.code === "HOLD_NOT_ACTIVE" || error.code === "BOOKING_NOT_PAYABLE")) {
        void refetchHold();
        void refetchBooking();
      }
    },
  });

  const onPay = () => {
    const result = normalisePhone(phoneInput);
    if (!result.ok) {
      setPhoneError(result.message);
      return;
    }
    setPhoneError(null);
    pay.mutate(result.e164);
  };

  // A payment that has just finished changes the booking (paid, or refund owed) and may have
  // changed the hold: read both again.
  const paymentStatus = paymentData?.status;
  useEffect(() => {
    if (paymentStatus && !isLivePayment(paymentStatus)) {
      void refetchBooking();
      void refetchHold();
    }
  }, [paymentStatus, refetchBooking, refetchHold]);

  // ------------------------------------------------------------------ outcomes
  // The booking and the payment are read separately, so one can be a moment behind the
  // other. A final status from either wins over "still waiting" from the other.
  const bookingStatus =
    bookingData && bookingData.status !== "PENDING_PAYMENT" ? bookingData.status : (paymentData?.booking_status ?? bookingData?.status);
  const paidReference = bookingStatus === "PAID" ? (bookingData?.reference ?? paymentData?.booking_reference ?? null) : null;
  const leaving = useRef(false);
  useEffect(() => {
    if (!paidReference || leaving.current) return;
    leaving.current = true;
    clearHold();
    void clearStoredCheckout();
    void qc.invalidateQueries({ queryKey: keys.tickets });
    void qc.invalidateQueries({ queryKey: keys.myHolds });
    router.replace({ pathname: "/booking/[reference]", params: { reference: paidReference } });
  }, [paidReference, clearHold, qc]);

  const now = useNow(500);
  const left = remainingMs(holdExpiresAt ?? holdData?.expires_at ?? null, now, serverOffset);
  const refundOwed = bookingStatus === "REFUND_REQUIRED" || bookingStatus === "REFUNDED";
  const holdGone = holdData ? holdData.status === "EXPIRED" || holdData.status === "RELEASED" : false;

  // The clock reached zero: ask the server before believing it (the expiry may have been
  // pushed out for a payment). While a payment is live the server keeps the seats.
  const askedAtZero = useRef<string | null>(null);
  useEffect(() => {
    if (!holdData || holdData.status !== "ACTIVE" || left > 0 || paymentLive) return;
    const stamp = holdExpiresAt ?? holdData.expires_at;
    if (askedAtZero.current === stamp) return;
    askedAtZero.current = stamp;
    void refetchHold();
    void refetchBooking();
  }, [holdData, left, paymentLive, holdExpiresAt, refetchHold, refetchBooking]);

  // The hold is over and nothing was paid: back to the seat map with a clear message.
  const blockId = holdData?.seats[0]?.location.block_id;
  const expiredOut = holdGone && !refundOwed && !paidReference && !paymentLive;
  useEffect(() => {
    if (!expiredOut || leaving.current || eventId === undefined) return;
    leaving.current = true;
    clearHold();
    void clearStoredCheckout();
    void qc.invalidateQueries({ queryKey: keys.myHolds });
    void qc.invalidateQueries({ queryKey: ["events", eventId] });
    if (blockId !== undefined) {
      router.dismissTo({ pathname: "/event/[id]/block/[blockId]", params: { id: String(eventId), blockId: String(blockId), expired: "1" } });
    } else {
      router.dismissTo({ pathname: "/event/[id]", params: { id: String(eventId) } });
    }
  }, [expiredOut, eventId, blockId, clearHold, qc]);

  // ------------------------------------------------------------------ leaving
  const canRelease = holdData?.status === "ACTIVE" && !paymentLive && !refundOwed;
  const { mutate: releaseNow } = release;
  const confirmLeave = useCallback(() => {
    if (!canRelease || eventId === undefined) {
      router.back();
      return;
    }
    Alert.alert("Leave checkout?", "Your seats are still held for you. You can keep them and come back, or give them up now so someone else can buy them.", [
      { text: "Stay", style: "cancel" },
      { text: "Keep seats", onPress: () => router.back() },
      {
        text: "Release seats",
        style: "destructive",
        onPress: () =>
          releaseNow(
            { id: holdId, eventId },
            {
              onSuccess: () => {
                leaving.current = true;
                clearHold();
                void clearStoredCheckout();
                router.back();
              },
              onError: (error) => Alert.alert("We could not release the seats", errorMessage(error)),
            },
          ),
      },
    ]);
    return;
  }, [canRelease, eventId, holdId, releaseNow, clearHold]);

  useFocusEffect(
    useCallback(() => {
      const sub = BackHandler.addEventListener("hardwareBackPress", () => {
        confirmLeave();
        return true;
      });
      return () => sub.remove();
    }, [confirmLeave]),
  );

  const header = (
    <Stack.Screen
      options={{
        headerBackVisible: false,
        headerLeft: () => (
          <Pressable onPress={confirmLeave} accessibilityRole="button" accessibilityLabel="Leave checkout" hitSlop={12} style={styles.back} testID="leave-checkout">
            <View style={styles.backIcon}>
              <Icon name="chevron" color={palette.champagne} size={22} />
            </View>
          </Pressable>
        ),
      }}
    />
  );

  if (hold.isPending) return <>{header}<Loading label="Loading your seats…" /></>;
  if (hold.isError || !holdData) {
    return <>{header}<ErrorState error={hold.error} onRetry={() => void hold.refetch()} title="We could not load your seats" /></>;
  }
  if (paidReference || expiredOut) return <>{header}<Loading label={paidReference ? "Getting your tickets…" : "Going back to the seat map…"} /></>;

  const seats = holdData.seats;
  const first = seats[0]?.location;
  const outcome = paymentData ? paymentOutcome({ status: paymentData.status, failure_reason: paymentData.failure_reason, booking_status: bookingStatus ?? paymentData.booking_status }) : null;
  const failed = outcome?.tone === "failed";
  const total = bookingData?.total_cents ?? null;
  const urgent = isUrgent(left) && !paymentLive;
  const waiting = paymentLive || pay.isPending;

  return (
    <Screen
      footer={
        refundOwed ? (
          <Button title="Back to events" onPress={() => { void clearStoredCheckout(); router.dismissTo("/events"); }} testID="refund-done" />
        ) : (
          <>
            <Button
              title={waiting ? "Waiting for M-Pesa…" : failed ? `Try again · ${total !== null ? formatKes(total) : ""}` : total !== null ? `Pay ${formatKes(total)} with M-Pesa` : "Pay with M-Pesa"}
              onPress={onPay}
              // Disabled from the tap until the payment has a final answer, so it cannot be sent twice.
              disabled={waiting || left <= 0 || !checkout || (failed && !outcome?.canRetry)}
              loading={pay.isPending}
              icon={waiting ? undefined : "lock"}
              testID="pay"
            />
            <Text style={[type.small, styles.center]}>We send a prompt to your phone. You enter your M-Pesa PIN there, never in this app.</Text>
          </>
        )
      }
    >
      {header}

      {refundOwed ? null : (
        <View style={[styles.countdown, urgent && styles.countdownUrgent]} accessibilityRole="timer" accessibilityLabel={`${formatCountdown(left)} left to pay`}>
          <Icon name="clock" color={urgent ? palette.error : palette.warning} />
          <View style={styles.fill}>
            <Text style={[type.label, { color: urgent ? palette.error : palette.warning }]}>{paymentLive ? "Your seats are safe while you pay" : "Your seats are held for"}</Text>
            {holdData.extended ? <Text style={type.small}>We added time so your payment can finish.</Text> : null}
          </View>
          <Text style={[styles.clock, { color: urgent ? palette.error : palette.warning }]} testID="countdown" maxFontSizeMultiplier={1.3}>
            {formatCountdown(left)}
          </Text>
        </View>
      )}

      {paymentLive ? (
        <Card style={styles.waiting} >
          <ActivityIndicator size="large" color={palette.emeraldInk} />
          <Text style={[type.h2, styles.center]} testID="payment-waiting">Check your phone</Text>
          <Text style={[type.body, styles.center]}>
            We sent an M-Pesa prompt to {formatPhone(paymentData?.phone)}. Enter your PIN to pay{total !== null ? ` ${formatKes(total)}` : ""}.
          </Text>
          <Text style={[type.small, styles.center]}>This page updates by itself. You can leave the app to enter your PIN and come back.</Text>
        </Card>
      ) : null}

      {outcome && !paymentLive && outcome.tone !== "success" && !pay.isPending ? (
        <Banner tone={outcome.tone === "waiting" ? "info" : "error"} title={outcome.title} testID="payment-outcome">
          {`${outcome.body}${failed && left > 0 ? " Your seats are still held: you can try again." : ""}`}
        </Banner>
      ) : null}
      {pay.isError ? <Banner tone="error" testID="pay-error">{errorMessage(pay.error)}</Banner> : null}
      {ensureBooking.isError && !pay.isError ? (
        <Banner tone="error" action="Try again" onAction={() => checkout && ensureBooking.mutate(checkout)}>
          {errorMessage(ensureBooking.error)}
        </Banner>
      ) : null}

      <Card>
        <Text style={type.overline}>{bookingData?.event.competition ?? "Your seats"}</Text>
        <Text style={type.h2}>{bookingData?.event.title ?? (first ? `${first.stand_name} · ${first.block_label}` : "Your seats")}</Text>
        {bookingData ? <Text style={type.muted}>{formatDateTime(bookingData.event.kickoff_at)} · {bookingData.event.venue_name}</Text> : null}
        {first ? (
          <Text style={type.muted}>
            {first.stand_name} · {first.block_label} · Gate {first.gate_group}
          </Text>
        ) : null}
        <View style={styles.rule} />
        {seats.map((s) => (
          <View key={s.event_seat_id} style={styles.line}>
            <View style={styles.fill}>
              <Text style={type.body}>
                Row {s.location.row_label}, seat {s.location.seat_number}
              </Text>
              <Text style={type.small}>
                {s.category_name}
                {s.location.seat_type === "RESTRICTED_VIEW" ? " · restricted view" : s.location.seat_type === "ACCESSIBLE" ? " · wheelchair space" : s.location.seat_type === "COMPANION" ? " · companion seat" : ""}
              </Text>
            </View>
            <Text style={type.body}>{formatKes(bookingData?.items.find((i) => i.event_seat_id === s.event_seat_id)?.unit_price_cents ?? s.price_cents)}</Text>
          </View>
        ))}
        <View style={styles.rule} />
        <View style={styles.line}>
          <Text style={[type.muted, styles.fill]}>
            {seats.length} ticket{seats.length === 1 ? "" : "s"}
          </Text>
          <Text style={type.body} testID="subtotal">{formatKes(bookingData?.subtotal_cents ?? holdData.subtotal_cents)}</Text>
        </View>
        <View style={styles.line}>
          <Text style={[type.muted, styles.fill]}>Booking fee</Text>
          {bookingData ? <Text style={type.body} testID="fee">{bookingData.fee_cents === 0 ? "None" : formatKes(bookingData.fee_cents)}</Text> : <ActivityIndicator color={palette.emeraldInk} />}
        </View>
        <View style={styles.line}>
          <Text style={[type.h3, styles.fill]}>Total</Text>
          {total !== null ? <Text style={type.h2} testID="total">{formatKes(total)}</Text> : <ActivityIndicator color={palette.emeraldInk} />}
        </View>
        {bookingData ? <Text style={type.small}>Booking reference {bookingData.reference}</Text> : null}
      </Card>

      {refundOwed ? null : (
        <Card>
          <Text style={type.h3}>Pay with M-Pesa</Text>
          <Field
            label="M-Pesa number"
            testID="mpesa-phone"
            value={phoneInput}
            onChangeText={(t) => {
              setPhoneInput(t);
              setPhoneError(null);
            }}
            keyboardType="phone-pad"
            editable={!waiting}
            error={phoneError}
            hint="The number that will get the payment prompt. It can be different from the one you signed in with."
          />
          {__DEV__ ? (
            <View style={styles.dev} testID="mock-picker">
              <Text style={styles.devLabel}>DEVELOPMENT ONLY · MOCK OUTCOME</Text>
              <View style={styles.devOptions}>
                {MOCK_OUTCOMES.map((o) => (
                  <Pressable key={o} onPress={() => setMock(o)} disabled={waiting} accessibilityRole="radio" accessibilityState={{ selected: mock === o }} testID={`mock-${o}`} style={[styles.devOption, mock === o && styles.devOptionOn]}>
                    <Text style={[styles.devOptionText, mock === o && { color: palette.white }]}>{o}</Text>
                  </Pressable>
                ))}
              </View>
            </View>
          ) : null}
        </Card>
      )}
    </Screen>
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1 },
  center: { textAlign: "center" },
  back: { minWidth: 44, minHeight: 44, justifyContent: "center" },
  backIcon: { transform: [{ rotate: "180deg" }] },
  countdown: { flexDirection: "row", alignItems: "center", gap: space.md, padding: space.md, borderRadius: radius.md, borderWidth: 1, backgroundColor: palette.warningBg, borderColor: palette.warningBorder },
  countdownUrgent: { backgroundColor: palette.errorBg, borderColor: palette.errorBorder },
  clock: { fontSize: 28, fontWeight: "800", fontVariant: ["tabular-nums"] },
  waiting: { alignItems: "center", gap: space.md, paddingVertical: space.xl },
  rule: { height: StyleSheet.hairlineWidth, backgroundColor: palette.grey300, marginVertical: space.xs },
  line: { flexDirection: "row", alignItems: "center", gap: space.md, paddingVertical: 3 },
  dev: { backgroundColor: palette.warningBg, borderColor: palette.warningBorder, borderWidth: 1, borderStyle: "dashed", borderRadius: radius.md, padding: space.md, gap: space.sm },
  devLabel: { ...type.overline, color: palette.warning },
  devOptions: { flexDirection: "row", flexWrap: "wrap", gap: 6 },
  devOption: { paddingHorizontal: 10, minHeight: 32, justifyContent: "center", borderRadius: radius.pill, borderWidth: 1, borderColor: palette.warningBorder, backgroundColor: palette.white },
  devOptionOn: { backgroundColor: palette.warning, borderColor: palette.warning },
  devOptionText: { fontSize: 12, fontWeight: "600", color: palette.warning },
});
