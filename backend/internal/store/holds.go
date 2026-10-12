package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The locking statements of the hold flow (docs/ARCHITECTURE.md §6, corrections 4 to 7).
//
// LOCK ORDER. Every transaction that takes more than one of these locks takes them in this
// order and never the other way round. That is what makes a deadlock impossible; it is also
// written down in docs/DECISIONS.md.
//
//	0. the event's gate         (an advisory lock: hold, release, extension shared;
//	                             publish, cancel, edits exclusive, in the LockEvent query)
//	1. the event row            (hold, release, extension, sweeper: FOR SHARE;
//	                             publish, cancel, edits: FOR UPDATE)
//	2. the (user, event) advisory lock
//	3. hold rows                (in id order when there are several)
//	4. event_seats rows         (always in id order) and the hold's booking and payments
//
// A hold's seats, its booking and that booking's payments are only ever touched by a
// transaction that already has the hold's row lock (step 3), so the order among those three
// does not matter: two such transactions took turns at the hold. Seats that belong to no
// hold (AVAILABLE ones) are locked in id order and nothing else is waited for afterwards.
//
// Each function below must be called inside a transaction; the locks last until it ends.
//
// WAITS FOR THE EVENT ARE BOUNDED. A publish or a cancel of a full stadium keeps the event
// (steps 0 and 1) for many seconds. Callers take those two steps through the *Within
// functions in locks.go, which set lock_timeout for that one statement and lift it again:
// a request that cannot get the event in time is answered EVENT_BUSY rather than left to
// the statement timeout. The limit is lifted before step 2 so that waits for hold and seat
// rows, which last milliseconds and are what makes holds correct, are never cut short.
//
// SEAT WRITES COME LAST. event_seats.updated_at is stamped when the row is written, and the
// seat map's change poll looks back only 20 seconds from its marker. A transaction that
// wrote a seat and then waited for some other lock could commit a stamp older than that and
// hide the change from every open seat map. So in every transaction here the statement that
// writes event_seats is the last one before commit; all locks and all other writes come
// first.
//
// NOTHING HERE WRITES THE events ROW. Any write to it makes every poll of every block of the
// event answer with the full block for 20 seconds.

// EventForHold is the part of an event the hold rules need, read under the event lock.
type EventForHold struct {
	ID                int64
	Status            string
	SalesOpenAt       *time.Time
	SalesCloseAt      *time.Time
	MaxTicketsPerUser int32
	// Now is the database's wall clock, read with the row. The sales window is judged
	// against it, not against the API server's clock.
	Now time.Time
}

// Locks: the one events row, FOR SHARE, until the transaction ends.
// Why: cancel (and publish, and every edit of the event) takes the same row FOR UPDATE and
// only then reads the status. With this lock a hold and a cancel of one event can never
// interleave: either the hold commits first and cancel then frees its seats, or cancel
// commits first and the hold reads CANCELLED and is refused. A seat can therefore never be
// HELD on a cancelled event. It also fixes the lock order: event first, seats after, the same
// as cancel, so the two cannot deadlock.
// FOR SHARE and not FOR KEY SHARE: KEY SHARE conflicts with FOR UPDATE only, so it would let
// through any writer that updates the row without taking FOR UPDATE first (a plain UPDATE of
// status or the sales window takes FOR NO KEY UPDATE). FOR SHARE conflicts with both, and
// any number of holds can share it, so holds do not queue behind each other here.
//
// Before the row lock it takes the event's GATE shared: a transaction-level advisory lock that
// every writer of the event takes exclusive (the LockEvent query). The row lock alone is
// unfair: Postgres lets a new FOR SHARE join the ones already on a row even while a FOR
// UPDATE is waiting, so a steady stream of holds starved a cancel for as long as the stream
// lasted. Advisory locks queue in arrival order: once a writer is waiting at the gate, later
// holds queue behind it, and it waits only for the holds already in flight.
const lockEventForHold = `
WITH gate AS MATERIALIZED (
    SELECT pg_advisory_xact_lock_shared(hashtextextended('talanta:event:' || $1::bigint::text, 0))
)
SELECT e.id, e.status, e.sales_open_at, e.sales_close_at, e.max_tickets_per_user, clock_timestamp()
FROM events e, gate
WHERE e.id = $1
FOR SHARE OF e`

