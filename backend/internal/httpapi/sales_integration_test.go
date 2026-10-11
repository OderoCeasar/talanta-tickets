//go:build integration

package httpapi_test

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

// The dashboard's numbers must equal what is in bookings, booking_items and event_seats.
// Everything here is inserted directly, so the expected figures are known exactly.
func TestSalesSummaryAndBookings(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(2, 2, 4) // block 1 in the N stand, block 2 in the S stand; 16 seats
	ev := e.publishedEvent(v)
	token := ev.Token
	vip, regular := ev.Cats["VIP"], ev.Cats["Regular"]
	b0 := eventSeatIDs(t, ev.ID, v.BlockIDs[0]) // b0[0..3] is the VIP row
	b1 := eventSeatIDs(t, ev.ID, v.BlockIDs[1])
	fanID, _ := e.newFan()

	// A Nairobi midnight two days back: 21:00 UTC.
	nairobi := time.FixedZone("EAT", 3*3600)
	l := time.Now().In(nairobi).AddDate(0, 0, -2)
	day0 := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, nairobi).UTC()
	t1 := day0.Add(23*time.Hour + 30*time.Minute) // 20:30 UTC, last Nairobi hour of the day
	t3 := day0.Add(23*time.Hour + 59*time.Minute + 59*time.Second)
	t2 := day0.Add(24*time.Hour + 10*time.Minute) // ten past the next Nairobi midnight

	// Three paid bookings. The second was bought at an older Regular price.
	p1, ref1 := insertBooking(t, ev.ID, fanID, "PAID", t1, &t1, bookingItem{b0[0], vip, 500_000}, bookingItem{b0[4], regular, 100_000})
	p2, _ := insertBooking(t, ev.ID, fanID, "PAID", t2, &t2, bookingItem{b1[0], regular, 90_000})
	p3, _ := insertBooking(t, ev.ID, fanID, "PAID", t3, &t3, bookingItem{b1[1], regular, 100_000})
	sellSeats(t, b0[0], b0[4], b1[0], b1[1])
	failed := insertPayment(t, p1, "FAILED", 600_000, t1.Add(-2*time.Minute))
	exec(t, `UPDATE payments SET result_code = 1, result_desc = 'The balance is insufficient for the transaction.' WHERE id = $1`, failed)
	insertPayment(t, p1, "SUCCESS", 600_000, t1)
	insertPayment(t, p2, "SUCCESS", 90_000, t2)
	insertPayment(t, p3, "SUCCESS", 100_000, t3)

	// Two bookings owed a refund: one with its refund queued, one not yet.
	r1, refR1 := insertBooking(t, ev.ID, fanID, "REFUND_REQUIRED", t2.Add(time.Hour), nil, bookingItem{b1[2], regular, 100_000})
	payR1 := insertPayment(t, r1, "SUCCESS", 100_000, t2.Add(time.Hour))
	exec(t, `INSERT INTO refunds (booking_id, payment_id, amount_cents, reason) VALUES ($1, $2, 100000, 'Paid after the hold expired; seats were resold')`, r1, payR1)
	insertBooking(t, ev.ID, fanID, "REFUND_REQUIRED", t2.Add(2*time.Hour), nil, bookingItem{b0[2], vip, 500_000})

	// One unpaid booking on a live hold, one blocked seat, two alerts (one acknowledged).
	holdSeats(t, ev.ID, fanID, time.Now().Add(10*time.Minute), b0[1])
	pending, _ := insertBooking(t, ev.ID, fanID, "PENDING_PAYMENT", t2.Add(3*time.Hour), nil, bookingItem{b0[1], vip, 500_000})
	exec(t, `UPDATE event_seats SET status = 'BLOCKED' WHERE id = $1`, b1[7])
	exec(t, `INSERT INTO organiser_alerts (event_id, booking_id, kind, message) VALUES ($1, $2, 'REFUND_REQUIRED', 'Refund needed')`, ev.ID, r1)
	exec(t, `INSERT INTO organiser_alerts (event_id, kind, message, acknowledged_at) VALUES ($1, 'PAYMENT_UNRESOLVED', 'Seen', now())`, ev.ID)
	// Another event's sales must not leak in.
	otherEv := e.publishedEvent(v)
	otherSeats := eventSeatIDs(t, otherEv.ID, v.BlockIDs[0])
	insertBooking(t, otherEv.ID, fanID, "PAID", t1, &t1, bookingItem{otherSeats[5], otherEv.Cats["Regular"], 100_000})
	sellSeats(t, otherSeats[5])

	e.clock.Advance(4 * time.Second) // past the counts cached by the publish answer
	salesPath := fmt.Sprintf("/admin/events/%d/sales", ev.ID)
	rfc := func(tm time.Time) string { return tm.UTC().Format(time.RFC3339) }
	window := "from=" + rfc(day0.Add(23*time.Hour)) + "&to=" + rfc(day0.Add(26*time.Hour))

	s := want(t, e.do("GET", salesPath+"?"+window, nil, token), http.StatusOK)
	assert.EqualValues(t, ev.ID, s["event_id"])
	assert.Equal(t, "KES", s["currency"])
	assert.EqualValues(t, 790_000, s["revenue_cents"], "PAID bookings only: 600,000 + 90,000 + 100,000")
	assert.Equal(t, obj{"sold": 4.0, "held": 1.0, "available": 10.0, "blocked": 1.0, "total": 16.0}, s["seats"])
	assert.EqualValues(t, 2, s["refund_required_count"])
	assert.EqualValues(t, 600_000, s["refund_required_cents"], "the queued refund, plus the full total of the booking with none queued")
	assert.EqualValues(t, 1, s["open_alert_count"])

	byName := map[string]obj{}
	for _, c := range list(s, "categories") {
		byName[c.(obj)["name"].(string)] = c.(obj)
	}
	assert.Equal(t, obj{
		"category_id": float64(vip), "name": "VIP", "color_hex": "#2563EB", "price_cents": 500_000.0,
		"sold": 1.0, "held": 1.0, "available": 2.0, "blocked": 0.0, "total": 4.0, "revenue_cents": 500_000.0,
	}, byName["VIP"])
	assert.Equal(t, obj{
		"category_id": float64(regular), "name": "Regular", "color_hex": "#059669", "price_cents": 100_000.0,
		"sold": 3.0, "held": 0.0, "available": 8.0, "blocked": 1.0, "total": 12.0, "revenue_cents": 290_000.0,
	}, byName["Regular"], "revenue is what was paid (90,000 once), not seats × today's price")

	assert.Equal(t, []any{
		obj{"block_id": float64(v.BlockIDs[0]), "stand_id": float64(v.StandIDs[0]), "sold": 2.0, "held": 1.0, "total": 8.0},
		obj{"block_id": float64(v.BlockIDs[1]), "stand_id": float64(v.StandIDs[1]), "sold": 2.0, "held": 0.0, "total": 7.0},
	}, s["blocks"], "the heatmap; a blocked seat is not part of a block's total")

	// Hour buckets: on the hour, zero-filled, oldest first, `to` exclusive.
	series := s["series"].(obj)
	assert.Equal(t, "hour", series["bucket"], "hour is the default")
	assert.Equal(t, []any{
		obj{"bucket_start": rfc(day0.Add(23 * time.Hour)), "bookings": 2.0, "tickets": 3.0, "revenue_cents": 700_000.0},
		obj{"bucket_start": rfc(day0.Add(24 * time.Hour)), "bookings": 1.0, "tickets": 1.0, "revenue_cents": 90_000.0},
		obj{"bucket_start": rfc(day0.Add(25 * time.Hour)), "bookings": 0.0, "tickets": 0.0, "revenue_cents": 0.0},
	}, series["points"])

	// Day buckets start at Nairobi midnight (21:00 UTC), so 20:59:59 and 21:10 UTC fall on
	// different days although they share a UTC date.
	days := want(t, e.do("GET", salesPath+"?bucket=day&"+window, nil, token), http.StatusOK)["series"].(obj)
	assert.Equal(t, "day", days["bucket"])
	assert.Equal(t, []any{
		obj{"bucket_start": rfc(day0), "bookings": 2.0, "tickets": 3.0, "revenue_cents": 700_000.0},
		obj{"bucket_start": rfc(day0.Add(24 * time.Hour)), "bookings": 1.0, "tickets": 1.0, "revenue_cents": 90_000.0},
	}, days["points"])
	assert.Equal(t, 21, day0.Hour(), "a Nairobi midnight is 21:00 UTC")

	// A window that cuts a bucket only counts what is inside the window.
	cut := want(t, e.do("GET", salesPath+"?from="+rfc(t1.Add(time.Minute))+"&to="+rfc(t2), nil, token), http.StatusOK)["series"].(obj)
	assert.Equal(t, []any{
		obj{"bucket_start": rfc(day0.Add(23 * time.Hour)), "bookings": 1.0, "tickets": 1.0, "revenue_cents": 100_000.0},
		obj{"bucket_start": rfc(day0.Add(24 * time.Hour)), "bookings": 0.0, "tickets": 0.0, "revenue_cents": 0.0},
	}, cut["points"])

	// Defaults: from the first paid booking to now.
	def := list(want(t, e.do("GET", salesPath, nil, token), http.StatusOK)["series"].(obj), "points")
	require.GreaterOrEqual(t, len(def), 24)
	assert.Equal(t, rfc(day0.Add(23*time.Hour)), def[0].(obj)["bucket_start"])
	total := 0.0
	for _, p := range def {
		total += p.(obj)["bookings"].(float64)
	}
	assert.EqualValues(t, 3, total)

	wantFields(t, e.do("GET", salesPath+"?bucket=week", nil, token), "bucket")
	wantFields(t, e.do("GET", salesPath+"?from=today", nil, token), "from")
	wantFields(t, e.do("GET", salesPath+"?from="+rfc(t2)+"&to="+rfc(t1), nil, token), "to")
	wantFields(t, e.do("GET", salesPath+"?from=2000-01-01T00:00:00Z", nil, token), "from")
	want(t, e.do("GET", salesPath+"?bucket=day&from=2025-01-01T00:00:00Z", nil, token), http.StatusOK)
	wantError(t, e.do("GET", "/admin/events/999999999/sales", nil, token), http.StatusNotFound, domain.CodeNotFound)

	// A draft has no inventory and no sales; the answer is all zeros, not an error.
	draft := e.readyEvent(v)
	empty := want(t, e.do("GET", fmt.Sprintf("/admin/events/%d/sales", draft.ID), nil, token), http.StatusOK)
	assert.EqualValues(t, 0, empty["revenue_cents"])
	assert.Equal(t, obj{"sold": 0.0, "held": 0.0, "available": 0.0, "blocked": 0.0, "total": 0.0}, empty["seats"])
	assert.Empty(t, list(empty["series"].(obj), "points"))
	assert.Len(t, list(empty, "blocks"), 2)

	// --- The booking list ----------------------------------------------------------------
	bookingsPath := fmt.Sprintf("/admin/events/%d/bookings", ev.ID)
	var refs []string
	var all []obj
	cursor := ""
	for pages := 0; pages < 6; pages++ {
		o := want(t, e.do("GET", bookingsPath+"?limit=2"+cursor, nil, token), http.StatusOK)
		for _, it := range items(o) {
			all = append(all, it.(obj))
			refs = append(refs, it.(obj)["reference"].(string))
		}
		if o["next_cursor"] == nil {
			break
		}
		cursor = "&cursor=" + o["next_cursor"].(string)
	}
	require.Len(t, all, 6, "three pages of two")
	assert.Equal(t, float64(pending), all[0]["id"], "newest first")
	assert.Equal(t, ref1, refs[5], "oldest last")
	assert.Equal(t, float64(p3), all[4]["id"])

	first := all[5]
	assert.Equal(t, "PAID", first["status"])
	assert.EqualValues(t, 2, first["seat_count"])
	assert.Equal(t, []any{"B1 A1", "B1 B1"}, first["seat_labels"], "block code and seat label")
	assert.EqualValues(t, 600_000, first["total_cents"])
	assert.Equal(t, float64(fanID), first["user"].(obj)["id"])
	assert.Regexp(t, `^\+2547\d{8}$`, first["user"].(obj)["phone"])
	pay := first["latest_payment"].(obj)
	assert.Equal(t, "SUCCESS", pay["status"], "the latest attempt, not the failed one before it")
	assert.Equal(t, ref1, pay["booking_reference"])
	assert.Nil(t, pay["failure_reason"])
	assert.Nil(t, first["refund"])
	assert.NotNil(t, first["paid_at"])

	owed := want(t, e.do("GET", bookingsPath+"?status=REFUND_REQUIRED", nil, token), http.StatusOK)
	require.Len(t, items(owed), 2)
	withRefund := items(owed)[1].(obj)
	assert.Equal(t, refR1, withRefund["reference"])
	refund := withRefund["refund"].(obj)
	assert.EqualValues(t, 100_000, refund["amount_cents"])
	assert.Equal(t, "PENDING", refund["status"])
	assert.Equal(t, float64(payR1), refund["payment_id"])
	assert.Equal(t, float64(ev.ID), refund["event_id"])
	assert.Equal(t, "+254700000009", refund["phone"], "the number that paid")
	assert.NotNil(t, refund["mpesa_receipt"])
	assert.Nil(t, items(owed)[0].(obj)["refund"])
	assert.Nil(t, items(owed)[0].(obj)["latest_payment"])

	// A failed payment carries the stable failure reason.
	exec(t, `UPDATE payments SET status = 'FAILED', mpesa_receipt = NULL, result_code = 2001 WHERE booking_id = $1`, p2)
	for _, it := range items(want(t, e.do("GET", bookingsPath+"?status=PAID&limit=100", nil, token), http.StatusOK)) {
		if it.(obj)["id"] == float64(p2) {
			assert.Equal(t, "wrong_pin", it.(obj)["latest_payment"].(obj)["failure_reason"])
		}
	}

	assert.Empty(t, items(want(t, e.do("GET", bookingsPath+"?status=EXPIRED", nil, token), http.StatusOK)))
	wantFields(t, e.do("GET", bookingsPath+"?status=PAYED", nil, token), "status")
	wantFields(t, e.do("GET", bookingsPath+"?cursor=xx", nil, token), "cursor")
	wantFields(t, e.do("GET", bookingsPath+"?limit=1000", nil, token), "limit")
	wantError(t, e.do("GET", "/admin/events/999999999/bookings", nil, token), http.StatusNotFound, domain.CodeNotFound)
}
