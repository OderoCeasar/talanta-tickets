//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi/contracttest"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments/mock"
)

// ---------------------------------------------------------------------------
// §8.2, one test per row of the failure table.
// ---------------------------------------------------------------------------

// §8.2 row 1. The callback never arrives: reconciliation asks the provider about a PENDING
// payment once it is 60 seconds old, and the answer goes through the same finishing function
// a callback would have.
func TestPayment_CallbackNeverArrives_ReconciliationSettlesIt(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:14]...)
	p := e.pay(c, mock.NoCallback)
	pid := id64(p["id"])
	require.Zero(t, e.timers.waiting(), "no_callback schedules no callback")
	ctx := context.Background()

	// Younger than 60 seconds: not looked at.
	res, err := e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.Zero(t, res.Claimed)
	assert.Equal(t, "PENDING", paymentStatus(t, pid))
	assert.Equal(t, 0, queryInt(t, `SELECT query_attempts FROM payments WHERE id = $1`, pid))

	// 61 seconds old, and the provider still has no answer: asked, left pending.
	agePayment(t, pid, 61*time.Second)
	res, err = e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Claimed)
	assert.Equal(t, 1, res.Pending)
	assert.Equal(t, "PENDING", paymentStatus(t, pid))
	assert.Equal(t, 1, queryInt(t, `SELECT query_attempts FROM payments WHERE id = $1 AND last_queried_at IS NOT NULL`, pid))
	assert.Equal(t, "PENDING_PAYMENT", e.getBooking(c.Token, c.Ref)["status"])

	// The fan has paid by now: the provider says so, and reconciliation sells the seats.
	e.mockClock.Advance(time.Minute)
	res, err = e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Sold)
	e.payments.Wait()

	got := e.getPayment(c.Token, pid)
	assert.Equal(t, "SUCCESS", got["status"])
	assert.Equal(t, "PAID", got["booking_status"])
	assert.NotNil(t, got["mpesa_receipt"])
	assert.NotNil(t, got["completed_at"])
	b := e.getBooking(c.Token, c.Ref)
	assert.Equal(t, "PAID", b["status"])
	assert.Len(t, list(b, "tickets"), 4)
	assert.Equal(t, 2, queryInt(t, `SELECT query_attempts FROM payments WHERE id = $1`, pid))
	assert.Equal(t, map[string]int{"SOLD": 4}, seatStatuses(t, c.Seats))
	assert.Equal(t, 1, e.sms.count(c.Phone), "the ticket SMS is sent once, after the commit")

	// Nothing is left to reconcile.
	res, err = e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.Zero(t, res.Claimed)
	assert.Equal(t, 4, ledger(t, ev.ID).Sold)
}

// §8.2 row 1, the other end: no final answer within 5 minutes. The payment is closed as
// TIMEOUT, the organiser is alerted because money may have moved, and the booking stays
// payable while its hold lives.
func TestPayment_NoAnswerForFiveMinutes_TimesOutAndAlerts(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	pid := id64(e.pay(c, mock.NoCallback)["id"])

	// The mock's clock is not moved: it keeps answering "being processed".
	agePayment(t, pid, 4*time.Minute+50*time.Second)
	res, err := e.payments.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.Pending, "under five minutes: still waiting")
	assert.Equal(t, "PENDING", paymentStatus(t, pid))

	agePayment(t, pid, 20*time.Second)
	res, err = e.payments.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.TimedOut)

	got := e.getPayment(c.Token, pid)
	assert.Equal(t, "TIMEOUT", got["status"])
	assert.Equal(t, "timeout", got["failure_reason"])
	assert.Equal(t, "PENDING_PAYMENT", got["booking_status"], "the hold is alive: the fan can try again")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE booking_id = $1 AND kind = 'PAYMENT_UNRESOLVED'`, c.BookingID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refunds WHERE booking_id = $1`, c.BookingID))
	assert.Equal(t, map[string]int{"HELD": 2}, seatStatuses(t, c.Seats))
	ledger(t, ev.ID)
}

