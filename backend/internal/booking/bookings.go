package booking

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// Booking is a booking as its owner sees it.
type Booking struct {
	Row       store.Booking
	Event     store.Event
	VenueName string
	// EventStatus is the event's effective status when the booking was read.
	EventStatus domain.EventStatus
	// HoldExpiresAt is when the seats stop being reserved. Set only while the booking is
	// PENDING_PAYMENT; nil once it is final.
	HoldExpiresAt *time.Time
	// Items are the booked seats with the prices copied at booking time, by block, row and
	// seat number.
	Items []store.ListBookingItemViewsRow
	// LatestPayment is the most recent payment attempt, nil if none was started.
	LatestPayment *store.Payment
}

// BookingCursor is the position after the last booking of a page of ListBookings.
type BookingCursor struct {
	CreatedAt time.Time `json:"c"`
	ID        int64     `json:"i"`
}

// Limits of the Idempotency-Key header, as in the contract.
const (
	MinIdempotencyKeyLength = 8
	MaxIdempotencyKeyLength = 128
)

// ValidIdempotencyKey reports whether key has the shape the contract asks for: 8 to 128 of
// letters, digits, dash and underscore.
func ValidIdempotencyKey(key string) bool {
	if len(key) < MinIdempotencyKeyLength || len(key) > MaxIdempotencyKeyLength {
		return false
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// referenceAlphabet is what a booking reference is drawn from: capitals and digits without
// the ones people confuse when they read a reference out or type it (0 and O, 1 and I and L).
// 31 symbols, six of them: about 887 million references.
const referenceAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// referenceTries is how many references are tried before giving up on a collision. One
// collision is already unlikely; several in a row mean something else is wrong.
const referenceTries = 8

// NewReference makes a booking reference, "TLT-" and six symbols from crypto/rand.
func NewReference() (string, error) {
	const n = 6
	// Rejection sampling: 248 is the largest multiple of 31 that fits a byte, so every
	// symbol is equally likely.
	limit := byte(256 - 256%len(referenceAlphabet))
	out := make([]byte, 0, n)
	buf := make([]byte, 2*n)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("random booking reference: %w", err)
		}
		for _, b := range buf {
			if b < limit && len(out) < n {
				out = append(out, referenceAlphabet[int(b)%len(referenceAlphabet)])
			}
		}
	}
	return "TLT-" + string(out), nil
}

// CreateBooking turns a hold into a booking awaiting payment (§8.1) and reports whether this
// call created it (false: the idempotency key replayed and the existing booking is returned).
//
//   - The hold must be the caller's, ACTIVE and unexpired: otherwise NOT_FOUND, HOLD_EXPIRED
//     or HOLD_NOT_ACTIVE.
//   - The same key again returns the same booking and creates nothing, even after the hold
//     has ended. The same key with another hold: IDEMPOTENCY_KEY_REUSED.
//   - A hold that already has a live booking made with another key: HOLD_ALREADY_BOOKED.
//   - Prices are copied from the seats' categories as they are now onto booking_items and are
//     never read from the categories again. The fee is domain.BookingFeeCents, the same
//     function the hold's estimate used.
//
// Lock order: the event (shared, bounded wait: EVENT_BUSY), the hold's row, then the booking
// insert. Two requests for one hold take turns at the hold's row: the second finds the
// first's booking, whichever key it carries, so one hold never gets two live bookings. The
// bookings_live_hold_key and (user_id, idempotency_key) unique indexes stand behind that and
// are handled as the same answers, never as a 500.
func (s *Service) CreateBooking(ctx context.Context, userID, holdID int64, key string) (Booking, bool, error) {
	if !ValidIdempotencyKey(key) {
		return Booking{}, false, IdempotencyKeyRequired()
	}
	// A replay is answered before any lock: the key's booking is immutable in the columns
	// compared here, and a retry must work even when the event is busy or the hold is gone.
	if b, found, err := s.replay(ctx, s.q, userID, holdID, key); err != nil || found {
		return b, false, err
	}

	var out Booking
	created := false
	err := store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		out, created, err = s.createBookingTx(ctx, tx, userID, holdID, key)
		return err
	})
	switch {
	case store.IsUniqueViolation(err, store.ConstraintBookingIdempotency):
		// The same key raced itself on two different holds (same hold would have waited at
		// the hold's row). Whoever committed first owns the key.
		b, found, err := s.replay(ctx, s.q, userID, holdID, key)
		if err == nil && !found {
			err = domain.IdempotencyKeyReused()
		}
		return b, false, err
	case store.IsUniqueViolation(err, store.ConstraintBookingLiveHold):
		// Cannot happen under the hold's row lock; answered as what it means all the same.
		live, lerr := s.q.GetLiveBookingOfHold(ctx, holdID)
		if lerr != nil {
			return Booking{}, false, fmt.Errorf("booking for hold %d: %w", holdID, errors.Join(err, lerr))
		}
		return Booking{}, false, domain.HoldAlreadyBooked(live.Reference, domain.BookingStatus(live.Status))
	case err != nil:
		return Booking{}, false, err
	}
	return out, created, nil
}

