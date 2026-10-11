package booking

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// SweepResult counts what one sweeper run did.
type SweepResult struct {
	Holds    int // lapsed holds closed
	Seats    int // seats put back on sale
	Bookings int // unpaid bookings expired
	// Skipped is true when another sweeper was running and this one left at once.
	Skipped bool
}

// Sweep closes holds whose time has run out (§6.3, correction 7): it frees the seats they
// still hold, marks them EXPIRED and expires their unpaid booking when no payment for it is
// live. The worker calls it every SWEEP_INTERVAL.
//
// It is tidying, not correctness: a lapsed hold's seats can already be held by anyone (the
// hold statement reclaims them) and are already shown as available. What the sweeper adds is
// that the rows say so, and that unpaid bookings do not stay PENDING_PAYMENT for ever.
//
// It works in batches, one short transaction each, driven from the holds table with
// SKIP LOCKED, so it never waits behind a live request and never frees part of a hold that a
// release or a payment is working on. An advisory lock makes a second sweeper leave at once.
func (s *Service) Sweep(ctx context.Context) (SweepResult, error) {
	var total SweepResult
	if err := errors.Join(
		domain.TransitionHold(domain.HoldActive, domain.HoldExpired),
		domain.TransitionSeat(domain.SeatHeld, domain.SeatAvailable),
	); err != nil {
		return total, err
	}
	for batch := 0; batch < s.policy.SweepMaxBatches; batch++ {
		n, skipped, err := s.sweepBatch(ctx, &total)
		if store.IsDeadlock(err) {
			// The lock order is meant to rule this out. If it happens anyway the batch was
			// rolled back whole and can simply be run again, once.
			s.log.WarnContext(ctx, "hold sweeper: deadlock, retrying the batch once", "err", err)
			n, skipped, err = s.sweepBatch(ctx, &total)
		}
		if err != nil {
			return total, err
		}
		if skipped {
			total.Skipped = true
		}
		if skipped || n < s.policy.SweepBatch {
			break
		}
	}
	return total, nil
}

// sweepBatch closes one batch of lapsed holds in one transaction and adds what it did to
// total. It returns how many holds it closed and whether another sweeper had the lock.
func (s *Service) sweepBatch(ctx context.Context, total *SweepResult) (n int, skipped bool, err error) {
	var done SweepResult
	err = store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		alone, err := store.TrySweeperLock(ctx, tx)
		if err != nil {
			return fmt.Errorf("take the sweeper lock: %w", err)
		}
		if !alone {
			skipped = true
			return nil
		}
		// Lock order: events, then holds, then bookings, and the seats written last.
		events, err := store.LockEventsWithExpiredHolds(ctx, tx)
		if err != nil {
			return fmt.Errorf("lock events with lapsed holds: %w", err)
		}
		if len(events) == 0 {
			return nil
		}
		ids, err := store.LockExpiredHolds(ctx, tx, events, s.policy.SweepBatch)
		if err != nil {
			return fmt.Errorf("lock lapsed holds: %w", err)
		}
		if len(ids) == 0 {
			return nil
		}
		holds, err := store.ExpireHolds(ctx, tx, ids)
		if err != nil {
			return fmt.Errorf("expire lapsed holds: %w", err)
		}
		bookings, err := store.CloseUnpaidBookingsOfHolds(ctx, tx, ids, "EXPIRED")
		if err != nil {
			return fmt.Errorf("expire bookings of lapsed holds: %w", err)
		}
		seats, err := store.FreeSeatsOfHolds(ctx, tx, ids)
		if err != nil {
			return fmt.Errorf("free seats of lapsed holds: %w", err)
		}
		n = len(ids)
		done = SweepResult{Holds: int(holds), Seats: int(seats), Bookings: int(bookings)}
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	total.Holds += done.Holds
	total.Seats += done.Seats
	total.Bookings += done.Bookings
	return n, skipped, nil
}
