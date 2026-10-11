package booking

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// validateSeatIDs applies the contract's rules for seat_ids: 1 to 50 positive ids, no
// repeats. The HTTP layer checks the same rules on the way in; they are repeated here so no
// other caller can skip them.
func validateSeatIDs(seatIDs []int64) error {
	switch {
	case len(seatIDs) == 0:
		return domain.InvalidField("seat_ids", "min", "Give at least 1.")
	case len(seatIDs) > domain.MaxSeatsPerHold:
		return domain.InvalidField("seat_ids", "max", fmt.Sprintf("Give at most %d.", domain.MaxSeatsPerHold))
	}
	seen := make(map[int64]bool, len(seatIDs))
	for i, id := range seatIDs {
		if id < 1 {
			return domain.InvalidField(fmt.Sprintf("seat_ids[%d]", i), "gte", "Must be 1 or more.")
		}
		if seen[id] {
			return domain.InvalidField("seat_ids", "unique", "Must not repeat a value.")
		}
		seen[id] = true
	}
	return nil
}

// Hold holds exactly the given event seats for the user, all or nothing.
//
// Errors: NOT_FOUND (no such event, or a draft), EVENT_NOT_ON_SALE / SALES_NOT_OPEN /
// SALES_CLOSED, TICKET_LIMIT_EXCEEDED, and SEATS_UNAVAILABLE with exactly the seat ids that
// could not be held. After any error nothing is held.
func (s *Service) Hold(ctx context.Context, userID, eventID int64, seatIDs []int64) (Hold, error) {
	if err := validateSeatIDs(seatIDs); err != nil {
		return Hold{}, err
	}
	ids := slices.Clone(seatIDs)
	slices.Sort(ids)

	var out Hold
	err := store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		holdID, err := s.holdTx(ctx, tx, userID, eventID, ids)
		if err != nil {
			return err
		}
		// The answer is built here, inside the transaction. Read after the commit, a failed
		// read answered 500 to a fan who by then held the seats and could not know it.
		// These are plain reads: they take no lock and wait for none, so the seat write
		// before them is still, in effect, the last thing this transaction does.
		out, err = s.load(ctx, store.New(tx), userID, holdID)
		return err
	})
	if err != nil {
		return Hold{}, err
	}
	return out, nil
}

// admit runs the checks that come before any seat is looked at, taking the first two locks
// of the lock order: the event (FOR SHARE) and the (user, event) advisory lock.
func (s *Service) admit(ctx context.Context, tx store.DBTX, userID, eventID int64, quantity int) error {
	// 1. The event row. A concurrent cancel waits for this transaction, or we wait for it
	// and then read CANCELLED. The wait is bounded: an edit takes milliseconds, but a
	// publish or a cancel of a full stadium keeps the event for many seconds, and a fan is
	// better served by "busy, try again" now than by a timeout later.
	ev, err := store.LockEventForHoldWithin(ctx, tx, eventID, s.policy.EventLockWait)
	if errors.Is(err, store.ErrNoRows) {
		return domain.ErrNotFound
	}
	if store.IsLockTimeout(err) {
		busy := domain.EventBusy("This event is being updated. Try again in a moment.")
		busy.Err = err
		return busy
	}
	if err != nil {
		return fmt.Errorf("lock event %d: %w", eventID, err)
	}
	if err := domain.CheckOnSale(domain.EventStatus(ev.Status), ev.SalesOpenAt, ev.SalesCloseAt, ev.Now); err != nil {
		return err
	}

	// 2. Correction 6: one hold request per (user, event) at a time, so the count below
	// cannot be passed by two requests at once.
	if err := store.LockUserEvent(ctx, tx, userID, eventID); err != nil {
		return fmt.Errorf("lock user %d for event %d: %w", userID, eventID, err)
	}
	have, err := store.New(tx).CountUserSeatsForEvent(ctx, store.CountUserSeatsForEventParams{UserID: userID, EventID: eventID})
	if err != nil {
		return fmt.Errorf("count seats of user %d for event %d: %w", userID, eventID, err)
	}
	if allowed := int(ev.MaxTicketsPerUser); int(have)+quantity > allowed {
		return domain.TicketLimitExceeded(allowed, int(have), quantity)
	}
	return nil
}

