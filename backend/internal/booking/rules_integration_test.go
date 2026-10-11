//go:build integration

package booking_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/booking"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

func details[T any](t testing.TB, err error, code domain.Code) T {
	t.Helper()
	var de *domain.Error
	require.True(t, errors.As(err, &de), "want a domain error, got %v", err)
	require.Equal(t, code, de.Code, "%v", err)
	d, ok := de.Details.(T)
	require.True(t, ok, "details are %T", de.Details)
	return d
}

func TestHold_HappyPath(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 2, SeatsPerRow: 5})
	svc := newService(t)
	ctx := timeout(t)
	user := newUser(t)
	seats := f.row(1)

	before := queryText(t, `SELECT max(updated_at)::text FROM event_seats WHERE event_id = $1`, f.EventID)
	h, err := svc.Hold(ctx, user, f.EventID, []int64{seats[3], seats[1]})
	require.NoError(t, err)

	assert.Equal(t, domain.HoldActive, h.Status)
	assert.InDelta(t, 600, h.RemainingSeconds, 2)
	assert.Nil(t, h.BookingReference)
	assert.Nil(t, h.Row.ExtendedAt)
	assert.EqualValues(t, 200000, h.SubtotalCents)
	require.Len(t, h.Seats, 2)
	// Ordered by block, row, seat number, whatever order they were asked for in.
	assert.Equal(t, []int64{seats[1], seats[3]}, []int64{h.Seats[0].EventSeatID, h.Seats[1].EventSeatID})
	assert.Equal(t, "B2", h.Seats[0].SeatLabel)
	assert.Equal(t, "Regular", h.Seats[0].CategoryName)
	assert.Equal(t, "N", h.Seats[0].StandCode)

	// The seats carry the hold and the same expiry as the hold row, to the microsecond, and
	// a fresh change stamp.
	assert.Equal(t, 2, queryInt(t, `
		SELECT count(*) FROM event_seats es JOIN holds h ON h.id = es.hold_id
		WHERE es.id = ANY($1) AND es.status = 'HELD' AND es.hold_id = $2 AND es.hold_expires_at = h.expires_at
		  AND es.updated_at > $3::timestamptz AND es.version = 1`, []int64{seats[1], seats[3]}, h.Row.ID, before))

	// Validation: none, too many, a repeat, a bad id.
	for name, ids := range map[string][]int64{
		"none": {}, "repeat": {seats[0], seats[0]}, "zero id": {0}, "too many": make([]int64, 51),
	} {
		_, err := svc.Hold(ctx, user, f.EventID, ids)
		assert.Equal(t, domain.CodeValidationFailed, domain.CodeOf(err), name)
	}
	assert.Equal(t, 2, f.check().Held)
}