// LockEventForHold takes the event's share lock and returns the row. ErrNoRows when there is
// no such event.
func LockEventForHold(ctx context.Context, db DBTX, eventID int64) (EventForHold, error) {
	var e EventForHold
	err := db.QueryRow(ctx, lockEventForHold, eventID).
		Scan(&e.ID, &e.Status, &e.SalesOpenAt, &e.SalesCloseAt, &e.MaxTicketsPerUser, &e.Now)
	return e, err
}

// Locks: a transaction-level advisory lock on (user, event), held until the transaction ends.
// Why (correction 6): the ticket limit is "count what the user has, then hold more". Two
// hold requests of one user for one event at the same moment would both count before either
// inserts, and both pass. With this lock the second waits until the first has committed or
// rolled back and then counts the first one's seats. Different users, and the same user on
// different events, never wait for each other. The key is a 64-bit hash of the pair; a
// collision only makes two unrelated requests take turns.
const lockUserEvent = `SELECT pg_advisory_xact_lock(hashtextextended('talanta:hold:' || $1::bigint::text || ':' || $2::bigint::text, 0))`

// LockUserEvent serialises the hold requests of one user for one event.
func LockUserEvent(ctx context.Context, db DBTX, userID, eventID int64) error {
	_, err := db.Exec(ctx, lockUserEvent, userID, eventID)
	return err
}

// Locks: the ACTIVE holds whose time has run out and that still own one of the requested
// seats, FOR UPDATE, in id order.
// Why: the hold statement is about to take those seats over, and the old hold must then be
// closed if it has no seat left. Holds are locked BEFORE seats everywhere (release, sweeper,
// payment extension), so the old holds have to be locked here, before the seat statement,
// not after it: locking a hold while holding seat locks is the reverse order and can
// deadlock with a sweeper or a release working on that hold. It also makes two requests that
// each take a different seat of the same lapsed hold take turns, so the second one sees the
// hold empty and closes it.
// The seat read inside is a plain read (no lock); it only tells us which holds to lock.
const lockLapsedHoldsOfSeats = `
SELECT h.id
FROM holds h
WHERE h.status = 'ACTIVE'
  AND h.id IN (
      SELECT es.hold_id FROM event_seats es
      WHERE es.event_id = $1 AND es.id = ANY($2::bigint[])
        AND es.status = 'HELD' AND es.hold_expires_at <= now())
ORDER BY h.id
FOR UPDATE OF h`

// LockLapsedHoldsOfSeats locks the lapsed holds that own any of the given seats and returns
// their ids.
func LockLapsedHoldsOfSeats(ctx context.Context, db DBTX, eventID int64, seatIDs []int64) ([]int64, error) {
	return queryIDs(ctx, db, lockLapsedHoldsOfSeats, eventID, seatIDs)
}

// FreeSeat is one requested seat that was free and is now locked by this transaction.
type FreeSeat struct {
	ID int64
	// PreviousHoldID is the lapsed hold the seat is being taken over from; nil when the seat
	// was simply available.
	PreviousHoldID *int64
}

