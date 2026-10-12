// Package booking holds the rules that stop two fans getting the same seat: holding seats,
// best available, release, the expiry sweeper, the per-user ticket limit and the one
// extension a hold gets when a payment starts (docs/ARCHITECTURE.md §6, corrections 2, 4, 5,
// 6 and 7 in CLAUDE.md).
//
// The SQL is in internal/store; every statement that locks rows is in store/holds.go with a
// comment saying what it locks and why, and the lock order is written at the top of that
// file. State rules are in internal/domain (TransitionHold, TransitionSeat, CheckOnSale).
package booking

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// DB is what the service needs from the pool: run queries and open transactions.
type DB interface {
	store.DBTX
	store.TxBeginner
}

// Policy is every number in the hold rules, in one place.
type Policy struct {
	// HoldTTL is how long a hold lasts (§6.1: 10 minutes).
	HoldTTL time.Duration
	// PaymentWindow is the least time a hold must have left when a payment starts
	// (correction 4: 6 minutes, one more than the 5 minutes payment checks run for).
	PaymentWindow time.Duration
	// BestAvailableTries is how many runs best available tries to hold before it answers
	// NO_ADJACENT_SEATS.
	BestAvailableTries int
	// SweepBatch is how many lapsed holds the sweeper closes per transaction.
	SweepBatch int
	// SweepMaxBatches bounds one sweeper run.
	SweepMaxBatches int
	// EventLockWait is how long a hold request waits for the event when an organiser's
	// publish, cancel or edit has it locked, before it answers EVENT_BUSY. Without a limit
	// the request sat behind a long cancel until the statement timeout and answered 500.
	EventLockWait time.Duration
	// BookingFeeBPS is the booking fee in basis points of the subtotal (BOOKING_FEE_BPS).
	// Zero means no fee. A hold shows the fee as an estimate; the booking charges it.
	BookingFeeBPS int
	// BestAvailableWalk is how many candidate runs one later try of best available walks
	// through (in an order shuffled per request) before it reads the free seats again.
	BestAvailableWalk int
}

// DefaultPolicy is what production runs.
func DefaultPolicy() Policy {
	return Policy{
		HoldTTL:            10 * time.Minute,
		PaymentWindow:      6 * time.Minute,
		BestAvailableTries: 6,
		BestAvailableWalk:  8,
		SweepBatch:         200,
		SweepMaxBatches:    50,
		EventLockWait:      3 * time.Second,
	}
}

// Config is what the service is built from.
type Config struct {
	DB     DB
	Logger *slog.Logger
	// Policy zero values are replaced by DefaultPolicy's.
	Policy Policy
}

// Service implements the hold rules. It is safe for concurrent use. It keeps no clock of its
// own: every time that matters (expiry, the sales window, the countdown) is read from the
// database, so several API instances and the worker always agree.
type Service struct {
	db     DB
	q      *store.Queries
	log    *slog.Logger
	policy Policy
}