// IdempotencyKeyRequired builds the 400 for a missing or malformed Idempotency-Key header.
func IdempotencyKeyRequired() *domain.Error {
	return &domain.Error{
		Code:    domain.CodeIdempotencyKeyRequired,
		Message: "Send an Idempotency-Key header with this request.",
		Details: domain.ValidationDetails{Fields: []domain.FieldError{{
			Field: "Idempotency-Key", Rule: "required",
			Message: fmt.Sprintf("Send %d to %d letters, digits, dashes or underscores, and the same value on every retry.",
				MinIdempotencyKeyLength, MaxIdempotencyKeyLength),
		}}},
	}
}

// replay returns the booking the user's key already made, if any. A key that made a booking
// from a different hold is IDEMPOTENCY_KEY_REUSED.
func (s *Service) replay(ctx context.Context, q *store.Queries, userID, holdID int64, key string) (Booking, bool, error) {
	existing, err := q.GetBookingByIdempotencyKey(ctx, store.GetBookingByIdempotencyKeyParams{UserID: userID, IdempotencyKey: key})
	if errors.Is(err, store.ErrNoRows) {
		return Booking{}, false, nil
	}
	if err != nil {
		return Booking{}, false, fmt.Errorf("look up idempotency key: %w", err)
	}
	if existing.HoldID == nil || *existing.HoldID != holdID {
		return Booking{}, false, domain.IdempotencyKeyReused()
	}
	b, err := s.loadBooking(ctx, q, existing.ID)
	return b, err == nil, err
}

func (s *Service) createBookingTx(ctx context.Context, tx pgx.Tx, userID, holdID int64, key string) (Booking, bool, error) {
	q := store.New(tx)

	// 1. The event, shared: no price can change and no cancel can run while the prices are
	// copied. Bounded, like a hold's wait.
	if _, err := store.LockEventOfHoldWithin(ctx, tx, holdID, s.policy.EventLockWait); errors.Is(err, store.ErrNoRows) {
		return Booking{}, false, domain.ErrNotFound
	} else if store.IsLockTimeout(err) {
		busy := domain.EventBusy("This event is being updated. Try again in a moment.")
		busy.Err = err
		return Booking{}, false, busy
	} else if err != nil {
		return Booking{}, false, fmt.Errorf("lock event of hold %d: %w", holdID, err)
	}

	// 2. The hold's row. Everything below is judged under it.
	h, err := store.LockHold(ctx, tx, holdID)
	if errors.Is(err, store.ErrNoRows) || (err == nil && h.UserID != userID) {
		return Booking{}, false, domain.ErrNotFound
	}
	if err != nil {
		return Booking{}, false, fmt.Errorf("lock hold %d: %w", holdID, err)
	}

	// The same key may have got here first while this request waited for the row.
	if b, found, err := s.replay(ctx, q, userID, holdID, key); err != nil || found {
		return b, false, err
	}
	switch status := domain.EffectiveHoldStatus(domain.HoldStatus(h.Status), h.ExpiresAt, h.Now); status {
	case domain.HoldActive:
	case domain.HoldExpired:
		return Booking{}, false, domain.HoldExpiredError(h.ID, h.ExpiresAt)
	default:
		return Booking{}, false, domain.HoldNotActive(h.ID, status, h.ExpiresAt)
	}
	live, err := q.GetLiveBookingOfHold(ctx, holdID)
	switch {
	case err == nil:
		return Booking{}, false, domain.HoldAlreadyBooked(live.Reference, domain.BookingStatus(live.Status))
	case !errors.Is(err, store.ErrNoRows):
		return Booking{}, false, fmt.Errorf("look for a booking of hold %d: %w", holdID, err)
	}

	// 3. The money, in integer cents, from the seats' categories as they are now.
	held, err := q.SumHeldSeatsOfHold(ctx, holdID)
	if err != nil {
		return Booking{}, false, fmt.Errorf("price seats of hold %d: %w", holdID, err)
	}
	if held.Seats == 0 {
		// A live hold always has seats; one without is treated as gone.
		return Booking{}, false, domain.HoldExpiredError(h.ID, h.ExpiresAt)
	}
	fee := domain.BookingFeeCents(held.SubtotalCents, s.policy.BookingFeeBPS)

	// 4. The booking. Each try runs under a savepoint so that a reference collision (a
	// unique violation, which would otherwise abort the transaction) can be tried again.
	var row store.Booking
	for try := 1; ; try++ {
		reference, err := NewReference()
		if err != nil {
			return Booking{}, false, err
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return Booking{}, false, fmt.Errorf("booking for hold %d: savepoint: %w", holdID, err)
		}
		row, err = store.New(sp).InsertBooking(ctx, store.InsertBookingParams{
			Reference: reference, EventID: h.EventID, UserID: userID, HoldID: holdID,
			SubtotalCents: held.SubtotalCents, FeeCents: fee, TotalCents: held.SubtotalCents + fee, IdempotencyKey: key,
		})
		if err == nil {
			if err := sp.Commit(ctx); err != nil {
				return Booking{}, false, fmt.Errorf("booking for hold %d: release savepoint: %w", holdID, err)
			}
			break
		}
		if rbErr := sp.Rollback(context.WithoutCancel(ctx)); rbErr != nil {
			return Booking{}, false, errors.Join(err, rbErr)
		}
		if store.IsUniqueViolation(err, store.ConstraintBookingReference) && try < referenceTries {
			continue
		}
		// The other unique violations are told apart by CreateBooking.
		return Booking{}, false, fmt.Errorf("insert booking for hold %d: %w", holdID, err)
	}
	items, err := q.InsertBookingItemsFromHold(ctx, store.InsertBookingItemsFromHoldParams{BookingID: row.ID, HoldID: holdID})
	if err != nil {
		return Booking{}, false, fmt.Errorf("insert items of booking %d: %w", row.ID, err)
	}
	if items != int64(held.Seats) {
		return Booking{}, false, fmt.Errorf("booking %d: copied %d items for %d held seats", row.ID, items, held.Seats)
	}

	// The answer is built inside the transaction, like a hold's.
	out, err := s.loadBooking(ctx, q, row.ID)
	return out, err == nil, err
}

