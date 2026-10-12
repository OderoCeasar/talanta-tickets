//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/booking"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/testinvariants"
)

// m6Operations is every operation of the holds milestone. The harness requires a
// contract-checked success answer and error answer for each.
var m6Operations = []string{"createHold", "createBestAvailableHold", "getHold", "releaseHold", "listMyHolds"}

func withHoldPolicy(fn func(*booking.Policy)) envOption {
	return func(c *envConfig) { fn(&c.holdPolicy) }
}

func (e *env) hold(token string, eventID int64, seatIDs ...int64) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("POST", "/holds", obj{"event_id": eventID, "seat_ids": seatIDs}, token)
}

func (e *env) best(token string, eventID, categoryID int64, quantity int) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("POST", "/holds/best-available", obj{"event_id": eventID, "category_id": categoryID, "quantity": quantity}, token)
}

func holdErr(t testing.TB, rec *httptest.ResponseRecorder, status int, code domain.Code) obj {
	t.Helper()
	require.Equal(t, status, rec.Code, rec.Body.String())
	errObj := body(t, rec)["error"].(obj)
	require.Equal(t, string(code), errObj["code"], rec.Body.String())
	d, _ := errObj["details"].(obj)
	return d
}

func ids(v any) []int64 {
	out := []int64{}
	for _, x := range v.([]any) {
		out = append(out, id64(x))
	}
	return out
}

// lapseHold moves a hold's expiry into the past on the hold and its seats, as if its ten
// minutes had run out and no sweeper had come by.
func lapseHold(t testing.TB, holdID int64) {
	t.Helper()
	exec(t, `UPDATE holds SET expires_at = now() - interval '1 minute' WHERE id = $1`, holdID)
	exec(t, `UPDATE event_seats es SET hold_expires_at = h.expires_at FROM holds h WHERE h.id = $1 AND es.hold_id = h.id AND es.status = 'HELD'`, holdID)
}

func checkInventory(t testing.TB, eventID int64) testinvariants.Counts {
	t.Helper()
	return testinvariants.Check(t, db.Pool, eventID)
}

