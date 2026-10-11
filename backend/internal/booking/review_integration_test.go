//go:build integration

package booking_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/booking"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// Tests for what the code review of the hold flow found (Part A of the payment milestone).

// waitUntil polls a condition of the database until it holds. The tests below wait for a
// state (a backend blocked on a lock, the clock passing an expiry), never for a length of time.
func waitUntil(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting until %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// blockedOnHoldLock reports how many backends are waiting for a row lock in the hold-lock
// statement.
func blockedOnHoldLock(t testing.TB) int {
	return queryInt(t, `SELECT count(*) FROM pg_stat_activity
		WHERE wait_event_type = 'Lock' AND query LIKE '%FROM holds%WHERE id = $1%FOR UPDATE%'`)
}

func expired(t testing.TB, holdID int64) bool {
	return queryInt(t, `SELECT count(*) FROM holds WHERE id = $1 AND expires_at <= clock_timestamp()`, holdID) == 1
}

// A1. Extension.Seats is how many seats the hold has on EVERY path, not only on the call
// that moved the expiry. The payment flow compares it with the booking's item count before
// it starts a payment; a zero on the "already extended" path refused every retry.
func TestExtendForPayment_CountsSeatsOnEveryPath(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 2, SeatsPerRow: 10})
	svc := newService(t)
	me := newUser(t)
	ctx := timeout(t)
	h, err := svc.Hold(ctx, me, f.EventID, f.row(0)[:3])
	require.NoError(t, err)

	// Plenty of time left: nothing moves.
	ext, err := svc.ExtendForPayment(ctx, me, h.Row.ID)
	require.NoError(t, err)
	assert.False(t, ext.Extended)
	assert.False(t, ext.AlreadyExtended)
	assert.Equal(t, 3, ext.Seats, "seats on the nothing-to-do path")

	// Little time left: the one extension.
	setExpiry(t, h.Row.ID, time.Minute)
	ext, err = svc.ExtendForPayment(ctx, me, h.Row.ID)
	require.NoError(t, err)
	assert.True(t, ext.Extended)
	assert.Equal(t, 3, ext.Seats, "seats on the extending path")

	// Already extended: left alone, and the count is still there.
	setExpiry(t, h.Row.ID, time.Minute)
	ext, err = svc.ExtendForPayment(ctx, me, h.Row.ID)
	require.NoError(t, err)
	assert.True(t, ext.AlreadyExtended)
	assert.False(t, ext.Extended)
	assert.Equal(t, 3, ext.Seats, "seats on the already-extended path")
	assert.InDelta(t, 60, ext.Remaining.Seconds(), 3)
}

// A1. A call that waits for the hold's row lock while the hold runs out must not push its
// expiry out afterwards. Postgres computes a statement's select list before it waits for a
// row lock, so a clock read in the locking statement is the time the wait BEGAN: the hold
// looked live, and the UPDATE (which had no expiry condition) revived it.
func TestExtendForPayment_DoesNotReviveAHoldThatExpiredWhileItWaitedForTheLock(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 2, SeatsPerRow: 10})
	svc := newService(t)
	me := newUser(t)
	ctx := timeout(t)
	h, err := svc.Hold(ctx, me, f.EventID, f.row(0)[:2])
	require.NoError(t, err)
	// Three seconds left: enough for the extension to start waiting while the hold is live.
	setExpiry(t, h.Row.ID, 3*time.Second)

	// Someone else has the hold's row (a release, another payment start, the sweeper).
	blocker, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(context.Background()) }()
	_, err = store.LockHold(ctx, blocker, h.Row.ID)
	require.NoError(t, err)

	type result struct {
		ext booking.Extension
		err error
	}
	done := make(chan result, 1)
	go func() {
		ext, err := svc.ExtendForPayment(context.Background(), me, h.Row.ID)
		done <- result{ext, err}
	}()
	waitUntil(t, "the extension is waiting for the hold's row lock", func() bool { return blockedOnHoldLock(t) >= 1 })
	require.False(t, expired(t, h.Row.ID), "the extension must have started waiting while the hold was still live")
	waitUntil(t, "the hold's time has run out", func() bool { return expired(t, h.Row.ID) })
	require.NoError(t, blocker.Commit(ctx))

	var got result
	select {
	case got = <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the extension did not return")
	}
	require.Error(t, got.err, "an expired hold was extended: %+v", got.ext)
	assert.Equal(t, domain.CodeHoldExpired, domain.CodeOf(got.err))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM holds WHERE id = $1 AND extended_at IS NOT NULL`, h.Row.ID), "extended_at must stay empty")
	assert.True(t, expired(t, h.Row.ID), "the hold must still be past its expiry")
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM event_seats WHERE hold_id = $1 AND hold_expires_at <= clock_timestamp()`, h.Row.ID),
		"the seats' expiry must not have moved either")
}

