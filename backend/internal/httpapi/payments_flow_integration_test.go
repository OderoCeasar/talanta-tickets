//go:build integration

package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/booking"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/config"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi/contracttest"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments/mock"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments/providers"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// countingProvider counts the status queries made to the provider it wraps.
type countingProvider struct {
	payments.Provider
	queries atomic.Int64
}

func (c *countingProvider) Query(ctx context.Context, id string) (payments.PaymentStatus, error) {
	c.queries.Add(1)
	return c.Provider.Query(ctx, id)
}

// flakyProvider is the mock behind a switch: while down, every call fails as an unreachable
// M-Pesa would.
type flakyProvider struct {
	payments.Provider
	down atomic.Bool
}

var errProviderDown = errors.New("test: connection refused")

func (f *flakyProvider) Initiate(ctx context.Context, req payments.PaymentRequest) (payments.PaymentInit, error) {
	if f.down.Load() {
		return payments.PaymentInit{}, errProviderDown
	}
	return f.Provider.Initiate(ctx, req)
}

func (f *flakyProvider) Query(ctx context.Context, id string) (payments.PaymentStatus, error) {
	if f.down.Load() {
		return payments.PaymentStatus{}, errProviderDown
	}
	return f.Provider.Query(ctx, id)
}

// ---------------------------------------------------------------------------
// Bookings over HTTP.
// ---------------------------------------------------------------------------