// §8.2 row 2. The callback arrives twice: the second changes nothing.
func TestPayment_CallbackTwice_TheSecondIsANoOp(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:13]...)
	p := e.pay(c, mock.DuplicateCallback)
	pid := id64(p["id"])
	require.Equal(t, 2, e.timers.waiting(), "duplicate_callback schedules the callback twice")

	fired := e.timers.fire(t, soon)
	require.Len(t, fired, 2)
	assert.Equal(t, payments.ActionSold, fired[0].Action)
	assert.Equal(t, 3, fired[0].Tickets)
	assert.Equal(t, payments.ActionNone, fired[1].Action, "the repeat must do nothing")
	e.payments.Wait()

	first := e.getPayment(c.Token, pid)
	assert.Equal(t, "SUCCESS", first["status"])
	assert.Equal(t, 3, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, c.BookingID), "one set of tickets")
	assert.Equal(t, 1, e.sms.count(c.Phone), "one SMS")
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refunds WHERE booking_id = $1`, c.BookingID))

	// The same thing through the real webhook, a third and fourth time, in Daraja's envelope.
	body := stkCallback(p["checkout_request_id"].(string), 0, "The service request is processed successfully.",
		paidItems(json.Number(fmt.Sprintf("%d.00", c.Total/100)), first["mpesa_receipt"].(string))...)
	e.webhook(body)
	e.webhook(body)
	again := e.getPayment(c.Token, pid)
	assert.Equal(t, first, again, "the payment is byte for byte what the first callback left")
	assert.Equal(t, 3, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, c.BookingID))
	assert.Equal(t, 1, e.sms.count(c.Phone))
	assert.Equal(t, 3, ledger(t, ev.ID).Sold)
}

// §8.2 row 3, the sharpest edge in the system. The callback arrives after the hold expired
// and the seats were resold to another fan: the payment is recorded, the booking becomes
// REFUND_REQUIRED, the organiser is alerted, a refund is queued, and the other fan's seats
// are not touched. No seat is sold twice to fix the ledger.
func TestPayment_LateCallbackAfterSeatsResold_RefundRequiredAndNoDoubleSale(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	seats := north[10:14]

	// Fan A pays; M-Pesa will answer late.
	a := e.checkout(ev.ID, seats...)
	pa := id64(e.pay(a, mock.LateCallback)["id"])

	// A's hold runs out. No sweeper has come by: the hold is still ACTIVE in the table.
	lapseHold(t, a.HoldID)

	// Fan B takes the same four seats and pays for them.
	b := e.checkout(ev.ID, seats...)
	pb := id64(e.pay(b, mock.Success)["id"])
	fired := e.timers.fire(t, soon)
	require.Len(t, fired, 1, "only B's callback is due; A's is late")
	require.Equal(t, payments.ActionSold, fired[0].Action)
	require.Equal(t, "PAID", bookingStatus(t, b.BookingID))
	bTickets := queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1 AND status = 'VALID'`, b.BookingID)
	require.Equal(t, 4, bTickets)

	// A's booking was closed when B took the last seat of A's lapsed hold... except that
	// A has a payment in flight, so it was left for the payment flow.
	assert.Equal(t, "EXPIRED", queryText(t, `SELECT status FROM holds WHERE id = $1`, a.HoldID))
	assert.Equal(t, "PENDING_PAYMENT", bookingStatus(t, a.BookingID))

	// Now A's money arrives.
	fired = e.timers.fire(t, late)
	require.Len(t, fired, 1)
	assert.Equal(t, payments.ActionRefund, fired[0].Action)
	e.payments.Wait()

	got := e.getPayment(a.Token, pa)
	assert.Equal(t, "SUCCESS", got["status"], "the payment is recorded: the money did arrive")
	assert.Equal(t, "REFUND_REQUIRED", got["booking_status"])
	assert.Nil(t, got["failure_reason"])
	assert.NotNil(t, got["mpesa_receipt"])
	ba := e.getBooking(a.Token, a.Ref)
	assert.Equal(t, "REFUND_REQUIRED", ba["status"])
	assert.Empty(t, list(ba, "tickets"), "A gets no ticket")
	assert.Nil(t, ba["hold_expires_at"])

	// The refund, for what A paid, and the alert.
	var refundAmount int64
	var refundStatus, reason string
	require.NoError(t, db.Pool.QueryRow(context.Background(),
		`SELECT amount_cents, status, reason FROM refunds WHERE payment_id = $1`, pa).Scan(&refundAmount, &refundStatus, &reason))
	assert.Equal(t, a.Total, refundAmount)
	assert.Equal(t, "PENDING", refundStatus)
	assert.Contains(t, reason, "Paid after the hold")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE booking_id = $1 AND kind = 'REFUND_REQUIRED' AND acknowledged_at IS NULL`, a.BookingID))

	// B is untouched: the same four seats, sold once, through B's hold, with B's tickets.
	assert.Equal(t, 4, queryInt(t, `SELECT count(*) FROM event_seats WHERE id = ANY($1) AND status = 'SOLD' AND hold_id = $2`, seats, b.HoldID))
	assert.Equal(t, "PAID", bookingStatus(t, b.BookingID))
	assert.Equal(t, "SUCCESS", paymentStatus(t, pb))
	assert.Equal(t, 4, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1 AND status = 'VALID'`, b.BookingID))
	assert.Equal(t, 4, queryInt(t, `SELECT count(*) FROM tickets WHERE event_seat_id = ANY($1)`, seats), "four tickets exist for the four seats, not eight")
	assert.Equal(t, 0, e.sms.count(a.Phone), "no 'you're going' SMS for A")
	assert.Equal(t, 1, e.sms.count(b.Phone))

	c := ledger(t, ev.ID)
	assert.Equal(t, 4, c.Sold, "four seats exist in this sale and four are sold: none twice")
}