// NewService checks its dependencies and builds the service.
func NewService(cfg Config) (*Service, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("booking: DB is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	def := DefaultPolicy()
	p := cfg.Policy
	if p.HoldTTL <= 0 {
		p.HoldTTL = def.HoldTTL
	}
	if p.PaymentWindow <= 0 {
		p.PaymentWindow = def.PaymentWindow
	}
	if p.BestAvailableTries <= 0 {
		p.BestAvailableTries = def.BestAvailableTries
	}
	if p.BestAvailableWalk <= 0 {
		p.BestAvailableWalk = def.BestAvailableWalk
	}
	if p.SweepBatch <= 0 {
		p.SweepBatch = def.SweepBatch
	}
	if p.SweepMaxBatches <= 0 {
		p.SweepMaxBatches = def.SweepMaxBatches
	}
	if p.EventLockWait <= 0 {
		p.EventLockWait = def.EventLockWait
	}
	return &Service{db: cfg.DB, q: store.New(cfg.DB), log: cfg.Logger, policy: p}, nil
}

// Hold is a hold as its owner sees it.
type Hold struct {
	Row store.Hold
	// Status is the effective status (domain.EffectiveHoldStatus): an ACTIVE hold whose
	// time has run out is EXPIRED here even before the sweeper has closed it.
	Status domain.HoldStatus
	// RemainingSeconds is by the database clock; zero unless the hold is live.
	RemainingSeconds int
	// BookingReference is the booking made from this hold, if any.
	BookingReference *string
	// Seats are the seats that belong to the hold now, ordered by block, row and seat
	// number. A hold that was released, expired or swept has none: its seats went back on
	// sale and no longer point at it.
	Seats []store.ListSeatsOfHoldsRow
	// SubtotalCents, FeeCents and TotalCents are an estimate at the categories' prices
	// now: the sum of the seats' prices, the booking fee on it (domain.BookingFeeCents),
	// and the two together. The binding prices are copied when the booking is made.
	SubtotalCents int64
	FeeCents      int64
	TotalCents    int64
}

func newHold(row store.Hold, now time.Time, bookingReference string) Hold {
	h := Hold{Row: row, Status: domain.EffectiveHoldStatus(domain.HoldStatus(row.Status), row.ExpiresAt, now), Seats: []store.ListSeatsOfHoldsRow{}}
	if h.Status == domain.HoldActive {
		h.RemainingSeconds = max(int(row.ExpiresAt.Sub(now)/time.Second), 0)
	}
	if bookingReference != "" {
		h.BookingReference = &bookingReference
	}
	return h
}

// ownsSeats reports whether a hold in this status still has seats pointing at it.
func ownsSeats(status domain.HoldStatus) bool {
	return status == domain.HoldActive || status == domain.HoldConverted
}

// attachSeats reads the seats of the given holds in one query.
func (s *Service) attachSeats(ctx context.Context, q *store.Queries, holds []Hold) error {
	var ids []int64
	for _, h := range holds {
		if ownsSeats(h.Status) {
			ids = append(ids, h.Row.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	seats, err := q.ListSeatsOfHolds(ctx, ids)
	if err != nil {
		return fmt.Errorf("list seats of holds: %w", err)
	}
	byHold := make(map[int64]*Hold, len(holds))
	for i := range holds {
		byHold[holds[i].Row.ID] = &holds[i]
	}
	for _, seat := range seats {
		if h, ok := byHold[seat.HoldID]; ok && ownsSeats(h.Status) {
			h.Seats = append(h.Seats, seat)
			h.SubtotalCents += seat.PriceCents
		}
	}
	for i := range holds {
		holds[i].FeeCents = domain.BookingFeeCents(holds[i].SubtotalCents, s.policy.BookingFeeBPS)
		holds[i].TotalCents = holds[i].SubtotalCents + holds[i].FeeCents
	}
	return nil
}

// Get returns one hold of the given user in whatever status it has. Someone else's hold is
// NOT_FOUND, the same as one that does not exist.
func (s *Service) Get(ctx context.Context, userID, holdID int64) (Hold, error) {
	return s.load(ctx, s.q, userID, holdID)
}

// load is Get on the pool or on a transaction.
func (s *Service) load(ctx context.Context, q *store.Queries, userID, holdID int64) (Hold, error) {
	row, err := q.GetHoldForUser(ctx, store.GetHoldForUserParams{ID: holdID, UserID: userID})
	if errors.Is(err, store.ErrNoRows) {
		return Hold{}, domain.ErrNotFound
	}
	if err != nil {
		return Hold{}, fmt.Errorf("get hold %d: %w", holdID, err)
	}
	holds := []Hold{newHold(row.Hold, row.NowAt, row.BookingReference)}
	if err := s.attachSeats(ctx, q, holds); err != nil {
		return Hold{}, err
	}
	return holds[0], nil
}

// ListMine returns the user's live holds, newest first, optionally for one event only.
func (s *Service) ListMine(ctx context.Context, userID int64, eventID *int64) ([]Hold, error) {
	rows, err := s.q.ListActiveHoldsOfUser(ctx, store.ListActiveHoldsOfUserParams{UserID: userID, EventID: eventID})
	if err != nil {
		return nil, fmt.Errorf("list holds of user %d: %w", userID, err)
	}
	holds := make([]Hold, 0, len(rows))
	for _, row := range rows {
		holds = append(holds, newHold(row.Hold, row.NowAt, row.BookingReference))
	}
	if err := s.attachSeats(ctx, s.q, holds); err != nil {
		return nil, err
	}
	return holds, nil
}

// closeUnpaidBookings moves the bookings of the given holds that may become `to` (EXPIRED
// when the hold ran out, CANCELLED when the fan released it) and returns how many it moved.
// The caller has the hold rows locked.
//
// Each booking is read under its row lock in whatever status it has, and
// domain.TransitionBooking is asked with that status: a booking the rule will not move
// (PAID, REFUND_REQUIRED, already closed) is left alone. A booking with a payment still in
// flight is left alone too; the payment flow closes it when the payment ends.
func closeUnpaidBookings(ctx context.Context, tx store.DBTX, holdIDs []int64, to domain.BookingStatus) (int64, error) {
	bookings, err := store.LockBookingsOfHolds(ctx, tx, holdIDs)
	if err != nil {
		return 0, fmt.Errorf("lock bookings of holds: %w", err)
	}
	byStatus := map[string][]int64{}
	for _, b := range bookings {
		if b.LivePayment || domain.TransitionBooking(domain.BookingStatus(b.Status), to) != nil {
			continue
		}
		byStatus[b.Status] = append(byStatus[b.Status], b.ID)
	}
	var moved int64
	for from, ids := range byStatus {
		n, err := store.SetBookingsStatus(ctx, tx, ids, string(to), from)
		if err != nil {
			return 0, fmt.Errorf("move bookings %s -> %s: %w", from, to, err)
		}
		if int(n) != len(ids) {
			return 0, fmt.Errorf("move bookings %s -> %s: wrote %d of %d locked rows", from, to, n, len(ids))
		}
		moved += n
	}
	return moved, nil
}