// A1. The extend UPDATE itself refuses a hold whose time has run out, whatever the caller
// read before: zero rows.
func TestExtendHoldStatement_RefusesAnExpiredHold(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 10})
	svc := newService(t)
	me := newUser(t)
	ctx := timeout(t)
	h, err := svc.Hold(ctx, me, f.EventID, f.row(0)[:2])
	require.NoError(t, err)
	lapse(t, h.Row.ID)

	n, err := store.New(pool).ExtendHold(ctx, store.ExtendHoldParams{ID: h.Row.ID, ExpiresAt: time.Now().Add(6 * time.Minute)})
	require.NoError(t, err)
	assert.EqualValues(t, 0, n, "the UPDATE must not touch a hold past its expiry")

	live, err := svc.Hold(ctx, me, f.EventID, f.row(0)[5:7])
	require.NoError(t, err)
	n, err = store.New(pool).ExtendHold(ctx, store.ExtendHoldParams{ID: live.Row.ID, ExpiresAt: time.Now().Add(20 * time.Minute)})
	require.NoError(t, err)
	assert.EqualValues(t, 1, n)
	// Keep the seats' copy of the expiry in step, as ExtendForPayment does.
	exec(t, `UPDATE event_seats es SET hold_expires_at = h.expires_at FROM holds h WHERE h.id = $1 AND es.hold_id = h.id`, live.Row.ID)
}

// A1. The extension's wait for the event is bounded: EVENT_BUSY, not a wait until the
// statement timeout, when an organiser's publish, cancel or edit has the event.
func TestExtendForPayment_EventBusy(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 10})
	svc := newService(t, func(p *booking.Policy) { p.EventLockWait = 200 * time.Millisecond })
	me := newUser(t)
	ctx := timeout(t)
	h, err := svc.Hold(ctx, me, f.EventID, f.row(0)[:2])
	require.NoError(t, err)

	organiser, err := adminPool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = organiser.Rollback(context.Background()) }()
	_, err = store.New(organiser).LockEvent(ctx, f.EventID)
	require.NoError(t, err)

	started := time.Now()
	_, err = svc.ExtendForPayment(ctx, me, h.Row.ID)
	took := time.Since(started)
	require.Error(t, err)
	assert.Equal(t, domain.CodeEventBusy, domain.CodeOf(err))
	assert.Less(t, took, 5*time.Second, "the wait must be bounded")

	// And inside a caller's transaction, as the payment flow calls it.
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	_, err = booking.ExtendForPayment(ctx, tx, me, h.Row.ID, svc.PaymentWindow(), 200*time.Millisecond)
	assert.Equal(t, domain.CodeEventBusy, domain.CodeOf(err))
	require.NoError(t, tx.Rollback(ctx))

	require.NoError(t, organiser.Rollback(ctx))
	ext, err := svc.ExtendForPayment(ctx, me, h.Row.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, ext.Seats)
}

// A1. Release labels the hold by the clock as it is once it has the lock: a release that
// queued while the hold ran out records EXPIRED (and its booking EXPIRED), not RELEASED.
func TestRelease_LabelsTheHoldByTheClockAfterTheLockWait(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 10})
	svc := newService(t)
	me := newUser(t)
	ctx := timeout(t)
	h, err := svc.Hold(ctx, me, f.EventID, f.row(0)[:2])
	require.NoError(t, err)
	bookingID := insertBooking(t, f, me, &h.Row.ID, "PENDING_PAYMENT", f.row(0)[:2]...)
	setExpiry(t, h.Row.ID, 3*time.Second)

	blocker, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocker.Rollback(context.Background()) }()
	_, err = store.LockHold(ctx, blocker, h.Row.ID)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- svc.Release(context.Background(), me, h.Row.ID) }()
	waitUntil(t, "the release is waiting for the hold's row lock", func() bool { return blockedOnHoldLock(t) >= 1 })
	require.False(t, expired(t, h.Row.ID))
	waitUntil(t, "the hold's time has run out", func() bool { return expired(t, h.Row.ID) })
	require.NoError(t, blocker.Commit(ctx))
	require.NoError(t, <-done)

	assert.Equal(t, "EXPIRED", holdStatus(t, h.Row.ID), "the hold ran out before the release got to it")
	assert.Equal(t, "EXPIRED", queryText(t, `SELECT status FROM bookings WHERE id = $1`, bookingID))
	assert.Equal(t, 0, f.check().Held)
}