func TestCreateBooking_OverHTTP(t *testing.T) {
	e := newEnv(t)
	ev, north, south := e.ticketEvent()
	fanID, token := e.newFan()
	h := want(t, e.hold(token, ev.ID, north[0], north[10], south[10]), http.StatusCreated)
	holdID := id64(h["id"])
	key := newKey(t)

	// 201, then the same key again: 200 and the same booking.
	rec := e.book(token, holdID, key)
	b := want(t, rec, http.StatusCreated)
	assert.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	assert.Regexp(t, `^TLT-[A-Z0-9]{6}$`, b["reference"])
	assert.Equal(t, "PENDING_PAYMENT", b["status"])
	assert.Equal(t, "KES", b["currency"])
	assert.EqualValues(t, 700_000, b["subtotal_cents"], "one VIP seat and two Regular")
	assert.EqualValues(t, 0, b["fee_cents"])
	assert.EqualValues(t, 700_000, b["total_cents"])
	assert.Equal(t, h["total_cents"], b["total_cents"], "the booking charges what the hold showed")
	assert.Len(t, list(b, "items"), 3)
	assert.Empty(t, list(b, "tickets"))
	assert.Nil(t, b["latest_payment"])
	again := want(t, e.book(token, holdID, key), http.StatusOK)
	assert.Equal(t, b["id"], again["id"])
	assert.Equal(t, b["reference"], again["reference"])
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM bookings WHERE hold_id = $1`, holdID))

	// A price change after the booking does not reach it.
	exec(t, `UPDATE event_categories SET price_cents = price_cents * 3 WHERE event_id = $1`, ev.ID)
	after := e.getBooking(token, b["reference"].(string))
	assert.EqualValues(t, 700_000, after["total_cents"])
	exec(t, `UPDATE event_categories SET price_cents = price_cents / 3 WHERE event_id = $1`, ev.ID)

	// Another key for the same hold.
	d := holdErr(t, e.book(token, holdID, newKey(t)), http.StatusConflict, domain.CodeHoldAlreadyBooked)
	assert.Equal(t, b["reference"], d["booking_reference"])
	assert.Equal(t, "PENDING_PAYMENT", d["booking_status"])

	// The same key for another hold.
	h2 := want(t, e.hold(token, ev.ID, north[20]), http.StatusCreated)
	holdErr(t, e.book(token, id64(h2["id"]), key), http.StatusUnprocessableEntity, domain.CodeIdempotencyKeyReused)

	// The header: missing, too short, wrong characters.
	noKey := e.request("POST", "/bookings", obj{"hold_id": id64(h2["id"])}, token)
	wantError(t, e.doReq(noKey, contracttest.LenientRequest()), http.StatusBadRequest, domain.CodeIdempotencyKeyRequired)
	for _, bad := range []string{"short", "has spaces in it", "semi;colon;key"} {
		wantError(t, e.doReq(func() *http.Request {
			r := e.request("POST", "/bookings", obj{"hold_id": id64(h2["id"])}, token)
			r.Header.Set("Idempotency-Key", bad)
			return r
		}(), contracttest.LenientRequest()), http.StatusBadRequest, domain.CodeIdempotencyKeyRequired)
	}

	// The body.
	bad := e.request("POST", "/bookings", obj{"hold_id": 0}, token)
	bad.Header.Set("Idempotency-Key", newKey(t))
	wantFields(t, e.doReq(bad, contracttest.LenientRequest()), "hold_id")
	extra := e.request("POST", "/bookings", obj{"hold_id": holdID, "total_cents": 1}, token)
	extra.Header.Set("Idempotency-Key", newKey(t))
	wantFields(t, e.doReq(extra, contracttest.LenientRequest()), "total_cents")

	// Someone else's hold, a hold that does not exist, an expired one, a released one.
	_, stranger := e.newFan()
	wantError(t, e.book(stranger, id64(h2["id"]), newKey(t)), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.book(token, 1<<50, newKey(t)), http.StatusNotFound, domain.CodeNotFound)
	lapseHold(t, id64(h2["id"]))
	d = holdErr(t, e.book(token, id64(h2["id"]), newKey(t)), http.StatusConflict, domain.CodeHoldExpired)
	assert.Equal(t, "EXPIRED", d["hold_status"])
	h3 := want(t, e.hold(token, ev.ID, north[21]), http.StatusCreated)
	want(t, e.do("DELETE", fmt.Sprintf("/holds/%d", id64(h3["id"])), nil, token), http.StatusNoContent)
	holdErr(t, e.book(token, id64(h3["id"]), newKey(t)), http.StatusConflict, domain.CodeHoldNotActive)

	// Who may call it.
	wantError(t, e.book("", holdID, newKey(t)), http.StatusUnauthorized, domain.CodeUnauthenticated)
	wantError(t, e.book(e.staffToken(domain.RoleOrganiser), holdID, newKey(t)), http.StatusForbidden, domain.CodeForbidden)
	_ = fanID
	checkInventory(t, ev.ID)
}

// A double-tapped Pay through the router: the same key on several requests at once makes one
// booking, with one 201 and the rest 200.
func TestCreateBooking_DoubleTapOverHTTP(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	for round := 0; round < 5; round++ {
		_, token := e.newFan()
		h := want(t, e.hold(token, ev.ID, north[10+round*2], north[11+round*2]), http.StatusCreated)
		holdID, key := id64(h["id"]), newKey(t)
		const taps = 6
		codes := make([]int, taps)
		refs := make([]string, taps)
		gate := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < taps; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-gate
				rec := e.book(token, holdID, key)
				codes[i] = rec.Code
				if rec.Code < 300 {
					refs[i] = decode[obj](t, rec)["reference"].(string)
				}
			}()
		}
		close(gate)
		wg.Wait()
		created := 0
		for i, code := range codes {
			require.Contains(t, []int{http.StatusOK, http.StatusCreated}, code, "tap %d", i)
			require.Equal(t, refs[0], refs[i])
			if code == http.StatusCreated {
				created++
			}
		}
		require.Equal(t, 1, created, "round %d", round)
		require.Equal(t, 1, queryInt(t, `SELECT count(*) FROM bookings WHERE hold_id = $1`, holdID))
	}
	checkInventory(t, ev.ID)
}

func TestListMyBookings_PagingAndFilter(t *testing.T) {
	e := newEnv(t)
	ev, north, south := e.ticketEvent()
	fanID, token := e.newFan()
	var refs []string
	for i := 0; i < 3; i++ {
		c := e.checkoutAs(fanID, token, ev.ID, south[10+i])
		// Distinct creation times, oldest first, so "newest first" has one right answer.
		exec(t, `UPDATE bookings SET created_at = now() - make_interval(mins => $2) WHERE id = $1`, c.BookingID, 30-i*10)
		refs = append(refs, c.Ref)
	}
	paid, _ := func() (checkout, int64) {
		c := e.checkoutAs(fanID, token, ev.ID, north[10])
		p := e.pay(c, mock.Success)
		e.timers.fire(t, soon)
		e.payments.Wait()
		return c, id64(p["id"])
	}()
	_, otherToken := e.newFan()

	first := want(t, e.do("GET", "/me/bookings?limit=3", nil, token), http.StatusOK)
	require.Len(t, items(first), 3)
	assert.Equal(t, paid.Ref, items(first)[0].(obj)["reference"], "newest first")
	assert.Len(t, list(items(first)[0].(obj), "tickets"), 1, "a paid booking carries its tickets")
	assert.Equal(t, refs[2], items(first)[1].(obj)["reference"])
	assert.Equal(t, refs[1], items(first)[2].(obj)["reference"])
	require.NotNil(t, first["next_cursor"])
	second := want(t, e.do("GET", "/me/bookings?limit=3&cursor="+first["next_cursor"].(string), nil, token), http.StatusOK)
	require.Len(t, items(second), 1)
	assert.Equal(t, refs[0], items(second)[0].(obj)["reference"])
	assert.Nil(t, second["next_cursor"])

	onlyPaid := want(t, e.do("GET", "/me/bookings?status=PAID", nil, token), http.StatusOK)
	require.Len(t, items(onlyPaid), 1)
	assert.Equal(t, paid.Ref, items(onlyPaid)[0].(obj)["reference"])
	assert.Empty(t, items(want(t, e.do("GET", "/me/bookings?status=REFUNDED", nil, token), http.StatusOK)))
	assert.Empty(t, items(want(t, e.do("GET", "/me/bookings", nil, otherToken), http.StatusOK)), "only the caller's")

	wantFields(t, e.do("GET", "/me/bookings?status=SOLD", nil, token, contracttest.LenientRequest()), "status")
	wantFields(t, e.do("GET", "/me/bookings?limit=0", nil, token, contracttest.LenientRequest()), "limit")
	wantFields(t, e.do("GET", "/me/bookings?cursor=not-a-cursor", nil, token), "cursor")
	wantError(t, e.do("GET", "/me/bookings", nil, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
	wantError(t, e.do("GET", "/me/bookings", nil, e.staffToken(domain.RoleAdmin)), http.StatusForbidden, domain.CodeForbidden)
	ledger(t, ev.ID)
}

// Another fan's booking and payment are 404, exactly like ones that do not exist.
func TestBookingsAndPayments_OwnerOnly(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c, pid := e.paid(ev.ID, north[10:12]...)
	_, stranger := e.newFan()
	staff := e.staffToken(domain.RoleOrganiser)

	want(t, e.do("GET", "/bookings/"+c.Ref, nil, c.Token), http.StatusOK)
	theirs := wantError(t, e.do("GET", "/bookings/"+c.Ref, nil, stranger), http.StatusNotFound, domain.CodeNotFound)
	none := wantError(t, e.do("GET", "/bookings/TLT-ZZZZZ9", nil, stranger), http.StatusNotFound, domain.CodeNotFound)
	assert.Equal(t, none.Error.Message, theirs.Error.Message, "someone else's and none are the same answer")
	wantFields(t, e.do("GET", "/bookings/not-a-reference", nil, c.Token, contracttest.LenientRequest()), "reference")
	wantError(t, e.do("GET", "/bookings/"+c.Ref, nil, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
	wantError(t, e.do("GET", "/bookings/"+c.Ref, nil, staff), http.StatusForbidden, domain.CodeForbidden)

	rec := e.do("GET", fmt.Sprintf("/payments/%d", pid), nil, c.Token)
	want(t, rec, http.StatusOK)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	wantError(t, e.do("GET", fmt.Sprintf("/payments/%d", pid), nil, stranger), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("GET", "/payments/999999999", nil, c.Token), http.StatusNotFound, domain.CodeNotFound)
	wantFields(t, e.do("GET", "/payments/abc", nil, c.Token, contracttest.LenientRequest()), "paymentId")
	wantError(t, e.do("GET", fmt.Sprintf("/payments/%d", pid), nil, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
	wantError(t, e.do("GET", fmt.Sprintf("/payments/%d", pid), nil, staff), http.StatusForbidden, domain.CodeForbidden)

	// Paying for someone else's booking.
	other := e.checkout(ev.ID, north[20])
	wantError(t, e.stk(stranger, other.BookingID, payPhone, mock.Success), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.stk(stranger, 1<<50, payPhone, ""), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.stk("", other.BookingID, payPhone, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
	wantError(t, e.stk(staff, other.BookingID, payPhone, ""), http.StatusForbidden, domain.CodeForbidden)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM payments WHERE booking_id = $1`, other.BookingID))
	ledger(t, ev.ID)
}