// Locks: the requested event_seats rows that are free, FOR UPDATE, in id order.
// Why: this is the statement that stops two fans getting the same seat (§6.2).
//   - FOR UPDATE: a second transaction that wants the same seat waits here until the first
//     ends, then re-reads the row, finds it HELD and unexpired, and does not get it. Without
//     the lock both would read AVAILABLE and both would write.
//   - ORDER BY id: every transaction locks seats in the same order, so requests for {5, 9}
//     and {9, 5} cannot each hold one and wait for the other.
//   - A seat is free when it is AVAILABLE, or HELD by a hold whose time has run out. Lapsed
//     holds are reclaimed here, in the WHERE clause; nothing depends on the sweeper.
//
// A seat that is BLOCKED, SOLD, held by a live hold, has no category (always BLOCKED), or is
// not part of the event is simply not a candidate: it is missing from the result, and the
// caller reports it as unavailable and rolls back.
//
// §6.2 writes the seats in the same statement. Here the lock and the write are two
// statements of one transaction (this one, then markSeatsHeld) so that the seat write can be
// the LAST thing the transaction does: the seat map's change poll goes by the time a row was
// written, so nothing that might wait may come between that write and the commit.
const lockFreeSeats = `
SELECT id, CASE WHEN status = 'HELD' THEN hold_id END AS previous_hold_id
FROM event_seats
WHERE event_id = $1
  AND id = ANY($2::bigint[])
  AND (status = 'AVAILABLE' OR (status = 'HELD' AND hold_expires_at <= now()))
ORDER BY id
FOR UPDATE`

// LockFreeSeats locks the free seats among seatIDs and returns them. Fewer than
// len(seatIDs) means the hold cannot be made: the caller must roll back.
func LockFreeSeats(ctx context.Context, db DBTX, eventID int64, seatIDs []int64) ([]FreeSeat, error) {
	rows, err := db.Query(ctx, lockFreeSeats, eventID, seatIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (FreeSeat, error) {
		var s FreeSeat
		err := row.Scan(&s.ID, &s.PreviousHoldID)
		return s, err
	})
}

// Writes: the seats this transaction locked with lockFreeSeats, to HELD by the new hold.
// It takes no new lock: every row named here is already locked by this transaction, which is
// why it has no status condition of its own (the lock statement checked it and nothing can
// have changed since). It must be the last statement before commit. version is bumped and
// updated_at is set by the table's trigger, so the seat map poll sees the change.
const markSeatsHeld = `
UPDATE event_seats
SET status = 'HELD', hold_id = $2, hold_expires_at = $3, version = version + 1
WHERE id = ANY($1::bigint[])`

// MarkSeatsHeld marks seats already locked by LockFreeSeats as HELD by holdID until
// expiresAt and returns how many rows it wrote.
func MarkSeatsHeld(ctx context.Context, db DBTX, seatIDs []int64, holdID int64, expiresAt time.Time) (int64, error) {
	tag, err := db.Exec(ctx, markSeatsHeld, seatIDs, holdID, expiresAt)
	return tag.RowsAffected(), err
}

// Locks: the given ACTIVE holds, FOR UPDATE, skipping any that another transaction has locked.
// Why: the fallback for a take-over the hold flow could not prepare for (the old hold ran
// out between the read that looks for lapsed holds and the seat statement). The seats are
// already locked, so waiting for a hold here would be the reverse lock order; SKIP LOCKED
// never waits, so it cannot deadlock. A hold that is skipped is being worked on by a sweeper
// or a release, which closes it itself.
const tryLockHolds = `
SELECT id FROM holds WHERE id = ANY($1::bigint[]) AND status = 'ACTIVE'
ORDER BY id
FOR UPDATE SKIP LOCKED`

// TryLockHolds locks the holds it can get without waiting and returns their ids.
func TryLockHolds(ctx context.Context, db DBTX, holdIDs []int64) ([]int64, error) {
	return queryIDs(ctx, db, tryLockHolds, holdIDs)
}

// Writes: the given holds, already locked by the caller, to EXPIRED when the only seats still
// HELD by them are the ones in $2, which the caller has locked and is about to take over.
// A lapsed hold whose seats were all taken over must not stay ACTIVE pointing at nothing; one
// that keeps other seats stays as it is until they are taken, released or swept.
const expireEmptiedHolds = `
UPDATE holds h SET status = 'EXPIRED'
WHERE h.id = ANY($1::bigint[]) AND h.status = 'ACTIVE'
  AND NOT EXISTS (
      SELECT 1 FROM event_seats es
      WHERE es.hold_id = h.id AND es.status = 'HELD' AND es.id <> ALL($2::bigint[]))
RETURNING h.id`

