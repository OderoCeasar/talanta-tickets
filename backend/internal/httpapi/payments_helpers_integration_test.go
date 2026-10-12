//go:build integration

package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi/contracttest"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments/mock"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/testinvariants"
)

// m7PaymentOperations is every operation of the booking and payment milestone.
var m7PaymentOperations = []string{
	"createBooking", "getBooking", "listMyBookings",
	"startMpesaStkPush", "getPayment", "mpesaCallback",
	"listAlerts", "ackAlert", "listRefunds", "completeRefund",
}

// operationsThatOnlyAnswer200 have no error answer in the contract to check.
var operationsThatOnlyAnswer200 = []string{"mpesaCallback"}

func withPaymentPolicy(fn func(*payments.Policy)) envOption {
	return func(c *envConfig) { fn(&c.paymentPolicy) }
}

func withProvider(fn func(m *mock.Provider) payments.Provider) envOption {
	return func(c *envConfig) { c.provider = fn }
}

func withTicketIssuer(fn func(real payments.TicketIssuer) payments.TicketIssuer) envOption {
	return func(c *envConfig) { c.ticketIssuer = fn }
}

func withPaymentClock(now func() time.Time) envOption {
	return func(c *envConfig) { c.paymentNow = now }
}

func withCallbackAllowlist(cidrs ...string) envOption {
	return func(c *envConfig) {
		for _, s := range cidrs {
			c.callbackAllowed = append(c.callbackAllowed, netip.MustParsePrefix(s))
		}
	}
}

// productionPaymentEnv sets what production must configure for payments (fake values), for
// tests that load the real configuration.
func productionPaymentEnv(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"PAYMENT_PROVIDER": "", "MPESA_ENV": "sandbox", "MPESA_CONSUMER_KEY": "key", "MPESA_CONSUMER_SECRET": "secret",
		"MPESA_SHORTCODE": "174379", "MPESA_PASSKEY": "passkey", "MPESA_TRANSACTION_TYPE": "",
		"MPESA_CALLBACK_URL":           "https://talanta.example.com/v1/webhooks/mpesa/callback",
		"MPESA_CALLBACK_ALLOWED_CIDRS": "196.201.214.0/24",
	} {
		t.Setenv(k, v)
	}
}

// mockTimers is the mock provider's scheduler in tests. Nothing runs on a timer: the mock
// hands its delayed callbacks here and the test fires them when it wants them delivered.
type mockTimers struct {
	mu      sync.Mutex
	pending []mockTimer
	results []payments.Finished
	errs    []error
}

type mockTimer struct {
	after time.Duration
	fn    func()
}

func (m *mockTimers) After(d time.Duration, fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending = append(m.pending, mockTimer{after: d, fn: fn})
}

func (m *mockTimers) record(fin payments.Finished, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.errs = append(m.errs, err)
		return
	}
	m.results = append(m.results, fin)
}

func (m *mockTimers) waiting() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending)
}

// fire delivers, in the order they were scheduled, the callbacks due within `within`, and
// returns what each did.
func (m *mockTimers) fire(t testing.TB, within time.Duration) []payments.Finished {
	t.Helper()
	m.mu.Lock()
	var due, later []mockTimer
	for _, tm := range m.pending {
		if tm.after <= within {
			due = append(due, tm)
		} else {
			later = append(later, tm)
		}
	}
	m.pending = later
	from := len(m.results)
	m.mu.Unlock()

	for _, tm := range due {
		tm.fn()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	require.Empty(t, m.errs, "a mock callback could not be applied")
	return append([]payments.Finished(nil), m.results[from:]...)
}

// soon is longer than the mock's callback delay and far shorter than its late delay.
const soon = time.Minute

// late is longer than the mock's late delay.
const late = time.Hour

// doReq sends a prepared request through the contract check.
func (e *env) doReq(req *http.Request, opts ...contracttest.Option) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.contract.Do(e.t, e.router, req, opts...)
}

// book posts /bookings for a hold with an idempotency key.
func (e *env) book(token string, holdID int64, key string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := e.request("POST", "/bookings", obj{"hold_id": holdID}, token)
	req.Header.Set("Idempotency-Key", key)
	return e.doReq(req)
}

