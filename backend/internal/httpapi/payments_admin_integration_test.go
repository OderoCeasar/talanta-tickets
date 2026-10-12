//go:build integration

package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/events"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi/contracttest"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments/mock"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

func (e *env) sales(ev tEvent) obj {
	e.t.Helper()
	return want(e.t, e.do("GET", fmt.Sprintf("/admin/events/%d/sales", ev.ID), nil, ev.Token), http.StatusOK)
}

// The sales summary reflects a real booking, made and paid through the API from start to
// finish (the earlier sales tests wrote their bookings straight into the tables).
func TestSales_ReflectARealBookingEndToEnd(t *testing.T) {
	e := newEnv(t, withEventPolicy(func(p *events.Policy) { p.CountsTTL = time.Nanosecond; p.ListCountsTTL = time.Nanosecond }))
	ev, north, south := e.ticketEvent()
	total := 60

	before := e.sales(ev)
	assert.EqualValues(t, 0, before["revenue_cents"])
	assert.EqualValues(t, total, before["seats"].(obj)["available"])

	// One VIP and two Regular seats, held: counted as held, no revenue yet.
	c := e.checkout(ev.ID, north[0], north[10], south[10])
	held := e.sales(ev)
	assert.EqualValues(t, 0, held["revenue_cents"], "a booking awaiting payment is not revenue")
	assert.EqualValues(t, 3, held["seats"].(obj)["held"])

	e.pay(c, mock.Success)
	e.timers.fire(t, soon)
	e.payments.Wait()

	s := e.sales(ev)
	assert.EqualValues(t, 700_000, s["revenue_cents"])
	seats := s["seats"].(obj)
	assert.EqualValues(t, 3, seats["sold"])
	assert.EqualValues(t, 0, seats["held"])
	assert.EqualValues(t, total-3, seats["available"])
	assert.EqualValues(t, total, seats["total"])
	assert.EqualValues(t, 0, s["refund_required_count"])
	assert.EqualValues(t, 0, s["refund_required_cents"])
	assert.EqualValues(t, 0, s["open_alert_count"])
	byName := map[string]obj{}
	for _, cat := range list(s, "categories") {
		byName[cat.(obj)["name"].(string)] = cat.(obj)
	}
	assert.EqualValues(t, 500_000, byName["VIP"]["revenue_cents"])
	assert.EqualValues(t, 1, byName["VIP"]["sold"])
	assert.EqualValues(t, 200_000, byName["Regular"]["revenue_cents"])
	assert.EqualValues(t, 2, byName["Regular"]["sold"])
	var bookings, tickets, revenue float64
	for _, p := range list(s["series"].(obj), "points") {
		bookings += p.(obj)["bookings"].(float64)
		tickets += p.(obj)["tickets"].(float64)
		revenue += p.(obj)["revenue_cents"].(float64)
	}
	assert.EqualValues(t, 1, bookings)
	assert.EqualValues(t, 3, tickets)
	assert.EqualValues(t, 700_000, revenue)

	// The organiser's booking list shows it with its payment.
	listed := want(t, e.do("GET", fmt.Sprintf("/admin/events/%d/bookings?status=PAID", ev.ID), nil, ev.Token), http.StatusOK)
	require.Len(t, items(listed), 1)
	row := items(listed)[0].(obj)
	assert.Equal(t, c.Ref, row["reference"])
	assert.EqualValues(t, 3, row["seat_count"])
	assert.Equal(t, "SUCCESS", row["latest_payment"].(obj)["status"])
	assert.Nil(t, row["refund"])

	// The public event counts agree.
	public := want(t, e.do("GET", fmt.Sprintf("/events/%d", ev.ID), nil, ""), http.StatusOK)
	assert.EqualValues(t, total-3, public["seats_available"])
	assert.Equal(t, 3, ledger(t, ev.ID).Sold)
}

