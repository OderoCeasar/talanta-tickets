package store

import (
	"context"
)

// The two statements of ticket issue that take explicit row locks. Both sit inside the lock
// order written at the top of holds.go: the event row (step 1) before the booking (step 4).

// Locks: the one events row, FOR SHARE, until the transaction ends.
// Why: cancel takes the same row FOR UPDATE, voids the event's tickets and only then
// commits. A ticket inserted while a cancel is running, by a transaction that did not hold
// the event, would be missed by that cancel's void statement and stay VALID for an event
// that is off. With this lock an issue either commits before cancel reads anything (and its
// tickets are voided with the rest) or waits and then reads CANCELLED and is refused.
// A payment transaction that follows the lock order already holds this row FOR SHARE (it
// took the event shared before it touched the hold); locking a row this transaction already
// has locked returns at once, so for that caller the statement is only the status read.
const lockEventForTicketIssue = `
SELECT status FROM events WHERE id = $1 FOR SHARE`

// LockEventForTicketIssue share-locks an event and returns its stored status. ErrNoRows when
// there is no such event.
func LockEventForTicketIssue(ctx context.Context, db DBTX, eventID int64) (string, error) {
	var status string
	err := db.QueryRow(ctx, lockEventForTicketIssue, eventID).Scan(&status)
	return status, err
}

// BookingForTickets is a booking row read under its lock.
type BookingForTickets struct {
	ID        int64
	EventID   int64
	UserID    int64
	HoldID    *int64
	Status    string
	Reference string
}

// Locks: the one bookings row, FOR UPDATE, until the transaction ends.
// Why: issuing is "look for this booking's tickets, and make them if there are none". Two
// transactions doing that for one booking at once would both find none. With the row lock
// they take turns: the second waits for the first to commit, then finds its tickets and
// returns those. (tickets_live_seat_idx would stop the second insert anyway, but as an error
// in the middle of a payment, not as the same answer.) The status read under the lock is
// also what the "is this booking PAID" precondition is judged on.
const lockBookingForTickets = `
SELECT id, event_id, user_id, hold_id, status, reference FROM bookings WHERE id = $1 FOR UPDATE`

// LockBookingForTickets takes a booking's row lock and returns the row. ErrNoRows when there
// is no such booking.
func LockBookingForTickets(ctx context.Context, db DBTX, bookingID int64) (BookingForTickets, error) {
	var b BookingForTickets
	err := db.QueryRow(ctx, lockBookingForTickets, bookingID).
		Scan(&b.ID, &b.EventID, &b.UserID, &b.HoldID, &b.Status, &b.Reference)
	return b, err
}

// ConstraintTicketsLiveSeat is the unique index that allows one live ticket per seat.
const ConstraintTicketsLiveSeat = "tickets_live_seat_idx"

// TicketView is a ticket with everything needed to show it: its event, booking reference
// and seat in words. The three ticket view queries select the same columns, so their row
// types convert to this one.
type TicketView = ListTicketViewsOfBookingsRow
