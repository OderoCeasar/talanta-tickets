//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/config"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi/contracttest"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments/mock"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments/mpesa"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments/providers"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
)

// The tests of the money-path review. The rule every one of them holds the system to: once
// there is any evidence that money moved for a payment, the payment ends in exactly one of
// (a) seats SOLD and tickets issued, or (b) a refund row and an organiser alert. Never
// neither, never both. settled checks exactly that for one payment.

// settled asserts that a payment for which money moved ended in tickets XOR a refund.
func settled(t testing.TB, paymentID, bookingID int64) (tickets, refunds int) {
	t.Helper()
	refunds = queryInt(t, `SELECT count(*) FROM refunds WHERE payment_id = $1`, paymentID)
	tickets = 0
	if refunds == 0 {
		tickets = queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1 AND status = 'VALID'`, bookingID)
	}
	bought := paymentStatus(t, paymentID) == "SUCCESS" && bookingStatus(t, bookingID) == "PAID" && refunds == 0
	switch {
	case bought:
		assert.Positive(t, tickets, "payment %d bought its booking: it must have tickets", paymentID)
	default:
		assert.Equal(t, 1, refunds, "payment %d took money and bought nothing: it must have exactly one refund row", paymentID)
		assert.Positive(t, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE payment_id = $1 AND kind IN ('REFUND_REQUIRED', 'AMOUNT_MISMATCH')`, paymentID),
			"payment %d: a refund must come with an alert", paymentID)
	}
	return tickets, refunds
}

// scriptedProvider is the mock behind two hooks a test can set: what Initiate answers and
// what Query answers. next is the mock's own answer.
type scriptedProvider struct {
	payments.Provider
	mu       sync.Mutex
	initiate func(next func() (payments.PaymentInit, error)) (payments.PaymentInit, error)
	query    func(id string, next func() (payments.PaymentStatus, error)) (payments.PaymentStatus, error)
	// lost are the IDs the mock handed out and the hook threw away.
	lost    []payments.PaymentInit
	queries atomic.Int64
}

func (s *scriptedProvider) Initiate(ctx context.Context, req payments.PaymentRequest) (payments.PaymentInit, error) {
	s.mu.Lock()
	hook := s.initiate
	s.mu.Unlock()
	next := func() (payments.PaymentInit, error) { return s.Provider.Initiate(ctx, req) }
	if hook == nil {
		return next()
	}
	return hook(next)
}

func (s *scriptedProvider) Query(ctx context.Context, id string) (payments.PaymentStatus, error) {
	s.queries.Add(1)
	s.mu.Lock()
	hook := s.query
	s.mu.Unlock()
	next := func() (payments.PaymentStatus, error) { return s.Provider.Query(ctx, id) }
	if hook == nil {
		return next()
	}
	return hook(id, next)
}

func (s *scriptedProvider) setInitiate(fn func(next func() (payments.PaymentInit, error)) (payments.PaymentInit, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initiate = fn
}

func (s *scriptedProvider) setQuery(fn func(id string, next func() (payments.PaymentStatus, error)) (payments.PaymentStatus, error)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.query = fn
}

// loseTheAnswer makes Initiate behave like an STK push that reached M-Pesa and whose answer
// never reached us: the mock accepts the request (so its ID exists and can be queried), and
// the caller gets a timeout. The error is NOT marked payments.NotSent.
func (s *scriptedProvider) loseTheAnswer() {
	s.setInitiate(func(next func() (payments.PaymentInit, error)) (payments.PaymentInit, error) {
		init, err := next()
		if err != nil {
			return payments.PaymentInit{}, err
		}
		s.mu.Lock()
		s.lost = append(s.lost, init)
		s.mu.Unlock()
		return payments.PaymentInit{}, errors.New("test: timeout awaiting response headers (the request was written)")
	})
}

func (s *scriptedProvider) lastLost() payments.PaymentInit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lost[len(s.lost)-1]
}

// likeDaraja makes Query answer as Daraja's STK query does: no amount and no receipt.
func (s *scriptedProvider) likeDaraja() {
	s.setQuery(func(_ string, next func() (payments.PaymentStatus, error)) (payments.PaymentStatus, error) {
		st, err := next()
		st.AmountCents, st.Receipt = nil, ""
		return st, err
	})
}

func scripted(sp **scriptedProvider) envOption {
	return withProvider(func(m *mock.Provider) payments.Provider { *sp = &scriptedProvider{Provider: m}; return *sp })
}

func withPaymentDB(fn func(real payments.DB) payments.DB) envOption {
	return func(c *envConfig) { c.paymentDB = fn }
}

// switchableDB is the pool behind a switch: while down, everything fails as an unreachable
// database would.
type switchableDB struct {
	payments.DB
	down atomic.Bool
}

var errDBDown = errors.New("test: the database is unreachable")

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

func (d *switchableDB) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if d.down.Load() {
		return pgconn.CommandTag{}, errDBDown
	}
	return d.DB.Exec(ctx, sql, args...)
}

func (d *switchableDB) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if d.down.Load() {
		return nil, errDBDown
	}
	return d.DB.Query(ctx, sql, args...)
}

func (d *switchableDB) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if d.down.Load() {
		return errRow{errDBDown}
	}
	return d.DB.QueryRow(ctx, sql, args...)
}

func (d *switchableDB) Begin(ctx context.Context) (pgx.Tx, error) {
	if d.down.Load() {
		return nil, errDBDown
	}
	return d.DB.Begin(ctx)
}

// paidBody is Daraja's "paid" callback for a checkout request ID and a booking total.
func paidBody(t testing.TB, checkoutID string, totalCents int64) obj {
	return stkCallback(checkoutID, 0, "The service request is processed successfully.",
		paidItems(json.Number(fmt.Sprintf("%d.00", totalCents/100)), newReceipt(t))...)
}