// §8.2 row 3, when only SOME of the seats were resold: still nothing is sold for the late
// payment. A partial booking is never created to save part of the money.
func TestPayment_LateCallbackAfterOneSeatResold_SellsNothing(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	a := e.checkout(ev.ID, north[10:13]...)
	pa := id64(e.pay(a, mock.LateCallback)["id"])
	lapseHold(t, a.HoldID)

	// B takes one of A's three seats. A's hold keeps the other two and stays ACTIVE.
	b := e.checkout(ev.ID, north[11])
	require.Equal(t, "ACTIVE", queryText(t, `SELECT status FROM holds WHERE id = $1`, a.HoldID))

	fired := e.timers.fire(t, late)
	require.Len(t, fired, 1)
	assert.Equal(t, payments.ActionRefund, fired[0].Action)
	assert.Equal(t, "Paid after the hold expired; seats were resold", fired[0].Reason)

	assert.Equal(t, "SUCCESS", paymentStatus(t, pa))
	assert.Equal(t, "REFUND_REQUIRED", bookingStatus(t, a.BookingID))
	assert.Equal(t, "ACTIVE", queryText(t, `SELECT status FROM holds WHERE id = $1`, a.HoldID), "CONVERTED was undone with the sale")
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM event_seats WHERE id = ANY($1) AND status = 'SOLD'`, a.Seats), "not one of A's seats is sold")
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, a.BookingID))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM event_seats WHERE id = $1 AND status = 'HELD' AND hold_id = $2`, north[11], b.HoldID), "B still holds the seat")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM refunds WHERE payment_id = $1 AND amount_cents = $2`, pa, a.Total))
	assert.Zero(t, ledger(t, ev.ID).Sold)
}

// §8.2 row 4. The fan pays the wrong amount: the payment is FAILED with failure_reason
// wrong_amount, what was actually paid is queued for refund, the booking is REFUND_REQUIRED
// and nothing is sold.
func TestPayment_WrongAmount_FailedAndRefundQueued(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	pid := id64(e.pay(c, mock.WrongAmount)["id"])

	fired := e.timers.fire(t, soon)
	require.Len(t, fired, 1)
	assert.Equal(t, payments.ActionRefund, fired[0].Action)
	e.payments.Wait()

	got := e.getPayment(c.Token, pid)
	assert.Equal(t, "FAILED", got["status"])
	assert.Equal(t, "wrong_amount", got["failure_reason"])
	assert.Equal(t, "REFUND_REQUIRED", got["booking_status"])
	assert.EqualValues(t, 0, got["result_code"], "M-Pesa's code was success; we refused the amount")
	assert.EqualValues(t, c.Total, got["amount_cents"], "amount_cents stays what we asked for")
	assert.NotNil(t, got["mpesa_receipt"])

	var refundAmount int64
	require.NoError(t, db.Pool.QueryRow(context.Background(), `SELECT amount_cents FROM refunds WHERE payment_id = $1 AND status = 'PENDING'`, pid).Scan(&refundAmount))
	assert.Equal(t, c.Total+100, refundAmount, "the refund is what was actually paid, not the booking total")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE booking_id = $1 AND kind = 'AMOUNT_MISMATCH'`, c.BookingID))
	assert.Equal(t, map[string]int{"HELD": 2}, seatStatuses(t, c.Seats), "nothing is sold")
	b := e.getBooking(c.Token, c.Ref)
	assert.Equal(t, "REFUND_REQUIRED", b["status"])
	assert.Empty(t, list(b, "tickets"))
	assert.Equal(t, 0, e.sms.count(c.Phone))
	assert.Zero(t, ledger(t, ev.ID).Sold)
}