// ---------------------------------------------------------------------------
// Starting a payment.
// ---------------------------------------------------------------------------

// Every mock outcome, end to end through the HTTP API.
func TestPayment_EveryMockOutcomeEndToEnd(t *testing.T) {
	cases := []struct {
		outcome                       string
		fire                          time.Duration // 0: reconciliation settles it
		status, reason, bookingStatus string
		resultCode                    any
		tickets                       int
		refund                        bool
	}{
		{mock.Success, soon, "SUCCESS", "", "PAID", 0, 2, false},
		{mock.InsufficientFunds, soon, "FAILED", "insufficient_funds", "PENDING_PAYMENT", 1, 0, false},
		{mock.WrongPIN, soon, "FAILED", "wrong_pin", "PENDING_PAYMENT", 2001, 0, false},
		{mock.UserCancel, soon, "FAILED", "cancelled", "PENDING_PAYMENT", 1032, 0, false},
		{mock.Timeout, soon, "FAILED", "timeout", "PENDING_PAYMENT", 1037, 0, false},
		{mock.NoCallback, 0, "SUCCESS", "", "PAID", 0, 2, false},
		{mock.DuplicateCallback, soon, "SUCCESS", "", "PAID", 0, 2, false},
		{mock.LateCallback, late, "SUCCESS", "", "PAID", 0, 2, false},
		{mock.WrongAmount, soon, "FAILED", "wrong_amount", "REFUND_REQUIRED", 0, 0, true},
	}
	require.Len(t, cases, len(payments.MockOutcomes), "one case per outcome of the contract's enum")
	e := newEnv(t)
	ev, north, south := e.ticketEvent()
	all := append(append([]int64{}, north[10:]...), south[10:]...)
	sold := 0
	for i, tc := range cases {
		t.Run(tc.outcome, func(t *testing.T) {
			c := e.checkout(ev.ID, all[i*2], all[i*2+1])
			p := e.pay(c, tc.outcome)
			pid := id64(p["id"])
			assert.Equal(t, "MPESA", p["provider"])
			assert.EqualValues(t, c.Total, p["amount_cents"])
			assert.Equal(t, c.Ref, p["booking_reference"])
			assert.Equal(t, "PENDING_PAYMENT", p["booking_status"])
			assert.Nil(t, p["failure_reason"])
			assert.Nil(t, p["completed_at"])

			if tc.fire > 0 {
				require.NotEmpty(t, e.timers.fire(t, tc.fire))
			} else {
				agePayment(t, pid, 2*time.Minute)
				e.mockClock.Advance(time.Minute)
				res, err := e.payments.Reconcile(context.Background())
				require.NoError(t, err)
				require.Equal(t, 1, res.Sold)
			}
			e.payments.Wait()

			got := e.getPayment(c.Token, pid)
			assert.Equal(t, tc.status, got["status"])
			if tc.reason == "" {
				assert.Nil(t, got["failure_reason"])
			} else {
				assert.Equal(t, tc.reason, got["failure_reason"])
			}
			assert.Equal(t, tc.bookingStatus, got["booking_status"])
			assert.EqualValues(t, tc.resultCode, got["result_code"])
			assert.NotEmpty(t, got["result_desc"])
			assert.NotNil(t, got["completed_at"])
			b := e.getBooking(c.Token, c.Ref)
			assert.Equal(t, tc.bookingStatus, b["status"])
			assert.Len(t, list(b, "tickets"), tc.tickets)
			assert.Equal(t, tc.refund, queryInt(t, `SELECT count(*) FROM refunds WHERE payment_id = $1`, pid) == 1)
			assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM payments WHERE id = $1 AND (raw_callback IS NOT NULL) = $2`, pid, tc.fire > 0),
				"the raw callback is stored whenever one arrived")
			sold += tc.tickets
		})
	}
	assert.Equal(t, sold, ledger(t, ev.ID).Sold)
}

// Without a header the mock succeeds, unless the phone is one of the magic numbers.
func TestPayment_MagicPhoneNumbers(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10])
	p := want(t, e.stk(c.Token, c.BookingID, "+254700000102", ""), http.StatusAccepted)
	e.timers.fire(t, soon)
	assert.Equal(t, "wrong_pin", e.getPayment(c.Token, id64(p["id"]))["failure_reason"])

	// The Kenyan local spelling is accepted and stored as E.164.
	local := e.request("POST", "/payments/mpesa/stk", obj{"booking_id": c.BookingID, "phone": "0712 345 678"}, c.Token)
	p = want(t, e.doReq(local, contracttest.LenientRequest()), http.StatusAccepted)
	assert.Equal(t, "+254712345678", p["phone"])
	e.timers.fire(t, soon)
	assert.Equal(t, "SUCCESS", e.getPayment(c.Token, id64(p["id"]))["status"])
	assert.Equal(t, 1, ledger(t, ev.ID).Sold)
}

func TestPayment_StartValidation(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10])
	for _, phone := range []string{"", "12345", "+15551234567", "+255712345678", "+2540712345678"} {
		rec := e.doReq(e.request("POST", "/payments/mpesa/stk", obj{"booking_id": c.BookingID, "phone": phone}, c.Token), contracttest.LenientRequest())
		wantFields(t, rec, "phone")
	}
	wantFields(t, e.doReq(e.request("POST", "/payments/mpesa/stk", obj{"phone": payPhone}, c.Token), contracttest.LenientRequest()), "booking_id")
	wantFields(t, e.doReq(e.request("POST", "/payments/mpesa/stk", obj{"booking_id": c.BookingID, "phone": payPhone, "amount_cents": 1}, c.Token),
		contracttest.LenientRequest()), "amount_cents")
	// An outcome that is not one of the nine.
	req := e.request("POST", "/payments/mpesa/stk", obj{"booking_id": c.BookingID, "phone": payPhone}, c.Token)
	req.Header.Set("X-Mock-Outcome", "free_tickets")
	wantFields(t, e.doReq(req, contracttest.LenientRequest()), "X-Mock-Outcome")
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM payments WHERE booking_id = $1`, c.BookingID))
	ledger(t, ev.ID)
}

// A second live payment for the same booking is refused, and says which one is in the way.
func TestPayment_SecondLivePaymentIsRefused(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	first := e.pay(c, mock.Success)

	d := holdErr(t, e.stk(c.Token, c.BookingID, payPhone, mock.Success), http.StatusConflict, domain.CodePaymentInProgress)
	assert.Equal(t, first["id"], d["payment_id"])
	assert.Equal(t, "PENDING", d["payment_status"])
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM payments WHERE booking_id = $1`, c.BookingID))
	assert.Equal(t, 1, e.timers.waiting(), "one prompt went out, not two")

	// Several taps of Pay at once: one payment.
	c2 := e.checkout(ev.ID, north[20:22]...)
	const taps = 6
	codes := make([]int, taps)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := 0; i < taps; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			codes[i] = e.stk(c2.Token, c2.BookingID, payPhone, mock.Success).Code
		}()
	}
	close(gate)
	wg.Wait()
	accepted := 0
	for _, code := range codes {
		require.Contains(t, []int{http.StatusAccepted, http.StatusConflict}, code)
		if code == http.StatusAccepted {
			accepted++
		}
	}
	assert.Equal(t, 1, accepted)
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM payments WHERE booking_id = $1`, c2.BookingID))

	e.timers.fire(t, soon)
	// Once it is paid, it is not payable.
	d = holdErr(t, e.stk(c.Token, c.BookingID, payPhone, mock.Success), http.StatusConflict, domain.CodeBookingNotPayable)
	assert.Equal(t, "PAID", d["booking_status"])
	assert.Equal(t, c.Ref, d["booking_reference"])
	assert.Equal(t, 4, ledger(t, ev.ID).Sold)
}