func openAlerts(t testing.TB, bookingID int64, kind string) int {
	t.Helper()
	return queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE booking_id = $1 AND kind = $2 AND acknowledged_at IS NULL`, bookingID, kind)
}

// ---------------------------------------------------------------------------
// Fix 1. A success that arrives after a FAILED verdict is not dropped.
// ---------------------------------------------------------------------------

// fakeDaraja is a Daraja made of an httptest server: the token, the STK push and the STK
// query. The test scripts what the query answers per CheckoutRequestID. Nothing here talks
// to Safaricom.
type fakeDaraja struct {
	srv *httptest.Server
	mu  sync.Mutex
	n   int
	// answers maps a CheckoutRequestID to the ResultCode the query reports for it.
	answers map[string]string
}

func newFakeDaraja(t *testing.T) *fakeDaraja {
	d := &fakeDaraja{answers: map[string]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /oauth/v1/generate", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"test-token","expires_in":"3599"}`)
	})
	mux.HandleFunc("POST /mpesa/stkpush/v1/processrequest", func(w http.ResponseWriter, _ *http.Request) {
		d.mu.Lock()
		d.n++
		id := fmt.Sprintf("ws_CO_fake_%d_%d", time.Now().UnixNano(), d.n)
		d.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{
			"MerchantRequestID": "m-" + id, "CheckoutRequestID": id, "ResponseCode": "0", "ResponseDescription": "Success. Request accepted for processing",
		})
	})
	mux.HandleFunc("POST /mpesa/stkpushquery/v1/query", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ CheckoutRequestID string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		d.mu.Lock()
		code := d.answers[body.CheckoutRequestID]
		d.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{
			"ResponseCode": "0", "ResponseDescription": "The service request has been accepted successsfully",
			"CheckoutRequestID": body.CheckoutRequestID, "ResultCode": code, "ResultDesc": "as scripted",
		})
	})
	d.srv = httptest.NewServer(mux)
	t.Cleanup(d.srv.Close)
	return d
}

func (d *fakeDaraja) answer(checkoutID, resultCode string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.answers[checkoutID] = resultCode
}

// Fix 1 (i), end to end through the real Daraja provider against a fake Daraja. The payment
// is PENDING; reconciliation asks at 60 seconds and Daraja answers HTTP 200 with a non-zero
// ResultCode that is NOT a final result ("the transaction is still under processing"). That
// used to be written as FAILED, and the fan's real payment a moment later hit "already
// final" and was dropped: money taken, no tickets, no refund, no alert.
func TestReview1_ANonFinalQueryAnswerDoesNotCloseThePaymentTheFanThenPays(t *testing.T) {
	daraja := newFakeDaraja(t)
	e := newEnv(t, withProvider(func(*mock.Provider) payments.Provider {
		p, err := mpesa.New(mpesa.Config{
			BaseURL: daraja.srv.URL, ConsumerKey: "k", ConsumerSecret: "s", ShortCode: "174379", Passkey: "p",
			CallbackURL: "https://talanta.example.com/v1/webhooks/mpesa/callback",
		})
		require.NoError(t, err)
		return p
	}))
	ev, north, _ := e.ticketEvent()
	ctx := context.Background()

	for _, code := range []string{"4999", "17"} { // still under processing; a code nobody documents as final
		t.Run("result code "+code, func(t *testing.T) {
			c := e.checkout(ev.ID, north[10:12]...)
			pid := id64(e.pay(c, "")["id"])
			checkoutID := checkoutIDOf(t, pid)
			daraja.answer(checkoutID, code)

			agePayment(t, pid, 61*time.Second)
			res, err := e.payments.Reconcile(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, res.Claimed)
			assert.Equal(t, 1, res.Pending, "a result code that is not a known final failure is 'still pending'")
			assert.Zero(t, res.Failed)
			require.Equal(t, "PENDING", paymentStatus(t, pid), "the payment must stay open: the fan is still entering the PIN")

			// The fan enters the PIN. Daraja now says paid, and sends its callback.
			daraja.answer(checkoutID, "0")
			e.webhook(paidBody(t, checkoutID, c.Total))
			assert.Equal(t, "SUCCESS", paymentStatus(t, pid))
			assert.Equal(t, "PAID", bookingStatus(t, c.BookingID))
			tickets, refunds := settled(t, pid, c.BookingID)
			assert.Equal(t, 2, tickets)
			assert.Zero(t, refunds)
			// Release the seats for the next round.
			exec(t, `UPDATE payments SET created_at = created_at - interval '30 days' WHERE id = $1`, pid)
		})
		north = north[2:]
	}
	assert.Equal(t, 4, ledger(t, ev.ID).Sold)
}