// The whole life of a hold over HTTP, every answer checked against the contract.
func TestHolds_CreateReadReleaseOverHTTP(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 3, 6)
	ev := e.publishedEvent(v)
	seats := eventSeatIDs(t, ev.ID, v.BlockIDs[0]) // row A (VIP) is seats[0:6], row B seats[6:12]
	fanID, fan := e.newFan()
	_, stranger := e.newFan()

	rec := e.hold(fan, ev.ID, seats[7], seats[6])
	h := want(t, rec, http.StatusCreated)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	holdID := id64(h["id"])
	assert.Equal(t, "ACTIVE", h["status"])
	assert.EqualValues(t, ev.ID, h["event_id"])
	assert.InDelta(t, 600, h["remaining_seconds"], 2)
	assert.Equal(t, false, h["extended"])
	assert.Nil(t, h["booking_reference"])
	assert.Equal(t, "KES", h["currency"])
	assert.EqualValues(t, 200_000, h["subtotal_cents"])
	assert.EqualValues(t, 0, h["fee_cents"], "no fee by default")
	assert.EqualValues(t, 200_000, h["total_cents"])
	hs := list(h, "seats")
	require.Len(t, hs, 2)
	first := hs[0].(obj)
	assert.EqualValues(t, seats[6], first["event_seat_id"], "ordered by row and seat number")
	assert.Equal(t, "Regular", first["category_name"])
	assert.EqualValues(t, 100_000, first["price_cents"])
	loc := first["location"].(obj)
	assert.Equal(t, "B1", loc["seat_label"])
	assert.Equal(t, "N-LOWER", loc["gate_group"])
	assert.EqualValues(t, v.BlockIDs[0], loc["block_id"])
	// expires_at and remaining_seconds come from the database clock.
	expires, err := time.Parse(time.RFC3339Nano, h["expires_at"].(string))
	require.NoError(t, err)
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM holds WHERE id = $1 AND user_id = $2 AND expires_at = $3`, holdID, fanID, expires))

	// Read it back; list it.
	got := want(t, e.do("GET", fmt.Sprintf("/holds/%d", holdID), nil, fan), http.StatusOK)
	assert.Equal(t, h["expires_at"], got["expires_at"])
	assert.Len(t, list(got, "seats"), 2)
	mine := items(want(t, e.do("GET", "/me/holds", nil, fan), http.StatusOK))
	require.Len(t, mine, 1)
	assert.EqualValues(t, holdID, mine[0].(obj)["id"])
	assert.Len(t, items(want(t, e.do("GET", fmt.Sprintf("/me/holds?event_id=%d", ev.ID), nil, fan), http.StatusOK)), 1)
	assert.Empty(t, items(want(t, e.do("GET", "/me/holds?event_id=999999999", nil, fan), http.StatusOK)))
	assert.Empty(t, items(want(t, e.do("GET", "/me/holds", nil, stranger), http.StatusOK)))
	wantFields(t, e.do("GET", "/me/holds?event_id=abc", nil, fan), "event_id")

	// Another fan can neither read nor release it: 404, the same as a hold that is not there.
	wantError(t, e.do("GET", fmt.Sprintf("/holds/%d", holdID), nil, stranger), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("DELETE", fmt.Sprintf("/holds/%d", holdID), nil, stranger), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("GET", "/holds/999999999", nil, fan), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("DELETE", "/holds/999999999", nil, fan), http.StatusNotFound, domain.CodeNotFound)
	wantFields(t, e.do("GET", "/holds/abc", nil, fan), "holdId")
	wantFields(t, e.do("DELETE", "/holds/0", nil, fan), "holdId")
	assert.Equal(t, 2, checkInventory(t, ev.ID).Held, "the stranger's attempts changed nothing")

	// Fans only, signed in only.
	organiser := e.staffToken(domain.RoleOrganiser)
	for _, c := range []struct{ method, path string }{
		{"POST", "/holds"}, {"POST", "/holds/best-available"}, {"GET", fmt.Sprintf("/holds/%d", holdID)},
		{"DELETE", fmt.Sprintf("/holds/%d", holdID)}, {"GET", "/me/holds"},
	} {
		var b any
		if c.method == "POST" {
			b = obj{"event_id": ev.ID, "seat_ids": []int64{seats[8]}}
			if c.path != "/holds" {
				b = obj{"event_id": ev.ID, "category_id": ev.Cats["Regular"], "quantity": 1}
			}
		}
		wantError(t, e.do(c.method, c.path, b, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
		wantError(t, e.do(c.method, c.path, b, organiser), http.StatusForbidden, domain.CodeForbidden)
	}

	// Release: the seats are free at once; repeating it is still 204.
	rec = e.do("DELETE", fmt.Sprintf("/holds/%d", holdID), nil, fan)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Body.String())
	require.Equal(t, http.StatusNoContent, e.do("DELETE", fmt.Sprintf("/holds/%d", holdID), nil, fan).Code)
	got = want(t, e.do("GET", fmt.Sprintf("/holds/%d", holdID), nil, fan), http.StatusOK)
	assert.Equal(t, "RELEASED", got["status"])
	assert.EqualValues(t, 0, got["remaining_seconds"])
	assert.Empty(t, list(got, "seats"))
	assert.EqualValues(t, 0, got["total_cents"])
	assert.Empty(t, items(want(t, e.do("GET", "/me/holds", nil, fan), http.StatusOK)))
	assert.Equal(t, 0, checkInventory(t, ev.ID).Held)

	// A hold past its time is reported EXPIRED before any sweeper has touched it.
	again := want(t, e.hold(fan, ev.ID, seats[6]), http.StatusCreated)
	lapseHold(t, id64(again["id"]))
	got = want(t, e.do("GET", fmt.Sprintf("/holds/%d", id64(again["id"])), nil, fan), http.StatusOK)
	assert.Equal(t, "EXPIRED", got["status"])
	assert.EqualValues(t, 0, got["remaining_seconds"])
	assert.Empty(t, items(want(t, e.do("GET", "/me/holds", nil, fan), http.StatusOK)))
	require.Equal(t, http.StatusNoContent, e.do("DELETE", fmt.Sprintf("/holds/%d", id64(again["id"])), nil, fan).Code)
	checkInventory(t, ev.ID)
}

// Every error answer of POST /holds and POST /holds/best-available.
func TestHolds_CreateErrors(t *testing.T) {
	e := newEnv(t, withLimits(func(l *httpapi.RateLimits) { l.HoldPerUser = ratelimit.Rule{} }))
	v := e.newVenue(1, 2, 6)
	ev := e.publishedEvent(v, func(o obj) { o["max_tickets_per_user"] = 4 })
	seats := eventSeatIDs(t, ev.ID, v.BlockIDs[0])
	regular, vip := ev.Cats["Regular"], ev.Cats["VIP"]
	_, fan := e.newFan()
	_, rival := e.newFan()

	// 400: the body.
	for name, b := range map[string]any{
		"no seats":     obj{"event_id": ev.ID, "seat_ids": []int64{}},
		"a repeat":     obj{"event_id": ev.ID, "seat_ids": []int64{seats[0], seats[0]}},
		"51 seats":     obj{"event_id": ev.ID, "seat_ids": make51()},
		"a zero id":    obj{"event_id": ev.ID, "seat_ids": []int64{0}},
		"no event":     obj{"seat_ids": []int64{seats[0]}},
		"wrong type":   `{"event_id": "x", "seat_ids": [1]}`,
		"extra field":  obj{"event_id": ev.ID, "seat_ids": []int64{seats[0]}, "user_id": 1},
		"not an array": `{"event_id": 1, "seat_ids": 5}`,
	} {
		rec := e.do("POST", "/holds", b, fan)
		wantError(t, rec, http.StatusBadRequest, domain.CodeValidationFailed)
		_ = name
	}
	f := wantFields(t, e.do("POST", "/holds", obj{"event_id": ev.ID, "seat_ids": []int64{seats[0], seats[0]}}, fan), "seat_ids")
	assert.Equal(t, "unique", f["seat_ids"])
	f = wantFields(t, e.do("POST", "/holds", obj{"event_id": ev.ID, "seat_ids": make51()}, fan), "seat_ids")
	assert.Equal(t, "max", f["seat_ids"])
	for _, b := range []any{
		obj{"event_id": ev.ID, "category_id": regular, "quantity": 0},
		obj{"event_id": ev.ID, "category_id": regular, "quantity": 51},
		obj{"event_id": ev.ID, "quantity": 2},
	} {
		wantError(t, e.do("POST", "/holds/best-available", b, fan), http.StatusBadRequest, domain.CodeValidationFailed)
	}

	// 404: no such event (a draft counts as missing), no such category.
	draft := e.readyEvent(v)
	wantError(t, e.hold(fan, 999999999, 1), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.hold(fan, draft.ID, 1), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.best(fan, 999999999, regular, 2), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.best(fan, ev.ID, draft.Cats["Regular"], 2), http.StatusNotFound, domain.CodeNotFound)

	// 409 SEATS_UNAVAILABLE: exactly the lost ids, and nothing held.
	want(t, e.hold(rival, ev.ID, seats[7], seats[9]), http.StatusCreated)
	d := holdErr(t, e.hold(fan, ev.ID, seats[6], seats[7], seats[8], seats[9]), http.StatusConflict, domain.CodeSeatsUnavailable)
	assert.Equal(t, []int64{seats[7], seats[9]}, ids(d["unavailable_seat_ids"]))
	d = holdErr(t, e.hold(fan, ev.ID, seats[6], 999999999999), http.StatusConflict, domain.CodeSeatsUnavailable)
	assert.Equal(t, []int64{999999999999}, ids(d["unavailable_seat_ids"]), "a seat that is not part of the event")
	assert.Empty(t, items(want(t, e.do("GET", "/me/holds", nil, fan), http.StatusOK)))
	assert.Equal(t, 2, checkInventory(t, ev.ID).Held)

	// 422 TICKET_LIMIT_EXCEEDED.
	want(t, e.hold(fan, ev.ID, seats[0], seats[1], seats[2]), http.StatusCreated)
	d = holdErr(t, e.hold(fan, ev.ID, seats[3], seats[4]), http.StatusUnprocessableEntity, domain.CodeTicketLimitExceeded)
	assert.Equal(t, obj{"allowed": 4.0, "held_or_bought": 3.0, "requested": 2.0, "remaining": 1.0}, d)
	d = holdErr(t, e.best(fan, ev.ID, regular, 2), http.StatusUnprocessableEntity, domain.CodeTicketLimitExceeded)
	assert.EqualValues(t, 1, d["remaining"])

	// 409 NO_ADJACENT_SEATS: row B has seats 1, 3, 5, 6 free (2 and 4 are the rival's): a
	// pair exists, three together do not.
	_, third := e.newFan()
	d = holdErr(t, e.best(third, ev.ID, regular, 3), http.StatusConflict, domain.CodeNoAdjacentSeats)
	assert.Equal(t, obj{"category_id": float64(regular), "quantity": 3.0, "available": 4.0}, d)
	pair := want(t, e.best(third, ev.ID, regular, 2), http.StatusCreated)
	ps := list(pair, "seats")
	assert.Equal(t, []int64{seats[10], seats[11]}, []int64{id64(ps[0].(obj)["event_seat_id"]), id64(ps[1].(obj)["event_seat_id"])})
	vipHold := want(t, e.best(third, ev.ID, vip, 1), http.StatusCreated)
	assert.EqualValues(t, 500_000, vipHold["subtotal_cents"])

	// 409 for the sales window and the event status.
	for _, c := range []struct {
		sql    string
		code   domain.Code
		status string
	}{
		{`UPDATE events SET sales_open_at = now() + interval '1 hour' WHERE id = $1`, domain.CodeSalesNotOpen, "PUBLISHED"},
		{`UPDATE events SET sales_open_at = now() - interval '2 hours', sales_close_at = now() - interval '1 hour' WHERE id = $1`, domain.CodeSalesClosed, "PUBLISHED"},
	} {
		exec(t, c.sql, ev.ID)
		for _, rec := range []*httptest.ResponseRecorder{e.hold(third, ev.ID, seats[6]), e.best(third, ev.ID, regular, 1)} {
			d = holdErr(t, rec, http.StatusConflict, c.code)
			assert.Equal(t, c.status, d["event_status"])
			assert.NotNil(t, d["sales_open_at"])
			assert.NotNil(t, d["sales_close_at"])
		}
	}
	exec(t, `UPDATE events SET sales_close_at = kickoff_at WHERE id = $1`, ev.ID)

	// A cancelled event: its seats are AVAILABLE again, and only the status check stops a
	// new hold. EVENT_NOT_ON_SALE, seats untouched.
	want(t, e.do("POST", fmt.Sprintf("/admin/events/%d/cancel", ev.ID), nil, ev.Token), http.StatusOK)
	c := checkInventory(t, ev.ID)
	require.Equal(t, 0, c.Held)
	d = holdErr(t, e.hold(third, ev.ID, seats[6]), http.StatusConflict, domain.CodeEventNotOnSale)
	assert.Equal(t, "CANCELLED", d["event_status"])
	holdErr(t, e.best(third, ev.ID, regular, 1), http.StatusConflict, domain.CodeEventNotOnSale)
	assert.Equal(t, c, checkInventory(t, ev.ID), "nothing was held on the cancelled event")
}

func make51() []int64 {
	out := make([]int64, 51)
	for i := range out {
		out[i] = int64(i + 1)
	}
	return out
}

// Release answers 409 while a payment is live, and 409 once the seats are bought.
func TestHolds_ReleaseConflicts(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 2, 4)
	ev := e.publishedEvent(v)
	seats := eventSeatIDs(t, ev.ID, v.BlockIDs[0])
	fanID, fan := e.newFan()
	regular := ev.Cats["Regular"]

	h := want(t, e.hold(fan, ev.ID, seats[4], seats[5]), http.StatusCreated)
	holdID := id64(h["id"])
	bookingID, ref := insertBooking(t, ev.ID, fanID, "PENDING_PAYMENT", time.Now(), nil,
		bookingItem{seats[4], regular, 100_000}, bookingItem{seats[5], regular, 100_000})
	exec(t, `UPDATE bookings SET hold_id = $2 WHERE id = $1`, bookingID, holdID)
	got := want(t, e.do("GET", fmt.Sprintf("/holds/%d", holdID), nil, fan), http.StatusOK)
	assert.Equal(t, ref, got["booking_reference"])

	pay := insertPayment(t, bookingID, "PENDING", 200_000, time.Now())
	d := holdErr(t, e.do("DELETE", fmt.Sprintf("/holds/%d", holdID), nil, fan), http.StatusConflict, domain.CodePaymentInProgress)
	assert.Equal(t, obj{"payment_id": float64(pay), "payment_status": "PENDING"}, d)
	assert.Equal(t, 2, checkInventory(t, ev.ID).Held, "still held")

	// The payment fails: the release goes through and cancels the unpaid booking.
	exec(t, `UPDATE payments SET status = 'FAILED' WHERE id = $1`, pay)
	require.Equal(t, http.StatusNoContent, e.do("DELETE", fmt.Sprintf("/holds/%d", holdID), nil, fan).Code)
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM bookings WHERE id = $1 AND status = 'CANCELLED'`, bookingID))
	assert.Equal(t, 0, checkInventory(t, ev.ID).Held)

	// Bought: HOLD_NOT_ACTIVE.
	h = want(t, e.hold(fan, ev.ID, seats[6]), http.StatusCreated)
	exec(t, `UPDATE event_seats SET status = 'SOLD', hold_expires_at = NULL WHERE hold_id = $1`, id64(h["id"]))
	exec(t, `UPDATE holds SET status = 'CONVERTED' WHERE id = $1`, id64(h["id"]))
	d = holdErr(t, e.do("DELETE", fmt.Sprintf("/holds/%d", id64(h["id"])), nil, fan), http.StatusConflict, domain.CodeHoldNotActive)
	assert.Equal(t, "CONVERTED", d["hold_status"])
	assert.EqualValues(t, id64(h["id"]), d["hold_id"])
	assert.Equal(t, 1, checkInventory(t, ev.ID).Sold)
}