// poolOnlyBegins is a pool on which only transactions work: every read or write made
// straight on the pool fails. It stands in for "the read after the commit failed".
type poolOnlyBegins struct{ store.TxBeginner }

var errPoolRead = errors.New("test: reads on the pool are broken")

func (poolOnlyBegins) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errPoolRead
}
func (poolOnlyBegins) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errPoolRead
}
func (poolOnlyBegins) QueryRow(context.Context, string, ...any) pgx.Row { return failingRow{} }
func (poolOnlyBegins) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errPoolRead
}

type failingRow struct{}

func (failingRow) Scan(...any) error { return errPoolRead }

// A2. The answer of a hold is built inside its transaction. It used to be read again after
// the commit; when that read failed, the fan got a 500 while holding the seats.
func TestHold_TheAnswerIsBuiltInsideTheTransaction(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 10})
	svc, err := booking.NewService(booking.Config{DB: poolOnlyBegins{pool}})
	require.NoError(t, err)
	me := newUser(t)

	h, err := svc.Hold(timeout(t), me, f.EventID, f.row(0)[:3])
	require.NoError(t, err, "a hold must not depend on a read made after its commit")
	assert.Equal(t, domain.HoldActive, h.Status)
	require.Len(t, h.Seats, 3)
	assert.EqualValues(t, 300000, h.TotalCents)
	assert.Greater(t, h.RemainingSeconds, 590)
	assert.Equal(t, 3, f.check().Held)

	// The wrapper really does break reads on the pool: Get, which reads there, fails.
	_, err = svc.Get(timeout(t), me, h.Row.ID)
	require.ErrorIs(t, err, errPoolRead)
}

// A5. Closing a hold's bookings asks domain.TransitionBooking with each booking's real
// status: a booking owed a refund (a payment of the wrong amount) is not turned into
// CANCELLED or EXPIRED by a release or a sweep of its hold, and a paid one is never touched.
func TestClosingAHold_OnlyMovesBookingsTheRuleAllows(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 4, SeatsPerRow: 10})
	svc := newService(t)
	ctx := timeout(t)
	me := newUser(t)

	// Release: one booking owed a refund, one newer booking awaiting payment.
	h, err := svc.Hold(ctx, me, f.EventID, f.row(0)[:2])
	require.NoError(t, err)
	owed := insertBooking(t, f, me, &h.Row.ID, "REFUND_REQUIRED", f.row(0)[:2]...)
	waiting := insertBooking(t, f, me, &h.Row.ID, "PENDING_PAYMENT", f.row(0)[:2]...)
	require.NoError(t, svc.Release(ctx, me, h.Row.ID))
	assert.Equal(t, "REFUND_REQUIRED", queryText(t, `SELECT status FROM bookings WHERE id = $1`, owed), "release must not close a booking owed a refund")
	assert.Equal(t, "CANCELLED", queryText(t, `SELECT status FROM bookings WHERE id = $1`, waiting))

	// Sweep: the same pair on a lapsed hold, plus a lapsed hold whose booking has a live payment.
	other := newUser(t)
	h2, err := svc.Hold(ctx, other, f.EventID, f.row(1)[:2])
	require.NoError(t, err)
	owed2 := insertBooking(t, f, other, &h2.Row.ID, "REFUNDED", f.row(1)[:2]...)
	waiting2 := insertBooking(t, f, other, &h2.Row.ID, "PENDING_PAYMENT", f.row(1)[:2]...)
	third := newUser(t)
	h3, err := svc.Hold(ctx, third, f.EventID, f.row(2)[:2])
	require.NoError(t, err)
	paying := insertBooking(t, f, third, &h3.Row.ID, "PENDING_PAYMENT", f.row(2)[:2]...)
	insertPayment(t, paying, "PENDING")
	lapse(t, h2.Row.ID)
	lapse(t, h3.Row.ID)

	res, err := svc.Sweep(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, res.Holds, 2)
	assert.Equal(t, "REFUNDED", queryText(t, `SELECT status FROM bookings WHERE id = $1`, owed2), "the sweeper must not reopen or close a refunded booking")
	assert.Equal(t, "EXPIRED", queryText(t, `SELECT status FROM bookings WHERE id = $1`, waiting2))
	assert.Equal(t, "PENDING_PAYMENT", queryText(t, `SELECT status FROM bookings WHERE id = $1`, paying), "a booking under a live payment is left to the payment flow")
	assert.Equal(t, "EXPIRED", holdStatus(t, h3.Row.ID))
	assert.Equal(t, 0, f.check().Held)
}