// ExpireEmptiedHolds closes the given holds that will have no seat left once takenSeatIDs
// are taken over, and returns their ids.
func ExpireEmptiedHolds(ctx context.Context, db DBTX, holdIDs, takenSeatIDs []int64) ([]int64, error) {
	return queryIDs(ctx, db, expireEmptiedHolds, holdIDs, takenSeatIDs)
}

// Locks: the events row a hold belongs to, FOR SHARE, until the transaction ends.
// Why: release and the payment extension start from a hold, not from an event, but they
// follow the same lock order as everything else: event first. A hold's event never changes,
// so reading it with a plain subquery before any lock is taken is safe. With the event
// shared, a cancel of that event either finishes before the release starts (and the release
// then finds the hold already RELEASED) or waits for it.
// Like a hold, it goes through the event's gate (shared) first; see lockEventForHold.
const lockEventOfHold = `
WITH ev AS MATERIALIZED (
    SELECT h.event_id FROM holds h WHERE h.id = $1
), gate AS MATERIALIZED (
    SELECT pg_advisory_xact_lock_shared(hashtextextended('talanta:event:' || ev.event_id::text, 0)) FROM ev
)
SELECT e.id FROM events e, ev, gate
WHERE e.id = ev.event_id
FOR SHARE OF e`

// LockEventOfHold share-locks the event of a hold and returns its id. ErrNoRows when there
// is no such hold.
func LockEventOfHold(ctx context.Context, db DBTX, holdID int64) (int64, error) {
	var id int64
	err := db.QueryRow(ctx, lockEventOfHold, holdID).Scan(&id)
	return id, err
}

// LockedHold is a hold row read under its lock, with the database clock.
type LockedHold struct {
	Hold
	// Now is the database's wall clock read AFTER the row lock was granted.
	Now time.Time
}

// Locks: one holds row, FOR UPDATE, until the transaction ends.
// Why: release, the payment extension, booking and the sale of a hold's seats each read the
// hold's status and then act on it and on its seats. The row lock makes them take turns, so a
// release cannot free seats a payment is in the middle of selling, and the "extended once"
// check cannot be passed twice. It is taken before any seat lock (lock order).
//
// The clock is NOT read in this statement. Postgres computes the select list before it waits
// for the row lock, so a clock_timestamp() here is the time the wait BEGAN: a caller that
// queued behind another transaction for longer than the hold had left would judge a hold
// that has since run out as still live, and could extend (revive) it. LockHold reads the
// clock in a second statement, once the lock is held.
const lockHold = `
SELECT id, event_id, user_id, expires_at, status, created_at, extended_at
FROM holds
WHERE id = $1
FOR UPDATE`

// LockHold takes a hold's row lock and returns the row together with the database clock as
// it is once the lock is held. ErrNoRows when there is no such hold.
func LockHold(ctx context.Context, db DBTX, holdID int64) (LockedHold, error) {
	var h LockedHold
	err := db.QueryRow(ctx, lockHold, holdID).
		Scan(&h.ID, &h.EventID, &h.UserID, &h.ExpiresAt, &h.Status, &h.CreatedAt, &h.ExtendedAt)
	if err != nil {
		return h, err
	}
	if h.Now, err = Clock(ctx, db); err != nil {
		return h, fmt.Errorf("read the clock under the lock of hold %d: %w", holdID, err)
	}
	return h, nil
}

// Clock reads the database's wall clock. Everything that judges a time under a lock calls it
// in a statement of its own AFTER the lock is held (see lockHold for why).
func Clock(ctx context.Context, db DBTX) (time.Time, error) {
	var now time.Time
	err := db.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now)
	return now, err
}