// After a failed payment the fan may try again while the hold lives, and the retry succeeds.
func TestPayment_RetryAfterAFailedPaymentSucceeds(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:14]...)
	// A hold with a minute left: the first payment uses the one extension.
	exec(t, `UPDATE holds SET expires_at = now() + interval '60 seconds' WHERE id = $1`, c.HoldID)
	exec(t, `UPDATE event_seats SET hold_expires_at = (SELECT expires_at FROM holds WHERE id = $1) WHERE hold_id = $1`, c.HoldID)

	first := id64(e.pay(c, mock.WrongPIN)["id"])
	e.timers.fire(t, soon)
	got := e.getPayment(c.Token, first)
	assert.Equal(t, "FAILED", got["status"])
	assert.Equal(t, "wrong_pin", got["failure_reason"])
	assert.Equal(t, "PENDING_PAYMENT", got["booking_status"])
	assert.Equal(t, map[string]int{"HELD": 4}, seatStatuses(t, c.Seats), "the seats are still the fan's")

	// The retry: the hold is already extended and is not extended again, and that is fine.
	second := id64(e.pay(c, mock.Success)["id"])
	require.NotEqual(t, first, second)
	e.timers.fire(t, soon)
	e.payments.Wait()
	b := e.getBooking(c.Token, c.Ref)
	assert.Equal(t, "PAID", b["status"])
	assert.Len(t, list(b, "tickets"), 4)
	assert.EqualValues(t, second, b["latest_payment"].(obj)["id"])
	assert.Equal(t, "FAILED", paymentStatus(t, first), "the failed attempt stays as it was")
	assert.Equal(t, 4, ledger(t, ev.ID).Sold)
}