// The event must be on sale, judged under the event lock by the database clock.
func TestHold_EventMustBeOnSale(t *testing.T) {
	svc := newService(t)
	ctx := timeout(t)
	user := newUser(t)

	cases := map[string]struct {
		sql    string
		code   domain.Code
		status domain.EventStatus
	}{
		"before the window":             {`UPDATE events SET sales_open_at = now() + interval '1 hour' WHERE id = $1`, domain.CodeSalesNotOpen, domain.EventPublished},
		"after the window":              {`UPDATE events SET sales_close_at = now() - interval '1 second' WHERE id = $1`, domain.CodeSalesClosed, domain.EventPublished},
		"stored PUBLISHED, window shut": {`UPDATE events SET status = 'PUBLISHED', sales_open_at = now() + interval '1 day' WHERE id = $1`, domain.CodeSalesNotOpen, domain.EventPublished},
		"cancelled":                     {`UPDATE events SET status = 'CANCELLED' WHERE id = $1`, domain.CodeEventNotOnSale, domain.EventCancelled},
		"completed":                     {`UPDATE events SET status = 'COMPLETED' WHERE id = $1`, domain.CodeEventNotOnSale, domain.EventCompleted},
		"sold out":                      {`UPDATE events SET status = 'SOLD_OUT' WHERE id = $1`, domain.CodeEventNotOnSale, domain.EventSoldOut},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 4})
			exec(t, c.sql, f.EventID)
			for what, call := range map[string]func() error{
				"hold": func() error { _, err := svc.Hold(ctx, user, f.EventID, f.row(0)[:2]); return err },
				"best": func() error { _, err := svc.HoldBestAvailable(ctx, user, f.EventID, f.CategoryID, 2); return err },
			} {
				d := details[domain.SalesWindowDetails](t, call(), c.code)
				assert.Equal(t, c.status, d.EventStatus, what)
				assert.NotNil(t, d.SalesOpenAt, what)
				assert.NotNil(t, d.SalesCloseAt, what)
			}
			// Seats untouched, no hold row.
			c := f.check()
			assert.Equal(t, 0, c.Held)
			assert.Equal(t, 4, c.Available)
			assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM holds WHERE event_id = $1`, f.EventID))
		})
	}

	t.Run("stored PUBLISHED inside the window is on sale", func(t *testing.T) {
		f := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 4})
		exec(t, `UPDATE events SET status = 'PUBLISHED' WHERE id = $1`, f.EventID)
		_, err := svc.Hold(ctx, user, f.EventID, f.row(0)[:1])
		require.NoError(t, err)
	})
	t.Run("a draft and a missing event are not found", func(t *testing.T) {
		f := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 4})
		exec(t, `UPDATE events SET status = 'DRAFT' WHERE id = $1`, f.EventID)
		_, err := svc.Hold(ctx, user, f.EventID, f.row(0)[:1])
		assert.Equal(t, domain.CodeNotFound, domain.CodeOf(err))
		_, err = svc.Hold(ctx, user, 1<<60, []int64{1})
		assert.Equal(t, domain.CodeNotFound, domain.CodeOf(err))
		_, err = svc.HoldBestAvailable(ctx, user, f.EventID, f.CategoryID, 1)
		assert.Equal(t, domain.CodeNotFound, domain.CodeOf(err))
		exec(t, `UPDATE events SET status = 'ON_SALE' WHERE id = $1`, f.EventID)
	})
}

// The limit counts live holds, paid bookings and bookings with a payment in flight; it does
// not count what was released, what lapsed, or other events.
func TestHold_TicketLimit(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 3, SeatsPerRow: 10, MaxPerUser: 6})
	other := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 10, MaxPerUser: 6})
	svc := newService(t)
	ctx := timeout(t)
	user := newUser(t)
	a, b, c := f.row(0), f.row(1), f.row(2)

	// Seven at once is over the limit before anything is held.
	_, err := svc.Hold(ctx, user, f.EventID, a[:7])
	assert.Equal(t, domain.TicketLimitDetails{Allowed: 6, HeldOrBought: 0, Requested: 7, Remaining: 6},
		details[domain.TicketLimitDetails](t, err, domain.CodeTicketLimitExceeded))

	first, err := svc.Hold(ctx, user, f.EventID, a[:4])
	require.NoError(t, err)
	_, err = svc.Hold(ctx, user, f.EventID, b[:3])
	d := details[domain.TicketLimitDetails](t, err, domain.CodeTicketLimitExceeded)
	assert.Equal(t, domain.TicketLimitDetails{Allowed: 6, HeldOrBought: 4, Requested: 3, Remaining: 2}, d)
	_, err = svc.HoldBestAvailable(ctx, user, f.EventID, f.CategoryID, 3)
	assert.Equal(t, domain.CodeTicketLimitExceeded, domain.CodeOf(err))
	// Exactly up to the limit is fine, and another event has its own count.
	second, err := svc.Hold(ctx, user, f.EventID, b[:2])
	require.NoError(t, err)
	_, err = svc.Hold(ctx, user, other.EventID, other.row(0)[:6])
	require.NoError(t, err)
	_, err = svc.Hold(ctx, user, f.EventID, c[:1])
	assert.Equal(t, domain.TicketLimitDetails{Allowed: 6, HeldOrBought: 6, Requested: 1, Remaining: 0},
		details[domain.TicketLimitDetails](t, err, domain.CodeTicketLimitExceeded))

	// Released seats stop counting; lapsed ones too, sweeper or no sweeper.
	require.NoError(t, svc.Release(ctx, user, second.Row.ID))
	lapse(t, first.Row.ID)
	third, err := svc.Hold(ctx, user, f.EventID, c[:6])
	require.NoError(t, err)

	// Bought seats count. The hold becomes CONVERTED and its seats SOLD, as the payment
	// flow will do it.
	paid := newUser(t)
	h, err := svc.Hold(ctx, paid, f.EventID, b[4:8])
	require.NoError(t, err)
	insertBooking(t, f, paid, &h.Row.ID, "PAID", b[4:8]...)
	exec(t, `UPDATE event_seats SET status = 'SOLD', hold_expires_at = NULL WHERE hold_id = $1`, h.Row.ID)
	exec(t, `UPDATE holds SET status = 'CONVERTED' WHERE id = $1`, h.Row.ID)
	_, err = svc.Hold(ctx, paid, f.EventID, b[8:10])
	require.NoError(t, err, "4 bought + 2 held = 6")
	_, err = svc.Hold(ctx, paid, f.EventID, a[9:10])
	assert.Equal(t, domain.TicketLimitDetails{Allowed: 6, HeldOrBought: 6, Requested: 1, Remaining: 0},
		details[domain.TicketLimitDetails](t, err, domain.CodeTicketLimitExceeded))

	// A booking still being paid for counts once, not twice (its seats are also held), and
	// keeps counting if the hold lapses while M-Pesa is still deciding.
	paying := newUser(t)
	hp, err := svc.Hold(ctx, paying, f.EventID, a[4:8])
	require.NoError(t, err)
	bk := insertBooking(t, f, paying, &hp.Row.ID, "PENDING_PAYMENT", a[4:8]...)
	insertPayment(t, bk, "PENDING")
	_, err = svc.Hold(ctx, paying, f.EventID, []int64{a[8]})
	require.NoError(t, err, "4 being paid for + 1 = 5, counted once")
	lapse(t, hp.Row.ID)
	_, err = svc.Hold(ctx, paying, f.EventID, []int64{b[2], b[3]})
	assert.Equal(t, domain.TicketLimitDetails{Allowed: 6, HeldOrBought: 5, Requested: 2, Remaining: 1},
		details[domain.TicketLimitDetails](t, err, domain.CodeTicketLimitExceeded))

	_ = third
	f.check()
	other.check()
}

func TestBestAvailable_PicksNeighboursInOneRow(t *testing.T) {
	f := newFixture(t, fixtureOpts{Blocks: 3, Rows: 3, SeatsPerRow: 8, MaxPerUser: 50})
	svc := newService(t)
	ctx := timeout(t)

	// The middle block of the stand is the best block; in it the front row; in it the run
	// nearest the middle of the row. Runs are cut from seat 1, so with 8 seats and groups of
	// 3 the runs are 1-3 and 4-6, and 4-6 is nearer the middle.
	h, err := svc.HoldBestAvailable(ctx, newUser(t), f.EventID, f.CategoryID, 3)
	require.NoError(t, err)
	require.Len(t, h.Seats, 3)
	assert.Equal(t, f.BlockIDs[1], h.Seats[0].BlockID, "the central block")
	assert.Equal(t, "A", h.Seats[0].RowLabel, "the front row")
	assert.Equal(t, []int32{4, 5, 6}, []int32{h.Seats[0].SeatNumber, h.Seats[1].SeatNumber, h.Seats[2].SeatNumber})

	// Next best: the other run of the same row.
	h, err = svc.HoldBestAvailable(ctx, newUser(t), f.EventID, f.CategoryID, 3)
	require.NoError(t, err)
	assert.Equal(t, []int32{1, 2, 3}, []int32{h.Seats[0].SeatNumber, h.Seats[1].SeatNumber, h.Seats[2].SeatNumber})
	assert.Equal(t, "A", h.Seats[0].RowLabel)

	// Seats 7 and 8 of row A are free but are only two: a group of three goes to row B
	// rather than being split.
	h, err = svc.HoldBestAvailable(ctx, newUser(t), f.EventID, f.CategoryID, 3)
	require.NoError(t, err)
	assert.Equal(t, "B", h.Seats[0].RowLabel)
	// A pair fits there.
	h, err = svc.HoldBestAvailable(ctx, newUser(t), f.EventID, f.CategoryID, 2)
	require.NoError(t, err)
	assert.Equal(t, "A", h.Seats[0].RowLabel)
	assert.Equal(t, []int32{7, 8}, []int32{h.Seats[0].SeatNumber, h.Seats[1].SeatNumber})

	// A category of another event, or none: not found.
	other := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 2})
	_, err = svc.HoldBestAvailable(ctx, newUser(t), f.EventID, other.CategoryID, 2)
	assert.Equal(t, domain.CodeNotFound, domain.CodeOf(err))
	for _, q := range []int{0, 51} {
		_, err = svc.HoldBestAvailable(ctx, newUser(t), f.EventID, f.CategoryID, q)
		assert.Equal(t, domain.CodeValidationFailed, domain.CodeOf(err))
	}
	f.check()
}

func TestBestAvailable_NeverAcrossAGapAndSeatTypes(t *testing.T) {
	f := newFixture(t, fixtureOpts{Blocks: 1, Rows: 2, SeatsPerRow: 6, MaxPerUser: 50})
	svc := newService(t)
	ctx := timeout(t)
	a, b := f.row(0), f.row(1)

	// Row A: seat 3 is sold, so the free seats are 1-2 and 4-6. Row B: seat 3 is switched
	// off (no inventory row), seat 5 is a wheelchair space and seat 6 its companion seat.
	exec(t, `UPDATE event_seats SET status = 'SOLD' WHERE id = $1`, a[2])
	exec(t, `UPDATE seats SET is_active = FALSE WHERE id = (SELECT seat_id FROM event_seats WHERE id = $1)`, b[2])
	exec(t, `DELETE FROM event_seats WHERE id = $1`, b[2])
	exec(t, `UPDATE seats SET seat_type = 'ACCESSIBLE' WHERE id = (SELECT seat_id FROM event_seats WHERE id = $1)`, b[4])
	exec(t, `UPDATE seats SET seat_type = 'COMPANION' WHERE id = (SELECT seat_id FROM event_seats WHERE id = $1)`, b[5])

	// Four together do not exist anywhere: A has 2 + 3 around the sold seat, B has 2 + 1
	// standard seats around the gap. Eight standard seats are free in all.
	_, err := svc.HoldBestAvailable(ctx, newUser(t), f.EventID, f.CategoryID, 4)
	d := details[domain.NoAdjacentSeatsDetails](t, err, domain.CodeNoAdjacentSeats)
	assert.Equal(t, domain.NoAdjacentSeatsDetails{CategoryID: f.CategoryID, Quantity: 4, Available: 10}, d)
	assert.Equal(t, 0, f.check().Held, "nothing was held")

	// Three together: only A 4-6.
	h, err := svc.HoldBestAvailable(ctx, newUser(t), f.EventID, f.CategoryID, 3)
	require.NoError(t, err)
	assert.Equal(t, []int64{a[3], a[4], a[5]}, []int64{h.Seats[0].EventSeatID, h.Seats[1].EventSeatID, h.Seats[2].EventSeatID})
	// Two together: A 1-2, then B 1-2. Never B 4-5 (5 is accessible) or B 5-6.
	for _, want := range [][]int64{{a[0], a[1]}, {b[0], b[1]}} {
		h, err = svc.HoldBestAvailable(ctx, newUser(t), f.EventID, f.CategoryID, 2)
		require.NoError(t, err)
		assert.Equal(t, want, []int64{h.Seats[0].EventSeatID, h.Seats[1].EventSeatID})
	}
	// One seat: B 4, the last standard seat.
	h, err = svc.HoldBestAvailable(ctx, newUser(t), f.EventID, f.CategoryID, 1)
	require.NoError(t, err)
	assert.Equal(t, b[3], h.Seats[0].EventSeatID)
	// Only the accessible seat and its companion are left: best available never hands
	// them out, and says how many seats are still free.
	_, err = svc.HoldBestAvailable(ctx, newUser(t), f.EventID, f.CategoryID, 1)
	d = details[domain.NoAdjacentSeatsDetails](t, err, domain.CodeNoAdjacentSeats)
	assert.Equal(t, 2, d.Available)
	// They can still be picked by hand.
	_, err = svc.Hold(ctx, newUser(t), f.EventID, []int64{b[4], b[5]})
	require.NoError(t, err)

	// Restricted view: used only when the category has no run of standard seats.
	g := newFixture(t, fixtureOpts{Blocks: 1, Rows: 1, SeatsPerRow: 6, MaxPerUser: 50})
	r := g.row(0)
	exec(t, `UPDATE seats SET seat_type = 'RESTRICTED_VIEW' WHERE id IN (SELECT seat_id FROM event_seats WHERE id = ANY($1))`, r[:3])
	h, err = svc.HoldBestAvailable(ctx, newUser(t), g.EventID, g.CategoryID, 2)
	require.NoError(t, err)
	assert.Equal(t, []int64{r[3], r[4]}, []int64{h.Seats[0].EventSeatID, h.Seats[1].EventSeatID}, "standard seats first")
	h, err = svc.HoldBestAvailable(ctx, newUser(t), g.EventID, g.CategoryID, 2)
	require.NoError(t, err)
	assert.Equal(t, []int64{r[0], r[1]}, []int64{h.Seats[0].EventSeatID, h.Seats[1].EventSeatID}, "restricted view when nothing else is left")
	assert.Equal(t, "RESTRICTED_VIEW", h.Seats[0].SeatType)
	g.check()

	// A lapsed hold's seats are free to best available too, sweeper or no sweeper.
	k := newFixture(t, fixtureOpts{Blocks: 1, Rows: 1, SeatsPerRow: 2})
	old, err := svc.Hold(ctx, newUser(t), k.EventID, k.row(0))
	require.NoError(t, err)
	_, err = svc.HoldBestAvailable(ctx, newUser(t), k.EventID, k.CategoryID, 2)
	assert.Equal(t, 0, details[domain.NoAdjacentSeatsDetails](t, err, domain.CodeNoAdjacentSeats).Available)
	lapse(t, old.Row.ID)
	h, err = svc.HoldBestAvailable(ctx, newUser(t), k.EventID, k.CategoryID, 2)
	require.NoError(t, err)
	assert.Len(t, h.Seats, 2)
	assert.Equal(t, "EXPIRED", holdStatus(t, old.Row.ID))
	k.check()
}

func TestRelease(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 2, SeatsPerRow: 6, MaxPerUser: 12})
	svc := newService(t)
	ctx := timeout(t)
	me, stranger := newUser(t), newUser(t)
	seats := f.row(0)

	mine, err := svc.Hold(ctx, me, f.EventID, seats[:2])
	require.NoError(t, err)
	theirs, err := svc.Hold(ctx, stranger, f.EventID, seats[2:4])
	require.NoError(t, err)

	// Someone else's hold, or none: not found, and nothing changes.
	assert.Equal(t, domain.CodeNotFound, domain.CodeOf(svc.Release(ctx, me, theirs.Row.ID)))
	assert.Equal(t, domain.CodeNotFound, domain.CodeOf(svc.Release(ctx, me, 1<<60)))
	_, err = svc.Get(ctx, me, theirs.Row.ID)
	assert.Equal(t, domain.CodeNotFound, domain.CodeOf(err))
	assert.Equal(t, 4, f.check().Held)

	// Mine: my two seats are free again, with a fresh stamp; theirs are untouched.
	before := queryText(t, `SELECT max(updated_at)::text FROM event_seats WHERE id = ANY($1)`, seats[:2])
	require.NoError(t, svc.Release(ctx, me, mine.Row.ID))
	state := seatStates(t, seats)
	assert.Equal(t, "AVAILABLE", state[seats[0]].Status)
	assert.Equal(t, "AVAILABLE", state[seats[1]].Status)
	assert.Nil(t, state[seats[0]].HoldID)
	assert.Equal(t, theirs.Row.ID, *state[seats[2]].HoldID)
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM event_seats WHERE id = ANY($1) AND updated_at > $2::timestamptz`, seats[:2], before))
	assert.Equal(t, "RELEASED", holdStatus(t, mine.Row.ID))
	got, err := svc.Get(ctx, me, mine.Row.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.HoldReleased, got.Status)
	assert.Empty(t, got.Seats)
	assert.Zero(t, got.RemainingSeconds)
	// Again: nothing to do, no error.
	require.NoError(t, svc.Release(ctx, me, mine.Row.ID))
	assert.Equal(t, 2, f.check().Held)

	// A lapsed hold whose seat someone else took over: releasing it frees only what it
	// still has.
	lapsed, err := svc.Hold(ctx, me, f.EventID, seats[:2])
	require.NoError(t, err)
	lapse(t, lapsed.Row.ID)
	taker, err := svc.Hold(ctx, stranger, f.EventID, seats[:1])
	require.NoError(t, err)
	require.NoError(t, svc.Release(ctx, me, lapsed.Row.ID))
	state = seatStates(t, seats)
	assert.Equal(t, taker.Row.ID, *state[seats[0]].HoldID, "the new holder keeps the seat")
	assert.Equal(t, "HELD", state[seats[0]].Status)
	assert.Equal(t, "AVAILABLE", state[seats[1]].Status)
	assert.Equal(t, "EXPIRED", holdStatus(t, lapsed.Row.ID), "a hold released after its time is recorded as expired")

	// With an unpaid booking: the booking is cancelled with the hold.
	booked, err := svc.Hold(ctx, me, f.EventID, seats[4:6])
	require.NoError(t, err)
	bk := insertBooking(t, f, me, &booked.Row.ID, "PENDING_PAYMENT", seats[4:6]...)
	insertPayment(t, bk, "FAILED")
	got, err = svc.Get(ctx, me, booked.Row.ID)
	require.NoError(t, err)
	require.NotNil(t, got.BookingReference)
	require.NoError(t, svc.Release(ctx, me, booked.Row.ID))
	assert.Equal(t, "CANCELLED", queryText(t, `SELECT status FROM bookings WHERE id = $1`, bk))

	// With a live payment: refused, and nothing changes.
	for _, status := range []string{"INITIATED", "PENDING"} {
		paying, err := svc.Hold(ctx, me, f.EventID, f.row(1)[:2])
		require.NoError(t, err)
		bk := insertBooking(t, f, me, &paying.Row.ID, "PENDING_PAYMENT", f.row(1)[:2]...)
		pay := insertPayment(t, bk, status)
		err = svc.Release(ctx, me, paying.Row.ID)
		assert.Equal(t, domain.PaymentInProgressDetails{PaymentID: pay, PaymentStatus: status},
			details[domain.PaymentInProgressDetails](t, err, domain.CodePaymentInProgress))
		assert.Equal(t, "ACTIVE", holdStatus(t, paying.Row.ID))
		assert.Equal(t, "PENDING_PAYMENT", queryText(t, `SELECT status FROM bookings WHERE id = $1`, bk))
		assert.Equal(t, "HELD", seatStates(t, f.row(1)[:1])[f.row(1)[0]].Status)
		// The payment ends; now it can be released.
		exec(t, `UPDATE payments SET status = 'TIMEOUT' WHERE id = $1`, pay)
		require.NoError(t, svc.Release(ctx, me, paying.Row.ID))
	}

	// Bought: cannot be released, and the seats stay sold.
	sold, err := svc.Hold(ctx, me, f.EventID, f.row(1)[2:4])
	require.NoError(t, err)
	res, err := store.SellSeatsOfHold(ctx, pool, sold.Row.ID, 2)
	require.NoError(t, err)
	require.Equal(t, 2, res.Sold)
	exec(t, `UPDATE holds SET status = 'CONVERTED' WHERE id = $1`, sold.Row.ID)
	err = svc.Release(ctx, me, sold.Row.ID)
	hd := details[domain.HoldStateDetails](t, err, domain.CodeHoldNotActive)
	assert.Equal(t, domain.HoldConverted, hd.HoldStatus)
	assert.Equal(t, sold.Row.ID, hd.HoldID)
	assert.Equal(t, "SOLD", seatStates(t, f.row(1)[2:3])[f.row(1)[2]].Status)
	got, err = svc.Get(ctx, me, sold.Row.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.HoldConverted, got.Status)
	assert.Len(t, got.Seats, 2, "a bought hold still lists its seats")
	f.check()
}