// CountHeldSeatsOfHold counts the seats still HELD by a hold. A plain read: the caller has
// the hold's row lock, which is what a release, a sweep, a sale and a prepared take-over of
// the same hold wait for.
func CountHeldSeatsOfHold(ctx context.Context, db DBTX, holdID int64) (int, error) {
	var n int
	err := db.QueryRow(ctx, `SELECT count(*)::int FROM event_seats WHERE hold_id = $1 AND status = 'HELD'`, holdID).Scan(&n)
	return n, err
}

// Locks: the event_seats rows still HELD by the given holds, FOR UPDATE, in id order.
// Why: release and the sweeper give seats back. `hold_id = ANY(...) AND status = 'HELD'` is
// the whole safety rule: a seat that was taken over by a newer hold, or sold, no longer
// matches and is left alone, so a late release or sweep can never free someone else's seat.
// The id order is the same as the hold statement's, so the two cannot deadlock. The caller
// has the hold rows locked.
const freeSeatsOfHolds = `
WITH mine AS (
    SELECT id FROM event_seats
    WHERE hold_id = ANY($1::bigint[]) AND status = 'HELD'
    ORDER BY id
    FOR UPDATE
)
UPDATE event_seats es
SET status = 'AVAILABLE', hold_id = NULL, hold_expires_at = NULL, version = es.version + 1
FROM mine
WHERE es.id = mine.id`

// FreeSeatsOfHolds puts the seats still held by the given holds back on sale and returns
// how many it freed.
func FreeSeatsOfHolds(ctx context.Context, db DBTX, holdIDs []int64) (int64, error) {
	tag, err := db.Exec(ctx, freeSeatsOfHolds, holdIDs)
	return tag.RowsAffected(), err
}

// Locks: the event_seats rows HELD by one hold, FOR UPDATE, in id order.
// Why (correction 4): when a payment starts the hold's expiry is pushed out, and the seats
// carry their own copy of the expiry (it is what the hold statement and the seat map read),
// so both must move together or a seat could be taken over while its hold still looks live.
// The caller has the hold row locked.
const extendSeatsOfHold = `
WITH mine AS (
    SELECT id FROM event_seats
    WHERE hold_id = $1 AND status = 'HELD'
    ORDER BY id
    FOR UPDATE
)
UPDATE event_seats es
SET hold_expires_at = $2
FROM mine
WHERE es.id = mine.id`

// ExtendSeatsOfHold moves the expiry of a hold's seats and returns how many seats it has.
func ExtendSeatsOfHold(ctx context.Context, db DBTX, holdID int64, expiresAt time.Time) (int64, error) {
	tag, err := db.Exec(ctx, extendSeatsOfHold, holdID, expiresAt)
	return tag.RowsAffected(), err
}

// SoldSeats is the outcome of SellSeatsOfHold.
type SoldSeats struct {
	// StillHeld is how many seats the hold still had when the statement ran.
	StillHeld int
	// Sold is how many were flipped to SOLD: all of StillHeld when it equals what the
	// caller expected, otherwise zero.
	Sold int
}

// Locks: the event_seats rows HELD by one hold, FOR UPDATE, in id order.
// Why (correction 5): a payment can succeed after its hold ran out and some of its seats
// were taken over by another fan. Seats are sold ONLY where they still point at this hold
// and are still HELD, so a seat that now belongs to someone else can never be sold twice.
// The statement counts what is left under the lock and sells all of it or none of it: when
// the count is not what the booking expects, nothing is flipped, and the caller marks the
// booking REFUND_REQUIRED (a wrong ledger over a double sale). hold_id is kept on the sold
// seat as the record of which hold bought it; hold_expires_at is cleared.
const sellSeatsOfHold = `
WITH mine AS (
    SELECT id FROM event_seats
    WHERE hold_id = $1 AND status = 'HELD'
    ORDER BY id
    FOR UPDATE
), sold AS (
    UPDATE event_seats es
    SET status = 'SOLD', hold_expires_at = NULL, version = es.version + 1
    FROM mine
    WHERE es.id = mine.id AND (SELECT count(*) FROM mine) = $2
    RETURNING es.id
)
SELECT (SELECT count(*) FROM mine)::int, (SELECT count(*) FROM sold)::int`