// A payment is not started when the hold has too little time left even after the extension:
// the extension happens once, so a late retry can find a hold that is nearly over.
func TestPayment_RefusedWhenTheHoldHasTooLittleTimeLeft(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	setHold := func(seconds int) {
		exec(t, `UPDATE holds SET expires_at = now() + make_interval(secs => $2) WHERE id = $1`, c.HoldID, seconds)
		exec(t, `UPDATE event_seats SET hold_expires_at = (SELECT expires_at FROM holds WHERE id = $1) WHERE hold_id = $1`, c.HoldID)
	}

	// Never extended, 30 seconds left: extended to six minutes, and the payment starts.
	setHold(30)
	first := id64(e.pay(c, mock.UserCancel)["id"])
	e.timers.fire(t, soon)
	require.Equal(t, "FAILED", paymentStatus(t, first))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM holds WHERE id = $1 AND extended_at IS NOT NULL AND expires_at > now() + interval '5 minutes'`, c.HoldID))

	// Already extended, 90 seconds left: no second extension, and 90 seconds is not enough.
	setHold(90)
	d := holdErr(t, e.stk(c.Token, c.BookingID, payPhone, mock.Success), http.StatusConflict, domain.CodeHoldExpired)
	assert.Equal(t, "ACTIVE", d["hold_status"], "the hold is still live; it is only too short to pay on")
	assert.EqualValues(t, c.HoldID, d["hold_id"])
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM payments WHERE booking_id = $1`, c.BookingID), "no payment row was written")
	assert.Zero(t, e.timers.waiting(), "no prompt went out")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM holds WHERE id = $1 AND expires_at < now() + interval '2 minutes'`, c.HoldID), "the hold was not extended a second time")

	// Three minutes left is enough.
	setHold(180)
	e.pay(c, mock.Success)
	e.timers.fire(t, soon)
	assert.Equal(t, "PAID", bookingStatus(t, c.BookingID))

	// A hold that has run out, with the booking still open (no sweeper yet).
	c2 := e.checkout(ev.ID, north[20])
	lapseHold(t, c2.HoldID)
	d = holdErr(t, e.stk(c2.Token, c2.BookingID, payPhone, mock.Success), http.StatusConflict, domain.CodeHoldExpired)
	assert.Equal(t, "EXPIRED", d["hold_status"])
	// ...and once the sweeper has closed both, the booking says why it cannot be paid.
	_, err := e.holds.Sweep(context.Background())
	require.NoError(t, err)
	d = holdErr(t, e.stk(c2.Token, c2.BookingID, payPhone, mock.Success), http.StatusConflict, domain.CodeBookingNotPayable)
	assert.Equal(t, "EXPIRED", d["booking_status"])
	assert.Equal(t, 2, ledger(t, ev.ID).Sold)
}

// The provider is down: 502 PAYMENT_PROVIDER_UNAVAILABLE, no payment is left live, and the
// booking is still payable as soon as the provider is back.
func TestPayment_ProviderDown_502AndTheBookingStaysPayable(t *testing.T) {
	var flaky *flakyProvider
	e := newEnv(t, withProvider(func(m *mock.Provider) payments.Provider { flaky = &flakyProvider{Provider: m}; return flaky }))
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)

	flaky.down.Store(true)
	wantError(t, e.stk(c.Token, c.BookingID, payPhone, mock.Success), http.StatusBadGateway, domain.CodePaymentProviderUnavailable)
	b := e.getBooking(c.Token, c.Ref)
	assert.Equal(t, "PENDING_PAYMENT", b["status"])
	lp := b["latest_payment"].(obj)
	assert.Equal(t, "FAILED", lp["status"], "the attempt is recorded, and is not live")
	assert.Equal(t, "other", lp["failure_reason"])
	assert.Nil(t, lp["checkout_request_id"])
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM payments WHERE booking_id = $1 AND status IN ('INITIATED', 'PENDING')`, c.BookingID))
	assert.NotContains(t, e.do("GET", "/bookings/"+c.Ref, nil, c.Token).Body.String(), "connection refused", "the provider's error text stays in the log")

	// Twice more, to show a failed start never blocks the next one.
	wantError(t, e.stk(c.Token, c.BookingID, payPhone, mock.Success), http.StatusBadGateway, domain.CodePaymentProviderUnavailable)
	flaky.down.Store(false)
	e.pay(c, mock.Success)
	e.timers.fire(t, soon)
	assert.Equal(t, "PAID", bookingStatus(t, c.BookingID))
	assert.Equal(t, 2, ledger(t, ev.ID).Sold)
}

// M-Pesa takes whole shillings. A booking whose total is not a whole number of them is
// refused, not rounded.
func TestPayment_TotalThatIsNotWholeShillingsIsRefused(t *testing.T) {
	e := newEnv(t, withHoldPolicy(func(p *booking.Policy) { p.BookingFeeBPS = 133 }))
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	require.EqualValues(t, 202_660, c.Total, "1.33% of 2,000.00 is 26.60")

	d := holdErr(t, e.stk(c.Token, c.BookingID, payPhone, mock.Success), http.StatusConflict, domain.CodeBookingNotPayable)
	assert.Equal(t, "PENDING_PAYMENT", d["booking_status"])
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM payments WHERE booking_id = $1`, c.BookingID))
	assert.Zero(t, e.timers.waiting())

	// Both providers refuse such an amount themselves as well.
	_, err := e.mock.Initiate(context.Background(), payments.PaymentRequest{AmountCents: 202_660, Phone: payPhone})
	require.ErrorIs(t, err, payments.ErrAmountNotWholeShillings)
	ledger(t, ev.ID)
}

// Rules that never bend: the mock payment header is refused in production, and the mock
// provider cannot be selected there at all.
func TestPayment_MockIsRefusedInProduction(t *testing.T) {
	e := newEnv(t, production())
	ev, north, _ := e.ticketEvent()
	// The fan signs in by OTP in production; here a token is minted directly.
	c := e.checkout(ev.ID, north[10:12]...)

	for _, outcome := range []string{mock.Success, mock.WrongPIN, "", "anything"} {
		req := e.request("POST", "/payments/mpesa/stk", obj{"booking_id": c.BookingID, "phone": payPhone}, c.Token)
		req.Header.Set("X-Mock-Outcome", outcome)
		wantError(t, e.doReq(req, contracttest.LenientRequest()), http.StatusForbidden, domain.CodeMockNotAllowed)
	}
	// Refused before the body is even read.
	req := e.request("POST", "/payments/mpesa/stk", "{not json", c.Token)
	req.Header.Set("X-Mock-Outcome", mock.Success)
	wantError(t, e.doReq(req, contracttest.LenientRequest()), http.StatusForbidden, domain.CodeMockNotAllowed)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM payments WHERE booking_id = $1`, c.BookingID), "nothing was started")
	assert.Zero(t, e.timers.waiting())

	// The provider itself refuses to exist in production...
	_, err := mock.New(mock.Config{Allowed: false})
	require.ErrorIs(t, err, mock.ErrNotAllowed)
	for _, env := range []string{config.EnvProduction, "", "staging"} {
		_, _, err := providers.New(config.Payments{Provider: config.ProviderMock}, config.Base{AppEnv: env}, e.logger)
		require.ErrorIs(t, err, mock.ErrNotAllowed, "APP_ENV=%q", env)
	}
	// ...the configuration refuses to select it there...
	t.Setenv("DATABASE_URL", db.URL)
	t.Setenv("APP_ENV", "production")
	productionPaymentEnv(t)
	t.Setenv("PAYMENT_PROVIDER", "mock")
	_, err = config.LoadWorker()
	require.ErrorContains(t, err, "PAYMENT_PROVIDER=mock is refused")
	// ...and an M-Pesa that is not fully configured is an error, never the mock.
	t.Setenv("PAYMENT_PROVIDER", "mpesa")
	t.Setenv("MPESA_PASSKEY", "")
	_, err = config.LoadWorker()
	require.ErrorContains(t, err, "PAYMENT_PROVIDER=mpesa needs MPESA_PASSKEY")
	_, _, err = providers.New(config.Payments{Provider: config.ProviderMpesa, Mpesa: config.Mpesa{Env: "sandbox", ConsumerKey: "k"}}, config.Base{AppEnv: config.EnvProduction}, e.logger)
	require.ErrorContains(t, err, "not configured")

	// In development the same providers.New gives the mock, and the real one when asked.
	p, m, err := providers.New(config.Payments{Provider: config.ProviderMock}, config.Base{AppEnv: config.EnvDevelopment}, e.logger)
	require.NoError(t, err)
	assert.NotNil(t, m)
	assert.NotNil(t, p)
	ledger(t, ev.ID)
}