// Fix 1 (ii). A failure callback, then a success for the same payment. FAILED used to be
// final, so the success was discarded with only its raw body kept. Now a confirmed success
// for a FAILED (or TIMEOUT) payment is "money received": the normal success path runs.
func TestReview1_ASuccessAfterAFailureCallbackIsNotDropped(t *testing.T) {
	e := newEnv(t)
	ev, north, south := e.ticketEvent()
	failed := func(c checkout) (int64, string) {
		pid := id64(e.pay(c, mock.NoCallback)["id"])
		id := checkoutIDOf(t, pid)
		e.webhook(stkCallback(id, 1032, "Request cancelled by user."))
		require.Equal(t, "FAILED", paymentStatus(t, pid))
		require.Equal(t, "cancelled", e.getPayment(c.Token, pid)["failure_reason"])
		return pid, id
	}

	t.Run("the hold still has every seat: the seats are sold", func(t *testing.T) {
		c := e.checkout(ev.ID, north[10:12]...)
		pid, id := failed(c)
		require.Equal(t, "PENDING_PAYMENT", bookingStatus(t, c.BookingID))

		e.fanHasPaid()
		e.webhook(paidBody(t, id, c.Total))
		got := e.getPayment(c.Token, pid)
		assert.Equal(t, "SUCCESS", got["status"], "FAILED -> SUCCESS: the money did arrive")
		assert.Nil(t, got["failure_reason"])
		assert.Equal(t, "PAID", got["booking_status"])
		tickets, refunds := settled(t, pid, c.BookingID)
		assert.Equal(t, 2, tickets)
		assert.Zero(t, refunds)
		assert.Equal(t, 1, e.sms.count(c.Phone))
	})

	t.Run("the fan had already paid again: the first payment is refunded", func(t *testing.T) {
		c := e.checkout(ev.ID, north[20:22]...)
		first, id := failed(c)
		second := id64(e.pay(c, mock.Success)["id"])
		e.timers.fire(t, soon)
		require.Equal(t, "PAID", bookingStatus(t, c.BookingID))

		e.fanHasPaid()
		e.webhook(paidBody(t, id, c.Total))
		assert.Equal(t, "SUCCESS", paymentStatus(t, first))
		assert.Equal(t, "PAID", bookingStatus(t, c.BookingID), "the booking keeps its seats")
		assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM refunds WHERE payment_id = $1 AND amount_cents = $2 AND reason LIKE 'Paid twice%'`, first, c.Total))
		assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refunds WHERE payment_id = $1`, second))
		assert.Equal(t, 1, openAlerts(t, c.BookingID, "REFUND_REQUIRED"))
		assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1 AND status = 'VALID'`, c.BookingID))
	})

	t.Run("the hold is gone: the money is refunded", func(t *testing.T) {
		c := e.checkout(ev.ID, south[10:12]...)
		pid, id := failed(c)
		lapseHold(t, c.HoldID)
		_, err := e.holds.Sweep(context.Background())
		require.NoError(t, err)
		require.Equal(t, "EXPIRED", bookingStatus(t, c.BookingID))

		e.fanHasPaid()
		e.webhook(paidBody(t, id, c.Total))
		assert.Equal(t, "SUCCESS", paymentStatus(t, pid))
		assert.Equal(t, "REFUND_REQUIRED", bookingStatus(t, c.BookingID))
		tickets, refunds := settled(t, pid, c.BookingID)
		assert.Zero(t, tickets)
		assert.Equal(t, 1, refunds)
		assert.Equal(t, map[string]int{"AVAILABLE": 2}, seatStatuses(t, c.Seats), "seats that went back on sale are not taken back")
	})

	t.Run("a failure reported after a success stays ignored", func(t *testing.T) {
		c, pid := e.paid(ev.ID, south[20:22]...)
		e.webhook(stkCallback(checkoutIDOf(t, pid), 1032, "Request cancelled by user."))
		e.webhook(stkCallback(checkoutIDOf(t, pid), 1, "The balance is insufficient for the transaction."))
		assert.Equal(t, "SUCCESS", paymentStatus(t, pid))
		assert.Equal(t, "PAID", bookingStatus(t, c.BookingID))
		assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1 AND status = 'VALID'`, c.BookingID))
		assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refunds WHERE payment_id = $1`, pid))
	})
	assert.Equal(t, 6, ledger(t, ev.ID).Sold)
}

// ---------------------------------------------------------------------------
// Fix 2. An STK push that reached M-Pesa and whose answer we lost.
// ---------------------------------------------------------------------------

// The answer to the STK push is lost after the request was sent. The payment used to be
// closed FAILED ("never sent"); the fan paid; the callback carried an ID no payment had and
// was thrown away with one log line; and the fan could start a second payment and pay twice.
// Now the payment stays live, a second one is refused, and the callback is stored, matched by
// phone and amount, confirmed with the provider, and applied.
func TestReview2_ALostAnswerKeepsThePaymentLiveAndItsCallbackIsMatched(t *testing.T) {
	var sp *scriptedProvider
	e := newEnv(t, scripted(&sp))
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)

	sp.loseTheAnswer()
	p := want(t, e.stk(c.Token, c.BookingID, payPhone, mock.NoCallback), http.StatusAccepted)
	pid := id64(p["id"])
	assert.Equal(t, "PENDING", p["status"], "202, and the payment is live")
	assert.Equal(t, domain.PaymentUnknownIDMessage, p["result_desc"], "the fan is told we are checking")
	assert.Nil(t, p["failure_reason"])
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM payments WHERE id = $1 AND status = 'PENDING' AND dispatch = 'AMBIGUOUS' AND checkout_request_id IS NULL`, pid))
	sp.setInitiate(nil)

	// The fan cannot double-pay inside the window, and cannot let the seats go either.
	d := holdErr(t, e.stk(c.Token, c.BookingID, payPhone, mock.Success), http.StatusConflict, domain.CodePaymentInProgress)
	assert.EqualValues(t, pid, d["payment_id"])
	holdErr(t, e.do("DELETE", fmt.Sprintf("/holds/%d", c.HoldID), nil, c.Token), http.StatusConflict, domain.CodePaymentInProgress)
	assert.Zero(t, e.timers.waiting())

	// The fan pays on the prompt M-Pesa did send. Its callback carries M-Pesa's ID, which no
	// payment of ours has.
	hidden := sp.lastLost()
	e.fanHasPaid()
	e.webhook(paidBody(t, hidden.CheckoutRequestID, c.Total))

	got := e.getPayment(c.Token, pid)
	assert.Equal(t, "SUCCESS", got["status"])
	assert.Equal(t, "PAID", got["booking_status"])
	assert.NotNil(t, got["mpesa_receipt"])
	assert.Equal(t, hidden.CheckoutRequestID, checkoutIDOf(t, pid), "the ID is on the payment now")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM payments WHERE id = $1 AND dispatch = 'ACCEPTED'`, pid))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM unmatched_callbacks WHERE checkout_request_id = $1 AND verdict = 'MATCHED' AND matched_payment_id = $2`, hidden.CheckoutRequestID, pid))
	tickets, refunds := settled(t, pid, c.BookingID)
	assert.Equal(t, 2, tickets)
	assert.Zero(t, refunds)
	assert.Zero(t, openAlerts(t, c.BookingID, "PAYMENT_UNRESOLVED"), "matched at once: nobody needs to look")
	assert.Equal(t, 1, e.sms.count(c.Phone))
	assert.Equal(t, 2, ledger(t, ev.ID).Sold)
}

