package events

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// Readiness says whether an event can be published and, if not, what is missing.
type Readiness struct {
	EventID           int64
	Status            domain.EventStatus
	Ready             bool
	SeatTotal         int
	SeatsWithCategory int
	Problems          []domain.ReadinessProblem
}

// readiness works out what stops ev being published. It takes the queries so publish can
// run it inside its transaction, after the locks, on exactly what it is about to copy.
func (s *Service) readiness(ctx context.Context, q *store.Queries, ev store.Event) (Readiness, error) {
	out := Readiness{
		EventID:  ev.ID,
		Status:   domain.EffectiveEventStatus(domain.EventStatus(ev.Status), ev.SalesOpenAt, ev.SalesCloseAt, s.now()),
		Problems: []domain.ReadinessProblem{},
	}
	total, err := q.CountActiveSeatsByVenue(ctx, ev.VenueID)
	if err != nil {
		return out, fmt.Errorf("count seats of venue %d: %w", ev.VenueID, err)
	}
	cats, err := q.ListEventCategoriesCheapestFirst(ctx, ev.ID)
	if err != nil {
		return out, fmt.Errorf("list categories of event %d: %w", ev.ID, err)
	}
	unpainted, err := q.CountUnpaintedActiveSeatsByBlock(ctx, ev.ID)
	if err != nil {
		return out, fmt.Errorf("count unpainted seats of event %d: %w", ev.ID, err)
	}

	out.SeatTotal = int(total)
	out.SeatsWithCategory = out.SeatTotal
	if total == 0 {
		out.Problems = append(out.Problems, domain.ReadinessProblem{Code: domain.ReadinessNoSeats, Message: "The venue has no seats to sell."})
	}
	if len(cats) == 0 {
		out.Problems = append(out.Problems, domain.ReadinessProblem{Code: domain.ReadinessNoCategories, Message: "Add at least one ticket category."})
	}
	for _, u := range unpainted {
		id, code, n := u.BlockID, u.BlockCode, int(u.Seats)
		out.SeatsWithCategory -= n
		seats := "seats"
		if n == 1 {
			seats = "seat"
		}
		out.Problems = append(out.Problems, domain.ReadinessProblem{
			Code:    domain.ReadinessSeatsWithoutCategory,
			Message: fmt.Sprintf("Block %s has %d %s with no category.", code, n, seats),
			BlockID: &id, BlockCode: &code, SeatCount: &n,
		})
	}
	for _, c := range cats {
		if c.PriceCents > 0 {
			continue
		}
		id, name := c.ID, c.Name
		out.Problems = append(out.Problems, domain.ReadinessProblem{
			Code:       domain.ReadinessCategoryWithoutPrice,
			Message:    fmt.Sprintf("Category %s has no price.", name),
			CategoryID: &id, CategoryName: &name,
		})
	}
	out.Problems = append(out.Problems, domain.SalesWindowProblems(ev.KickoffAt, ev.SalesOpenAt, ev.SalesCloseAt, s.now())...)
	out.Ready = len(out.Problems) == 0
	return out, nil
}

// Readiness reports whether an event can be published.
func (s *Service) Readiness(ctx context.Context, eventID int64) (Readiness, error) {
	row, err := s.getEvent(ctx, eventID)
	if err != nil {
		return Readiness{}, err
	}
	return s.readiness(ctx, s.q, row.Event)
}

// PublishResult is the answer of Publish.
type PublishResult struct {
	Event Event
	// SeatsOnSale is the event's inventory rows that are for sale (not blocked).
	SeatsOnSale int
	// AlreadyPublished is true when this call changed nothing.
	AlreadyPublished bool
}

// Publish creates the event's inventory (one event_seats row per active seat) and moves it
// from DRAFT to ON_SALE when the sales window is open now, PUBLISHED otherwise.
//
// Order inside the one transaction, and why:
//
//  1. Raise the statement timeout for this transaction only: copying ~60,000 seats takes
//     longer than the pool's ordinary limit.
//  2. Lock the event row. Painting and category edits take the same lock, so none can slip
//     between "read the painting" and "copy it". They do not queue behind this for long:
//     every taker of the lock waits Policy.LockWait and then answers EVENT_BUSY, a second
//     publish included.
//  3. Check the status read under the lock. Already published: nothing to do, answer with
//     the current counts. Cancelled or completed: refused.
//  4. Lock the venue row (shared) so no block is deleted or regenerated mid-copy.
//  5. Check readiness on what is about to be copied; refuse with the problems.
//  6. Insert the inventory, then set the status and published_at.
func (s *Service) Publish(ctx context.Context, eventID int64) (PublishResult, error) {
	var already bool
	err := store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		if err := store.SetLocalStatementTimeout(ctx, tx, s.policy.PublishTimeout); err != nil {
			return err
		}
		q := store.New(tx)
		ev, err := s.lockEvent(ctx, q, eventID)
		if err != nil {
			return err
		}
		from := domain.EventStatus(ev.Status)
		if from.IsPublished() {
			already = true
			return nil
		}
		to := domain.PublishStatus(ev.SalesOpenAt, ev.SalesCloseAt, s.now())
		if err := domain.TransitionEvent(from, to); err != nil {
			return err
		}
		if _, err := q.LockVenueForPublish(ctx, ev.VenueID); err != nil {
			return fmt.Errorf("lock venue %d: %w", ev.VenueID, err)
		}
		ready, err := s.readiness(ctx, q, ev)
		if err != nil {
			return err
		}
		if !ready.Ready {
			return domain.EventNotReady(ready.Problems)
		}
		if _, err := store.PublishEventSeats(ctx, tx, eventID); err != nil {
			return err
		}
		moved, err := q.SetEventStatus(ctx, store.SetEventStatusParams{
			ID: eventID, Status: string(to), FromStatuses: []string{string(from)},
		})
		if err != nil {
			return fmt.Errorf("set status of event %d: %w", eventID, err)
		}
		if moved != 1 {
			// Cannot happen while the row lock is held; refusing beats publishing blind.
			return fmt.Errorf("publish event %d: status changed under the row lock", eventID)
		}
		return nil
	})
	if err != nil {
		return PublishResult{}, err
	}
	if !already {
		// Counts cached while the event was a draft (all zero) are now wrong.
		s.counts.invalidate(eventID)
		s.log.InfoContext(ctx, "event published", "event_id", eventID)
	}

	ev, err := s.GetAdmin(ctx, eventID)
	if err != nil {
		return PublishResult{}, err
	}
	inv, err := s.inventory(ctx, eventID)
	if err != nil {
		return PublishResult{}, err
	}
	return PublishResult{Event: ev, SeatsOnSale: inv.Total().Sellable(), AlreadyPublished: already}, nil
}