// The two writes of checkout are rate limited per user.
func TestCheckout_RateLimitedPerUser(t *testing.T) {
	e := newEnv(t, withLimits(func(l *httpapi.RateLimits) { l.CheckoutPerUser = ratelimit.Rule{Limit: 2, Window: time.Minute} }))
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10]) // one of the two
	e.pay(c, mock.Success)            // the other
	rec := e.stk(c.Token, c.BookingID, payPhone, mock.Success)
	wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	wantError(t, e.book(c.Token, c.HoldID, newKey(t)), http.StatusTooManyRequests, domain.CodeRateLimited)
	// Reading is not limited: the fan can always see where the payment stands.
	want(t, e.do("GET", "/bookings/"+c.Ref, nil, c.Token), http.StatusOK)
	e.timers.fire(t, soon)
	ledger(t, ev.ID)
}

// ---------------------------------------------------------------------------
// Payments against release, the sweeper, cancel and each other.
// ---------------------------------------------------------------------------

// A hold cannot be released while a payment for it is live.
func TestRelease_DuringALivePaymentIsRefused(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	p := e.pay(c, mock.InsufficientFunds)

	d := holdErr(t, e.do("DELETE", fmt.Sprintf("/holds/%d", c.HoldID), nil, c.Token), http.StatusConflict, domain.CodePaymentInProgress)
	assert.Equal(t, p["id"], d["payment_id"])
	assert.Equal(t, map[string]int{"HELD": 2}, seatStatuses(t, c.Seats))
	assert.Equal(t, "PENDING_PAYMENT", bookingStatus(t, c.BookingID))

	// Starting a payment and releasing at once, many times: never both.
	for round := 0; round < 10; round++ {
		c2 := e.checkout(ev.ID, north[20+round])
		var payCode, releaseCode int
		var wg sync.WaitGroup
		gate := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-gate
			payCode = e.stk(c2.Token, c2.BookingID, payPhone, mock.NoCallback).Code
		}()
		go func() {
			defer wg.Done()
			<-gate
			releaseCode = e.do("DELETE", fmt.Sprintf("/holds/%d", c2.HoldID), nil, c2.Token).Code
		}()
		close(gate)
		wg.Wait()
		switch {
		case payCode == http.StatusAccepted:
			require.Equal(t, http.StatusConflict, releaseCode, "round %d: the payment started, so the release must be refused", round)
			require.Equal(t, "ACTIVE", queryText(t, `SELECT status FROM holds WHERE id = $1`, c2.HoldID))
		case releaseCode == http.StatusNoContent:
			require.Equal(t, http.StatusConflict, payCode, "round %d: the hold was released, so no payment may start", round)
			require.Equal(t, 0, queryInt(t, `SELECT count(*) FROM payments WHERE booking_id = $1`, c2.BookingID))
			require.Equal(t, "CANCELLED", bookingStatus(t, c2.BookingID))
		default:
			t.Fatalf("round %d: pay answered %d and release %d", round, payCode, releaseCode)
		}
	}

	// When the payment has failed the hold can be released, and the booking goes with it.
	e.timers.fire(t, soon)
	want(t, e.do("DELETE", fmt.Sprintf("/holds/%d", c.HoldID), nil, c.Token), http.StatusNoContent)
	assert.Equal(t, "CANCELLED", bookingStatus(t, c.BookingID))
	assert.Equal(t, map[string]int{"AVAILABLE": 2}, seatStatuses(t, c.Seats))
	d = holdErr(t, e.stk(c.Token, c.BookingID, payPhone, mock.Success), http.StatusConflict, domain.CodeBookingNotPayable)
	assert.Equal(t, "CANCELLED", d["booking_status"])
	ledger(t, ev.ID)
}

