package payments

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// Cursor is the position after the last row of a page of alerts or refunds.
type Cursor struct {
	CreatedAt time.Time `json:"c"`
	ID        int64     `json:"i"`
}

func (c *Cursor) valid() bool { return c.ID >= 1 && !c.CreatedAt.IsZero() }

var errBadCursor = domain.InvalidField("cursor", "cursor", "Use the next_cursor value from the previous page.")

// AlertFilter narrows ListAlerts.
type AlertFilter struct {
	EventID *int64
	// Acknowledged nil means both; false is open alerts only.
	Acknowledged *bool
}

// ListAlerts returns one page of organiser alerts, newest first.
func (s *Service) ListAlerts(ctx context.Context, f AlertFilter, limit int, after *Cursor) ([]store.AlertView, *Cursor, error) {
	arg := store.ListAlertViewsParams{EventID: f.EventID, Acknowledged: f.Acknowledged, PageLimit: int32(limit) + 1}
	if after != nil {
		if !after.valid() {
			return nil, nil, errBadCursor
		}
		arg.HasCursor, arg.CursorCreatedAt, arg.CursorID = true, after.CreatedAt, after.ID
	}
	rows, err := s.q.ListAlertViews(ctx, arg)
	if err != nil {
		return nil, nil, fmt.Errorf("list alerts: %w", err)
	}
	var next *Cursor
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1].OrganiserAlert
		next = &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	out := make([]store.AlertView, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.AlertView(r))
	}
	return out, next, nil
}

// AckAlert marks an alert as seen. Safe to repeat: the first acknowledgement time is kept.
func (s *Service) AckAlert(ctx context.Context, alertID int64) (store.AlertView, error) {
	n, err := s.q.AckAlert(ctx, alertID)
	if err != nil {
		return store.AlertView{}, fmt.Errorf("acknowledge alert %d: %w", alertID, err)
	}
	if n == 0 {
		return store.AlertView{}, domain.ErrNotFound
	}
	v, err := s.q.GetAlertView(ctx, alertID)
	if err != nil {
		return store.AlertView{}, fmt.Errorf("get alert %d: %w", alertID, err)
	}
	return v, nil
}

// RefundFilter narrows ListRefunds.
type RefundFilter struct {
	Status  *string
	EventID *int64
}

// ListRefunds returns one page of the refund queue, oldest first.
func (s *Service) ListRefunds(ctx context.Context, f RefundFilter, limit int, after *Cursor) ([]store.RefundView, *Cursor, error) {
	arg := store.ListRefundViewsParams{Status: f.Status, EventID: f.EventID, PageLimit: int32(limit) + 1}
	if after != nil {
		if !after.valid() {
			return nil, nil, errBadCursor
		}
		arg.HasCursor, arg.CursorCreatedAt, arg.CursorID = true, after.CreatedAt, after.ID
	}
	rows, err := s.q.ListRefundViews(ctx, arg)
	if err != nil {
		return nil, nil, fmt.Errorf("list refunds: %w", err)
	}
	var next *Cursor
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1].Refund
		next = &Cursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	out := make([]store.RefundView, 0, len(rows))
	for _, r := range rows {
		out = append(out, store.RefundView(r))
	}
	return out, next, nil
}

// CompleteRefund records that a refund was paid back by hand in the M-Pesa portal. It moves
// no money. Safe to repeat: a refund that is already DONE is returned as it is.
//
// When it was the booking's last pending refund and the booking is REFUND_REQUIRED, the
// booking becomes REFUNDED and any ticket it still has is voided, in the same transaction.
// A booking that is PAID (the refund was for a second, duplicate payment) stays PAID and
// keeps its tickets.
//
// Seats are never touched here, and that is deliberate. A REFUND_REQUIRED booking on a live
// event never owns a SOLD seat: it got that way because the payment could NOT sell (the
// seats were resold, the hold was gone, the amount was wrong), so there is nothing to give
// back. The one way a booking with SOLD seats becomes REFUND_REQUIRED is its event being
// cancelled, and a cancelled event's sold seats stay SOLD on purpose (no SOLD -> AVAILABLE
// exists; the event is off, its tickets were voided by the cancel). There is no operation
// that refunds one paid booking of a live event; adding one would need a rule for putting
// its seats back on sale, which does not exist today.
//
// Lock order: the event (shared, bounded: EVENT_BUSY), the booking's hold, the booking, the
// refund.
func (s *Service) CompleteRefund(ctx context.Context, refundID int64) (store.RefundView, error) {
	err := store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		q := store.New(tx)
		scope, err := q.GetRefundScope(ctx, refundID)
		if errors.Is(err, store.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("complete refund %d: read scope: %w", refundID, err)
		}
		if _, err := store.LockEventForHoldWithin(ctx, tx, scope.EventID, s.policy.EventLockWait); store.IsLockTimeout(err) {
			busy := domain.EventBusy("This event is being updated. Try again in a moment.")
			busy.Err = err
			return busy
		} else if err != nil {
			return fmt.Errorf("complete refund %d: lock event %d: %w", refundID, scope.EventID, err)
		}
		if scope.HoldID != nil {
			if _, err := store.LockHold(ctx, tx, *scope.HoldID); err != nil {
				return fmt.Errorf("complete refund %d: lock hold %d: %w", refundID, *scope.HoldID, err)
			}
		}
		b, err := store.LockBooking(ctx, tx, scope.BookingID)
		if err != nil {
			return fmt.Errorf("complete refund %d: lock booking %d: %w", refundID, scope.BookingID, err)
		}
		r, err := store.LockRefund(ctx, tx, refundID)
		if err != nil {
			return fmt.Errorf("complete refund %d: lock refund: %w", refundID, err)
		}
		if r.Status == "DONE" {
			return nil
		}
		if n, err := q.CompleteRefund(ctx, refundID); err != nil || n != 1 {
			return fmt.Errorf("complete refund %d: %d rows: %w", refundID, n, err)
		}

		if domain.BookingStatus(b.Status) != domain.BookingRefundRequired {
			return nil
		}
		pending, err := q.CountPendingRefundsOfBooking(ctx, b.ID)
		if err != nil {
			return fmt.Errorf("complete refund %d: count pending refunds: %w", refundID, err)
		}
		if pending > 0 {
			return nil
		}
		if err := moveBooking(ctx, q, b, domain.BookingRefunded); err != nil {
			return err
		}
		if _, err := s.tickets.VoidForBooking(ctx, tx, b.ID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return store.RefundView{}, err
	}
	v, err := s.q.GetRefundView(ctx, refundID)
	if err != nil {
		return store.RefundView{}, fmt.Errorf("get refund %d: %w", refundID, err)
	}
	s.log.InfoContext(ctx, "refund marked as paid", "refund_id", refundID, "booking_id", v.Refund.BookingID)
	return v, nil
}