// The same, when the callback arrives before M-Pesa can confirm it (and when a callback fits
// no payment at all). The body alone decides nothing: the callback is stored, an alert names
// the receipt and the amount so a person can refund it, and the drain job settles it as soon
// as M-Pesa confirms. The alert is then closed by the system.
func TestReview2_AnUnknownIDCallbackIsStoredFlaggedAndSettledOnceConfirmed(t *testing.T) {
	var sp *scriptedProvider
	e := newEnv(t, scripted(&sp))
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	sp.loseTheAnswer()
	pid := id64(want(t, e.stk(c.Token, c.BookingID, payPhone, mock.NoCallback), http.StatusAccepted)["id"])
	sp.setInitiate(nil)
	hidden := sp.lastLost()

	// A "paid" that fits no payment: a different amount.
	exec(t, `DELETE FROM unmatched_callbacks WHERE checkout_request_id = 'ws_CO_somebody_elses'`)
	stray := newReceipt(t)
	e.webhook(stkCallback("ws_CO_somebody_elses", 0, "ok", paidItems(json.Number("777.00"), stray)...))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM unmatched_callbacks WHERE checkout_request_id = 'ws_CO_somebody_elses' AND processed_at IS NULL AND matched_payment_id IS NULL`))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE kind = 'PAYMENT_UNRESOLVED' AND acknowledged_at IS NULL
		AND message LIKE '%' || $1 || '%' AND message LIKE '%KES 777.00%' AND message LIKE '%could not be tied to any booking%'`, stray), "a person can find and refund it")
	assert.Equal(t, "PENDING", paymentStatus(t, pid))

	// The callback for OUR payment, before M-Pesa confirms it. Nothing is sold on its word.
	body := paidBody(t, hidden.CheckoutRequestID, c.Total)
	e.webhook(body)
	assert.Equal(t, "PENDING", paymentStatus(t, pid))
	assert.Equal(t, map[string]int{"HELD": 2}, seatStatuses(t, c.Seats))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refunds WHERE booking_id = $1`, c.BookingID), "no refund on the word of a body")
	assert.Equal(t, "", checkoutIDOf(t, pid), "the ID is not written on the payment until M-Pesa confirms it")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM unmatched_callbacks WHERE checkout_request_id = $1 AND processed_at IS NULL AND alert_id IS NOT NULL`, hidden.CheckoutRequestID))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE booking_id = $1 AND payment_id = $2 AND kind = 'PAYMENT_UNRESOLVED' AND acknowledged_at IS NULL`, c.BookingID, pid))
	// Sending it again raises no second alert.
	e.webhook(body)
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE booking_id = $1 AND kind = 'PAYMENT_UNRESOLVED'`, c.BookingID))

	// M-Pesa confirms. The drain job does the rest.
	e.fanHasPaid()
	e.drain()
	assert.Equal(t, "SUCCESS", paymentStatus(t, pid))
	assert.Equal(t, "PAID", bookingStatus(t, c.BookingID))
	tickets, refunds := settled(t, pid, c.BookingID)
	assert.Equal(t, 2, tickets)
	assert.Zero(t, refunds)
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE payment_id = $1 AND kind = 'PAYMENT_UNRESOLVED'
		AND acknowledged_at IS NOT NULL AND resolution = 'Resolved automatically: the payment succeeded and the tickets were issued.'`, pid),
		"the alert is closed by the system once the payment has resolved")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM unmatched_callbacks WHERE checkout_request_id = 'ws_CO_somebody_elses' AND matched_payment_id IS NULL`), "the stray one stays for a person")
	assert.Equal(t, 2, ledger(t, ev.ID).Sold)
}

// A payment with no provider ID that never resolves is closed TIMEOUT WITH an alert (money
// may have moved under an ID we never learned), without a single call to the provider. A
// callback that turns up after that is still matched to it, and the money is refunded.
func TestReview2_AnIDLessPaymentTimesOutWithAnAlertAndALateCallbackIsStillMatched(t *testing.T) {
	var sp *scriptedProvider
	e := newEnv(t, scripted(&sp))
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	sp.loseTheAnswer()
	pid := id64(want(t, e.stk(c.Token, c.BookingID, payPhone, mock.NoCallback), http.StatusAccepted)["id"])
	sp.setInitiate(nil)
	hidden := sp.lastLost()
	ctx := context.Background()

	agePayment(t, pid, 2*time.Minute)
	res, err := e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Pending, "not closed while its callback could still come")
	assert.Equal(t, "PENDING", paymentStatus(t, pid))
	agePayment(t, pid, 4*time.Minute)
	res, err = e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.TimedOut)
	assert.Equal(t, "TIMEOUT", paymentStatus(t, pid))
	assert.Equal(t, 1, openAlerts(t, c.BookingID, "PAYMENT_UNRESOLVED"), "closed WITH an alert")
	assert.Zero(t, sp.queries.Load(), "there was no ID to ask about")

	// The hold runs out and the seats go back on sale.
	lapseHold(t, c.HoldID)
	_, err = e.holds.Sweep(ctx)
	require.NoError(t, err)
	require.Equal(t, "EXPIRED", bookingStatus(t, c.BookingID))

	// Then M-Pesa's callback for the prompt it did send.
	e.fanHasPaid()
	e.webhook(paidBody(t, hidden.CheckoutRequestID, c.Total))
	assert.Equal(t, "SUCCESS", paymentStatus(t, pid), "TIMEOUT -> SUCCESS")
	assert.Equal(t, "REFUND_REQUIRED", bookingStatus(t, c.BookingID))
	tickets, refunds := settled(t, pid, c.BookingID)
	assert.Zero(t, tickets)
	assert.Equal(t, 1, refunds)
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM refunds WHERE payment_id = $1 AND amount_cents = $2`, pid, c.Total))
	assert.Zero(t, openAlerts(t, c.BookingID, "PAYMENT_UNRESOLVED"), "the timeout alert is closed: the payment resolved")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE payment_id = $1 AND kind = 'PAYMENT_UNRESOLVED' AND resolution LIKE '%refund is queued%'`, pid))
	assert.Equal(t, 1, openAlerts(t, c.BookingID, "REFUND_REQUIRED"))
	assert.Zero(t, ledger(t, ev.ID).Sold)
}