// Alerts and refunds: what the organiser sees after payments that could not buy their seats,
// and what completing a refund does.
func TestAdmin_AlertsAndRefunds(t *testing.T) {
	e := newEnv(t, withEventPolicy(func(p *events.Policy) { p.CountsTTL = time.Nanosecond }))
	ev, north, south := e.ticketEvent()
	organiser := e.staffToken(domain.RoleOrganiser)
	admin := e.staffToken(domain.RoleAdmin)

	// 1. A late payment whose seats were resold (REFUND_REQUIRED alert).
	a := e.checkout(ev.ID, north[10:12]...)
	pa := id64(e.pay(a, mock.LateCallback)["id"])
	lapseHold(t, a.HoldID)
	b := e.checkout(ev.ID, north[10:12]...)
	e.pay(b, mock.Success)
	e.timers.fire(t, soon)
	e.timers.fire(t, late)
	// 2. A payment of the wrong amount (AMOUNT_MISMATCH alert), made later.
	w := e.checkout(ev.ID, south[10:12]...)
	pw := id64(e.pay(w, mock.WrongAmount)["id"])
	e.timers.fire(t, soon)
	e.payments.Wait()
	exec(t, `UPDATE refunds SET created_at = now() - interval '2 hours' WHERE payment_id = $1`, pa)
	exec(t, `UPDATE refunds SET created_at = now() - interval '1 hour' WHERE payment_id = $1`, pw)
	exec(t, `UPDATE organiser_alerts SET created_at = now() - interval '2 hours' WHERE booking_id = $1`, a.BookingID)
	exec(t, `UPDATE organiser_alerts SET created_at = now() - interval '1 hour' WHERE booking_id = $1`, w.BookingID)
	// Another event's alert and refund must not leak into this event's lists.
	other, otherNorth, _ := e.ticketEvent()
	o := e.checkout(other.ID, otherNorth[10])
	e.pay(o, mock.WrongAmount)
	e.timers.fire(t, soon)

	s := e.sales(ev)
	assert.EqualValues(t, 200_000, s["revenue_cents"], "only B's paid booking is revenue")
	assert.EqualValues(t, 2, s["refund_required_count"])
	assert.EqualValues(t, a.Total+w.Total+100, s["refund_required_cents"], "what was actually paid on each")
	assert.EqualValues(t, 2, s["open_alert_count"])

	// --- Alerts.
	alerts := want(t, e.do("GET", fmt.Sprintf("/admin/alerts?event_id=%d", ev.ID), nil, organiser), http.StatusOK)
	require.Len(t, items(alerts), 2)
	newest, oldest := items(alerts)[0].(obj), items(alerts)[1].(obj)
	assert.Equal(t, "AMOUNT_MISMATCH", newest["kind"], "newest first")
	assert.Equal(t, queryText(t, `SELECT title FROM events WHERE id = $1`, ev.ID), newest["event_title"], "the page need not look the title up")
	assert.Nil(t, newest["resolution"], "open: nobody and nothing has closed it")
	assert.Equal(t, w.Ref, newest["booking_reference"])
	assert.EqualValues(t, w.BookingID, newest["booking_id"])
	assert.EqualValues(t, ev.ID, newest["event_id"])
	assert.Contains(t, newest["message"], "Paid KES 2,001.00 instead of KES 2,000.00")
	assert.Nil(t, newest["acknowledged_at"])
	assert.Equal(t, "REFUND_REQUIRED", oldest["kind"])
	assert.Equal(t, a.Ref, oldest["booking_reference"])
	assert.Contains(t, oldest["message"], "Paid after the hold ended")

	// Paging, newest first.
	page1 := want(t, e.do("GET", fmt.Sprintf("/admin/alerts?event_id=%d&limit=1", ev.ID), nil, admin), http.StatusOK)
	require.Len(t, items(page1), 1)
	assert.Equal(t, newest["id"], items(page1)[0].(obj)["id"])
	require.NotNil(t, page1["next_cursor"])
	page2 := want(t, e.do("GET", fmt.Sprintf("/admin/alerts?event_id=%d&limit=1&cursor=%s", ev.ID, page1["next_cursor"]), nil, admin), http.StatusOK)
	require.Len(t, items(page2), 1)
	assert.Equal(t, oldest["id"], items(page2)[0].(obj)["id"])
	assert.Nil(t, page2["next_cursor"])
	everything := want(t, e.do("GET", "/admin/alerts?limit=100", nil, organiser), http.StatusOK)
	assert.GreaterOrEqual(t, len(items(everything)), 3, "without event_id, alerts of every event")

	// Acknowledge: the time is set once and kept.
	alertID := id64(oldest["id"])
	acked := want(t, e.do("POST", fmt.Sprintf("/admin/alerts/%d/ack", alertID), nil, organiser), http.StatusOK)
	require.NotNil(t, acked["acknowledged_at"])
	assert.Nil(t, acked["resolution"], "acknowledged by a person: no resolution note")
	assert.Equal(t, oldest["message"], acked["message"])
	exec(t, `UPDATE organiser_alerts SET acknowledged_at = acknowledged_at - interval '1 hour' WHERE id = $1`, alertID)
	first := queryText(t, `SELECT acknowledged_at::text FROM organiser_alerts WHERE id = $1`, alertID)
	want(t, e.do("POST", fmt.Sprintf("/admin/alerts/%d/ack", alertID), nil, admin), http.StatusOK)
	assert.Equal(t, first, queryText(t, `SELECT acknowledged_at::text FROM organiser_alerts WHERE id = $1`, alertID), "a repeat keeps the first time")
	open := want(t, e.do("GET", fmt.Sprintf("/admin/alerts?event_id=%d&acknowledged=false", ev.ID), nil, organiser), http.StatusOK)
	require.Len(t, items(open), 1)
	assert.Equal(t, newest["id"], items(open)[0].(obj)["id"])
	seen := want(t, e.do("GET", fmt.Sprintf("/admin/alerts?event_id=%d&acknowledged=true", ev.ID), nil, organiser), http.StatusOK)
	require.Len(t, items(seen), 1)
	assert.Equal(t, oldest["id"], items(seen)[0].(obj)["id"])
	assert.EqualValues(t, 1, e.sales(ev)["open_alert_count"])

	// Alert errors.
	wantError(t, e.do("POST", "/admin/alerts/999999999/ack", nil, organiser), http.StatusNotFound, domain.CodeNotFound)
	wantFields(t, e.do("POST", "/admin/alerts/abc/ack", nil, organiser, contracttest.LenientRequest()), "alertId")
	wantFields(t, e.do("GET", "/admin/alerts?acknowledged=maybe", nil, organiser, contracttest.LenientRequest()), "acknowledged")
	wantFields(t, e.do("GET", "/admin/alerts?event_id=0", nil, organiser, contracttest.LenientRequest()), "event_id")
	wantFields(t, e.do("GET", "/admin/alerts?cursor=%7B%7D", nil, organiser), "cursor")
	wantError(t, e.do("GET", "/admin/alerts", nil, a.Token), http.StatusForbidden, domain.CodeForbidden)
	wantError(t, e.do("GET", "/admin/alerts", nil, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
	wantError(t, e.do("POST", fmt.Sprintf("/admin/alerts/%d/ack", alertID), nil, a.Token), http.StatusForbidden, domain.CodeForbidden)
	wantError(t, e.do("POST", fmt.Sprintf("/admin/alerts/%d/ack", alertID), nil, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)

	// --- Refunds: oldest first, so the longest-waiting fan is on top.
	refunds := want(t, e.do("GET", fmt.Sprintf("/admin/refunds?event_id=%d", ev.ID), nil, organiser), http.StatusOK)
	require.Len(t, items(refunds), 2)
	ra, rw := items(refunds)[0].(obj), items(refunds)[1].(obj)
	assert.EqualValues(t, pa, ra["payment_id"], "oldest first")
	assert.Equal(t, a.Ref, ra["booking_reference"])
	assert.EqualValues(t, a.BookingID, ra["booking_id"])
	assert.EqualValues(t, ev.ID, ra["event_id"])
	assert.EqualValues(t, a.Total, ra["amount_cents"])
	assert.Equal(t, "KES", ra["currency"])
	assert.Equal(t, payPhone, ra["phone"], "the number that paid, not the account's")
	assert.NotNil(t, ra["mpesa_receipt"])
	assert.Equal(t, "Paid after the hold ended; the seats were no longer reserved", ra["reason"])
	assert.Equal(t, "PENDING", ra["status"])
	assert.Nil(t, ra["completed_at"])
	assert.Equal(t, "REFUND_REQUIRED", ra["booking_status"], "what completing it will do depends on this")
	assert.NotContains(t, ra, "already_completed", "only the answer of completeRefund carries it")
	// One booking's refunds, so an alert can link to its refund.
	byRef := want(t, e.do("GET", "/admin/refunds?booking_reference="+w.Ref, nil, organiser), http.StatusOK)
	require.Len(t, items(byRef), 1)
	assert.Equal(t, rw["id"], items(byRef)[0].(obj)["id"])
	assert.Empty(t, items(want(t, e.do("GET", "/admin/refunds?booking_reference=TLT-ZZZZZ9", nil, organiser), http.StatusOK)))
	assert.Empty(t, items(want(t, e.do("GET", fmt.Sprintf("/admin/refunds?booking_reference=%s&event_id=%d", w.Ref, other.ID), nil, organiser), http.StatusOK)))
	wantFields(t, e.do("GET", "/admin/refunds?booking_reference=nope", nil, organiser, contracttest.LenientRequest()), "booking_reference")
	assert.EqualValues(t, w.Total+100, rw["amount_cents"])
	rp1 := want(t, e.do("GET", fmt.Sprintf("/admin/refunds?event_id=%d&limit=1", ev.ID), nil, organiser), http.StatusOK)
	require.NotNil(t, rp1["next_cursor"])
	rp2 := want(t, e.do("GET", fmt.Sprintf("/admin/refunds?event_id=%d&limit=1&cursor=%s", ev.ID, rp1["next_cursor"]), nil, organiser), http.StatusOK)
	require.Len(t, items(rp2), 1)
	assert.Equal(t, rw["id"], items(rp2)[0].(obj)["id"])
	assert.Nil(t, rp2["next_cursor"])

	// The fan sees a booking owed a refund.
	assert.Equal(t, "REFUND_REQUIRED", e.getBooking(a.Token, a.Ref)["status"])

	// Complete the first refund.
	refundID := id64(ra["id"])
	done := want(t, e.do("POST", fmt.Sprintf("/admin/refunds/%d/complete", refundID), nil, organiser), http.StatusOK)
	assert.Equal(t, "DONE", done["status"])
	require.NotNil(t, done["completed_at"])
	assert.Equal(t, false, done["already_completed"], "this call did the work")
	assert.Equal(t, "REFUNDED", done["booking_status"])
	assert.Equal(t, "REFUNDED", bookingStatus(t, a.BookingID), "its only refund is paid")
	assert.Equal(t, "REFUNDED", e.getBooking(a.Token, a.Ref)["status"])
	assert.Equal(t, "REFUND_REQUIRED", bookingStatus(t, w.BookingID), "the other booking is still owed")
	// Safe to repeat: still 200, the same refund, the same completion time; only
	// already_completed tells the repeat from the call that did the work.
	again := want(t, e.do("POST", fmt.Sprintf("/admin/refunds/%d/complete", refundID), nil, admin), http.StatusOK)
	assert.Equal(t, true, again["already_completed"])
	again["already_completed"] = false
	assert.Equal(t, done, again)
	// The REFUND_REQUIRED alert of that payment was acknowledged by a person earlier; the
	// system does not rewrite that.
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM organiser_alerts WHERE id = $1 AND resolution IS NULL`, alertID))

	// Completing the other refund closes ITS alert, which nobody had acknowledged, with a
	// note that the system did it. Then the test puts the refund back for what follows.
	wDone := want(t, e.do("POST", fmt.Sprintf("/admin/refunds/%d/complete", id64(rw["id"])), nil, organiser), http.StatusOK)
	assert.Equal(t, false, wDone["already_completed"])
	closed := want(t, e.do("GET", fmt.Sprintf("/admin/alerts?event_id=%d&acknowledged=true", ev.ID), nil, organiser), http.StatusOK)
	require.Len(t, items(closed), 2)
	var byKind = map[string]obj{}
	for _, it := range items(closed) {
		byKind[it.(obj)["kind"].(string)] = it.(obj)
	}
	assert.Equal(t, "Resolved automatically: the refund was marked as paid back.", byKind["AMOUNT_MISMATCH"]["resolution"])
	assert.NotNil(t, byKind["AMOUNT_MISMATCH"]["acknowledged_at"])
	assert.Nil(t, byKind["REFUND_REQUIRED"]["resolution"])
	assert.EqualValues(t, 0, e.sales(ev)["open_alert_count"])
	exec(t, `UPDATE refunds SET status = 'PENDING', completed_at = NULL WHERE id = $1`, id64(rw["id"]))
	exec(t, `UPDATE bookings SET status = 'REFUND_REQUIRED' WHERE id = $1`, w.BookingID)

	// The sales summary stops counting a booking once its refund is done.
	s = e.sales(ev)
	assert.EqualValues(t, 1, s["refund_required_count"])
	assert.EqualValues(t, w.Total+100, s["refund_required_cents"])
	assert.EqualValues(t, 200_000, s["revenue_cents"])

	pending := want(t, e.do("GET", fmt.Sprintf("/admin/refunds?event_id=%d&status=PENDING", ev.ID), nil, organiser), http.StatusOK)
	require.Len(t, items(pending), 1)
	assert.Equal(t, rw["id"], items(pending)[0].(obj)["id"])
	finished := want(t, e.do("GET", fmt.Sprintf("/admin/refunds?event_id=%d&status=DONE", ev.ID), nil, organiser), http.StatusOK)
	require.Len(t, items(finished), 1)
	assert.Equal(t, ra["id"], items(finished)[0].(obj)["id"])
	// The organiser's booking list carries the refund.
	owed := want(t, e.do("GET", fmt.Sprintf("/admin/events/%d/bookings?status=REFUND_REQUIRED", ev.ID), nil, ev.Token), http.StatusOK)
	require.Len(t, items(owed), 1)
	assert.Equal(t, w.Ref, items(owed)[0].(obj)["reference"])
	assert.EqualValues(t, w.Total+100, items(owed)[0].(obj)["refund"].(obj)["amount_cents"])
	assert.Equal(t, "wrong_amount", items(owed)[0].(obj)["latest_payment"].(obj)["failure_reason"])

	// B, who really has the seats, is untouched by any of it.
	assert.Equal(t, "PAID", bookingStatus(t, b.BookingID))
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1 AND status = 'VALID'`, b.BookingID))
	assert.Equal(t, map[string]int{"SOLD": 2}, seatStatuses(t, b.Seats))

	// Refund errors.
	wantError(t, e.do("POST", "/admin/refunds/999999999/complete", nil, organiser), http.StatusNotFound, domain.CodeNotFound)
	wantFields(t, e.do("POST", "/admin/refunds/0/complete", nil, organiser, contracttest.LenientRequest()), "refundId")
	wantFields(t, e.do("GET", "/admin/refunds?status=PAID", nil, organiser, contracttest.LenientRequest()), "status")
	wantFields(t, e.do("GET", "/admin/refunds?limit=1000", nil, organiser, contracttest.LenientRequest()), "limit")
	wantError(t, e.do("GET", "/admin/refunds", nil, a.Token), http.StatusForbidden, domain.CodeForbidden)
	wantError(t, e.do("GET", "/admin/refunds", nil, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
	wantError(t, e.do("POST", fmt.Sprintf("/admin/refunds/%d/complete", id64(rw["id"])), nil, a.Token), http.StatusForbidden, domain.CodeForbidden)
	wantError(t, e.do("POST", fmt.Sprintf("/admin/refunds/%d/complete", id64(rw["id"])), nil, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
	assert.Equal(t, "PENDING", queryText(t, `SELECT status FROM refunds WHERE id = $1`, id64(rw["id"])))

	// A busy event: EVENT_BUSY, and nothing changed.
	quick := newEnv(t, withPaymentPolicy(func(p *payments.Policy) { p.EventLockWait = 200 * time.Millisecond }))
	ctx := context.Background()
	lock, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	_, err = store.New(lock).LockEvent(ctx, ev.ID)
	require.NoError(t, err)
	rec := quick.do("POST", fmt.Sprintf("/admin/refunds/%d/complete", id64(rw["id"])), nil, quick.staffToken(domain.RoleOrganiser))
	require.NoError(t, lock.Rollback(ctx))
	wantError(t, rec, http.StatusConflict, domain.CodeEventBusy)
	assert.Equal(t, "PENDING", queryText(t, `SELECT status FROM refunds WHERE id = $1`, id64(rw["id"])))
	assert.Equal(t, 2, ledger(t, ev.ID).Sold)
	ledger(t, other.ID)
}

// A cancelled event's refunds: completing them moves each booking to REFUNDED. Its sold
// seats stay SOLD (the event is off; nothing goes SOLD -> AVAILABLE) and its tickets, voided
// by the cancel, stay void.
func TestAdmin_CompletingRefundsOfACancelledEvent(t *testing.T) {
	e := newEnv(t, withEventPolicy(func(p *events.Policy) { p.CountsTTL = time.Nanosecond }))
	ev, north, _ := e.ticketEvent()
	c1, _ := e.paid(ev.ID, north[10:12]...)
	c2, _ := e.paid(ev.ID, north[20:23]...)

	cancelled := want(t, e.do("POST", fmt.Sprintf("/admin/events/%d/cancel", ev.ID), nil, ev.Token), http.StatusOK)
	assert.EqualValues(t, 2, cancelled["refunds_queued"])
	assert.EqualValues(t, 5, cancelled["tickets_voided"])
	assert.EqualValues(t, c1.Total+c2.Total, cancelled["refund_total_cents"])
	require.Equal(t, "REFUND_REQUIRED", bookingStatus(t, c1.BookingID))
	s := e.sales(ev)
	assert.EqualValues(t, 2, s["refund_required_count"])
	assert.EqualValues(t, c1.Total+c2.Total, s["refund_required_cents"])
	assert.EqualValues(t, 0, s["revenue_cents"])

	refunds := want(t, e.do("GET", fmt.Sprintf("/admin/refunds?event_id=%d&status=PENDING", ev.ID), nil, ev.Token), http.StatusOK)
	require.Len(t, items(refunds), 2)
	assert.Equal(t, "The event was cancelled", items(refunds)[0].(obj)["reason"])
	for i, r := range items(refunds) {
		want(t, e.do("POST", fmt.Sprintf("/admin/refunds/%d/complete", id64(r.(obj)["id"])), nil, ev.Token), http.StatusOK)
		s := e.sales(ev)
		assert.EqualValues(t, 1-i, s["refund_required_count"])
	}
	assert.Equal(t, "REFUNDED", bookingStatus(t, c1.BookingID))
	assert.Equal(t, "REFUNDED", bookingStatus(t, c2.BookingID))
	assert.EqualValues(t, 0, e.sales(ev)["refund_required_cents"])
	assert.Equal(t, 5, queryInt(t, `SELECT count(*) FROM tickets WHERE event_id = $1 AND status = 'VOID'`, ev.ID))
	assert.Equal(t, 5, queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1 AND status = 'SOLD'`, ev.ID),
		"a cancelled event's sold seats stay SOLD")
	b := e.getBooking(c1.Token, c1.Ref)
	assert.Equal(t, "REFUNDED", b["status"])
	assert.Equal(t, "CANCELLED", b["event"].(obj)["status"])
	for _, tk := range list(b, "tickets") {
		assert.Equal(t, "VOID", tk.(obj)["status"])
	}
	testinvariantsLedgerOnly(t, ev.ID)
}