// §8.2 row 5. Forged callbacks. The body is never trusted alone: every one of these gets
// the fixed 200 answer and sells nothing.
func TestWebhook_ForgedCallbacksChangeNothing(t *testing.T) {
	const safaricom = "196.201.214.200:44321"
	e := newEnv(t, withCallbackAllowlist("196.201.214.0/24", "196.201.213.114/32"))
	ev, north, south := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	p := e.pay(c, mock.NoCallback)
	pid, checkoutID := id64(p["id"]), p["checkout_request_id"].(string)
	shillings := json.Number(fmt.Sprintf("%d.00", c.Total/100))
	unchanged := func(what string) {
		t.Helper()
		assert.Equal(t, "PENDING", paymentStatus(t, pid), what)
		assert.Equal(t, "PENDING_PAYMENT", bookingStatus(t, c.BookingID), what)
		assert.Equal(t, map[string]int{"HELD": 2}, seatStatuses(t, c.Seats), what)
		assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, c.BookingID), what)
		assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refunds WHERE booking_id = $1`, c.BookingID), what)
	}

	// 1. An ID we never issued, from an allowed address.
	e.remoteAddr = safaricom
	before := queryInt(t, `SELECT count(*) FROM payments`)
	e.webhook(stkCallback("ws_CO_never_issued_by_us", 0, "ok", paidItems(shillings, newReceipt(t))...))
	assert.Equal(t, before, queryInt(t, `SELECT count(*) FROM payments`))
	assert.Contains(t, e.logs.String(), "unknown CheckoutRequestID")
	unchanged("unknown CheckoutRequestID")

	// 2. The right ID and the right amount, from an address that is not Safaricom's.
	e.remoteAddr = "203.0.113.50:5555"
	e.webhook(stkCallback(checkoutID, 0, "ok", paidItems(shillings, newReceipt(t))...))
	assert.Contains(t, e.logs.String(), "source address is not in MPESA_CALLBACK_ALLOWED_CIDRS")
	unchanged("disallowed source address")
	assert.Nil(t, e.getPayment(c.Token, pid)["mpesa_receipt"])

	// 2b. A forged X-Forwarded-For does not get it onto the list: no proxy is trusted.
	req := e.request("POST", "/webhooks/mpesa/callback", stkCallback(checkoutID, 0, "ok", paidItems(shillings, newReceipt(t))...), "")
	req.Header.Set("X-Forwarded-For", "196.201.214.200")
	require.Equal(t, http.StatusOK, e.doReq(req).Code)
	e.payments.Wait()
	unchanged("forged X-Forwarded-For")

	// 3. A replay of an old, real success for a DIFFERENT booking, pointed at this payment:
	// the receipt already settled another payment.
	e.remoteAddr = "198.51.100.7:40000"
	old, oldPayment := e.paid(ev.ID, south[10:12]...)
	oldReceipt := e.getPayment(old.Token, oldPayment)["mpesa_receipt"].(string)
	var oldRaw string
	require.NoError(t, db.Pool.QueryRow(context.Background(), `SELECT raw_callback::text FROM payments WHERE id = $1`, oldPayment).Scan(&oldRaw))
	require.Contains(t, oldRaw, oldReceipt, "the raw callback is stored")
	e.remoteAddr = safaricom
	e.webhook(stkCallback(checkoutID, 0, "ok", paidItems(shillings, oldReceipt)...))
	unchanged("replayed receipt")
	assert.Contains(t, e.logs.String(), "the M-Pesa receipt already belongs to another payment")
	// ...and the old body replayed verbatim finds its own payment already final.
	e.webhook(oldRaw, contracttest.LenientRequest())
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, old.BookingID))
	unchanged("old callback replayed verbatim")

	// 4. "Paid" with no amount at all cannot be checked against anything. It is not applied;
	// the provider is asked, and the provider says the fan has not paid yet.
	e.webhook(stkCallback(checkoutID, 0, "ok", obj{"Name": "MpesaReceiptNumber", "Value": newReceipt(t)}))
	unchanged("success with no amount")
	e.webhook(stkCallback(checkoutID, 0, "ok", paidItems("a lot", newReceipt(t))...), contracttest.LenientRequest())
	unchanged("success with an unreadable amount")
	e.webhook(stkCallback(checkoutID, 0, "ok"))
	unchanged("success with no metadata")

	// 5. Things that are not callbacks at all.
	for _, junk := range []string{``, `{`, `[]`, `{"Body":{}}`, `{"Body":{"stkCallback":{"ResultCode":0}}}`, `not json`, strings.Repeat("x", 70<<10)} {
		e.webhook(junk, contracttest.LenientRequest())
	}
	unchanged("junk bodies")

	// 6. The wrong amount, from the right address: handled as a wrong amount (row 4), never a sale.
	e.webhook(stkCallback(checkoutID, 0, "ok", paidItems(json.Number("1.00"), newReceipt(t))...))
	assert.Equal(t, "FAILED", paymentStatus(t, pid))
	assert.Equal(t, "wrong_amount", e.getPayment(c.Token, pid)["failure_reason"])
	assert.Equal(t, map[string]int{"HELD": 2}, seatStatuses(t, c.Seats))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, c.BookingID))

	// What a real one looks like, for contrast: allowed address, known pending payment, right amount.
	c2 := e.checkout(ev.ID, north[20:22]...)
	p2 := e.pay(c2, mock.NoCallback)
	e.webhook(stkCallback(p2["checkout_request_id"].(string), 0, "The service request is processed successfully.",
		paidItems(json.Number(fmt.Sprintf("%d.00", c2.Total/100)), newReceipt(t))...))
	assert.Equal(t, "PAID", bookingStatus(t, c2.BookingID))
	assert.Equal(t, 4, ledger(t, ev.ID).Sold, "the old booking and the real one; nothing forged")
}

// §8.2 row 5: a "paid" with no amount is settled by asking the provider, never by the body.
func TestWebhook_SuccessWithoutAmountIsSettledByAskingTheProvider(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	p := e.pay(c, mock.NoCallback)
	pid, checkoutID := id64(p["id"]), p["checkout_request_id"].(string)

	e.webhook(stkCallback(checkoutID, 0, "ok", obj{"Name": "MpesaReceiptNumber", "Value": newReceipt(t)}))
	assert.Equal(t, "PENDING", paymentStatus(t, pid), "the provider has no result yet")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM payments WHERE id = $1 AND raw_callback IS NOT NULL`, pid), "the body is kept all the same")

	// Once the provider itself says "paid", the same callback gets the payment settled.
	e.mockClock.Advance(time.Minute)
	e.webhook(stkCallback(checkoutID, 0, "ok", obj{"Name": "MpesaReceiptNumber", "Value": newReceipt(t)}))
	assert.Equal(t, "SUCCESS", paymentStatus(t, pid))
	assert.Equal(t, "PAID", bookingStatus(t, c.BookingID))
	assert.Equal(t, 2, ledger(t, ev.ID).Sold)
}