// ---------------------------------------------------------------------------
// Fix 3. Nothing that says "paid" is kept only in memory, and TIMEOUT is asked about again.
// ---------------------------------------------------------------------------

// A TIMEOUT payment used to be final unless a callback arrived. Now reconciliation keeps
// asking the provider about it on a slow schedule, so the provider's last word reaches the
// finishing function with no callback at all.
func TestReview3_ATimedOutPaymentIsAskedAboutAgainOnASlowSchedule(t *testing.T) {
	var sp *scriptedProvider
	e := newEnv(t, scripted(&sp))
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	pid := id64(e.pay(c, mock.NoCallback)["id"])
	ctx := context.Background()
	lastAsked := func(ago time.Duration) {
		exec(t, `UPDATE payments SET last_queried_at = clock_timestamp() - make_interval(secs => $2) WHERE id = $1`, pid, ago.Seconds())
	}
	created := func(ago time.Duration) {
		exec(t, `UPDATE payments SET created_at = clock_timestamp() - make_interval(secs => $2) WHERE id = $1`, pid, ago.Seconds())
	}

	// Five minutes, no answer: TIMEOUT, with its alert. No callback will ever come.
	agePayment(t, pid, 6*time.Minute)
	res, err := e.payments.Reconcile(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.TimedOut)
	require.Equal(t, "TIMEOUT", paymentStatus(t, pid))
	require.Equal(t, 1, openAlerts(t, c.BookingID, "PAYMENT_UNRESOLVED"))
	asked := sp.queries.Load()

	// The schedule, by the database clock: not again within five minutes...
	res, err = e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.Zero(t, res.Requeried)
	lastAsked(4 * time.Minute)
	res, err = e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.Zero(t, res.Requeried)
	assert.Equal(t, asked, sp.queries.Load())
	// ...then every five minutes for the first day (the provider still has no answer)...
	lastAsked(6 * time.Minute)
	res, err = e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Requeried)
	assert.Equal(t, 1, res.Pending)
	assert.Equal(t, asked+1, sp.queries.Load())
	assert.Equal(t, "TIMEOUT", paymentStatus(t, pid), "no answer changes nothing, and it is not timed out twice")
	// ...then once a day...
	created(2 * 24 * time.Hour)
	lastAsked(6 * time.Hour)
	res, err = e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.Zero(t, res.Requeried, "after the first day, once a day")
	lastAsked(25 * time.Hour)
	res, err = e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Requeried)
	// ...and not at all after a week.
	created(8 * 24 * time.Hour)
	lastAsked(48 * time.Hour)
	res, err = e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.Zero(t, res.Requeried, "bounded: a week")
	assert.Equal(t, asked+2, sp.queries.Load())

	// Back inside the first day: the fan did pay, and M-Pesa says so when asked.
	created(20 * time.Minute)
	lastAsked(6 * time.Minute)
	e.fanHasPaid()
	res, err = e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Requeried)
	assert.Equal(t, 1, res.Sold, "the hold still has every seat, so the fan gets them")
	e.payments.Wait()
	assert.Equal(t, "SUCCESS", paymentStatus(t, pid))
	tickets, refunds := settled(t, pid, c.BookingID)
	assert.Equal(t, 2, tickets)
	assert.Zero(t, refunds)
	assert.Zero(t, openAlerts(t, c.BookingID, "PAYMENT_UNRESOLVED"), "the timeout alert is closed by the system")
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM payment_callbacks WHERE payment_id = $1`, pid), "no callback was involved")
	assert.Equal(t, 2, ledger(t, ev.ID).Sold)
}

// A callback is stored before the webhook answers and is retried until it is dealt with: a
// late success for a TIMEOUT payment survives a busy event, a provider that cannot be asked,
// and a restart between the 200 and the work.
func TestReview3_AStoredCallbackIsRetriedUntilItIsApplied(t *testing.T) {
	var sp *scriptedProvider
	e := newEnv(t, scripted(&sp), withPaymentPolicy(func(p *payments.Policy) { p.EventLockWait = 200 * time.Millisecond }))
	ev, north, south := e.ticketEvent()
	ctx := context.Background()

	t.Run("a TIMEOUT payment whose late callback finds the event busy", func(t *testing.T) {
		c := e.checkout(ev.ID, north[10:12]...)
		pid := id64(e.pay(c, mock.NoCallback)["id"])
		agePayment(t, pid, 6*time.Minute)
		res, err := e.payments.Reconcile(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, res.TimedOut)

		organiser, err := db.Pool.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = organiser.Rollback(ctx) }()
		_, err = store.New(organiser).LockEvent(ctx, ev.ID)
		require.NoError(t, err)
		e.fanHasPaid()
		e.webhook(paidBody(t, checkoutIDOf(t, pid), c.Total))
		require.Equal(t, "TIMEOUT", paymentStatus(t, pid), "the event was busy: nothing could be applied")
		require.NoError(t, organiser.Rollback(ctx))

		// Reconciliation does not look at it (it was asked about seconds ago). The drain does.
		res, err = e.payments.Reconcile(ctx)
		require.NoError(t, err)
		require.Zero(t, res.Requeried)
		require.Equal(t, "TIMEOUT", paymentStatus(t, pid))
		assert.Equal(t, 1, e.drain().Done)
		assert.Equal(t, "SUCCESS", paymentStatus(t, pid))
		tickets, refunds := settled(t, pid, c.BookingID)
		assert.Equal(t, 2, tickets)
		assert.Zero(t, refunds)
	})

	t.Run("the provider cannot be asked: the body is not believed, and the callback waits", func(t *testing.T) {
		c := e.checkout(ev.ID, north[20:22]...)
		pid := id64(e.pay(c, mock.NoCallback)["id"])
		e.fanHasPaid()
		sp.setQuery(func(string, func() (payments.PaymentStatus, error)) (payments.PaymentStatus, error) {
			return payments.PaymentStatus{}, errProviderDown
		})
		e.webhook(paidBody(t, checkoutIDOf(t, pid), c.Total))
		assert.Equal(t, "PENDING", paymentStatus(t, pid), "left pending rather than believe the body")
		assert.Equal(t, map[string]int{"HELD": 2}, seatStatuses(t, c.Seats))
		assert.Equal(t, 1, e.drain().Retry)
		assert.Equal(t, "PENDING", paymentStatus(t, pid))
		assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM payment_callbacks WHERE payment_id = $1 AND processed_at IS NULL AND attempts = 2 AND last_error LIKE '%connection refused%'`, pid))

		sp.setQuery(nil)
		assert.Equal(t, 1, e.drain().Done)
		assert.Equal(t, "SUCCESS", paymentStatus(t, pid))
		tickets, _ := settled(t, pid, c.BookingID)
		assert.Equal(t, 2, tickets)
	})

	t.Run("a restart between the 200 and the work", func(t *testing.T) {
		c := e.checkout(ev.ID, south[10:12]...)
		pid := id64(e.pay(c, mock.NoCallback)["id"])
		e.fanHasPaid()
		amount := c.Total
		// Stored, as the webhook does before it answers; then the process dies.
		stored, err := e.payments.StoreCallback(ctx, payments.Callback{
			CheckoutRequestID: checkoutIDOf(t, pid), ResultCode: 0, ResultDesc: "ok", AmountCents: &amount,
			Receipt: newReceipt(t), Phone: payPhone, Raw: []byte(`{"Body":{"stkCallback":{"ResultCode":0}}}`),
		})
		require.NoError(t, err)
		require.NotZero(t, stored.ID)
		require.Equal(t, "PENDING", paymentStatus(t, pid))

		// The worker's drain job, in what could be another process, finds it.
		res, err := e.payments.DrainCallbacks(ctx)
		require.NoError(t, err)
		e.payments.Wait()
		assert.Equal(t, 1, res.Done)
		assert.Equal(t, "SUCCESS", paymentStatus(t, pid))
		tickets, _ := settled(t, pid, c.BookingID)
		assert.Equal(t, 2, tickets)
	})
	assert.Equal(t, 6, ledger(t, ev.ID).Sold)
}

