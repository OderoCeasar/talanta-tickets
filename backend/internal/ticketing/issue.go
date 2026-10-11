package ticketing

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// Why an issue was refused. None is a domain error: each means the caller broke the
// precondition (or the data is inconsistent), which is a fault on our side, never something
// a fan did. Match with errors.Is.
var (
	ErrBookingNotFound = errors.New("ticketing: no such booking")
	// ErrBookingNotPaid: the booking's status, read under its row lock, is not PAID.
	ErrBookingNotPaid = errors.New("ticketing: booking is not PAID")
	// ErrBookingEmpty: the booking has no items, so there is nothing to issue.
	ErrBookingEmpty = errors.New("ticketing: booking has no items")
	// ErrSeatsNotSold: at least one of the booking's seats is not SOLD, is not SOLD through
	// the booking's own hold, or belongs to another event.
	ErrSeatsNotSold = errors.New("ticketing: the booking's seats are not all SOLD to it")
	// ErrEventCancelled: tickets are not issued for a cancelled event.
	ErrEventCancelled = errors.New("ticketing: event is cancelled")
	// ErrSeatAlreadyTicketed: a seat of the booking already has a live ticket that belongs
	// to another booking (the tickets_live_seat_idx unique index refused the insert).
	ErrSeatAlreadyTicketed = errors.New("ticketing: a seat already has a live ticket from another booking")
)

// bookingPaid is the only booking status tickets are issued in.
const bookingPaid = "PAID"

// IssueForBooking creates the tickets of a paid booking, one per booking item, inside the
// caller's transaction, and returns them. It makes no call outside the database: the
// signing is arithmetic on a key already in memory.
//
// PRECONDITION. In this same transaction the caller has already:
//
//  1. taken the event shared, as the lock order requires before it touched the hold
//     (store.LockEventForHoldWithin or store.LockEventOfHoldWithin);
//  2. sold the booking's seats (store.SellSeatsOfHold answered Sold): every booking item's
//     seat is SOLD, of the booking's event, and carries the booking's hold_id;
//  3. set the booking's status to PAID.
//
// Issue checks 2 and 3 itself, and that the event is not CANCELLED, and refuses with one of
// the Err values above when any is not so. It re-takes the event's row lock (a no-op for a
// caller that did 1) so that a cancel can never run between "is the event on" and the
// insert.
//
// IDEMPOTENT. A booking that already has tickets, in any status, gets exactly those back and
// nothing is written. Two calls at once for one booking take turns on the booking's row
// lock; the second returns the first one's tickets.
//
// ON ERROR the caller's transaction is still usable: everything here runs under a savepoint
// that is rolled back, so the caller can go on to mark the booking REFUND_REQUIRED and
// commit that instead. (The exception is a failed transaction underneath, for example a
// cancelled context: then nothing can be committed at all.)
//
// The SMS is not sent here. Call SendTicketSMS after the transaction has committed.
func (s *Service) IssueForBooking(ctx context.Context, tx pgx.Tx, bookingID int64) ([]Ticket, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("issue tickets for booking %d: savepoint: %w", bookingID, err)
	}
	tickets, err := s.issue(ctx, sp, bookingID)
	if err != nil {
		if rbErr := sp.Rollback(context.WithoutCancel(ctx)); rbErr != nil {
			return nil, errors.Join(err, fmt.Errorf("roll back savepoint: %w", rbErr))
		}
		return nil, err
	}
	if err := sp.Commit(ctx); err != nil {
		return nil, fmt.Errorf("issue tickets for booking %d: release savepoint: %w", bookingID, err)
	}
	return tickets, nil
}