// §8.2 row 6. The fan closes the app mid-payment: the booking's state is on the server, and
// GET /bookings/{ref} tells the truth at every stage.
func TestBooking_ResumeShowsTheTruthAtEveryStage(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	fanID, token := e.newFan()

	// Stage 0: seats held, nothing booked. /me/holds is how the app finds this.
	h := want(t, e.hold(token, ev.ID, north[10:12]...), http.StatusCreated)
	assert.Nil(t, h["booking_reference"])

	// Stage 1: booked, not paid.
	c := e.checkoutAs(fanID, token, ev.ID, north[20:22]...)
	b := e.getBooking(token, c.Ref)
	assert.Equal(t, "PENDING_PAYMENT", b["status"])
	assert.Nil(t, b["latest_payment"])
	assert.NotNil(t, b["hold_expires_at"])
	assert.Empty(t, list(b, "tickets"))
	assert.Nil(t, b["paid_at"])
	assert.EqualValues(t, c.HoldID, b["hold_id"])
	require.Len(t, list(b, "items"), 2)
	item := list(b, "items")[0].(obj)
	assert.EqualValues(t, 100_000, item["unit_price_cents"])
	assert.Equal(t, "Regular", item["category_name"])
	assert.Equal(t, "N-LOWER", item["location"].(obj)["gate_group"])
	assert.EqualValues(t, 200_000, b["total_cents"])
	assert.EqualValues(t, ev.ID, b["event"].(obj)["id"])
	held := want(t, e.do("GET", fmt.Sprintf("/holds/%d", c.HoldID), nil, token), http.StatusOK)
	assert.Equal(t, c.Ref, held["booking_reference"], "the hold names its booking")

	// Stage 2: the PIN prompt is on the phone. The app is closed here.
	exec(t, `UPDATE holds SET expires_at = now() + interval '90 seconds' WHERE id = $1`, c.HoldID)
	exec(t, `UPDATE event_seats SET hold_expires_at = (SELECT expires_at FROM holds WHERE id = $1) WHERE hold_id = $1`, c.HoldID)
	p := e.pay(c, mock.Success)
	b = e.getBooking(token, c.Ref)
	assert.Equal(t, "PENDING_PAYMENT", b["status"])
	lp := b["latest_payment"].(obj)
	assert.Equal(t, "PENDING", lp["status"])
	assert.Equal(t, p["id"], lp["id"])
	assert.Equal(t, payPhone, lp["phone"])
	expires, err := time.Parse(time.RFC3339, b["hold_expires_at"].(string))
	require.NoError(t, err)
	assert.Greater(t, time.Until(expires), 5*time.Minute+30*time.Second, "starting the payment pushed the hold out to six minutes (correction 4)")
	assert.Equal(t, true, want(t, e.do("GET", fmt.Sprintf("/holds/%d", c.HoldID), nil, token), http.StatusOK)["extended"])

	// Stage 3: the fan paid while the app was closed.
	e.timers.fire(t, soon)
	e.payments.Wait()
	b = e.getBooking(token, c.Ref)
	assert.Equal(t, "PAID", b["status"])
	assert.NotNil(t, b["paid_at"])
	assert.Nil(t, b["hold_expires_at"], "null once the booking is final")
	lp = b["latest_payment"].(obj)
	assert.Equal(t, "SUCCESS", lp["status"])
	assert.Equal(t, "PAID", lp["booking_status"])
	tickets := list(b, "tickets")
	require.Len(t, tickets, 2)
	for _, tk := range tickets {
		token := tk.(obj)["qr_token"].(string)
		_, err := e.tickets.Verifier().Verify(token)
		require.NoError(t, err, "the booking carries tickets that verify")
		assert.Equal(t, "VALID", tk.(obj)["status"])
	}
	mine := decode[page[ticketJSON]](t, e.do("GET", "/me/tickets", nil, token))
	assert.Len(t, mine.Items, 2)
	assert.Contains(t, e.sms.last(c.Phone), c.Ref)

	// And the list shows both of the fan's bookings... there is one; the bare hold has none.
	listed := want(t, e.do("GET", "/me/bookings", nil, token), http.StatusOK)
	require.Len(t, items(listed), 1)
	assert.Equal(t, c.Ref, items(listed)[0].(obj)["reference"])
	assert.Equal(t, 2, ledger(t, ev.ID).Sold)
}