func TestGetAndListMine(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 6})
	other := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 2})
	svc := newService(t)
	ctx := timeout(t)
	me := newUser(t)

	first, err := svc.Hold(ctx, me, f.EventID, f.row(0)[:1])
	require.NoError(t, err)
	second, err := svc.Hold(ctx, me, f.EventID, f.row(0)[1:3])
	require.NoError(t, err)
	elsewhere, err := svc.Hold(ctx, me, other.EventID, other.row(0)[:1])
	require.NoError(t, err)
	released, err := svc.Hold(ctx, me, f.EventID, f.row(0)[3:4])
	require.NoError(t, err)
	require.NoError(t, svc.Release(ctx, me, released.Row.ID))

	ids := func(hs []booking.Hold) []int64 {
		out := []int64{}
		for _, h := range hs {
			out = append(out, h.Row.ID)
		}
		return out
	}
	all, err := svc.ListMine(ctx, me, nil)
	require.NoError(t, err)
	assert.Equal(t, []int64{elsewhere.Row.ID, second.Row.ID, first.Row.ID}, ids(all), "live holds, newest first")
	assert.Len(t, all[1].Seats, 2)
	assert.EqualValues(t, 200000, all[1].SubtotalCents)
	one, err := svc.ListMine(ctx, me, &f.EventID)
	require.NoError(t, err)
	assert.Equal(t, []int64{second.Row.ID, first.Row.ID}, ids(one))

	// A hold past its time is not listed and is reported as expired, before any sweep.
	lapse(t, second.Row.ID)
	one, err = svc.ListMine(ctx, me, &f.EventID)
	require.NoError(t, err)
	assert.Equal(t, []int64{first.Row.ID}, ids(one))
	got, err := svc.Get(ctx, me, second.Row.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.HoldExpired, got.Status)
	assert.Zero(t, got.RemainingSeconds)
	assert.Empty(t, got.Seats)
	assert.Equal(t, "ACTIVE", holdStatus(t, second.Row.ID), "not swept")

	none, err := svc.ListMine(ctx, newUser(t), nil)
	require.NoError(t, err)
	assert.Empty(t, none)
	f.check()
}