func (s *Service) issue(ctx context.Context, db store.DBTX, bookingID int64) ([]Ticket, error) {
	q := store.New(db)

	// Lock order: the event (step 1) before the booking (step 4). The booking's event never
	// changes, so reading it without a lock to know which event to lock is safe.
	eventID, err := q.GetBookingEventID(ctx, bookingID)
	if errors.Is(err, store.ErrNoRows) {
		return nil, fmt.Errorf("%w: %d", ErrBookingNotFound, bookingID)
	}
	if err != nil {
		return nil, fmt.Errorf("issue tickets for booking %d: read event: %w", bookingID, err)
	}
	eventStatus, err := store.LockEventForTicketIssue(ctx, db, eventID)
	if err != nil {
		return nil, fmt.Errorf("issue tickets for booking %d: lock event %d: %w", bookingID, eventID, err)
	}
	booking, err := store.LockBookingForTickets(ctx, db, bookingID)
	if err != nil {
		return nil, fmt.Errorf("issue tickets for booking %d: lock booking: %w", bookingID, err)
	}

	// The explicit "already issued" check, under the booking's lock. It comes before the
	// state checks on purpose: a repeat must return the same tickets even if something has
	// happened to the booking since (a refund voided them, the event was cancelled).
	existing, err := q.ListTicketViewsOfBookings(ctx, []int64{bookingID})
	if err != nil {
		return nil, fmt.Errorf("issue tickets for booking %d: read existing tickets: %w", bookingID, err)
	}
	if len(existing) > 0 {
		return newTickets(existing), nil
	}

	if booking.Status != bookingPaid {
		return nil, fmt.Errorf("%w: booking %d is %s", ErrBookingNotPaid, bookingID, booking.Status)
	}
	if domain.EventStatus(eventStatus) == domain.EventCancelled {
		return nil, fmt.Errorf("%w: event %d, booking %d", ErrEventCancelled, eventID, bookingID)
	}

	seats, err := q.ListBookingSeatsForIssue(ctx, bookingID)
	if err != nil {
		return nil, fmt.Errorf("issue tickets for booking %d: read seats: %w", bookingID, err)
	}
	if len(seats) == 0 {
		return nil, fmt.Errorf("%w: %d", ErrBookingEmpty, bookingID)
	}
	for _, seat := range seats {
		soldToThisBooking := seat.Status == string(domain.SeatSold) && seat.EventID == booking.EventID && sameHold(seat.HoldID, booking.HoldID)
		if !soldToThisBooking {
			return nil, fmt.Errorf("%w: booking %d, seat %d is %s", ErrSeatsNotSold, bookingID, seat.EventSeatID, seat.Status)
		}
	}

	holder, err := q.GetUserByID(ctx, booking.UserID)
	if err != nil {
		return nil, fmt.Errorf("issue tickets for booking %d: read user %d: %w", bookingID, booking.UserID, err)
	}

	params := store.InsertTicketsParams{
		BookingID: bookingID, EventID: booking.EventID, KeyID: s.keys.Signer.KeyID(),
		HolderName: holder.Name, HolderPhone: holder.PhoneE164,
	}
	for _, seat := range seats {
		claims := Claims{EventID: booking.EventID, GateGroup: domain.GateGroup(seat.StandCode, seat.Tier)}
		if claims.TicketID, err = newUUID(); err != nil {
			return nil, fmt.Errorf("issue tickets for booking %d: %w", bookingID, err)
		}
		if _, err := rand.Read(claims.Nonce[:]); err != nil {
			return nil, fmt.Errorf("issue tickets for booking %d: nonce: %w", bookingID, err)
		}
		token, err := s.keys.Signer.Sign(claims)
		if err != nil {
			return nil, fmt.Errorf("issue tickets for booking %d, seat %d: %w", bookingID, seat.EventSeatID, err)
		}
		params.Ids = append(params.Ids, pgtype.UUID{Bytes: claims.TicketID, Valid: true})
		params.EventSeatIds = append(params.EventSeatIds, seat.EventSeatID)
		params.GateGroups = append(params.GateGroups, claims.GateGroup)
		params.TokenNonces = append(params.TokenNonces, claims.Nonce[:])
		params.QrTokens = append(params.QrTokens, token)
	}

	inserted, err := q.InsertTickets(ctx, params)
	if store.IsUniqueViolation(err, store.ConstraintTicketsLiveSeat) {
		return nil, fmt.Errorf("%w: booking %d", ErrSeatAlreadyTicketed, bookingID)
	}
	if err != nil {
		return nil, fmt.Errorf("issue tickets for booking %d: insert: %w", bookingID, err)
	}
	if int(inserted) != len(seats) {
		return nil, fmt.Errorf("issue tickets for booking %d: inserted %d tickets for %d seats", bookingID, inserted, len(seats))
	}

	rows, err := q.ListTicketViewsOfBookings(ctx, []int64{bookingID})
	if err != nil {
		return nil, fmt.Errorf("issue tickets for booking %d: read back: %w", bookingID, err)
	}
	return newTickets(rows), nil
}

// sameHold reports whether a sold seat's hold is the booking's. A sold seat keeps its
// hold_id as the record of which hold bought it; two nulls are a booking made without a
// hold, which only tests and imports do.
func sameHold(seatHold, bookingHold *int64) bool {
	if seatHold == nil || bookingHold == nil {
		return seatHold == nil && bookingHold == nil
	}
	return *seatHold == *bookingHold
}

// VoidForBooking marks every live ticket of a booking VOID and returns how many it changed.
// It is for refunds and runs inside the caller's transaction, which should hold the
// booking's row lock or the event (as the refund flow does). Calling it again changes
// nothing and returns zero. Event cancel voids through the same status rule
// (domain.TransitionTicket) with its own statement for the whole event.
func (s *Service) VoidForBooking(ctx context.Context, tx pgx.Tx, bookingID int64) (int, error) {
	n, err := store.New(tx).VoidTicketsOfBooking(ctx, store.VoidTicketsOfBookingParams{
		BookingID: bookingID, FromStatuses: domain.TicketStatusesThatCanBecome(domain.TicketVoid),
	})
	if err != nil {
		return 0, fmt.Errorf("void tickets of booking %d: %w", bookingID, err)
	}
	return int(n), nil
}

// newUUID makes a random (version 4) UUID.
func newUUID() ([16]byte, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return id, fmt.Errorf("random ticket id: %w", err)
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return id, nil
}