// ---------------------------------------------------------------------------
// Racing results.
// ---------------------------------------------------------------------------

// Two callbacks for one payment at the same instant, several at a time, many rounds: the
// seats are sold once and the tickets issued once.
func TestPayment_CallbacksAtTheSameInstant_SellOnce(t *testing.T) {
	e := newEnv(t)
	ev, north, south := e.ticketEvent()
	all := append(append([]int64{}, north[10:]...), south[10:]...)
	const rounds, callers = 12, 6
	for round := 0; round < rounds; round++ {
		c := e.checkout(ev.ID, all[round*2], all[round*2+1])
		p := e.pay(c, mock.NoCallback)
		amount := c.Total
		cb := payments.Callback{
			CheckoutRequestID: p["checkout_request_id"].(string), ResultCode: 0, ResultDesc: "ok",
			AmountCents: &amount, Receipt: newReceipt(t), Raw: []byte(`{"Body":{"stkCallback":{}}}`),
		}
		results := make([]payments.Finished, callers)
		errs := make([]error, callers)
		gate := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-gate
				results[i], errs[i] = e.payments.HandleCallback(context.Background(), cb)
			}()
		}
		close(gate)
		wg.Wait()
		e.payments.Wait()

		sold := 0
		for i := range results {
			require.NoError(t, errs[i], "round %d", round)
			if results[i].Action == payments.ActionSold {
				sold++
			} else {
				require.Equal(t, payments.ActionNone, results[i].Action, "round %d", round)
			}
		}
		require.Equal(t, 1, sold, "round %d: exactly one callback sells", round)
		require.Equal(t, 2, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, c.BookingID), "round %d", round)
		require.Equal(t, 1, e.sms.count(c.Phone), "round %d: one SMS", round)
		require.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refunds WHERE booking_id = $1`, c.BookingID))
	}
	assert.Equal(t, rounds*2, ledger(t, ev.ID).Sold)
}

// The callback and reconciliation finishing the same payment at once, many rounds: whichever
// gets the payment's row first sells; the other finds it final.
func TestPayment_CallbackAndReconciliationAtOnce_SellOnce(t *testing.T) {
	e := newEnv(t)
	ev, north, south := e.ticketEvent()
	all := append(append([]int64{}, north[10:]...), south[10:]...)
	e.mockClock.Advance(time.Hour) // the provider answers "paid" to every query from the start... of each payment below
	const rounds = 20
	byCallback, byReconcile := 0, 0
	for round := 0; round < rounds; round++ {
		c := e.checkout(ev.ID, all[round*2], all[round*2+1])
		p := e.pay(c, mock.NoCallback)
		pid := id64(p["id"])
		agePayment(t, pid, 2*time.Minute)
		e.mockClock.Advance(time.Minute)
		amount := c.Total
		cb := payments.Callback{
			CheckoutRequestID: p["checkout_request_id"].(string), ResultCode: 0, ResultDesc: "ok", AmountCents: &amount,
			Raw: []byte(`{"Body":{"stkCallback":{}}}`),
		}

		var fin payments.Finished
		var cbErr, recErr error
		var rec payments.ReconcileResult
		gate := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-gate
			fin, cbErr = e.payments.HandleCallback(context.Background(), cb)
		}()
		go func() {
			defer wg.Done()
			<-gate
			rec, recErr = e.payments.Reconcile(context.Background())
		}()
		close(gate)
		wg.Wait()
		e.payments.Wait()
		require.NoError(t, cbErr, "round %d", round)
		require.NoError(t, recErr, "round %d", round)
		require.Zero(t, rec.Errors, "round %d", round)

		sold := rec.Sold
		if fin.Action == payments.ActionSold {
			sold++
			byCallback++
		} else {
			require.Equal(t, payments.ActionNone, fin.Action, "round %d", round)
			byReconcile += rec.Sold
		}
		require.Equal(t, 1, sold, "round %d: exactly one of the two sells (callback %s, reconcile %+v)", round, fin.Action, rec)
		require.Equal(t, "SUCCESS", paymentStatus(t, pid))
		require.Equal(t, 2, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, c.BookingID), "round %d", round)
		require.Equal(t, 1, e.sms.count(c.Phone), "round %d", round)
		require.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refunds WHERE booking_id = $1`, c.BookingID))
	}
	t.Logf("rounds=%d sold_by_callback=%d sold_by_reconciliation=%d", rounds, byCallback, byReconcile)
	assert.Equal(t, rounds*2, ledger(t, ev.ID).Sold)
}