// SellSeatsOfHold flips the seats of one hold from HELD to SOLD, but only if exactly
// `expected` of them are still held by it. Call it in the payment-success transaction, after
// LockHold, and make it the LAST statement before commit (the seat map poll goes by the time
// the row was written). When Sold < expected nothing was sold.
func SellSeatsOfHold(ctx context.Context, db DBTX, holdID int64, expected int) (SoldSeats, error) {
	var out SoldSeats
	err := db.QueryRow(ctx, sellSeatsOfHold, holdID, expected).Scan(&out.StillHeld, &out.Sold)
	return out, err
}

// BookingOfHold is one booking of a hold, read under its row lock.
type BookingOfHold struct {
	ID     int64
	HoldID int64
	// Status is the booking's status as stored, whatever it is.
	Status string
	// LivePayment is true while a payment for the booking is INITIATED or PENDING.
	LivePayment bool
}

// Locks: every bookings row of the given holds, in any status, FOR UPDATE, in id order.
// Why (correction 7): a hold that is closed takes its unpaid booking with it, unless a
// payment for it is still live: then the booking is left for the payment flow, which closes
// it when the payment ends. The rows are read WITHOUT a status filter on purpose: the caller
// asks domain.TransitionBooking with the status each row really has, so a booking that is
// already PAID, REFUND_REQUIRED or closed is refused by the rule and left alone, instead of
// being filtered out by a WHERE clause that only repeats what the caller hoped to find.
// Bookings are locked after their hold (lock order); the caller has the hold rows locked,
// and a payment is only ever inserted or finished under that same hold lock, so
// live_payment cannot change while this transaction runs.
const lockBookingsOfHolds = `
SELECT b.id, b.hold_id, b.status,
       EXISTS (SELECT 1 FROM payments p WHERE p.booking_id = b.id AND p.status IN ('INITIATED', 'PENDING')) AS live_payment
FROM bookings b
WHERE b.hold_id = ANY($1::bigint[])
ORDER BY b.id
FOR UPDATE OF b`

// LockBookingsOfHolds locks and returns every booking made from the given holds.
func LockBookingsOfHolds(ctx context.Context, db DBTX, holdIDs []int64) ([]BookingOfHold, error) {
	rows, err := db.Query(ctx, lockBookingsOfHolds, holdIDs)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (BookingOfHold, error) {
		var b BookingOfHold
		err := row.Scan(&b.ID, &b.HoldID, &b.Status, &b.LivePayment)
		return b, err
	})
}

// Writes: the given bookings, already locked by the caller (LockBookingsOfHolds), to status.
// The caller has asked domain.TransitionBooking for each row's real status; the WHERE clause
// repeats that status so a row that is not what was read changes nothing.
const setBookingsStatus = `UPDATE bookings SET status = $2 WHERE id = ANY($1::bigint[]) AND status = $3`

// SetBookingsStatus moves bookings from one status to another and returns how many it moved.
func SetBookingsStatus(ctx context.Context, db DBTX, bookingIDs []int64, to, from string) (int64, error) {
	if len(bookingIDs) == 0 {
		return 0, nil
	}
	tag, err := db.Exec(ctx, setBookingsStatus, bookingIDs, to, from)
	return tag.RowsAffected(), err
}

// Locks: a transaction-level advisory lock, taken only if it is free.
// Why: two workers must not sweep at once. They would not corrupt anything (each hold is
// taken with SKIP LOCKED), but they would double the load for nothing. The loser gets false
// and leaves; it does not wait.
const trySweeperLock = `SELECT pg_try_advisory_xact_lock(hashtextextended('talanta:hold-sweeper', 0))`

// TrySweeperLock reports whether this transaction is now the only sweeper.
func TrySweeperLock(ctx context.Context, db DBTX) (bool, error) {
	var ok bool
	err := db.QueryRow(ctx, trySweeperLock).Scan(&ok)
	return ok, err
}