// The webhook does not say "Accepted" for a callback it could not write down.
func TestReview3_TheWebhookAnswers503WhenTheCallbackCannotBeStored(t *testing.T) {
	var pdb *switchableDB
	e := newEnv(t, withPaymentDB(func(real payments.DB) payments.DB { pdb = &switchableDB{DB: real}; return pdb }))
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	pid := id64(e.pay(c, mock.NoCallback)["id"])
	e.fanHasPaid()
	body := paidBody(t, checkoutIDOf(t, pid), c.Total)

	pdb.down.Store(true)
	rec := e.doReq(e.request("POST", "/webhooks/mpesa/callback", body, ""))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"ResultCode":1,"ResultDesc":"Temporarily unavailable"}`, rec.Body.String())
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	// Still 200 for what would never be stored anyway: a prober learns nothing new.
	rec = e.doReq(e.request("POST", "/webhooks/mpesa/callback", `garbage`, ""), contracttest.LenientRequest())
	require.Equal(t, http.StatusOK, rec.Code)
	pdb.down.Store(false)
	e.payments.Wait()
	assert.Equal(t, "PENDING", paymentStatus(t, pid))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM payment_callbacks WHERE payment_id = $1`, pid))

	// Sent again once the database is back, it is stored and applied.
	e.webhook(body)
	assert.Equal(t, "SUCCESS", paymentStatus(t, pid))
	assert.Equal(t, 2, ledger(t, ev.ID).Sold)
}

// ---------------------------------------------------------------------------
// Fix 4. Tickets that cannot be issued.
// ---------------------------------------------------------------------------

// failingIssuer is the ticket service with a fault in IssueForBooking.
type failingIssuer struct {
	payments.TicketIssuer
	mu  sync.Mutex
	err error
}

func (f *failingIssuer) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *failingIssuer) IssueForBooking(ctx context.Context, tx pgx.Tx, bookingID int64) ([]ticketing.Ticket, error) {
	f.mu.Lock()
	err := f.err
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return f.TicketIssuer.IssueForBooking(ctx, tx, bookingID)
}

// A payment succeeds and its tickets cannot be made, for a reason that will not go away (a
// ticket that cannot be signed, for one: a stand code that does not fit a gate group). Any
// error that was not one of four sentinels used to roll the whole transaction back, every
// time, leaving the fan's money with us and the payment to time out. Now the sale is undone,
// the payment is recorded and the money is queued to go back. An error that may pass (the
// database went away, the request ran out of time) still rolls back, and is retried.
func TestReview4_TicketsThatCannotBeIssuedUndoTheSaleAndQueueARefund(t *testing.T) {
	var issuer *failingIssuer
	e := newEnv(t, withTicketIssuer(func(real payments.TicketIssuer) payments.TicketIssuer {
		issuer = &failingIssuer{TicketIssuer: real}
		return issuer
	}))
	ev, north, _ := e.ticketEvent()

	t.Run("a deterministic error", func(t *testing.T) {
		c := e.checkout(ev.ID, north[10:12]...)
		pid := id64(e.pay(c, mock.Success)["id"])
		issuer.fail(errors.New(`sign ticket: gate group "NORTHSTAND1-LOWER" cannot go in a ticket: it must be <stand code>-LOWER or -UPPER and at most 16 characters`))
		fired := e.timers.fire(t, soon)
		issuer.fail(nil)
		require.Len(t, fired, 1)
		assert.Equal(t, payments.ActionRefund, fired[0].Action)
		assert.Equal(t, "Paid, but the tickets could not be issued", fired[0].Reason)

		got := e.getPayment(c.Token, pid)
		assert.Equal(t, "SUCCESS", got["status"], "the money did arrive")
		assert.Equal(t, "REFUND_REQUIRED", got["booking_status"])
		tickets, refunds := settled(t, pid, c.BookingID)
		assert.Zero(t, tickets)
		assert.Equal(t, 1, refunds)
		assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM refunds WHERE payment_id = $1 AND amount_cents = $2 AND status = 'PENDING'`, pid, c.Total))
		assert.Equal(t, 1, openAlerts(t, c.BookingID, "REFUND_REQUIRED"))
		assert.Equal(t, map[string]int{"HELD": 2}, seatStatuses(t, c.Seats), "the sale was undone: not one seat is SOLD")
		assert.Equal(t, "ACTIVE", queryText(t, `SELECT status FROM holds WHERE id = $1`, c.HoldID))
		assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, c.BookingID))
		assert.Equal(t, 0, e.sms.count(c.Phone))
	})

	t.Run("a transient error is retried, not refunded", func(t *testing.T) {
		c := e.checkout(ev.ID, north[20:22]...)
		pid := id64(e.pay(c, mock.NoCallback)["id"])
		e.fanHasPaid()
		issuer.fail(fmt.Errorf("issue tickets: insert: %w", context.DeadlineExceeded))
		e.webhook(paidBody(t, checkoutIDOf(t, pid), c.Total))
		assert.Equal(t, "PENDING", paymentStatus(t, pid), "rolled back whole: nothing was written")
		assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refunds WHERE payment_id = $1`, pid))
		assert.Equal(t, map[string]int{"HELD": 2}, seatStatuses(t, c.Seats))

		issuer.fail(nil)
		assert.Equal(t, 1, e.drain().Done)
		assert.Equal(t, "SUCCESS", paymentStatus(t, pid))
		tickets, refunds := settled(t, pid, c.BookingID)
		assert.Equal(t, 2, tickets)
		assert.Zero(t, refunds)
	})
	assert.Equal(t, 2, ledger(t, ev.ID).Sold)
}