// Two workers reconciling at once do not both ask the provider about the same payment.
func TestReconcile_TwoWorkersDoNotDoubleProcess(t *testing.T) {
	var counting *countingProvider
	e := newEnv(t,
		withProvider(func(m *mock.Provider) payments.Provider { counting = &countingProvider{Provider: m}; return counting }),
		withPaymentPolicy(func(p *payments.Policy) { p.ReconcileLease = time.Hour }))
	ev, north, _ := e.ticketEvent()
	var pids []int64
	for i := 0; i < 5; i++ {
		c := e.checkout(ev.ID, north[10+i])
		pid := id64(e.pay(c, mock.NoCallback)["id"])
		agePayment(t, pid, 2*time.Minute)
		pids = append(pids, pid)
	}

	// The provider has no answer yet, so each payment stays live after being asked about.
	const workers = 6
	results := make([]payments.ReconcileResult, workers)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			var err error
			results[i], err = e.payments.Reconcile(context.Background())
			assert.NoError(t, err)
		}()
	}
	close(gate)
	wg.Wait()

	claimed := 0
	for _, r := range results {
		claimed += r.Claimed
	}
	assert.Equal(t, 5, claimed, "each payment is claimed by exactly one worker")
	assert.EqualValues(t, 5, counting.queries.Load(), "the provider is asked once per payment, not once per worker")
	assert.Equal(t, 5, queryInt(t, `SELECT count(*) FROM payments WHERE id = ANY($1) AND query_attempts = 1`, pids))
	ledger(t, ev.ID)
}