// The sweeper and a live payment. The booking is not expired while its payment is in
// flight; when the payment then fails and the hold is gone, the booking is closed.
func TestSweeper_DoesNotExpireABookingUnderALivePayment(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	pid := id64(e.pay(c, mock.WrongPIN)["id"])
	lapseHold(t, c.HoldID)

	res, err := e.holds.Sweep(context.Background())
	require.NoError(t, err)
	assert.GreaterOrEqual(t, res.Holds, 1)
	assert.Equal(t, "EXPIRED", queryText(t, `SELECT status FROM holds WHERE id = $1`, c.HoldID))
	assert.Equal(t, map[string]int{"AVAILABLE": 2}, seatStatuses(t, c.Seats), "the seats are back on sale")
	assert.Equal(t, "PENDING_PAYMENT", bookingStatus(t, c.BookingID), "not expired under a live payment")
	assert.Equal(t, "PENDING", paymentStatus(t, pid))

	// The payment fails. The hold is gone, so there is nothing to retry on: the booking closes.
	fired := e.timers.fire(t, soon)
	require.Len(t, fired, 1)
	assert.Equal(t, payments.ActionFailed, fired[0].Action)
	got := e.getPayment(c.Token, pid)
	assert.Equal(t, "FAILED", got["status"])
	assert.Equal(t, "EXPIRED", got["booking_status"])
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refunds WHERE booking_id = $1`, c.BookingID))

	// Had it succeeded instead: money and no seats, so a refund.
	c2 := e.checkout(ev.ID, north[20:22]...)
	pid2 := id64(e.pay(c2, mock.Success)["id"])
	lapseHold(t, c2.HoldID)
	_, err = e.holds.Sweep(context.Background())
	require.NoError(t, err)
	fired = e.timers.fire(t, soon)
	require.Len(t, fired, 1)
	assert.Equal(t, payments.ActionRefund, fired[0].Action)
	assert.Equal(t, "SUCCESS", paymentStatus(t, pid2))
	assert.Equal(t, "REFUND_REQUIRED", bookingStatus(t, c2.BookingID))
	assert.Equal(t, map[string]int{"AVAILABLE": 2}, seatStatuses(t, c2.Seats), "seats that went back on sale are not taken back")
	assert.Zero(t, ledger(t, ev.ID).Sold)
}

// Correction 5, the other half: a hold that has run out but still has every seat (nobody
// took one, no sweeper came) IS sold when its payment succeeds. The hold's stored status is
// what counts, not the clock.
func TestPayment_SuccessOnALapsedHoldThatStillHasEverySeat_IsSold(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:13]...)
	pid := id64(e.pay(c, mock.Success)["id"])
	lapseHold(t, c.HoldID)
	require.Equal(t, "EXPIRED", want(t, e.do("GET", fmt.Sprintf("/holds/%d", c.HoldID), nil, c.Token), http.StatusOK)["status"], "the hold reads as expired")

	fired := e.timers.fire(t, soon)
	require.Len(t, fired, 1)
	assert.Equal(t, payments.ActionSold, fired[0].Action)
	e.payments.Wait()
	assert.Equal(t, "SUCCESS", paymentStatus(t, pid))
	assert.Equal(t, "PAID", bookingStatus(t, c.BookingID))
	assert.Equal(t, "CONVERTED", queryText(t, `SELECT status FROM holds WHERE id = $1`, c.HoldID))
	assert.Equal(t, 3, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1 AND status = 'VALID'`, c.BookingID))
	assert.Equal(t, 3, ledger(t, ev.ID).Sold)
}

