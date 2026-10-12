import { router, useFocusEffect, useLocalSearchParams } from "expo-router";
import { useCallback } from "react";
import { BackHandler, StyleSheet, Text, View } from "react-native";

import { useBooking } from "../../src/api/queries";
import { TicketRow } from "../../src/features/tickets/TicketRow";
import { paymentOutcome } from "../../src/lib/errors";
import { formatKes } from "../../src/lib/money";
import { formatDateTime } from "../../src/lib/time";
import { palette, space, type } from "../../src/theme";
import { Banner, Button, Card, ErrorState, Icon, Loading, Screen } from "../../src/ui";

/** One booking as the server has it now: the tickets once paid, otherwise what became of the payment. */
export default function BookingScreen() {
  const { reference } = useLocalSearchParams<{ reference: string }>();
  const booking = useBooking(reference);
  const done = useCallback(() => router.dismissTo("/tickets"), []);

  // Back from here must not land on the checkout that has just finished.
  useFocusEffect(
    useCallback(() => {
      const sub = BackHandler.addEventListener("hardwareBackPress", () => {
        done();
        return true;
      });
      return () => sub.remove();
    }, [done]),
  );

  if (booking.isPending) return <Loading label="Getting your tickets…" />;
  if (booking.isError) return <ErrorState error={booking.error} onRetry={() => void booking.refetch()} title="We could not load this booking" />;

  const b = booking.data;
  const paid = b.status === "PAID";
  // Not paid: say what the server says about the latest payment now. A payment M-Pesa never
  // answered can still settle after this screen was first opened (the booking is re-read
  // while that is possible), so the words here change with it.
  const refund = b.status === "REFUND_REQUIRED" || b.status === "REFUNDED";
  const outcome = !paid && b.latest_payment ? paymentOutcome({ status: b.latest_payment.status, failure_reason: b.latest_payment.failure_reason, booking_status: b.status }) : null;

  return (
    <Screen footer={<Button title="Go to my tickets" onPress={done} testID="go-to-tickets" />}>
      {paid ? (
        <View style={styles.hero}>
          <View style={styles.tick}>
            <Icon name="check" size={30} color={palette.emeraldInk} />
          </View>
          <Text style={styles.heroTitle} testID="booking-paid">You are going!</Text>
          <Text style={styles.heroBody}>
            {b.tickets.length} ticket{b.tickets.length === 1 ? "" : "s"} for {b.event.title}
          </Text>
          <Text style={styles.heroBody}>{formatDateTime(b.event.kickoff_at)}</Text>
        </View>
      ) : (
        <Banner tone={refund ? "error" : "warning"} title={b.status === "REFUNDED" ? "This booking was refunded" : (outcome?.title ?? `Booking ${b.reference}`)} testID="booking-not-paid">
          {b.status === "REFUNDED"
            ? "We received your payment but could not give you these seats. The money has been sent back to your M-Pesa."
            : outcome
              ? `${outcome.body}${!refund && !outcome.unsettled ? " No tickets were issued for this booking." : ""}`
              : b.status === "PENDING_PAYMENT"
                ? "This booking has not been paid for yet."
                : "This booking was not completed and no tickets were issued."}
        </Banner>
      )}

      <Card>
        <Row label="Booking reference" value={b.reference} testID="booking-reference" />
        <Row label={paid ? "Paid" : "Total"} value={formatKes(b.total_cents)} />
        {b.fee_cents > 0 ? <Row label="Includes booking fee" value={formatKes(b.fee_cents)} /> : null}
        {b.latest_payment?.mpesa_receipt ? <Row label="M-Pesa receipt" value={b.latest_payment.mpesa_receipt} /> : null}
        {b.paid_at ? <Row label="Paid at" value={formatDateTime(b.paid_at)} /> : null}
      </Card>

      {paid ? (
        <>
          <Text style={type.h2} testID="ticket-count">
            Your {b.tickets.length} ticket{b.tickets.length === 1 ? "" : "s"}
          </Text>
          <Text style={type.muted}>Each person needs their own ticket at the gate. They are saved on this phone and open without internet.</Text>
          <View style={styles.list}>
            {b.tickets.map((t) => (
              <TicketRow key={t.id} ticket={t} />
            ))}
          </View>
        </>
      ) : null}
    </Screen>
  );
}

function Row({ label, value, testID }: { label: string; value: string; testID?: string }) {
  return (
    <View style={styles.row}>
      <Text style={[type.muted, styles.fill]}>{label}</Text>
      <Text style={type.label} testID={testID} selectable>
        {value}
      </Text>
    </View>
  );
}

const styles = StyleSheet.create({
  fill: { flex: 1 },
  hero: { backgroundColor: palette.emeraldInk, borderRadius: 16, padding: space.xl, alignItems: "center", gap: space.xs },
  tick: { width: 60, height: 60, borderRadius: 30, backgroundColor: palette.champagne, alignItems: "center", justifyContent: "center", marginBottom: space.sm },
  heroTitle: { ...type.display },
  heroBody: { ...type.body, color: palette.champagne, textAlign: "center" },
  row: { flexDirection: "row", alignItems: "center", gap: space.md, paddingVertical: 3 },
  list: { gap: space.sm },
});