// Correction 4.
func TestExtendForPayment(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 8, MaxPerUser: 8})
	svc := newService(t)
	ctx := timeout(t)
	me := newUser(t)
	seats := f.row(0)
	seatExpiry := func(id int64) time.Time {
		var at time.Time
		require.NoError(t, pool.QueryRow(ctx, `SELECT hold_expires_at FROM event_seats WHERE id = $1`, id).Scan(&at))
		return at
	}
	remaining := func(holdID int64) float64 {
		var secs float64
		require.NoError(t, pool.QueryRow(ctx, `SELECT extract(epoch FROM expires_at - clock_timestamp())::float8 FROM holds WHERE id = $1`, holdID).Scan(&secs))
		return secs
	}

	h, err := svc.Hold(ctx, me, f.EventID, seats[:2])
	require.NoError(t, err)

	// A fresh hold has ten minutes: nothing to push, and the one extension is not used up.
	ext, err := svc.ExtendForPayment(ctx, me, h.Row.ID)
	require.NoError(t, err)
	assert.False(t, ext.Extended)
	assert.False(t, ext.AlreadyExtended)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM holds WHERE id = $1 AND extended_at IS NOT NULL`, h.Row.ID))

	// Two minutes left when the payment starts: pushed out so six remain, on the hold row
	// and on both seats.
	setExpiry(t, h.Row.ID, 2*time.Minute)
	ext, err = svc.ExtendForPayment(ctx, me, h.Row.ID)
	require.NoError(t, err)
	assert.True(t, ext.Extended)
	assert.False(t, ext.AlreadyExtended)
	assert.Equal(t, 2, ext.Seats)
	assert.Equal(t, 6*time.Minute, ext.Remaining)
	assert.InDelta(t, 360, remaining(h.Row.ID), 2)
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM holds WHERE id = $1 AND extended_at IS NOT NULL`, h.Row.ID))
	for _, id := range seats[:2] {
		assert.True(t, ext.ExpiresAt.Equal(seatExpiry(id)), "seat %d carries the hold's new expiry", id)
	}
	got, err := svc.Get(ctx, me, h.Row.ID)
	require.NoError(t, err)
	assert.NotNil(t, got.Row.ExtendedAt)
	assert.InDelta(t, 360, got.RemainingSeconds, 2)
	f.check() // includes: the seats and the hold agree on the expiry

	// A second payment attempt with one minute left: not extended again.
	setExpiry(t, h.Row.ID, time.Minute)
	ext, err = svc.ExtendForPayment(ctx, me, h.Row.ID)
	require.NoError(t, err)
	assert.False(t, ext.Extended)
	assert.True(t, ext.AlreadyExtended)
	assert.InDelta(t, 60, ext.Remaining.Seconds(), 2)
	assert.InDelta(t, 60, remaining(h.Row.ID), 2, "the expiry did not move")

	// Time run out, not swept: HOLD_EXPIRED, and nothing is revived.
	lapse(t, h.Row.ID)
	_, err = svc.ExtendForPayment(ctx, me, h.Row.ID)
	hd := details[domain.HoldStateDetails](t, err, domain.CodeHoldExpired)
	assert.Equal(t, domain.HoldStateDetails{HoldID: h.Row.ID, HoldStatus: domain.HoldExpired, ExpiresAt: hd.ExpiresAt}, hd)
	assert.Less(t, remaining(h.Row.ID), 0.0)

	// A never-extended hold that has lapsed is not revived either.
	late, err := svc.Hold(ctx, me, f.EventID, seats[2:3])
	require.NoError(t, err)
	lapse(t, late.Row.ID)
	_, err = svc.ExtendForPayment(ctx, me, late.Row.ID)
	assert.Equal(t, domain.CodeHoldExpired, domain.CodeOf(err))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM holds WHERE id = $1 AND extended_at IS NOT NULL`, late.Row.ID))
	// Swept: still HOLD_EXPIRED.
	_, err = svc.Sweep(ctx)
	require.NoError(t, err)
	_, err = svc.ExtendForPayment(ctx, me, late.Row.ID)
	assert.Equal(t, domain.CodeHoldExpired, domain.CodeOf(err))

	// Released: HOLD_NOT_ACTIVE. Someone else's, or none: not found.
	gone, err := svc.Hold(ctx, me, f.EventID, seats[3:4])
	require.NoError(t, err)
	require.NoError(t, svc.Release(ctx, me, gone.Row.ID))
	_, err = svc.ExtendForPayment(ctx, me, gone.Row.ID)
	assert.Equal(t, domain.HoldReleased, details[domain.HoldStateDetails](t, err, domain.CodeHoldNotActive).HoldStatus)
	live, err := svc.Hold(ctx, me, f.EventID, seats[4:5])
	require.NoError(t, err)
	_, err = svc.ExtendForPayment(ctx, newUser(t), live.Row.ID)
	assert.Equal(t, domain.CodeNotFound, domain.CodeOf(err))
	_, err = svc.ExtendForPayment(ctx, me, 1<<60)
	assert.Equal(t, domain.CodeNotFound, domain.CodeOf(err))

	// Inside the caller's own transaction, as the payment flow will use it: rolled back
	// with that transaction.
	setExpiry(t, live.Row.ID, time.Minute)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	ext, err = booking.ExtendForPayment(ctx, tx, me, live.Row.ID, svc.PaymentWindow())
	require.NoError(t, err)
	assert.True(t, ext.Extended)
	require.NoError(t, tx.Rollback(ctx))
	assert.InDelta(t, 60, remaining(live.Row.ID), 2)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM holds WHERE id = $1 AND extended_at IS NOT NULL`, live.Row.ID))

	// Two payments starting at once on one hold: one extends, the other is told it already was.
	outs := storm(t, 20, stormDeadline, func(int) (booking.Hold, error) {
		e, err := svc.ExtendForPayment(context.Background(), me, live.Row.ID)
		h := booking.Hold{}
		if e.Extended {
			h.RemainingSeconds = 1
		}
		return h, err
	})
	extended := 0
	for _, o := range outs {
		require.NoError(t, o.Err)
		extended += o.Hold.RemainingSeconds
	}
	assert.Equal(t, 1, extended, "of 20 simultaneous calls exactly one extends")
	f.check()
}

