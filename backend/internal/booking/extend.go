package booking

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// Extension is the outcome of ExtendForPayment.
type Extension struct {
	HoldID  int64
	EventID int64
	// ExpiresAt is the hold's expiry after the call, whether or not it moved.
	ExpiresAt time.Time
	// Remaining is how long the hold has left, by the database clock.
	Remaining time.Duration
	// Extended is true when this call pushed the expiry out.
	Extended bool
	// AlreadyExtended is true when the hold had been extended before and this call left
	// it alone. Remaining may then be less than the payment window.
	AlreadyExtended bool
	// Seats is how many seats the hold has.
	Seats int
}

// ExtendForPayment is what the payment flow calls when a payment starts (correction 4): a
// hold lasts 10 minutes and payment checks run for up to 5, so a payment begun late could
// outlive its hold. This pushes the hold's expiry out so that at least minRemaining is left,
// on the hold row and on each of its seats, and records it in holds.extended_at.
//
// A hold is extended at most once:
//
//   - enough time left already: nothing is changed and the one extension is not used up;
//   - extended before: nothing is changed, AlreadyExtended is true, and there is no error.
//     The caller decides whether Remaining is enough to start a payment;
//   - not the user's hold, or none: NOT_FOUND;
//   - time already run out (swept or not): HOLD_EXPIRED;
//   - released or already paid for: HOLD_NOT_ACTIVE.
//
// Call it INSIDE the transaction that creates the payment, and BEFORE that transaction locks
// the booking row (lock order: event, hold, booking, payment; seats written last). It takes the
// event's share lock and the hold's row lock,
// which is what makes "release" and "start a payment" take turns: whichever comes second
// sees what the first did.
//
// It writes the seats' hold_expires_at. That is the one seat write that need not be the last
// statement of its transaction: the seats stay HELD, so what the seat map shows does not
// change, and a poller that misses the stamp misses nothing.
func ExtendForPayment(ctx context.Context, tx store.DBTX, userID, holdID int64, minRemaining time.Duration) (Extension, error) {
	if _, err := store.LockEventOfHold(ctx, tx, holdID); errors.Is(err, store.ErrNoRows) {
		return Extension{}, domain.ErrNotFound
	} else if err != nil {
		return Extension{}, fmt.Errorf("lock event of hold %d: %w", holdID, err)
	}
	h, err := store.LockHold(ctx, tx, holdID)
	if errors.Is(err, store.ErrNoRows) || (err == nil && h.UserID != userID) {
		return Extension{}, domain.ErrNotFound
	}
	if err != nil {
		return Extension{}, fmt.Errorf("lock hold %d: %w", holdID, err)
	}
	switch status := domain.EffectiveHoldStatus(domain.HoldStatus(h.Status), h.ExpiresAt, h.Now); status {
	case domain.HoldActive:
	case domain.HoldExpired:
		return Extension{}, domain.HoldExpiredError(h.ID, h.ExpiresAt)
	default:
		return Extension{}, domain.HoldNotActive(h.ID, status, h.ExpiresAt)
	}

	out := Extension{HoldID: h.ID, EventID: h.EventID, ExpiresAt: h.ExpiresAt, Remaining: h.ExpiresAt.Sub(h.Now)}
	wanted := h.Now.Add(minRemaining)
	switch {
	case h.ExtendedAt != nil:
		out.AlreadyExtended = true
		return out, nil
	case !h.ExpiresAt.Before(wanted):
		return out, nil
	}

	// HELD -> HELD: the seats stay held, only their expiry moves.
	if err := domain.TransitionSeat(domain.SeatHeld, domain.SeatHeld); err != nil {
		return Extension{}, err
	}
	moved, err := store.New(tx).ExtendHold(ctx, store.ExtendHoldParams{ID: h.ID, ExpiresAt: wanted})
	if err != nil {
		return Extension{}, fmt.Errorf("extend hold %d: %w", holdID, err)
	}
	if moved != 1 {
		return Extension{}, fmt.Errorf("extend hold %d: changed under its row lock", holdID)
	}
	seats, err := store.ExtendSeatsOfHold(ctx, tx, h.ID, wanted)
	if err != nil {
		return Extension{}, fmt.Errorf("extend seats of hold %d: %w", holdID, err)
	}
	out.ExpiresAt, out.Remaining, out.Extended, out.Seats = wanted, minRemaining, true, int(seats)
	return out, nil
}

// ExtendForPayment is the package function of the same name in a transaction of its own,
// with the policy's payment window. The payment flow should prefer the package function so
// the extension commits or rolls back together with the payment row.
func (s *Service) ExtendForPayment(ctx context.Context, userID, holdID int64) (Extension, error) {
	var out Extension
	err := store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		out, err = ExtendForPayment(ctx, tx, userID, holdID, s.policy.PaymentWindow)
		return err
	})
	return out, err
}

// PaymentWindow is the least time a hold must have left when a payment starts.
func (s *Service) PaymentWindow() time.Duration { return s.policy.PaymentWindow }