// CancelResult counts what cancelling an event did.
type CancelResult struct {
	Event             Event
	TicketsVoided     int
	BookingsCancelled int
	RefundsQueued     int
	RefundTotalCents  int64
	HoldsReleased     int
}

// cancelRefundReason is stored on every refund queued by a cancellation.
const cancelRefundReason = "The event was cancelled"

// Cancel moves an event to CANCELLED, releases its holds, voids its tickets, closes unpaid
// bookings and queues one refund per successful payment, all in one transaction. Cancelling
// a cancelled event changes nothing and answers with zero counts.
//
// On a sold-out stadium that is about 60,000 tickets and 15,000 bookings rewritten at once,
// so like publish it raises the statement timeout for its own transaction (and its route has
// the longer request timeout). It stays one transaction: a cancel that stopped half way
// would leave valid tickets for an event that is off.
func (s *Service) Cancel(ctx context.Context, eventID int64) (CancelResult, error) {
	var out CancelResult
	err := store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		if err := store.SetLocalStatementTimeout(ctx, tx, s.policy.CancelTimeout); err != nil {
			return err
		}
		q := store.New(tx)
		ev, err := s.lockEvent(ctx, q, eventID)
		if err != nil {
			return err
		}
		from := domain.EventStatus(ev.Status)
		if from == domain.EventCancelled {
			return nil
		}
		if err := domain.TransitionEvent(from, domain.EventCancelled); err != nil {
			return err
		}

		holds, err := q.ReleaseHoldsOfEvent(ctx, eventID)
		if err != nil {
			return fmt.Errorf("release holds of event %d: %w", eventID, err)
		}
		if _, err := q.FreeHeldSeatsOfEvent(ctx, eventID); err != nil {
			return fmt.Errorf("free held seats of event %d: %w", eventID, err)
		}
		tickets, err := q.VoidTicketsOfEvent(ctx, eventID)
		if err != nil {
			return fmt.Errorf("void tickets of event %d: %w", eventID, err)
		}
		unpaid, err := q.CancelUnpaidBookingsOfEvent(ctx, eventID)
		if err != nil {
			return fmt.Errorf("cancel unpaid bookings of event %d: %w", eventID, err)
		}
		refunds, err := q.QueueRefundsForEvent(ctx, store.QueueRefundsForEventParams{EventID: eventID, Reason: cancelRefundReason})
		if err != nil {
			return fmt.Errorf("queue refunds of event %d: %w", eventID, err)
		}
		if _, err := q.MarkPaidBookingsRefundRequired(ctx, eventID); err != nil {
			return fmt.Errorf("mark paid bookings of event %d: %w", eventID, err)
		}
		moved, err := q.SetEventStatus(ctx, store.SetEventStatusParams{
			ID: eventID, Status: string(domain.EventCancelled), FromStatuses: []string{string(from)},
		})
		if err != nil {
			return fmt.Errorf("set status of event %d: %w", eventID, err)
		}
		if moved != 1 {
			return fmt.Errorf("cancel event %d: status changed under the row lock", eventID)
		}

		out.HoldsReleased, out.TicketsVoided, out.BookingsCancelled = int(holds), int(tickets), int(unpaid)
		out.RefundsQueued = len(refunds)
		for _, cents := range refunds {
			out.RefundTotalCents += cents
		}
		return nil
	})
	if err != nil {
		return CancelResult{}, err
	}
	s.counts.invalidate(eventID)
	s.log.InfoContext(ctx, "event cancelled", "event_id", eventID, "tickets_voided", out.TicketsVoided,
		"refunds_queued", out.RefundsQueued, "holds_released", out.HoldsReleased)
	out.Event, err = s.GetAdmin(ctx, eventID)
	return out, err
}