// A late success for a booking that was EXPIRED by the sweeper, whose seats nobody else has
// taken. The seats are free, but the hold is gone, and seats that went back on sale are not
// taken back: the payment is recorded and refunded. (A TIMEOUT payment is still corrected by
// M-Pesa's late answer.)
func TestPayment_LateSuccessForAnExpiredBookingWithFreeSeats_IsRefunded(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	pid := id64(e.pay(c, mock.LateCallback)["id"])

	// Five minutes with no answer: TIMEOUT. Then the hold runs out and is swept.
	agePayment(t, pid, 6*time.Minute)
	res, err := e.payments.Reconcile(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, res.TimedOut)
	lapseHold(t, c.HoldID)
	_, err = e.holds.Sweep(context.Background())
	require.NoError(t, err)
	require.Equal(t, "EXPIRED", bookingStatus(t, c.BookingID), "no live payment any more, so the sweeper closed the booking")
	require.Equal(t, map[string]int{"AVAILABLE": 2}, seatStatuses(t, c.Seats))

	// M-Pesa's late answer: the fan did pay.
	fired := e.timers.fire(t, late)
	require.Len(t, fired, 1)
	assert.Equal(t, payments.ActionRefund, fired[0].Action)
	got := e.getPayment(c.Token, pid)
	assert.Equal(t, "SUCCESS", got["status"], "TIMEOUT was our verdict; M-Pesa's overrules it")
	assert.Nil(t, got["failure_reason"])
	assert.Equal(t, "REFUND_REQUIRED", got["booking_status"], "EXPIRED -> REFUND_REQUIRED")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM refunds WHERE payment_id = $1 AND amount_cents = $2`, pid, c.Total))
	assert.Equal(t, map[string]int{"AVAILABLE": 2}, seatStatuses(t, c.Seats))
	assert.Zero(t, ledger(t, ev.ID).Sold)
}

// A success after the event was cancelled. Cancel does not look at live payments: it frees
// the seats and cancels the booking. The money that then arrives is recorded and refunded;
// no seat of a cancelled event is sold and no ticket issued.
func TestPayment_SuccessAfterTheEventWasCancelled_IsRefunded(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	paidBefore, _ := e.paid(ev.ID, north[20:22]...)
	c := e.checkout(ev.ID, north[10:12]...)
	pid := id64(e.pay(c, mock.Success)["id"])

	cancelled := want(t, e.do("POST", fmt.Sprintf("/admin/events/%d/cancel", ev.ID), nil, ev.Token), http.StatusOK)
	assert.EqualValues(t, 1, cancelled["refunds_queued"], "the booking that was already paid")
	require.Equal(t, "CANCELLED", bookingStatus(t, c.BookingID))
	require.Equal(t, "REFUND_REQUIRED", bookingStatus(t, paidBefore.BookingID))
	require.Equal(t, "PENDING", paymentStatus(t, pid), "cancel leaves the live payment alone")

	fired := e.timers.fire(t, soon)
	require.Len(t, fired, 1)
	assert.Equal(t, payments.ActionRefund, fired[0].Action)
	assert.Equal(t, "Paid after the event was cancelled", fired[0].Reason)
	got := e.getPayment(c.Token, pid)
	assert.Equal(t, "SUCCESS", got["status"])
	assert.Equal(t, "REFUND_REQUIRED", got["booking_status"], "CANCELLED -> REFUND_REQUIRED")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM refunds WHERE payment_id = $1 AND amount_cents = $2`, pid, c.Total))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE booking_id = $1 AND kind = 'REFUND_REQUIRED'`, c.BookingID))
	assert.Equal(t, map[string]int{"AVAILABLE": 2}, seatStatuses(t, c.Seats), "no seat of a cancelled event is sold")
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, c.BookingID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM tickets WHERE event_id = $1 AND status IN ('VALID', 'USED')`, ev.ID))
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM refunds r JOIN bookings b ON b.id = r.booking_id WHERE b.event_id = $1`, ev.ID),
		"one refund per payment that took money: never two for one payment")

	// A failure arriving after a cancel leaves the booking cancelled.
	testinvariantsLedgerOnly(t, ev.ID)
}

// A second SUCCESS for a booking that is already PAID: the fan paid twice (the first prompt
// timed out on our side, the fan paid a second one, then M-Pesa reported the first as paid
// after all). The second payment to arrive is recorded and refunded; the booking stays PAID
// and its tickets are not touched.
func TestPayment_SecondSuccessForAPaidBooking_IsRefundedAndTicketsKept(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)

	first := e.pay(c, mock.NoCallback)
	firstID := id64(first["id"])
	agePayment(t, firstID, 6*time.Minute)
	res, err := e.payments.Reconcile(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, res.TimedOut)
	require.Equal(t, "TIMEOUT", paymentStatus(t, firstID))

	second := id64(e.pay(c, mock.Success)["id"])
	e.timers.fire(t, soon)
	e.payments.Wait()
	require.Equal(t, "PAID", bookingStatus(t, c.BookingID))
	ticketsBefore := queryText(t, `SELECT string_agg(id::text || ':' || status, ',' ORDER BY id) FROM tickets WHERE booking_id = $1`, c.BookingID)

	// The first payment's callback, late.
	amount := c.Total
	fin, err := e.payments.HandleCallback(context.Background(), payments.Callback{
		CheckoutRequestID: first["checkout_request_id"].(string), ResultCode: 0, ResultDesc: "ok",
		AmountCents: &amount, Receipt: newReceipt(t), Raw: []byte(`{"Body":{"stkCallback":{}}}`),
	})
	require.NoError(t, err)
	assert.Equal(t, payments.ActionRefund, fin.Action)
	assert.Contains(t, fin.Reason, "Paid twice")
	e.payments.Wait()

	assert.Equal(t, "SUCCESS", paymentStatus(t, firstID))
	assert.Equal(t, "SUCCESS", paymentStatus(t, second))
	assert.Equal(t, "PAID", bookingStatus(t, c.BookingID), "the booking keeps its seats")
	assert.Equal(t, ticketsBefore, queryText(t, `SELECT string_agg(id::text || ':' || status, ',' ORDER BY id) FROM tickets WHERE booking_id = $1`, c.BookingID),
		"the tickets are exactly as they were")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM refunds WHERE payment_id = $1 AND amount_cents = $2 AND status = 'PENDING'`, firstID, c.Total))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refunds WHERE payment_id = $1`, second))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE booking_id = $1 AND kind = 'REFUND_REQUIRED'`, c.BookingID))
	assert.Equal(t, 1, e.sms.count(c.Phone), "no second SMS")

	// Completing that refund leaves the booking PAID with valid tickets.
	organiser := e.staffToken(domain.RoleOrganiser)
	refunds := want(t, e.do("GET", fmt.Sprintf("/admin/refunds?event_id=%d&status=PENDING", ev.ID), nil, organiser), http.StatusOK)
	require.Len(t, items(refunds), 1)
	done := want(t, e.do("POST", fmt.Sprintf("/admin/refunds/%d/complete", id64(items(refunds)[0].(obj)["id"])), nil, organiser), http.StatusOK)
	assert.Equal(t, "DONE", done["status"])
	assert.Equal(t, "PAID", bookingStatus(t, c.BookingID))
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1 AND status = 'VALID'`, c.BookingID))
	assert.Equal(t, 2, ledger(t, ev.ID).Sold)
}

// The webhook answers 200 at once even when the payment's event is locked by an organiser
// and the work cannot be done: the payment is left for reconciliation, which finishes it.
func TestWebhook_Answers200AtOnceEvenWhenTheEventIsBusy(t *testing.T) {
	e := newEnv(t, withPaymentPolicy(func(p *payments.Policy) { p.EventLockWait = 300 * time.Millisecond }))
	ev, north, _ := e.ticketEvent()
	c := e.checkout(ev.ID, north[10:12]...)
	p := e.pay(c, mock.NoCallback)
	pid := id64(p["id"])
	body := stkCallback(p["checkout_request_id"].(string), 0, "ok", paidItems(c.Total/100, newReceipt(t))...)

	// An organiser's edit (or a cancel, or a publish) has the event.
	ctx := context.Background()
	organiser, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = organiser.Rollback(ctx) }()
	_, err = store.New(organiser).LockEvent(ctx, ev.ID)
	require.NoError(t, err)

	started := time.Now()
	rec := e.doReq(e.request("POST", "/webhooks/mpesa/callback", body, ""))
	answered := time.Since(started)
	require.Equal(t, http.StatusOK, rec.Code)
	require.JSONEq(t, `{"ResultCode":0,"ResultDesc":"Accepted"}`, rec.Body.String())
	assert.Less(t, answered, 250*time.Millisecond, "the answer does not wait for the event's lock (the work waits 300 ms for it)")

	e.payments.Wait()
	assert.Equal(t, "PENDING", paymentStatus(t, pid), "nothing was applied while the event was locked")
	assert.Contains(t, e.logs.String(), "reconciliation will finish the payment")
	require.NoError(t, organiser.Rollback(ctx))

	// Reconciliation picks it up.
	agePayment(t, pid, 2*time.Minute)
	e.mockClock.Advance(time.Minute)
	res, err := e.payments.Reconcile(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Sold)
	assert.Equal(t, "PAID", bookingStatus(t, c.BookingID))

	// Fast on the ordinary path too, and for every kind of body.
	for _, b := range []any{body, `{"Body":{"stkCallback":{"CheckoutRequestID":"ws_CO_unknown","ResultCode":1032,"ResultDesc":"cancelled","MerchantRequestID":"m"}}}`, `garbage`} {
		started := time.Now()
		rec := e.doReq(e.request("POST", "/webhooks/mpesa/callback", b, ""), contracttest.LenientRequest())
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Less(t, time.Since(started), 250*time.Millisecond)
	}
	// A stale Authorization header does not turn the webhook into a 401.
	rec = e.doReq(e.request("POST", "/webhooks/mpesa/callback", body, "not-a-token"))
	require.Equal(t, http.StatusOK, rec.Code)
	e.payments.Wait()
	assert.Equal(t, 2, ledger(t, ev.ID).Sold)
}