// The cause is closed off at the source too: a stand code that could not make a gate group
// cannot be stored (migration 000009). Nothing in the API writes stand codes; they are seeded.
func TestReview4_StandCodesThatCannotMakeAGateGroupAreRefused(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 1, 2)
	ctx := context.Background()
	for _, bad := range []string{"north", "N-1", "", "N LOWER", "ABCDEFGHIJK", "É"} {
		_, err := db.Pool.Exec(ctx, `INSERT INTO stands (venue_id, code, name, tier) VALUES ($1, $2, 'x', 'LOWER')`, v.ID, bad)
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr, "code %q", bad)
		assert.Equal(t, "stands_code_check", pgErr.ConstraintName, "code %q", bad)
	}
	for _, good := range []string{"X9", "ABCDEFGHIJ"} {
		_, err := db.Pool.Exec(ctx, `INSERT INTO stands (venue_id, code, name, tier) VALUES ($1, $2, 'x', 'UPPER')`, v.ID, good)
		require.NoError(t, err, "code %q", good)
		assert.LessOrEqual(t, len(domain.GateGroup(good, "UPPER")), domain.MaxGateGroupLength)
	}
	assert.Zero(t, queryInt(t, `SELECT count(*) FROM stands WHERE code !~ '^[A-Z0-9]{1,10}$'`), "every stand there is, the seeded ones included, passes")
}

// ---------------------------------------------------------------------------
// Fix 5. The callback body is a hint; the provider's answer is the authority.
// ---------------------------------------------------------------------------

// Against a provider that, like Daraja, names no amount in its query answer: a forged
// "Amount: 900000.00" on a payment M-Pesa confirms used to make the payment FAILED and queue
// a refund of KES 900,000. Now the confirmed payment is paid in full at the amount WE
// requested; the body's figure is refunded to nobody and raises an alert for a person.
func TestReview5_ACallbackAmountIsNeverTreatedAsMoney(t *testing.T) {
	var sp *scriptedProvider
	e := newEnv(t, scripted(&sp))
	sp.likeDaraja()
	ev, north, _ := e.ticketEvent()

	c := e.checkout(ev.ID, north[10:12]...)
	pid := id64(e.pay(c, mock.NoCallback)["id"])
	id := checkoutIDOf(t, pid)
	forged := stkCallback(id, 0, "ok", paidItems(json.Number("900000.00"), newReceipt(t))...)

	// M-Pesa does not confirm it: nothing at all happens.
	e.webhook(forged)
	assert.Equal(t, "PENDING", paymentStatus(t, pid))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refunds WHERE booking_id = $1`, c.BookingID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE booking_id = $1`, c.BookingID))

	// M-Pesa confirms the payment (and, as Daraja does, names no amount).
	e.fanHasPaid()
	e.webhook(forged)
	got := e.getPayment(c.Token, pid)
	assert.Equal(t, "SUCCESS", got["status"])
	assert.Equal(t, "PAID", got["booking_status"], "paid in full at the amount we requested")
	assert.Nil(t, got["failure_reason"])
	assert.EqualValues(t, c.Total, got["amount_cents"])
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refunds WHERE booking_id = $1`, c.BookingID), "no refund of a sum a body supplied")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE booking_id = $1 AND kind = 'AMOUNT_MISMATCH'
		AND message LIKE '%KES 900,000.00%' AND message LIKE '%no refund was queued%'`, c.BookingID), "but a person is told")
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1 AND status = 'VALID'`, c.BookingID))

	// The same with the right amount in the body: no alert at all.
	c2 := e.checkout(ev.ID, north[20:22]...)
	p2 := id64(e.pay(c2, mock.NoCallback)["id"])
	e.fanHasPaid()
	e.webhook(paidBody(t, checkoutIDOf(t, p2), c2.Total))
	assert.Equal(t, "PAID", bookingStatus(t, c2.BookingID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE booking_id = $1`, c2.BookingID))
	assert.Equal(t, 4, ledger(t, ev.ID).Sold)
}