// Locks: the events rows that have a lapsed ACTIVE hold, FOR SHARE, skipping any that a
// writer has locked.
// Why: the sweeper follows the same lock order as everything else (event, holds, seats).
// With the event shared, a cancel of that event cannot run between the sweeper's hold locks
// and its seat writes, so the two cannot deadlock, whatever order cancel touches seats in.
// SKIP LOCKED because the sweeper must never wait: an event an organiser is cancelling or
// editing right now is simply left for the next run. The inner read is a plain read through
// holds_active_expiry_idx.
const lockEventsWithExpiredHolds = `
SELECT e.id FROM events e
WHERE e.id IN (SELECT h.event_id FROM holds h WHERE h.status = 'ACTIVE' AND h.expires_at <= now())
ORDER BY e.id
FOR SHARE OF e SKIP LOCKED`

// LockEventsWithExpiredHolds share-locks the events the sweeper may work on and returns
// their ids.
func LockEventsWithExpiredHolds(ctx context.Context, db DBTX) ([]int64, error) {
	return queryIDs(ctx, db, lockEventsWithExpiredHolds)
}

// Locks: up to $2 ACTIVE holds of the given events whose time has run out, FOR UPDATE SKIP
// LOCKED.
// Why (correction 7): the sweeper is driven from holds, not from seats. Sweeping seat rows
// directly could free some seats of a hold while a payment transaction has the others
// locked, leaving half a hold. Locking the hold first means the sweeper either owns the
// whole hold or, when a release or a payment has it, skips the whole hold. SKIP LOCKED also
// means the sweeper never waits behind a live request. Oldest first, in batches, so one run
// never holds many locks for long. Read through holds_active_expiry_idx.
const lockExpiredHolds = `
SELECT id FROM holds
WHERE status = 'ACTIVE' AND expires_at <= now() AND event_id = ANY($1::bigint[])
ORDER BY expires_at, id
LIMIT $2
FOR UPDATE SKIP LOCKED`

// LockExpiredHolds locks a batch of lapsed holds for the sweeper and returns their ids.
func LockExpiredHolds(ctx context.Context, db DBTX, eventIDs []int64, limit int) ([]int64, error) {
	return queryIDs(ctx, db, lockExpiredHolds, eventIDs, limit)
}

// Writes: the given holds, locked by the sweeper, to EXPIRED.
const expireHolds = `UPDATE holds SET status = 'EXPIRED' WHERE id = ANY($1::bigint[]) AND status = 'ACTIVE'`

// ExpireHolds marks the sweeper's batch EXPIRED and returns how many it closed.
func ExpireHolds(ctx context.Context, db DBTX, holdIDs []int64) (int64, error) {
	tag, err := db.Exec(ctx, expireHolds, holdIDs)
	return tag.RowsAffected(), err
}

// IsDeadlock reports whether err is Postgres's "deadlock detected" (SQLSTATE 40P01). The lock
// order above is meant to make one impossible; a caller that can simply run again (the
// sweeper) checks for it anyway.
func IsDeadlock(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "40P01"
}

func queryIDs(ctx context.Context, db DBTX, sql string, args ...any) ([]int64, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("collect ids: %w", err)
	}
	return ids, nil
}

// The text of the hold queries, for tests that EXPLAIN them against a full venue.
const (
	SQLLockFreeSeats               = lockFreeSeats
	SQLMarkSeatsHeld               = markSeatsHeld
	SQLLockLapsedHoldsOfSeats      = lockLapsedHoldsOfSeats
	SQLFreeSeatsOfHolds            = freeSeatsOfHolds
	SQLLockExpiredHolds            = lockExpiredHolds
	SQLListCategoryBlocks          = listCategoryBlocks
	SQLListBestAvailableCandidates = listBestAvailableCandidates
	SQLCountFreeSeatsOfCategory    = countFreeSeatsOfCategory
)