// A payment left INITIATED by a crash between its insert and the call to the provider has no
// provider ID to ask about. Reconciliation closes it, and the booking is payable again.
func TestReconcile_ClosesAPaymentThatWasNeverSent(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	var pid int64
	require.NoError(t, db.Pool.QueryRow(context.Background(),
		`INSERT INTO payments (booking_id, amount_cents, phone_e164, status) VALUES ($1, $2, $3, 'INITIATED') RETURNING id`,
		c.BookingID, c.Total, payPhone).Scan(&pid))

	// While it is there, it blocks a second payment, as any live payment does.
	holdErr(t, e.stk(c.Token, c.BookingID, payPhone, mock.Success), http.StatusConflict, domain.CodePaymentInProgress)

	res, err := e.payments.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Zero(t, res.Claimed, "under a minute old it may still be on its way to the provider")
	agePayment(t, pid, 61*time.Second)
	res, err = e.payments.Reconcile(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, res.TimedOut)
	got := e.getPayment(c.Token, pid)
	assert.Equal(t, "TIMEOUT", got["status"])
	assert.Equal(t, "PENDING_PAYMENT", got["booking_status"])
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE booking_id = $1`, c.BookingID), "never sent: no money can have moved")

	e.pay(c, mock.Success)
	e.timers.fire(t, soon)
	assert.Equal(t, "PAID", bookingStatus(t, c.BookingID))
	assert.Equal(t, 2, ledger(t, ev.ID).Sold)
}