func newBooking(v store.BookingView) Booking {
	b := Booking{
		Row: v.Booking, Event: v.Event, VenueName: v.VenueName,
		EventStatus: domain.EffectiveEventStatus(domain.EventStatus(v.Event.Status), v.Event.SalesOpenAt, v.Event.SalesCloseAt, v.NowAt),
		Items:       []store.ListBookingItemViewsRow{},
	}
	if v.Booking.Status == string(domain.BookingPendingPayment) {
		b.HoldExpiresAt = v.HoldExpiresAt
	}
	return b
}

// attachBookingParts reads the items and the latest payment of the given bookings in two
// queries.
func attachBookingParts(ctx context.Context, q *store.Queries, bookings []Booking) error {
	if len(bookings) == 0 {
		return nil
	}
	ids := make([]int64, len(bookings))
	at := make(map[int64]*Booking, len(bookings))
	for i := range bookings {
		ids[i] = bookings[i].Row.ID
		at[ids[i]] = &bookings[i]
	}
	items, err := q.ListBookingItemViews(ctx, ids)
	if err != nil {
		return fmt.Errorf("list items of bookings: %w", err)
	}
	for _, it := range items {
		at[it.BookingID].Items = append(at[it.BookingID].Items, it)
	}
	payments, err := q.ListLatestPaymentsForBookings(ctx, ids)
	if err != nil {
		return fmt.Errorf("list payments of bookings: %w", err)
	}
	for i := range payments {
		at[payments[i].BookingID].LatestPayment = &payments[i]
	}
	return nil
}

func (s *Service) loadBooking(ctx context.Context, q *store.Queries, bookingID int64) (Booking, error) {
	v, err := q.GetBookingViewByID(ctx, bookingID)
	if err != nil {
		return Booking{}, fmt.Errorf("get booking %d: %w", bookingID, err)
	}
	out := []Booking{newBooking(v)}
	if err := attachBookingParts(ctx, q, out); err != nil {
		return Booking{}, err
	}
	return out[0], nil
}

// GetBooking returns one booking of the user by its reference. Someone else's booking is
// NOT_FOUND, the same as one that does not exist.
func (s *Service) GetBooking(ctx context.Context, userID int64, reference string) (Booking, error) {
	v, err := s.q.GetBookingViewForUser(ctx, store.GetBookingViewForUserParams{Reference: reference, UserID: userID})
	if errors.Is(err, store.ErrNoRows) {
		return Booking{}, domain.ErrNotFound
	}
	if err != nil {
		return Booking{}, fmt.Errorf("get booking %s: %w", reference, err)
	}
	out := []Booking{newBooking(store.BookingView(v))}
	if err := attachBookingParts(ctx, s.q, out); err != nil {
		return Booking{}, err
	}
	return out[0], nil
}

// ListBookings returns one page of the user's bookings, newest first. next is nil on the
// last page.
func (s *Service) ListBookings(ctx context.Context, userID int64, status *domain.BookingStatus, limit int, after *BookingCursor) ([]Booking, *BookingCursor, error) {
	arg := store.ListBookingViewsOfUserParams{UserID: userID, PageLimit: int32(limit) + 1}
	if status != nil {
		st := string(*status)
		arg.Status = &st
	}
	if after != nil {
		if after.ID < 1 || after.CreatedAt.IsZero() {
			return nil, nil, domain.InvalidField("cursor", "cursor", "Use the next_cursor value from the previous page.")
		}
		arg.HasCursor, arg.CursorCreatedAt, arg.CursorID = true, after.CreatedAt, after.ID
	}
	rows, err := s.q.ListBookingViewsOfUser(ctx, arg)
	if err != nil {
		return nil, nil, fmt.Errorf("list bookings of user %d: %w", userID, err)
	}
	var next *BookingCursor
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1].Booking
		next = &BookingCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	out := make([]Booking, 0, len(rows))
	for _, r := range rows {
		out = append(out, newBooking(store.BookingView(r)))
	}
	if err := attachBookingParts(ctx, s.q, out); err != nil {
		return nil, nil, err
	}
	return out, next, nil
}