// The fee and total on a hold: basis points of the subtotal, rounded to the nearest whole
// shilling (M-Pesa takes nothing smaller), half a shilling rounding up.
func TestHolds_FeeAndTotal(t *testing.T) {
	e := newEnv(t, withHoldPolicy(func(p *booking.Policy) { p.BookingFeeBPS = 250 }))
	v := e.newVenue(1, 2, 4)
	ev := e.readyEvent(v)
	// KES 1,500: 2.5% of one seat is 37.50 shillings, which must come out as 38.00.
	want(t, e.do("PATCH", fmt.Sprintf("/admin/categories/%d", ev.Cats["Regular"]), obj{"price_cents": 150_000}, ev.Token), http.StatusOK)
	want(t, e.publish(ev), http.StatusOK)
	seats := eventSeatIDs(t, ev.ID, v.BlockIDs[0])
	_, fan := e.newFan()

	one := want(t, e.hold(fan, ev.ID, seats[4]), http.StatusCreated)
	assert.EqualValues(t, 150_000, one["subtotal_cents"])
	assert.EqualValues(t, 3_800, one["fee_cents"])
	assert.EqualValues(t, 153_800, one["total_cents"])

	// Three seats: the fee is worked out once, on the subtotal (112.50 shillings → 113.00),
	// not three times 38.00.
	three := want(t, e.best(fan, ev.ID, ev.Cats["Regular"], 3), http.StatusCreated)
	assert.EqualValues(t, 450_000, three["subtotal_cents"])
	assert.EqualValues(t, 11_300, three["fee_cents"])
	assert.EqualValues(t, 461_300, three["total_cents"])

	got := want(t, e.do("GET", fmt.Sprintf("/holds/%d", id64(three["id"])), nil, fan), http.StatusOK)
	assert.EqualValues(t, 11_300, got["fee_cents"])
	mine := items(want(t, e.do("GET", "/me/holds", nil, fan), http.StatusOK))
	require.Len(t, mine, 2)
	for _, m := range mine {
		h := m.(obj)
		assert.EqualValues(t, id64(h["subtotal_cents"])+id64(h["fee_cents"]), h["total_cents"])
		assert.Positive(t, id64(h["fee_cents"]))
		assert.Zero(t, id64(h["fee_cents"])%100, "the fee is always a whole number of shillings")
	}
	// Every amount is a whole number in the JSON itself.
	assert.NotRegexp(t, `_cents":-?\d+[.eE]`, e.do("GET", "/me/holds", nil, fan).Body.String())
	checkInventory(t, ev.ID)
}