// stk posts /payments/mpesa/stk. outcome "" sends no X-Mock-Outcome header.
func (e *env) stk(token string, bookingID int64, phone, outcome string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := e.request("POST", "/payments/mpesa/stk", obj{"booking_id": bookingID, "phone": phone}, token)
	if outcome != "" {
		req.Header.Set("X-Mock-Outcome", outcome)
	}
	return e.doReq(req)
}

const payPhone = "+254712345678"

// checkout is a fan with seats held and a booking awaiting payment.
type checkout struct {
	FanID     int64
	Token     string
	Phone     string
	HoldID    int64
	BookingID int64
	Ref       string
	Seats     []int64
	Total     int64
}

func newKey(t testing.TB) string { return "key-" + randDigits(t, 20) }

// checkoutAs holds the seats for a fan and makes the booking, both through the API.
func (e *env) checkoutAs(fanID int64, token string, eventID int64, seats ...int64) checkout {
	e.t.Helper()
	h := want(e.t, e.hold(token, eventID, seats...), http.StatusCreated)
	b := want(e.t, e.book(token, id64(h["id"]), newKey(e.t)), http.StatusCreated)
	require.Equal(e.t, "PENDING_PAYMENT", b["status"])
	return checkout{
		FanID: fanID, Token: token, Phone: queryText(e.t, `SELECT phone_e164 FROM users WHERE id = $1`, fanID),
		HoldID: id64(h["id"]), BookingID: id64(b["id"]), Ref: b["reference"].(string),
		Seats: seats, Total: id64(b["total_cents"]),
	}
}

// checkout is checkoutAs for a new fan.
func (e *env) checkout(eventID int64, seats ...int64) checkout {
	e.t.Helper()
	fanID, token := e.newFan()
	return e.checkoutAs(fanID, token, eventID, seats...)
}

// pay starts a payment with the given mock outcome and returns the payment (PENDING).
func (e *env) pay(c checkout, outcome string) obj {
	e.t.Helper()
	p := want(e.t, e.stk(c.Token, c.BookingID, payPhone, outcome), http.StatusAccepted)
	require.Equal(e.t, "PENDING", p["status"])
	require.NotContains(e.t, p, "checkout_request_id", "Daraja's request IDs are not sent to clients")
	require.NotContains(e.t, p, "merchant_request_id")
	require.NotEmpty(e.t, checkoutIDOf(e.t, id64(p["id"])), "the ID is on the row all the same")
	return p
}

// checkoutIDOf reads a payment's CheckoutRequestID from the table: the API does not send it.
func checkoutIDOf(t testing.TB, paymentID int64) string {
	t.Helper()
	return queryText(t, `SELECT COALESCE(checkout_request_id, '') FROM payments WHERE id = $1`, paymentID)
}

// fanHasPaid moves the mock provider's clock past its callback delay: from now on its own
// answer to a status query is the payment's outcome. A hand-posted "paid" callback is only
// a hint to ask the provider, so tests that post one say first that the provider agrees.
func (e *env) fanHasPaid() { e.mockClock.Advance(time.Minute) }

// drain makes every stored callback due and runs the worker's drain job once.
func (e *env) drain() payments.DrainResult {
	e.t.Helper()
	exec(e.t, `UPDATE payment_callbacks SET next_attempt_at = now() WHERE processed_at IS NULL`)
	exec(e.t, `UPDATE unmatched_callbacks SET next_attempt_at = now() WHERE processed_at IS NULL`)
	res, err := e.payments.DrainCallbacks(context.Background())
	require.NoError(e.t, err)
	e.payments.Wait()
	return res
}

func (e *env) getPayment(token string, id int64) obj {
	e.t.Helper()
	return want(e.t, e.do("GET", fmt.Sprintf("/payments/%d", id), nil, token), http.StatusOK)
}

func (e *env) getBooking(token, ref string) obj {
	e.t.Helper()
	return want(e.t, e.do("GET", "/bookings/"+ref, nil, token), http.StatusOK)
}

// paid runs a whole successful checkout and returns it with the payment id.
func (e *env) paid(eventID int64, seats ...int64) (checkout, int64) {
	e.t.Helper()
	c := e.checkout(eventID, seats...)
	p := e.pay(c, mock.Success)
	fired := e.timers.fire(e.t, soon)
	require.Len(e.t, fired, 1)
	require.Equal(e.t, payments.ActionSold, fired[0].Action)
	e.payments.Wait()
	return c, id64(p["id"])
}