// When the provider is "none" (payments switched off) the webhook ignores every callback:
// 200, nothing stored, nothing changed.
func TestReview5_WithPaymentsOffEveryCallbackIsIgnored(t *testing.T) {
	e := newEnv(t, withProvider(func(*mock.Provider) payments.Provider {
		p, m, err := providers.New(config.Payments{Provider: config.ProviderNone}, config.Base{AppEnv: config.EnvProduction}, nil)
		require.NoError(t, err)
		require.Nil(t, m)
		return p
	}))
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	// No payment can be started...
	wantError(t, e.stk(c.Token, c.BookingID, payPhone, ""), http.StatusBadGateway, domain.CodePaymentProviderUnavailable)
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM payments WHERE booking_id = $1 AND status = 'FAILED' AND dispatch = 'NOT_SENT'`, c.BookingID))
	// ...and one left over from before payments were switched off is not touched by a callback.
	var pid int64
	require.NoError(t, db.Pool.QueryRow(context.Background(),
		`INSERT INTO payments (booking_id, amount_cents, phone_e164, status, checkout_request_id, dispatch)
		 VALUES ($1, $2, $3, 'PENDING', $4, 'ACCEPTED') RETURNING id`, c.BookingID, c.Total, payPhone, "ws_CO_off_"+randDigits(t, 12)).Scan(&pid))

	e.webhook(paidBody(t, checkoutIDOf(t, pid), c.Total))
	e.webhook(stkCallback(checkoutIDOf(t, pid), 1032, "Request cancelled by user."))
	e.webhook(paidBody(t, "ws_CO_unknown_while_off", c.Total))
	assert.Equal(t, "PENDING", paymentStatus(t, pid))
	assert.Equal(t, "PENDING_PAYMENT", bookingStatus(t, c.BookingID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM payment_callbacks WHERE payment_id = $1`, pid))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM unmatched_callbacks WHERE checkout_request_id = 'ws_CO_unknown_while_off'`))
	assert.Contains(t, e.logs.String(), "payment callback ignored: payments are switched off")
	assert.Equal(t, map[string]int{"HELD": 2}, seatStatuses(t, c.Seats))
	ledger(t, ev.ID)
}

// ---------------------------------------------------------------------------
// Fix 8. Minor.
// ---------------------------------------------------------------------------

// A "paid" of nothing: the booking is not moved to REFUND_REQUIRED with no refund row.
func TestReview8_ASuccessOfZeroShillingsMovesNoBooking(t *testing.T) {
	var sp *scriptedProvider
	e := newEnv(t, scripted(&sp))
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	pid := id64(e.pay(c, mock.NoCallback)["id"])
	e.fanHasPaid()
	sp.setQuery(func(_ string, next func() (payments.PaymentStatus, error)) (payments.PaymentStatus, error) {
		st, err := next()
		zero := int64(0)
		st.AmountCents = &zero
		return st, err
	})

	e.webhook(stkCallback(checkoutIDOf(t, pid), 0, "ok", paidItems(json.Number("0.00"), newReceipt(t))...))
	got := e.getPayment(c.Token, pid)
	assert.Equal(t, "FAILED", got["status"])
	assert.Equal(t, "other", got["failure_reason"], "not wrong_amount: nothing was paid and nothing is refunded")
	assert.Equal(t, "PENDING_PAYMENT", got["booking_status"], "the booking is not moved: the fan can pay again")
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refunds WHERE booking_id = $1`, c.BookingID))
	assert.Equal(t, 1, openAlerts(t, c.BookingID, "AMOUNT_MISMATCH"), "odd enough for a person to look at")
	assert.Equal(t, map[string]int{"HELD": 2}, seatStatuses(t, c.Seats))

	sp.setQuery(nil)
	e.pay(c, mock.Success)
	e.timers.fire(t, soon)
	assert.Equal(t, "PAID", bookingStatus(t, c.BookingID))
	assert.Equal(t, 2, ledger(t, ev.ID).Sold)
}

// Reconciliation handles the payments that need no provider call first, and stops starting
// provider calls when its time is up: a slow M-Pesa delays the payments it is slow about,
// not every other one.
func TestReview8_ReconciliationPutsIDLessPaymentsFirstAndBoundsItsRunTime(t *testing.T) {
	var sp *scriptedProvider
	var now atomic.Int64 // seconds on the run-time clock
	clock := func() time.Time { return time.Unix(1_800_000_000+now.Load(), 0) }
	e := newEnv(t, scripted(&sp), withPaymentClock(clock),
		withPaymentPolicy(func(p *payments.Policy) { p.ReconcileBudget = time.Minute }))
	ev, north, _ := e.ticketEvent()
	ctx := context.Background()

	var withID []int64
	for i := 0; i < 5; i++ {
		c := e.checkout(ev.ID, north[10+i])
		pid := id64(e.pay(c, mock.NoCallback)["id"])
		agePayment(t, pid, 3*time.Minute) // older than the ID-less one below
		withID = append(withID, pid)
	}
	idless := e.checkout(ev.ID, north[20])
	var idlessPID int64
	require.NoError(t, db.Pool.QueryRow(ctx,
		`INSERT INTO payments (booking_id, amount_cents, phone_e164, status, dispatch, created_at)
		 VALUES ($1, $2, $3, 'INITIATED', 'NOT_SENT', now() - interval '2 minutes') RETURNING id`, idless.BookingID, idless.Total, payPhone).Scan(&idlessPID))

	// Every query to the provider takes 40 seconds and brings no answer.
	sp.setQuery(func(_ string, next func() (payments.PaymentStatus, error)) (payments.PaymentStatus, error) {
		now.Add(40)
		return next()
	})
	res, err := e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.Equal(t, "TIMEOUT", paymentStatus(t, idlessPID), "the payment that needs no provider call was dealt with first, although it is the youngest")
	assert.Equal(t, 1, res.TimedOut)
	assert.True(t, res.OutOfTime)
	assert.EqualValues(t, 2, sp.queries.Load(), "two 40-second queries fit a 60-second budget; the run then stops asking")
	assert.Equal(t, 5, queryInt(t, `SELECT count(*) FROM payments WHERE id = ANY($1) AND status = 'PENDING'`, withID))

	// The next run carries on.
	sp.setQuery(nil)
	res, err = e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.False(t, res.OutOfTime)
	assert.Equal(t, 5, res.Claimed)
	assert.EqualValues(t, 7, sp.queries.Load())
	ledger(t, ev.ID)
}