// The seat map's change poll sees a hold, a release, a take-over and a sweep: "M" to the
// holder, "H" to everyone else, "A" when the seat is free again.
func TestHolds_SeatMapPollSeesEveryChange(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 2, 4)
	ev := e.publishedEvent(v)
	age(t, ev.ID, time.Hour)
	block := v.BlockIDs[0]
	seats := eventSeatIDs(t, ev.ID, block)
	_, holder := e.newFan()
	_, other := e.newFan()
	poll := func(marker, token string) (map[int64]string, string) {
		o := want(t, e.seats(ev.ID, block, marker, token), http.StatusOK)
		return changeCodes(t, o), o["marker"].(string)
	}
	// Changes are reported for 20 seconds (the poll's look-back), so each step looks only
	// at the seats it is about.
	pick := func(m map[int64]string, ids ...int64) []string {
		out := []string{}
		for _, id := range ids {
			out = append(out, m[id])
		}
		return out
	}
	marker := want(t, e.seats(ev.ID, block, "", ""), http.StatusOK)["marker"].(string)
	none, _ := poll(marker, other)
	require.Empty(t, none)

	// Hold.
	h := want(t, e.hold(holder, ev.ID, seats[4], seats[5]), http.StatusCreated)
	mine, _ := poll(marker, holder)
	theirs, _ := poll(marker, other)
	anon, _ := poll(marker, "")
	assert.Equal(t, map[int64]string{seats[4]: "M", seats[5]: "M"}, mine)
	assert.Equal(t, map[int64]string{seats[4]: "H", seats[5]: "H"}, theirs)
	assert.Equal(t, map[int64]string{seats[4]: "H", seats[5]: "H"}, anon)
	full := seatCodes(t, want(t, e.seats(ev.ID, block, "", holder), http.StatusOK))
	assert.Equal(t, []string{"M", "M", "A"}, pick(full, seats[4], seats[5], seats[6]))

	// Release.
	require.Equal(t, http.StatusNoContent, e.do("DELETE", fmt.Sprintf("/holds/%d", id64(h["id"])), nil, holder).Code)
	for _, token := range []string{holder, other, ""} {
		m, _ := poll(marker, token)
		assert.Equal(t, []string{"A", "A"}, pick(m, seats[4], seats[5]))
	}

	// A lapsed hold is "A" to everyone, its former holder included, with no sweep; taken
	// over by another fan it is that fan's "M" and "H" to its former holder.
	old := want(t, e.hold(holder, ev.ID, seats[6]), http.StatusCreated)
	lapseHold(t, id64(old["id"]))
	m, _ := poll(marker, holder)
	assert.Equal(t, "A", m[seats[6]])
	want(t, e.hold(other, ev.ID, seats[6]), http.StatusCreated)
	m, _ = poll(marker, holder)
	assert.Equal(t, "H", m[seats[6]])
	m, _ = poll(marker, other)
	assert.Equal(t, "M", m[seats[6]])

	// A sweep: the seat's row is written (AVAILABLE, fresh stamp) and the poll reports it.
	swept := want(t, e.hold(holder, ev.ID, seats[7]), http.StatusCreated)
	lapseHold(t, id64(swept["id"]))
	before := queryInt(t, `SELECT extract(epoch FROM updated_at)::int FROM event_seats WHERE id = $1`, seats[7])
	time.Sleep(1100 * time.Millisecond)
	res, err := e.holds.Sweep(context.Background())
	require.NoError(t, err)
	require.GreaterOrEqual(t, res.Seats, 1)
	assert.Greater(t, queryInt(t, `SELECT extract(epoch FROM updated_at)::int FROM event_seats WHERE id = $1`, seats[7]), before)
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM event_seats WHERE id = $1 AND status = 'AVAILABLE' AND hold_id IS NULL`, seats[7]))
	m, _ = poll(marker, "")
	assert.Equal(t, "A", m[seats[7]])
	// Nothing about holds changed the event row: the poll is still answered with a delta.
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM events WHERE id = $1 AND updated_at > now() - interval '30 minutes'`, ev.ID),
		"a hold, release, take-over or sweep must not write the event row")
	checkInventory(t, ev.ID)
}