// stkCallback builds Daraja's callback envelope.
func stkCallback(checkoutRequestID string, resultCode int, desc string, items ...obj) obj {
	cb := obj{
		"MerchantRequestID": "29115-34620561-1", "CheckoutRequestID": checkoutRequestID,
		"ResultCode": resultCode, "ResultDesc": desc,
	}
	if len(items) > 0 {
		cb["CallbackMetadata"] = obj{"Item": items}
	}
	return obj{"Body": obj{"stkCallback": cb}}
}

// paidItems is the metadata of a success callback: the amount in shillings and a receipt.
func paidItems(shillings any, receipt string) []obj {
	return []obj{
		{"Name": "Amount", "Value": shillings},
		{"Name": "MpesaReceiptNumber", "Value": receipt},
		{"Name": "Balance"},
		{"Name": "TransactionDate", "Value": 20261011121530},
		{"Name": "PhoneNumber", "Value": 254712345678},
	}
}

func newReceipt(t testing.TB) string { return "R" + randDigits(t, 9) }

// webhook posts a callback body to the M-Pesa webhook, requires the fixed 200 answer, and
// waits for the work the handler handed to the background.
func (e *env) webhook(body any, opts ...contracttest.Option) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := e.doReq(e.request("POST", "/webhooks/mpesa/callback", body, ""), opts...)
	require.Equal(e.t, http.StatusOK, rec.Code, rec.Body.String())
	require.JSONEq(e.t, `{"ResultCode":0,"ResultDesc":"Accepted"}`, rec.Body.String())
	e.payments.Wait()
	return rec
}

// ledger runs every invariant, inventory and ledger, and checks the one rule that matters
// most in its plainest form: no more seats are SOLD than the seats that were paid for, and
// never more than exist.
func ledger(t testing.TB, eventID int64) testinvariants.Counts {
	t.Helper()
	c := testinvariants.CheckLedger(t, db.Pool, eventID)
	assert.LessOrEqual(t, c.Sold, c.Total, "more seats sold than exist")
	paidSeats := queryInt(t, `SELECT count(*) FROM booking_items bi JOIN bookings b ON b.id = bi.booking_id WHERE b.event_id = $1 AND b.status = 'PAID'`, eventID)
	assert.Equal(t, paidSeats, c.Sold, "seats SOLD must be exactly the seats of PAID bookings")
	assert.Equal(t, c.Sold, queryInt(t, `SELECT count(DISTINCT event_seat_id) FROM tickets WHERE event_id = $1 AND status IN ('VALID', 'USED')`, eventID),
		"one live ticket per sold seat")
	return c
}

// testinvariantsLedgerOnly is ledger for a CANCELLED event. There the sold seats stay SOLD
// with void tickets and bookings owed a refund, so "sold seats = seats of PAID bookings"
// does not apply; every rule of the checker still does.
func testinvariantsLedgerOnly(t testing.TB, eventID int64) testinvariants.Counts {
	t.Helper()
	c := testinvariants.CheckLedger(t, db.Pool, eventID)
	assert.LessOrEqual(t, c.Sold, c.Total, "more seats sold than exist")
	assert.Zero(t, c.Held, "a cancelled event holds no seat")
	assert.Zero(t, queryInt(t, `SELECT count(*) FROM tickets WHERE event_id = $1 AND status IN ('VALID', 'USED')`, eventID), "a cancelled event has no live ticket")
	return c
}

func seatStatuses(t testing.TB, seats []int64) map[string]int {
	t.Helper()
	out := map[string]int{}
	rows, err := db.Pool.Query(context.Background(), `SELECT status, count(*)::int FROM event_seats WHERE id = ANY($1) GROUP BY status`, seats)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var s string
		var n int
		require.NoError(t, rows.Scan(&s, &n))
		out[s] = n
	}
	require.NoError(t, rows.Err())
	return out
}

func bookingStatus(t testing.TB, bookingID int64) string {
	return queryText(t, `SELECT status FROM bookings WHERE id = $1`, bookingID)
}

func paymentStatus(t testing.TB, paymentID int64) string {
	return queryText(t, `SELECT status FROM payments WHERE id = $1`, paymentID)
}

// agePayment moves a payment's creation into the past, as if it had been waiting that long.
func agePayment(t testing.TB, paymentID int64, by time.Duration) {
	t.Helper()
	exec(t, `UPDATE payments SET created_at = created_at - make_interval(secs => $2) WHERE id = $1`, paymentID, by.Seconds())
}