// The whole booking flow at the service: one booking per hold, however Pay is tapped.

func bookingsOfHold(t testing.TB, holdID int64) int {
	return queryInt(t, `SELECT count(*) FROM bookings WHERE hold_id = $1`, holdID)
}

// A double-tapped Pay: the same idempotency key, many times at once. One booking is made;
// every request gets that same booking.
func TestCreateBooking_SameKeyManyTimesAtOnceMakesOneBooking(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 2, SeatsPerRow: 10})
	svc := newService(t)
	me := newUser(t)
	h, err := svc.Hold(timeout(t), me, f.EventID, f.row(0)[:4])
	require.NoError(t, err)

	const taps = 24
	key := "double-tap-" + randDigits(t, 12)
	type res struct {
		b       booking.Booking
		created bool
		err     error
	}
	out := make([]res, taps)
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < taps; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			b, created, err := svc.CreateBooking(context.Background(), me, h.Row.ID, key)
			out[i] = res{b, created, err}
		}()
	}
	close(gate)
	wg.Wait()

	created := 0
	for i, r := range out {
		require.NoError(t, r.err, "tap %d", i)
		assert.Equal(t, out[0].b.Row.ID, r.b.Row.ID, "tap %d got a different booking", i)
		assert.Equal(t, out[0].b.Row.Reference, r.b.Row.Reference)
		assert.Len(t, r.b.Items, 4)
		if r.created {
			created++
		}
	}
	assert.Equal(t, 1, created, "exactly one request created the booking; the rest replayed it")
	assert.Equal(t, 1, bookingsOfHold(t, h.Row.ID))
	assert.Equal(t, 4, queryInt(t, `SELECT count(*) FROM booking_items WHERE booking_id = $1`, out[0].b.Row.ID))
}

// Pay tapped on two phones: two different keys for one hold at once. Exactly one booking
// exists; the loser is told which one, never a 500.
func TestCreateBooking_DifferentKeysAtOnceMakeOneBooking(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 2, SeatsPerRow: 10})
	svc := newService(t)
	for round := 0; round < 8; round++ {
		me := newUser(t)
		h, err := svc.Hold(timeout(t), me, f.EventID, []int64{f.Seats[0][round%2][round], f.Seats[0][(round+1)%2][round]})
		require.NoError(t, err)

		const phones = 6
		errs := make([]error, phones)
		refs := make([]string, phones)
		gate := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < phones; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-gate
				b, _, err := svc.CreateBooking(context.Background(), me, h.Row.ID, "phone-"+randDigits(t, 14))
				errs[i], refs[i] = err, b.Row.Reference
			}()
		}
		close(gate)
		wg.Wait()

		won, winner := 0, ""
		for i, err := range errs {
			if err == nil {
				won++
				winner = refs[i]
				continue
			}
			require.Equal(t, domain.CodeHoldAlreadyBooked, domain.CodeOf(err), "round %d: %v", round, err)
		}
		require.Equal(t, 1, won, "round %d: exactly one key gets the booking", round)
		require.Equal(t, 1, bookingsOfHold(t, h.Row.ID), "round %d", round)
		for _, err := range errs {
			var de *domain.Error
			if errors.As(err, &de) {
				assert.Equal(t, winner, de.Details.(domain.BookingStateDetails).BookingReference)
			}
		}
		require.NoError(t, svc.Release(timeout(t), me, h.Row.ID))
	}
}