// Creating holds is rate limited per user and per IP; 429 carries Retry-After.
func TestHolds_RateLimited(t *testing.T) {
	t.Run("per user", func(t *testing.T) {
		e := newEnv(t, withLimits(func(l *httpapi.RateLimits) { l.HoldPerUser = ratelimit.Rule{Limit: 3, Window: time.Minute} }))
		v := e.newVenue(1, 2, 6)
		ev := e.publishedEvent(v)
		seats := eventSeatIDs(t, ev.ID, v.BlockIDs[0])
		_, fan := e.newFan()
		_, other := e.newFan()

		// Three tries are let through whatever their outcome; create and best available
		// share the count.
		want(t, e.hold(fan, ev.ID, seats[6]), http.StatusCreated)
		wantError(t, e.hold(fan, ev.ID, seats[6]), http.StatusConflict, domain.CodeSeatsUnavailable)
		want(t, e.best(fan, ev.ID, ev.Cats["Regular"], 1), http.StatusCreated)
		for _, rec := range []*httptest.ResponseRecorder{e.hold(fan, ev.ID, seats[10]), e.best(fan, ev.ID, ev.Cats["Regular"], 1)} {
			d := holdErr(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
			retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
			require.NoError(t, err)
			assert.True(t, retry >= 1 && retry <= 60, "Retry-After %d", retry)
			assert.EqualValues(t, retry, d["retry_after_seconds"])
		}
		// Another fan is not affected, reading and releasing are never limited, and the
		// window ends.
		want(t, e.hold(other, ev.ID, seats[11]), http.StatusCreated)
		mine := items(want(t, e.do("GET", "/me/holds", nil, fan), http.StatusOK))
		require.Len(t, mine, 2)
		for _, m := range mine {
			require.Equal(t, http.StatusNoContent, e.do("DELETE", fmt.Sprintf("/holds/%d", id64(m.(obj)["id"])), nil, fan).Code)
		}
		e.clock.Advance(61 * time.Second)
		want(t, e.hold(fan, ev.ID, seats[9]), http.StatusCreated)
		checkInventory(t, ev.ID)
	})

	t.Run("per IP", func(t *testing.T) {
		e := newEnv(t, withLimits(func(l *httpapi.RateLimits) { l.HoldPerIP = ratelimit.Rule{Limit: 2, Window: time.Minute} }))
		v := e.newVenue(1, 2, 6)
		ev := e.publishedEvent(v)
		seats := eventSeatIDs(t, ev.ID, v.BlockIDs[0])
		for i := 0; i < 2; i++ {
			_, fan := e.newFan()
			want(t, e.hold(fan, ev.ID, seats[6+i]), http.StatusCreated)
		}
		_, fan := e.newFan()
		rec := e.hold(fan, ev.ID, seats[9])
		wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
		assert.NotEmpty(t, rec.Header().Get("Retry-After"))
		// From another address the same fan gets through.
		e.remoteAddr = "203.0.113.9:40000"
		want(t, e.hold(fan, ev.ID, seats[9]), http.StatusCreated)
		checkInventory(t, ev.ID)
	})
}

// The milestone's done-when, through the real router: 1,000 hold requests from 1,000 signed-in
// fans at the same 10 seats, 48 database transactions at a time. Exactly 10 answers are 201,
// exactly 990 are 409 SEATS_UNAVAILABLE naming the seat, and there is no other answer.
func TestHolds_ThousandRequestsThroughTheRouter(t *testing.T) {
	const requests, seatCount = 1000, 10
	cfg, err := pgxpool.ParseConfig(db.URL)
	require.NoError(t, err)
	cfg.MaxConns = 48
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "15000"
	wide, err := pgxpool.NewWithConfig(context.Background(), cfg)
	require.NoError(t, err)
	defer wide.Close()

	e := newEnv(t, func(c *envConfig) { c.holdDB = wide }, withLimits(func(l *httpapi.RateLimits) {
		// One address stands for a whole stadium here.
		l.HoldPerIP = ratelimit.Rule{}
	}))
	v := e.newVenue(1, 2, seatCount)
	ev := e.publishedEvent(v)
	target := eventSeatIDs(t, ev.ID, v.BlockIDs[0])[seatCount:] // row B, Regular
	tokens := make([]string, requests)
	for i := range tokens {
		_, tokens[i] = e.newFan()
	}

	type answer struct {
		status int
		code   string
		lost   []int64
		holdID int64
	}
	answers := make([]answer, requests)
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			rec := e.hold(tokens[i], ev.ID, target[i%seatCount])
			a := answer{status: rec.Code}
			o := obj{}
			_ = json.Unmarshal(rec.Body.Bytes(), &o)
			if errObj, ok := o["error"].(obj); ok {
				a.code, _ = errObj["code"].(string)
				if d, ok := errObj["details"].(obj); ok && d["unavailable_seat_ids"] != nil {
					a.lost = ids(d["unavailable_seat_ids"])
				}
			} else if o["id"] != nil {
				a.holdID = id64(o["id"])
			}
			answers[i] = a
		}()
	}
	started := time.Now()
	close(gate)
	wg.Wait()
	took := time.Since(started)

	byStatus := map[int]int{}
	winners := map[int64]int{}
	for i, a := range answers {
		byStatus[a.status]++
		seat := target[i%seatCount]
		switch a.status {
		case http.StatusCreated:
			winners[seat]++
		case http.StatusConflict:
			assert.Equal(t, "SEATS_UNAVAILABLE", a.code)
			assert.Equal(t, []int64{seat}, a.lost)
		}
	}
	heldTwice := 0
	for _, seat := range target {
		assert.Equal(t, 1, winners[seat], "seat %d", seat)
		if winners[seat] > 1 {
			heldTwice++
		}
	}
	t.Logf("requests=%d answers: 201=%d 409=%d other=%d seats_held_twice=%d took=%s",
		requests, byStatus[201], byStatus[409], requests-byStatus[201]-byStatus[409], heldTwice, took.Round(time.Millisecond))
	assert.Equal(t, map[int]int{http.StatusCreated: seatCount, http.StatusConflict: requests - seatCount}, byStatus)
	c := checkInventory(t, ev.ID)
	assert.Equal(t, seatCount, c.Held)
	assert.Equal(t, seatCount, queryInt(t, `SELECT count(*) FROM holds WHERE event_id = $1`, ev.ID))
}
