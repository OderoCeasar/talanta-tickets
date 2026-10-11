package booking

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// Release gives a hold's seats back now.
//
//   - Someone else's hold, or none: NOT_FOUND.
//   - Already RELEASED or EXPIRED: nothing to do, no error (safe to repeat).
//   - CONVERTED (the seats were bought): HOLD_NOT_ACTIVE.
//   - A payment for the hold's booking is still live: PAYMENT_IN_PROGRESS; nothing changes.
//   - Otherwise the seats still held by this hold become AVAILABLE, the hold becomes
//     RELEASED and its unpaid booking CANCELLED. (A hold whose time had already run out is
//     recorded as what it is: EXPIRED, its booking EXPIRED.)
//
// Only seats whose hold_id is this hold and that are still HELD are freed, so releasing a
// lapsed hold after someone else took its seats over frees nothing of theirs.
func (s *Service) Release(ctx context.Context, userID, holdID int64) error {
	return store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		// Lock order: the event, the hold row, the booking, and the seats written last.
		// Bounded like a hold's wait: the only thing that keeps an event for long is a
		// cancel of a well-sold one (which is releasing this hold anyway) or a publish.
		if _, err := store.LockEventOfHoldWithin(ctx, tx, holdID, s.policy.EventLockWait); errors.Is(err, store.ErrNoRows) {
			return domain.ErrNotFound
		} else if store.IsLockTimeout(err) {
			busy := domain.EventBusy("This event is being updated. Try again in a moment.")
			busy.Err = err
			return busy
		} else if err != nil {
			return fmt.Errorf("lock event of hold %d: %w", holdID, err)
		}
		h, err := store.LockHold(ctx, tx, holdID)
		if errors.Is(err, store.ErrNoRows) || (err == nil && h.UserID != userID) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock hold %d: %w", holdID, err)
		}
		switch domain.HoldStatus(h.Status) {
		case domain.HoldReleased, domain.HoldExpired:
			return nil
		case domain.HoldConverted:
			return domain.HoldNotActive(h.ID, domain.HoldConverted, h.ExpiresAt)
		}

		q := store.New(tx)
		// A payment starts by locking this same hold row (ExtendForPayment), so none can
		// appear between this read and the end of the transaction.
		live, err := q.GetLivePaymentOfHold(ctx, holdID)
		switch {
		case err == nil:
			return domain.PaymentInProgress(live.ID, live.Status)
		case !errors.Is(err, store.ErrNoRows):
			return fmt.Errorf("look for a live payment of hold %d: %w", holdID, err)
		}

		to, bookingTo := domain.HoldReleased, "CANCELLED"
		if domain.EffectiveHoldStatus(domain.HoldActive, h.ExpiresAt, h.Now) == domain.HoldExpired {
			to, bookingTo = domain.HoldExpired, "EXPIRED"
		}
		if err := errors.Join(
			domain.TransitionHold(domain.HoldStatus(h.Status), to),
			domain.TransitionSeat(domain.SeatHeld, domain.SeatAvailable),
		); err != nil {
			return err
		}
		if _, err := q.SetHoldStatus(ctx, store.SetHoldStatusParams{ID: holdID, Status: string(to)}); err != nil {
			return fmt.Errorf("close hold %d: %w", holdID, err)
		}
		if _, err := store.CloseUnpaidBookingsOfHolds(ctx, tx, []int64{holdID}, bookingTo); err != nil {
			return fmt.Errorf("close booking of hold %d: %w", holdID, err)
		}
		// The seat write, last.
		if _, err := store.FreeSeatsOfHolds(ctx, tx, []int64{holdID}); err != nil {
			return fmt.Errorf("free seats of hold %d: %w", holdID, err)
		}
		return nil
	})
}