// holdTx is the hold transaction. seatIDs are sorted and distinct. Returning an error rolls
// everything back, which is how "never a partial hold" is kept.
func (s *Service) holdTx(ctx context.Context, tx pgx.Tx, userID, eventID int64, seatIDs []int64) (int64, error) {
	if err := s.admit(ctx, tx, userID, eventID, len(seatIDs)); err != nil {
		return 0, err
	}

	// 3. Lapsed holds that still own a requested seat, before any seat lock (lock order).
	prepared, err := store.LockLapsedHoldsOfSeats(ctx, tx, eventID, seatIDs)
	if err != nil {
		return 0, fmt.Errorf("lock lapsed holds: %w", err)
	}

	// 4. The seats, FOR UPDATE in id order. Nothing has been written yet.
	free, err := store.LockFreeSeats(ctx, tx, eventID, seatIDs)
	if err != nil {
		return 0, fmt.Errorf("lock seats: %w", err)
	}
	if len(free) < len(seatIDs) {
		// All or nothing: the error rolls the transaction back and releases the locks.
		return 0, domain.SeatsUnavailable(len(seatIDs), lostSeats(seatIDs, free))
	}

	// Two moves are about to be made: AVAILABLE -> HELD, and HELD -> HELD for a seat taken
	// over from a lapsed hold.
	if err := errors.Join(
		domain.TransitionSeat(domain.SeatAvailable, domain.SeatHeld),
		domain.TransitionSeat(domain.SeatHeld, domain.SeatHeld),
	); err != nil {
		return 0, err
	}
	hold, err := store.New(tx).InsertHold(ctx, store.InsertHoldParams{
		EventID: eventID, UserID: userID, TtlSeconds: s.policy.HoldTTL.Seconds(),
	})
	if err != nil {
		return 0, fmt.Errorf("insert hold: %w", err)
	}
	if err := s.closeTakenOverHolds(ctx, tx, free, seatIDs, prepared); err != nil {
		return 0, err
	}

	// 5. The seat write, last (see store/holds.go: seat writes come last).
	written, err := store.MarkSeatsHeld(ctx, tx, seatIDs, hold.ID, hold.ExpiresAt)
	if err != nil {
		return 0, fmt.Errorf("hold seats: %w", err)
	}
	if int(written) != len(seatIDs) {
		return 0, fmt.Errorf("hold seats: wrote %d of %d locked seats", written, len(seatIDs))
	}
	return hold.ID, nil
}

// lostSeats returns the requested ids that are not among the seats won, in id order.
func lostSeats(asked []int64, won []store.FreeSeat) []int64 {
	got := make(map[int64]bool, len(won))
	for _, w := range won {
		got[w.ID] = true
	}
	lost := []int64{}
	for _, id := range asked {
		if !got[id] {
			lost = append(lost, id)
		}
	}
	return lost
}

// closeTakenOverHolds deals with the lapsed holds whose seats are being taken over: one that
// will have no seat left becomes EXPIRED, with its unpaid booking, so no hold is ever left ACTIVE
// pointing at nothing. One that still has seats stays as it is; its remaining seats are just
// as reclaimable, and it is closed when the last one goes or the sweeper gets to it.
func (s *Service) closeTakenOverHolds(ctx context.Context, tx pgx.Tx, won []store.FreeSeat, seatIDs, prepared []int64) error {
	var previous, unprepared []int64
	for _, w := range won {
		if w.PreviousHoldID == nil || slices.Contains(previous, *w.PreviousHoldID) {
			continue
		}
		previous = append(previous, *w.PreviousHoldID)
		if !slices.Contains(prepared, *w.PreviousHoldID) {
			unprepared = append(unprepared, *w.PreviousHoldID)
		}
	}
	if len(previous) == 0 {
		return nil
	}

	locked := prepared
	if len(unprepared) > 0 {
		// A hold that was not lapsed when step 3 looked but is now. The seats are locked
		// already, so it may only be locked without waiting; if someone else has it, they
		// are closing it.
		got, err := store.TryLockHolds(ctx, tx, unprepared)
		if err != nil {
			return fmt.Errorf("lock taken-over holds: %w", err)
		}
		locked = append(slices.Clone(prepared), got...)
	}
	var mine []int64
	for _, id := range previous {
		if slices.Contains(locked, id) {
			mine = append(mine, id)
		}
	}
	if len(mine) == 0 {
		return nil
	}

	if err := domain.TransitionHold(domain.HoldActive, domain.HoldExpired); err != nil {
		return err
	}
	emptied, err := store.ExpireEmptiedHolds(ctx, tx, mine, seatIDs)
	if err != nil {
		return fmt.Errorf("expire taken-over holds: %w", err)
	}
	if len(emptied) == 0 {
		return nil
	}
	if _, err := closeUnpaidBookings(ctx, tx, emptied, domain.BookingExpired); err != nil {
		return fmt.Errorf("expire bookings of taken-over holds: %w", err)
	}
	return nil
}