// Correction 5: seats are sold only where they still point at the hold and are still HELD,
// and all of them or none.
func TestSellSeatsOfHold(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 2, SeatsPerRow: 6, MaxPerUser: 12})
	svc := newService(t)
	ctx := timeout(t)
	buyer, other := newUser(t), newUser(t)
	a, b := f.row(0), f.row(1)

	sell := func(holdID int64, expected int) store.SoldSeats {
		var out store.SoldSeats
		require.NoError(t, store.InTxRaw(ctx, pool, func(tx pgx.Tx) error {
			if _, err := store.LockHold(ctx, tx, holdID); err != nil {
				return err
			}
			var err error
			out, err = store.SellSeatsOfHold(ctx, tx, holdID, expected)
			return err
		}))
		return out
	}

	// All four still held: all four sold, with a fresh stamp, and nobody else's seat touched.
	full, err := svc.Hold(ctx, buyer, f.EventID, a[:4])
	require.NoError(t, err)
	neighbour, err := svc.Hold(ctx, other, f.EventID, a[4:6])
	require.NoError(t, err)
	before := queryText(t, `SELECT max(updated_at)::text FROM event_seats WHERE id = ANY($1)`, a[:4])
	assert.Equal(t, store.SoldSeats{StillHeld: 4, Sold: 4}, sell(full.Row.ID, 4))
	assert.Equal(t, 4, queryInt(t, `SELECT count(*) FROM event_seats WHERE id = ANY($1) AND status = 'SOLD' AND hold_id = $2 AND hold_expires_at IS NULL AND updated_at > $3::timestamptz`,
		a[:4], full.Row.ID, before))
	state := seatStates(t, a)
	assert.Equal(t, "HELD", state[a[4]].Status)
	assert.Equal(t, neighbour.Row.ID, *state[a[4]].HoldID)
	// Again: nothing left to sell, nothing changes.
	assert.Equal(t, store.SoldSeats{StillHeld: 0, Sold: 0}, sell(full.Row.ID, 4))
	exec(t, `UPDATE holds SET status = 'CONVERTED' WHERE id = $1`, full.Row.ID)

	// The payment outlived the hold and one of the four seats was taken over by another
	// fan: the count is short (3 of 4), so NOTHING is sold, and the other fan keeps the seat.
	short, err := svc.Hold(ctx, buyer, f.EventID, b[:4])
	require.NoError(t, err)
	lapse(t, short.Row.ID)
	taker, err := svc.Hold(ctx, other, f.EventID, b[1:2])
	require.NoError(t, err)
	assert.Equal(t, store.SoldSeats{StillHeld: 3, Sold: 0}, sell(short.Row.ID, 4))
	state = seatStates(t, b)
	assert.Equal(t, "HELD", state[b[1]].Status)
	assert.Equal(t, taker.Row.ID, *state[b[1]].HoldID, "the seat that was taken over is not sold")
	for _, id := range []int64{b[0], b[2], b[3]} {
		assert.Equal(t, "HELD", state[id].Status, "nothing is sold when the count is short")
	}
	assert.Equal(t, 4, queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1 AND status = 'SOLD'`, f.EventID))

	// The hold lapsed but nobody took its seats: they are still its seats and can be sold.
	late, err := svc.Hold(ctx, buyer, f.EventID, b[4:6])
	require.NoError(t, err)
	lapse(t, late.Row.ID)
	assert.Equal(t, store.SoldSeats{StillHeld: 2, Sold: 2}, sell(late.Row.ID, 2))
	exec(t, `UPDATE holds SET status = 'CONVERTED' WHERE id = $1`, late.Row.ID)

	// A sold seat cannot be held, and a sweep does not put it back on sale.
	_, err = svc.Hold(ctx, other, f.EventID, a[:1])
	assert.Equal(t, a[:1], unavailable(t, err))
	_, err = svc.Sweep(ctx)
	require.NoError(t, err)
	c := f.check()
	assert.Equal(t, 6, c.Sold)
}

// Correction 7.
func TestSweep(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 2, SeatsPerRow: 6, MaxPerUser: 12})
	svc := newService(t)
	ctx := timeout(t)
	seats := f.all()
	hold := func(ids ...int64) (int64, booking.Hold) {
		u := newUser(t)
		h, err := svc.Hold(ctx, u, f.EventID, ids)
		require.NoError(t, err)
		return u, h
	}

	_, live := hold(seats[0])
	uPlain, plain := hold(seats[1], seats[2])
	uUnpaid, unpaid := hold(seats[3])
	uPaying, paying := hold(seats[4])
	_, locked := hold(seats[5])
	uTaken, taken := hold(seats[6], seats[7])
	bkUnpaid := insertBooking(t, f, uUnpaid, &unpaid.Row.ID, "PENDING_PAYMENT", seats[3])
	insertPayment(t, bkUnpaid, "FAILED")
	bkPaying := insertBooking(t, f, uPaying, &paying.Row.ID, "PENDING_PAYMENT", seats[4])
	insertPayment(t, bkPaying, "PENDING")
	for _, h := range []booking.Hold{plain, unpaid, paying, locked, taken} {
		lapse(t, h.Row.ID)
	}
	// One seat of `taken` now belongs to a new, live hold.
	_, newer := hold(seats[6])
	_ = uPlain
	_ = uTaken

	// While another sweeper has the lock, this one leaves at once and changes nothing.
	other, err := pool.Begin(ctx)
	require.NoError(t, err)
	alone, err := store.TrySweeperLock(ctx, other)
	require.NoError(t, err)
	require.True(t, alone)
	res, err := svc.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, booking.SweepResult{Skipped: true}, res)
	assert.Equal(t, "ACTIVE", holdStatus(t, plain.Row.ID))
	require.NoError(t, other.Rollback(ctx))

	// A hold some other transaction is working on (a release, a payment) is skipped whole.
	busy, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = store.LockHold(ctx, busy, locked.Row.ID)
	require.NoError(t, err)

	before := queryText(t, `SELECT clock_timestamp()::text`)
	res, err = svc.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, booking.SweepResult{Holds: 4, Seats: 5, Bookings: 1}, res,
		"plain (2 seats), unpaid (1), paying (1), taken (1 seat left); the busy hold is skipped")
	require.NoError(t, busy.Rollback(ctx))

	for _, h := range []booking.Hold{plain, unpaid, paying, taken} {
		assert.Equal(t, "EXPIRED", holdStatus(t, h.Row.ID))
	}
	assert.Equal(t, "ACTIVE", holdStatus(t, live.Row.ID))
	assert.Equal(t, "ACTIVE", holdStatus(t, locked.Row.ID), "skipped while locked")
	assert.Equal(t, "ACTIVE", holdStatus(t, newer.Row.ID))
	state := seatStates(t, seats)
	for _, i := range []int{1, 2, 3, 4, 7} {
		assert.Equal(t, "AVAILABLE", state[seats[i]].Status, "seat %d", i)
		assert.Nil(t, state[seats[i]].HoldID)
	}
	assert.Equal(t, live.Row.ID, *state[seats[0]].HoldID)
	assert.Equal(t, newer.Row.ID, *state[seats[6]].HoldID, "the sweep does not free a seat that was taken over")
	assert.Equal(t, 5, queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1 AND status = 'AVAILABLE' AND updated_at > $2::timestamptz`, f.EventID, before),
		"swept seats carry a fresh stamp")
	// The unpaid booking is expired; the one with a payment still live is left alone.
	assert.Equal(t, "EXPIRED", queryText(t, `SELECT status FROM bookings WHERE id = $1`, bkUnpaid))
	assert.Equal(t, "PENDING_PAYMENT", queryText(t, `SELECT status FROM bookings WHERE id = $1`, bkPaying))

	// The next run picks up the hold that was busy, and a third finds nothing.
	res, err = svc.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, booking.SweepResult{Holds: 1, Seats: 1}, res)
	res, err = svc.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, booking.SweepResult{}, res)

	// An event whose row a writer has locked (an organiser cancelling or editing) is left
	// for the next run instead of being waited for.
	_, waiting := hold(seats[8])
	lapse(t, waiting.Row.ID)
	editor, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = editor.Exec(ctx, `SELECT 1 FROM events WHERE id = $1 FOR UPDATE`, f.EventID)
	require.NoError(t, err)
	started := time.Now()
	res, err = svc.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, booking.SweepResult{}, res)
	assert.Less(t, time.Since(started), 2*time.Second, "the sweeper does not wait for the event")
	require.NoError(t, editor.Rollback(ctx))
	res, err = svc.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, res.Holds)

	c := f.check()
	assert.Equal(t, 2, c.Held)
}

// Batches: more lapsed holds than one batch are all closed in one run.
func TestSweep_Batches(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 5, SeatsPerRow: 10})
	svc := newService(t, func(p *booking.Policy) { p.SweepBatch = 8 })
	ctx := timeout(t)
	for i, u := range newUsers(t, 50) {
		h, err := svc.Hold(ctx, u, f.EventID, f.all()[i:i+1])
		require.NoError(t, err)
		if i < 45 {
			lapse(t, h.Row.ID)
		}
	}
	res, err := svc.Sweep(ctx)
	require.NoError(t, err)
	assert.Equal(t, 45, queryInt(t, `SELECT count(*) FROM holds WHERE event_id = $1 AND status = 'EXPIRED'`, f.EventID))
	// Other tests' events share the database, so this run may have closed their holds too.
	assert.GreaterOrEqual(t, res.Holds, 45)
	c := f.check()
	assert.Equal(t, 5, c.Held)
	assert.Equal(t, 45, c.Available)
}
