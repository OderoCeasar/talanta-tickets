import { ApiError } from "@talanta/shared";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import * as Crypto from "expo-crypto";
import { router, Stack, useFocusEffect, useLocalSearchParams } from "expo-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { ActivityIndicator, Alert, BackHandler, Pressable, StyleSheet, Text, View } from "react-native";

import type { Schemas } from "../../src/api/client";
import { createBooking, fetchBooking, isLivePayment, keys, startStkPush, useBooking, useEvent, useHold, usePayment, useReleaseHold } from "../../src/api/queries";
import { clearStoredCheckout, readStoredCheckout, writeStoredCheckout, type StoredCheckout } from "../../src/api/storage";
import { checkoutAmounts } from "../../src/lib/checkout";
import { formatCountdown, isUrgent, remainingMs } from "../../src/lib/countdown";
import { errorMessage, holdEnded, isEventBusy, isRetryable, livePaymentId, paymentOutcome } from "../../src/lib/errors";
import { formatKes } from "../../src/lib/money";
import { formatPhone, formatPhoneLocal, normalisePhone } from "../../src/lib/phone";
import { formatDateTime } from "../../src/lib/time";
import { useAppActive } from "../../src/lib/useAppActive";
import { useNow } from "../../src/lib/useNow";
import { useSession } from "../../src/session";
import { useBookingFlow } from "../../src/store/bookingFlow";
import { standTitle } from "../../src/lib/labels";
import { palette, radius, space, type } from "../../src/theme";
import { Banner, Button, Card, ErrorState, Field, FlowSteps, Icon, Loading, Screen } from "../../src/ui";

type MockOutcome = Schemas["MockOutcome"];
const MOCK_OUTCOMES: MockOutcome[] = ["success", "insufficient_funds", "wrong_pin", "user_cancel", "timeout", "wrong_amount", "no_callback", "late_callback", "duplicate_callback"];

function has<K extends string>(details: unknown, key: K): details is Record<K, unknown> {
  return typeof details === "object" && details !== null && key in details;
}