func TestCreateBooking_Rules(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 4, SeatsPerRow: 10, MaxPerUser: 20})
	svc := newService(t, func(p *booking.Policy) { p.BookingFeeBPS = 250 })
	ctx := timeout(t)
	me := newUser(t)
	h, err := svc.Hold(ctx, me, f.EventID, f.row(0)[:3])
	require.NoError(t, err)

	// Prices are copied; the fee is the hold's estimate (2.5% of 3,000.00 = 75.00).
	b, created, err := svc.CreateBooking(ctx, me, h.Row.ID, "key-rules-0001")
	require.NoError(t, err)
	assert.True(t, created)
	assert.Regexp(t, `^TLT-[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{6}$`, b.Row.Reference)
	assert.EqualValues(t, 300000, b.Row.SubtotalCents)
	assert.EqualValues(t, 7500, b.Row.FeeCents)
	assert.EqualValues(t, 307500, b.Row.TotalCents)
	assert.Equal(t, h.TotalCents, b.Row.TotalCents, "the booking charges what the hold estimated")
	assert.Equal(t, "PENDING_PAYMENT", b.Row.Status)
	assert.Len(t, b.Row.WebToken, 64)
	require.NotNil(t, b.HoldExpiresAt)
	assert.Nil(t, b.LatestPayment)
	require.Len(t, b.Items, 3)

	// A later price change does not move the booking.
	exec(t, `UPDATE event_categories SET price_cents = 999900 WHERE id = $1`, f.CategoryID)
	again, created, err := svc.CreateBooking(ctx, me, h.Row.ID, "key-rules-0001")
	require.NoError(t, err)
	assert.False(t, created, "the same key replays")
	assert.Equal(t, b.Row.ID, again.Row.ID)
	assert.EqualValues(t, 307500, again.Row.TotalCents)
	for _, it := range again.Items {
		assert.EqualValues(t, 100000, it.UnitPriceCents, "the copied price")
	}
	exec(t, `UPDATE event_categories SET price_cents = 100000 WHERE id = $1`, f.CategoryID)

	// Another key for the same hold: told about the booking it has.
	_, _, err = svc.CreateBooking(ctx, me, h.Row.ID, "key-rules-0002")
	require.Equal(t, domain.CodeHoldAlreadyBooked, domain.CodeOf(err))

	// The same key for another hold.
	h2, err := svc.Hold(ctx, me, f.EventID, f.row(1)[:2])
	require.NoError(t, err)
	_, _, err = svc.CreateBooking(ctx, me, h2.Row.ID, "key-rules-0001")
	require.Equal(t, domain.CodeIdempotencyKeyReused, domain.CodeOf(err))

	// Someone else's hold, and no hold.
	stranger := newUser(t)
	_, _, err = svc.CreateBooking(ctx, stranger, h2.Row.ID, "key-rules-0003")
	require.Equal(t, domain.CodeNotFound, domain.CodeOf(err))
	_, _, err = svc.CreateBooking(ctx, me, 1<<60, "key-rules-0004")
	require.Equal(t, domain.CodeNotFound, domain.CodeOf(err))

	// A lapsed hold, a released hold.
	lapse(t, h2.Row.ID)
	_, _, err = svc.CreateBooking(ctx, me, h2.Row.ID, "key-rules-0005")
	require.Equal(t, domain.CodeHoldExpired, domain.CodeOf(err))
	h3, err := svc.Hold(ctx, me, f.EventID, f.row(2)[:2])
	require.NoError(t, err)
	require.NoError(t, svc.Release(ctx, me, h3.Row.ID))
	_, _, err = svc.CreateBooking(ctx, me, h3.Row.ID, "key-rules-0006")
	require.Equal(t, domain.CodeHoldNotActive, domain.CodeOf(err))

	// A malformed key.
	for _, key := range []string{"", "short", "has space in it", "ключ-ключ-ключ", string(make([]byte, 129))} {
		_, _, err = svc.CreateBooking(ctx, me, h.Row.ID, key)
		require.Equal(t, domain.CodeIdempotencyKeyRequired, domain.CodeOf(err), "key %q", key)
	}

	// A replay still works after the hold has ended: the key's booking is what it is.
	lapse(t, h.Row.ID)
	late, created, err := svc.CreateBooking(ctx, me, h.Row.ID, "key-rules-0001")
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, b.Row.ID, late.Row.ID)

	// The event is busy: a bounded wait, then EVENT_BUSY, and nothing was made.
	h4, err := svc.Hold(ctx, me, f.EventID, f.row(3)[:2])
	require.NoError(t, err)
	quick := newService(t, func(p *booking.Policy) { p.EventLockWait = 200 * time.Millisecond })
	organiser, err := adminPool.Begin(ctx)
	require.NoError(t, err)
	_, err = store.New(organiser).LockEvent(ctx, f.EventID)
	require.NoError(t, err)
	_, _, err = quick.CreateBooking(ctx, me, h4.Row.ID, "key-rules-0007")
	require.NoError(t, organiser.Rollback(ctx))
	require.Equal(t, domain.CodeEventBusy, domain.CodeOf(err))
	assert.Equal(t, 0, bookingsOfHold(t, h4.Row.ID))
}

func TestNewReference(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		ref, err := booking.NewReference()
		require.NoError(t, err)
		require.Regexp(t, `^TLT-[ABCDEFGHJKMNPQRSTUVWXYZ23456789]{6}$`, ref)
		seen[ref] = true
	}
	assert.Greater(t, len(seen), 1990, "references come from crypto/rand and do not repeat like this")
}