/** The booking for a hold: made with the checkout's key, or the one the hold already has. */
async function bookingFor(record: StoredCheckout): Promise<Schemas["Booking"]> {
  try {
    // The same key again answers with the same booking, so a retry never makes a second one.
    return await createBooking(record.holdId, record.idempotencyKey);
  } catch (error) {
    // The hold was booked under a key this phone no longer has: use that booking.
    if (error instanceof ApiError && error.code === "HOLD_ALREADY_BOOKED" && has(error.details, "booking_reference")) {
      return fetchBooking(String(error.details.booking_reference));
    }
    throw error;
  }
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
  const clearHold = useBookingFlow((s) => s.clearHold);
  const holdExpiresAt = useBookingFlow((s) => s.holdExpiresAt);
  const serverOffset = useBookingFlow((s) => s.serverOffsetMs);

  // ------------------------------------------------------------------ the checkout record
  // One idempotency key per checkout. It is normally made when the hold is created; if the
  // phone has none for this hold (storage was cleared), a new one is made here, once.
  const [checkout, setCheckout] = useState<StoredCheckout | null>(null);
  const holdData = hold.data;
  const eventId = holdData?.event_id;
  // An ended hold answers with no seats, so the block to go back to is remembered from
  // the last answer that had them.
  const lastBlockId = useRef<number | undefined>(undefined);
  const heldBlockId = holdData?.seats[0]?.location.block_id;
  if (heldBlockId !== undefined) lastBlockId.current = heldBlockId;

  useEffect(() => {
    if (eventId === undefined) return;
    let cancelled = false;
    void readStoredCheckout().then(async (stored) => {
      let record = stored && stored.holdId === holdId ? stored : null;
      if (!record) {
        record = { holdId, eventId, blockId: heldBlockId, idempotencyKey: Crypto.randomUUID(), bookingReference: null };
        await writeStoredCheckout(record);
      }
      if (!cancelled) setCheckout(record);
    });
    return () => {
      cancelled = true;
    };
  }, [holdId, eventId, heldBlockId]);

  // ------------------------------------------------------------------ hold and countdown
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
  // There is no booking until the fan taps Pay: someone who only looks at the total must not
  // leave a pending booking behind. Before that, everything shown comes from the hold.
  const reference = holdData?.booking_reference ?? checkout?.bookingReference ?? null;
  const booking = useBooking(reference);
  const bookingData = booking.data;
  const refetchBooking = booking.refetch;
  const event = useEvent(eventId ?? 0, eventId !== undefined && !bookingData);

  // ------------------------------------------------------------------ payment
  const [paymentId, setPaymentId] = useState<number | null>(null);
  const latestPaymentId = bookingData?.latest_payment?.id ?? null;
  // Reopening the app mid-payment (or a newer attempt than the one this screen knows):
  // the booking names its latest payment; follow that one.
  useEffect(() => {
    if (latestPaymentId !== null && (paymentId === null || latestPaymentId > paymentId)) setPaymentId(latestPaymentId);
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

  // ------------------------------------------------------------------ leaving for the seat map
  const leaving = useRef(false);
  const leaveToMap = useCallback(
    (reason: "too_late" | "ended", keepPointer: boolean) => {
      if (leaving.current) return;
      leaving.current = true;
      clearHold();
      // A payment M-Pesa has not settled can still turn into tickets or a refund: the pointer
      // to its booking is kept so the app can say so later (see ResumeBanner).
      if (!keepPointer) void clearStoredCheckout();
      void qc.invalidateQueries({ queryKey: keys.myHolds });
      const blockId = lastBlockId.current ?? checkout?.blockId ?? bookingData?.items[0]?.location.block_id;
      const event = eventId ?? checkout?.eventId;
      if (event !== undefined) void qc.invalidateQueries({ queryKey: ["events", event] });
      if (event !== undefined && blockId !== undefined) {
        router.dismissTo({ pathname: "/event/[id]/block/[blockId]", params: { id: String(event), blockId: String(blockId), expired: reason } });
      } else if (event !== undefined) {
        router.dismissTo({ pathname: "/event/[id]", params: { id: String(event) } });
      } else {
        router.dismissTo("/events");
      }
    },
    [clearHold, qc, checkout, bookingData, eventId],
  );

  const { mutateAsync: releaseAsync } = release;
  const pay = useMutation({
    mutationFn: async (phone: string) => {
      if (!checkout) throw new Error("Checkout is not ready");
      let b = bookingData;
      if (!b) {
        b = await bookingFor(checkout);
        qc.setQueryData(keys.booking(b.reference), b);
        const next = { ...checkout, bookingReference: b.reference };
        setCheckout(next);
        await writeStoredCheckout(next);
      }
      try {
        // The mock header is sent only by development builds, and only when it is not the default.
        return await startStkPush(b.id, phone, __DEV__ && mock !== "success" ? mock : undefined);
      } catch (error) {
        // A prompt is already out for this booking (a double tap, or a second phone): follow it.
        const live = livePaymentId(error);
        if (live !== null) return { id: live } as Pick<Schemas["Payment"], "id">;
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
    onError: async (error) => {
      const ended = holdEnded(error);
      if (ended === "too_late" && eventId !== undefined) {
        // Under two minutes left: no prompt can be answered in time. Give the seats up now
        // (best effort; they lapse by themselves otherwise) and start again on the map.
        await releaseAsync({ id: holdId, eventId }).catch(() => undefined);
        leaveToMap("too_late", false);
        return;
      }
      // Whatever went wrong, the server's view of the hold and the booking is the truth: an
      // ended hold sends the fan back to the map below; a refused M-Pesa request left a
      // failed attempt on the booking.
      void refetchHold();
      void refetchBooking();
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
  const outcome = paymentData
    ? paymentOutcome({ status: paymentData.status, failure_reason: paymentData.failure_reason, booking_status: bookingStatus ?? paymentData.booking_status })
    : null;
  const paidReference = bookingStatus === "PAID" ? (bookingData?.reference ?? paymentData?.booking_reference ?? null) : null;
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
  // `outcome` covers a wrong amount seen on the payment before the booking read catches up.
  const refundOwed = bookingStatus === "REFUND_REQUIRED" || bookingStatus === "REFUNDED" || outcome?.tone === "refund";
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
  const expiredOut = holdGone && !refundOwed && !paidReference && !paymentLive && !pay.isPending;
  const unsettled = outcome?.unsettled ?? false;
  useEffect(() => {
    if (expiredOut) leaveToMap("ended", unsettled);
  }, [expiredOut, unsettled, leaveToMap]);

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
              onError: (error) => Alert.alert(isEventBusy(error) ? "Your seats are still held" : "We could not release the seats", errorMessage(error)),
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
            <Icon name="back" color={palette.emeraldInk} size={22} />
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

  const amounts = checkoutAmounts(holdData, bookingData);
  // Only a refund owed can leave nothing to show for a moment (hold ended, booking still loading).
  if (amounts.source === "none") {
    if (booking.isError) return <>{header}<ErrorState error={booking.error} onRetry={() => void booking.refetch()} title="We could not load your booking" /></>;
    return <>{header}<Loading label="Loading your booking…" /></>;
  }

  const first = amounts.lines[0]?.location;
  const count = amounts.lines.length;
  const total = amounts.totalCents;
  const eventInfo = bookingData?.event ?? event.data ?? null;
  const failed = outcome?.tone === "failed";
  const urgent = isUrgent(left) && !paymentLive;
  // Disabled from the tap until the payment has a final answer, so it cannot be sent twice.
  const waiting = paymentLive || pay.isPending || outcome?.tone === "waiting";
  const payError = pay.isError ? pay.error : null;
  const canPayAgain = left > 0 && !waiting;

  return (
    <Screen
      footer={
        refundOwed ? (
          <Button
            title="Back to events"
            loading={release.isPending}
            testID="refund-done"
            onPress={async () => {
              // The server can leave the hold (and its seats) in place after a payment that
              // has to be refunded. Nothing more can be paid on it, so give the seats back
              // now instead of letting them sit out the clock. Best effort.
              if (holdData.status === "ACTIVE") await releaseAsync({ id: holdId, eventId: holdData.event_id }).catch(() => undefined);
              leaving.current = true;
              clearHold();
              void clearStoredCheckout();
              router.dismissTo("/events");
            }}
          />
        ) : (
          <>
            <Button
              title={waiting ? "Waiting for M-Pesa…" : failed || payError ? `Try again · ${formatKes(total)}` : `Pay ${formatKes(total)} with M-Pesa`}
              onPress={onPay}
              disabled={waiting || left <= 0 || !checkout || (failed && !payError && !outcome?.canRetry)}
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
      {refundOwed ? null : <FlowSteps step={2} />}

      {refundOwed ? null : (
        <View style={[styles.countdown, urgent && styles.countdownUrgent]} accessibilityRole="timer" accessibilityLabel={`${formatCountdown(left)} left to pay`}>
          <Icon name="clock" color={urgent ? palette.error : palette.warning} />
          <View style={styles.fill}>
            <Text style={[type.label, { color: urgent ? palette.error : palette.warning }]}>{paymentLive ? "Your seats are safe while you pay" : "Your seats are held for"}</Text>
            {holdData.extended ? <Text style={type.small}>We added time so your payment can finish.</Text> : null}
          </View>
          <Text style={[type.clock, { color: urgent ? palette.error : palette.warning }]} testID="countdown" maxFontSizeMultiplier={1.3}>
            {formatCountdown(left)}
          </Text>
        </View>
      )}

      {paymentLive ? (
        <Card style={styles.waiting} >
          <ActivityIndicator size="large" color={palette.emeraldInk} />
          <Text style={[type.h2, styles.center]} testID="payment-waiting">{paymentData?.result_desc ? "Checking with M-Pesa" : "Check your phone"}</Text>
          {paymentData?.result_desc ? (
            // The server could not tell whether M-Pesa got the request ("Do not pay again
            // yet"): its own words are shown instead of promising a prompt.
            <Text style={[type.body, styles.center]} testID="payment-checking">{paymentData.result_desc}</Text>
          ) : (
            <Text style={[type.body, styles.center]}>
              We sent an M-Pesa prompt to {formatPhone(paymentData?.phone)}. Enter your PIN to pay {formatKes(total)}.
            </Text>
          )}
          <Text style={[type.small, styles.center]}>This page updates by itself. You can leave the app to enter your PIN and come back.</Text>
        </Card>
      ) : null}

      {/* One message at a time: why the last tap on Pay was refused wins over the outcome of an earlier payment. */}
      {payError ? (
        <Banner
          tone={isEventBusy(payError) ? "warning" : "error"}
          title={isEventBusy(payError) ? "One moment" : payError instanceof ApiError && payError.code === "PAYMENT_PROVIDER_UNAVAILABLE" ? "Payments are not available right now" : undefined}
          action={isRetryable(payError) && canPayAgain ? "Try again" : undefined}
          onAction={onPay}
          testID="pay-error"
        >
          {payError instanceof ApiError && payError.code === "PAYMENT_PROVIDER_UNAVAILABLE"
            ? "No money was taken. Your seats are still held: try again in a moment."
            : isEventBusy(payError)
              ? `${errorMessage(payError)} Your seats are still held.`
              : errorMessage(payError)}
        </Banner>
      ) : outcome && !paymentLive && outcome.tone !== "success" && !pay.isPending ? (
        <Banner tone={outcome.tone === "waiting" ? "info" : "error"} title={outcome.title} testID="payment-outcome">
          {`${outcome.body}${failed && outcome.canRetry && left > 0 ? " Your seats are still held: you can try again." : ""}`}
        </Banner>
      ) : null}

      <Card>
        <Text style={type.overline}>{eventInfo?.competition ?? "Your seats"}</Text>
        <Text style={type.h2}>{eventInfo?.title ?? (first ? `${standTitle(first.stand_name, first.tier)} · ${first.block_label}` : "Your seats")}</Text>
        {eventInfo ? <Text style={type.muted}>{formatDateTime(eventInfo.kickoff_at)} · {eventInfo.venue_name}</Text> : null}
        {first ? (
          <Text style={type.muted}>
            {standTitle(first.stand_name, first.tier)} · {first.block_label} · Gate {first.gate_group}
          </Text>
        ) : null}
        <View style={styles.rule} />
        {amounts.lines.map((line) => (
          <View key={line.eventSeatId} style={styles.line}>
            <View style={styles.fill}>
              <Text style={type.body}>
                Row {line.location.row_label}, seat {line.location.seat_number}
              </Text>
              <Text style={type.small}>
                {line.categoryName}
                {line.location.seat_type === "RESTRICTED_VIEW" ? " · restricted view" : line.location.seat_type === "ACCESSIBLE" ? " · wheelchair space" : line.location.seat_type === "COMPANION" ? " · companion seat" : ""}
              </Text>
            </View>
            <Text style={type.body}>{formatKes(line.priceCents)}</Text>
          </View>
        ))}
        <View style={styles.rule} />
        <View style={styles.line}>
          <Text style={[type.muted, styles.fill]}>
            {count} ticket{count === 1 ? "" : "s"}
          </Text>
          <Text style={type.body} testID="subtotal">{formatKes(amounts.subtotalCents)}</Text>
        </View>
        <View style={styles.line}>
          <Text style={[type.muted, styles.fill]}>Booking fee</Text>
          <Text style={type.body} testID="fee">{amounts.feeCents === 0 ? "None" : formatKes(amounts.feeCents)}</Text>
        </View>
        <View style={styles.line}>
          <Text style={[type.h3, styles.fill]}>Total</Text>
          <Text style={type.price} testID="total">{formatKes(total)}</Text>
        </View>
        {bookingData ? <Text style={type.small} testID="booking-reference">Booking reference {bookingData.reference}</Text> : null}
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
  countdown: { flexDirection: "row", alignItems: "center", gap: space.md, padding: space.md, borderRadius: radius.md, borderWidth: 1, backgroundColor: palette.warningBg, borderColor: palette.warningBorder },
  countdownUrgent: { backgroundColor: palette.errorBg, borderColor: palette.errorBorder },
  waiting: { alignItems: "center", gap: space.md, paddingVertical: space.xl },
  rule: { height: StyleSheet.hairlineWidth, backgroundColor: palette.grey300, marginVertical: space.xs },
  line: { flexDirection: "row", alignItems: "center", gap: space.md, paddingVertical: 3 },
  dev: { backgroundColor: palette.warningBg, borderColor: palette.warningBorder, borderWidth: 1, borderStyle: "dashed", borderRadius: radius.md, padding: space.md, gap: space.sm },
  devLabel: { ...type.overline, color: palette.warning },
  devOptions: { flexDirection: "row", flexWrap: "wrap", gap: 6 },
  devOption: { paddingHorizontal: 10, minHeight: 32, justifyContent: "center", borderRadius: radius.pill, borderWidth: 1, borderColor: palette.warningBorder, backgroundColor: palette.white },
  devOptionOn: { backgroundColor: palette.warning, borderColor: palette.warning },
  devOptionText: { ...type.chip, color: palette.warning },
});
