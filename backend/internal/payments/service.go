package payments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/booking"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
)

// DB is what the service needs from the pool: run queries and open transactions.
type DB interface {
	store.DBTX
	store.TxBeginner
}

// TicketIssuer is the part of the ticketing service the payment flow uses.
type TicketIssuer interface {
	// IssueForBooking makes the tickets of a paid booking inside the payment transaction.
	IssueForBooking(ctx context.Context, tx pgx.Tx, bookingID int64) ([]ticketing.Ticket, error)
	// VoidForBooking voids a booking's tickets inside the refund transaction.
	VoidForBooking(ctx context.Context, tx pgx.Tx, bookingID int64) (int, error)
	// SendTicketSMS sends the "you're going" message. Called after commit, never inside.
	SendTicketSMS(ctx context.Context, bookingID int64)
}

// Policy is every number in the payment rules, in one place.
type Policy struct {
	// HoldWindow is what the hold's expiry is pushed out to when a payment starts
	// (correction 4: at least 6 minutes left, once).
	HoldWindow time.Duration
	// MinHoldRemaining is the least time a hold must have left for a payment to start at
	// all, extension included. A retry after a failed payment finds the hold already
	// extended, so this is less than HoldWindow: it has to cover one PIN prompt (M-Pesa
	// gives up on a prompt after about a minute), not the whole reconciliation period. A
	// result that arrives after the hold has run out is still handled: the seats are sold
	// if the hold still has them all, otherwise the payment is refunded.
	MinHoldRemaining time.Duration
	// EventLockWait bounds the wait for an event an organiser has locked (EVENT_BUSY).
	EventLockWait time.Duration
	// InitiateTimeout bounds the whole call to the provider when a payment starts, retries
	// included. It must stay well under ReconcileAfter, so that reconciliation never looks
	// at a payment the API is still sending.
	InitiateTimeout time.Duration
	// QueryTimeout bounds one status query to the provider.
	QueryTimeout time.Duration
	// ReconcileAfter is how old a live payment must be before reconciliation asks the
	// provider about it (§8.2: 60 seconds).
	ReconcileAfter time.Duration
	// GiveUpAfter is when a payment with no final answer is marked TIMEOUT (§8.2: 5 minutes).
	GiveUpAfter time.Duration
	// ReconcileLease is how long a payment one reconciliation run has taken is left alone
	// by the next run (in this worker or another).
	ReconcileLease time.Duration
	// ReconcileBatch is how many payments one run takes.
	ReconcileBatch int
	// PhoneCountryCodes are the calling codes (digits, no plus) a payment may be sent to.
	PhoneCountryCodes []string
}

// DefaultPolicy is what production runs.
func DefaultPolicy() Policy {
	return Policy{
		HoldWindow:        6 * time.Minute,
		MinHoldRemaining:  2 * time.Minute,
		EventLockWait:     3 * time.Second,
		InitiateTimeout:   30 * time.Second,
		QueryTimeout:      15 * time.Second,
		ReconcileAfter:    60 * time.Second,
		GiveUpAfter:       5 * time.Minute,
		ReconcileLease:    20 * time.Second,
		ReconcileBatch:    100,
		PhoneCountryCodes: []string{"254"},
	}
}

// Config is what the service is built from.
type Config struct {
	DB       DB
	Logger   *slog.Logger
	Provider Provider
	Tickets  TicketIssuer
	// Background runs the SMS and the callback work off the request path. Nil makes one.
	Background *Background
	// Policy zero values are replaced by DefaultPolicy's.
	Policy Policy
}

// Service starts, finishes and reconciles payments. It is safe for concurrent use. Like the
// hold service it keeps no clock: every time that matters is read from the database.
type Service struct {
	db       DB
	q        *store.Queries
	log      *slog.Logger
	provider Provider
	tickets  TicketIssuer
	bg       *Background
	policy   Policy
}

// NewService checks its dependencies and builds the service.
func NewService(cfg Config) (*Service, error) {
	switch {
	case cfg.DB == nil:
		return nil, errors.New("payments: DB is required")
	case cfg.Provider == nil:
		return nil, errors.New("payments: Provider is required")
	case cfg.Tickets == nil:
		return nil, errors.New("payments: Tickets is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Background == nil {
		cfg.Background = NewBackground(0)
	}
	def, p := DefaultPolicy(), cfg.Policy
	setDuration := func(v *time.Duration, d time.Duration) {
		if *v <= 0 {
			*v = d
		}
	}
	setDuration(&p.HoldWindow, def.HoldWindow)
	setDuration(&p.MinHoldRemaining, def.MinHoldRemaining)
	setDuration(&p.EventLockWait, def.EventLockWait)
	setDuration(&p.InitiateTimeout, def.InitiateTimeout)
	setDuration(&p.QueryTimeout, def.QueryTimeout)
	setDuration(&p.ReconcileAfter, def.ReconcileAfter)
	setDuration(&p.GiveUpAfter, def.GiveUpAfter)
	// Zero means the default like every other field; a negative value means "no lease"
	// (every run takes every due payment), which only tests want.
	if p.ReconcileLease == 0 {
		p.ReconcileLease = def.ReconcileLease
	} else if p.ReconcileLease < 0 {
		p.ReconcileLease = 0
	}
	if p.ReconcileBatch <= 0 {
		p.ReconcileBatch = def.ReconcileBatch
	}
	if len(p.PhoneCountryCodes) == 0 {
		p.PhoneCountryCodes = def.PhoneCountryCodes
	}
	if p.MinHoldRemaining > p.HoldWindow {
		return nil, fmt.Errorf("payments: MinHoldRemaining (%s) must not exceed HoldWindow (%s)", p.MinHoldRemaining, p.HoldWindow)
	}
	if p.InitiateTimeout >= p.ReconcileAfter {
		return nil, fmt.Errorf("payments: InitiateTimeout (%s) must be shorter than ReconcileAfter (%s)", p.InitiateTimeout, p.ReconcileAfter)
	}
	return &Service{
		db: cfg.DB, q: store.New(cfg.DB), log: cfg.Logger, provider: cfg.Provider,
		tickets: cfg.Tickets, bg: cfg.Background, policy: p,
	}, nil
}

// Wait blocks until the background work this service started (callback processing, SMS) has
// finished. Call it at shutdown, after the HTTP server has stopped taking requests.
func (s *Service) Wait() { s.bg.Wait() }

// Payment is a payment as its owner sees it: the row and the booking it pays for.
type Payment struct {
	Row     store.Payment
	Booking store.Booking
}

// Get returns one payment of the user. Someone else's payment is NOT_FOUND, the same as one
// that does not exist.
func (s *Service) Get(ctx context.Context, userID, paymentID int64) (Payment, error) {
	row, err := s.q.GetPaymentForUser(ctx, store.GetPaymentForUserParams{ID: paymentID, UserID: userID})
	if errors.Is(err, store.ErrNoRows) {
		return Payment{}, domain.ErrNotFound
	}
	if err != nil {
		return Payment{}, fmt.Errorf("get payment %d: %w", paymentID, err)
	}
	return Payment{Row: row.Payment, Booking: row.Booking}, nil
}

// load reads a payment and its booking with no owner check.
func (s *Service) load(ctx context.Context, paymentID int64) (Payment, error) {
	p, err := s.q.GetPaymentByID(ctx, paymentID)
	if err != nil {
		return Payment{}, fmt.Errorf("get payment %d: %w", paymentID, err)
	}
	b, err := s.q.GetBookingByID(ctx, p.BookingID)
	if err != nil {
		return Payment{}, fmt.Errorf("get booking %d: %w", p.BookingID, err)
	}
	return Payment{Row: p, Booking: b}, nil
}

// Start sends an M-Pesa PIN prompt for one of the user's bookings (§8.1) and returns the
// payment, PENDING.
//
// One transaction decides whether a payment may start and writes its row; the provider is
// called only after that transaction has committed. Called inside it, a slow M-Pesa would
// keep the event's share lock, and with it every organiser edit, cancel and every hold
// queued behind those, waiting on Safaricom.
//
// Inside the transaction, in lock order:
//
//  1. the event (shared, bounded: EVENT_BUSY) and the hold's row, through
//     booking.ExtendForPayment, BEFORE the booking is locked. Release takes the same hold
//     row before it looks for a live payment, so a release and a payment start can never
//     both go ahead. Every start does this, a retry included. The hold's expiry is pushed
//     out once so at least Policy.HoldWindow remains (correction 4);
//  2. the hold must have Policy.MinHoldRemaining left (an already-extended hold is not
//     extended again and may be nearly over) and still hold every seat of the booking;
//  3. the booking's row: it must be PENDING_PAYMENT;
//  4. no payment of the booking may be live: PAYMENT_IN_PROGRESS;
//  5. the payment row, INITIATED.
//
// Then, outside any transaction, the provider is asked. Accepted: the payment becomes
// PENDING with the provider's IDs. Refused or unreachable: the payment becomes FAILED and the
// answer is PAYMENT_PROVIDER_UNAVAILABLE; the booking stays payable for as long as its hold
// lives.
func (s *Service) Start(ctx context.Context, userID, bookingID int64, rawPhone string) (Payment, error) {
	phone, ok := domain.NormalisePhone(rawPhone)
	if !ok || !domain.PhoneHasCountryCode(phone, s.policy.PhoneCountryCodes) {
		return Payment{}, domain.InvalidField("phone", "e164", "Enter an M-Pesa number, for example +254712345678.")
	}

	// Whose booking, and which hold to lock first. These columns never change, so this
	// read needs no lock; everything that can change is read again under the locks.
	pre, err := s.q.GetBookingByID(ctx, bookingID)
	if errors.Is(err, store.ErrNoRows) || (err == nil && pre.UserID != userID) {
		return Payment{}, domain.ErrNotFound
	}
	if err != nil {
		return Payment{}, fmt.Errorf("get booking %d: %w", bookingID, err)
	}
	if pre.HoldID == nil {
		// A booking with no hold has no seats reserved for it; there is nothing to pay for.
		return Payment{}, domain.BookingNotPayable(pre.Reference, domain.BookingStatus(pre.Status))
	}
	if pre.TotalCents%100 != 0 || pre.TotalCents <= 0 {
		// M-Pesa takes whole shillings. Rounding would charge a different amount from the
		// one the booking shows, so the booking is refused instead. Only a booking fee that
		// yields a fraction of a shilling can cause this: it is a configuration fault.
		s.log.ErrorContext(ctx, "booking total is not a whole number of shillings; it cannot be paid by M-Pesa. Check BOOKING_FEE_BPS",
			"booking_id", pre.ID, "total_cents", pre.TotalCents)
		e := domain.BookingNotPayable(pre.Reference, domain.BookingStatus(pre.Status))
		e.Message = "This booking's total is not a whole number of shillings, so M-Pesa cannot take it."
		return Payment{}, e
	}

	var pay store.Payment
	err = store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		pay, err = s.startTx(ctx, tx, userID, pre, phone)
		return err
	})
	if store.IsUniqueViolation(err, store.ConstraintPaymentLiveBooking) {
		// Cannot happen under the booking's row lock; answered as what it means all the same.
		live, lerr := s.q.GetLivePaymentOfBooking(ctx, bookingID)
		if lerr != nil {
			return Payment{}, fmt.Errorf("start payment for booking %d: %w", bookingID, errors.Join(err, lerr))
		}
		return Payment{}, domain.PaymentInProgress(live.ID, live.Status)
	}
	if err != nil {
		return Payment{}, err
	}

	// From here the payment row exists. Whatever happens to the request (the client hanging
	// up included), the provider's answer must be recorded, so the caller's cancellation is
	// dropped and the call gets its own deadline.
	callCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.policy.InitiateTimeout)
	defer cancel()
	init, err := s.provider.Initiate(callCtx, PaymentRequest{
		PaymentID: pay.ID, Reference: pre.Reference, AmountCents: pay.AmountCents, Phone: phone,
	})
	if err == nil && init.CheckoutRequestID == "" {
		err = errors.New("the provider accepted the request without a checkout request ID")
	}
	recordCtx, cancelRecord := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancelRecord()
	if err != nil {
		s.log.WarnContext(ctx, "payment provider did not accept the request", "payment_id", pay.ID, "booking_id", bookingID, "err", err)
		if _, ferr := s.Finish(recordCtx, pay.ID, Result{
			Source: SourceInternal, Outcome: OutcomeFailed, ResultDesc: "M-Pesa did not accept the request.",
		}); ferr != nil {
			// The row stays INITIATED; reconciliation closes it within a minute or two.
			s.log.ErrorContext(ctx, "could not mark the refused payment FAILED; reconciliation will close it",
				"payment_id", pay.ID, "err", ferr)
		}
		return Payment{}, domain.PaymentProviderUnavailable(err)
	}
	if err := s.recordAccepted(recordCtx, pay.ID, init); err != nil {
		return Payment{}, err
	}
	return s.load(recordCtx, pay.ID)
}

func (s *Service) startTx(ctx context.Context, tx pgx.Tx, userID int64, pre store.Booking, phone string) (store.Payment, error) {
	q := store.New(tx)
	holdID := *pre.HoldID

	// 1. Event and hold, then (maybe) the one extension.
	ext, err := booking.ExtendForPayment(ctx, tx, userID, holdID, s.policy.HoldWindow, s.policy.EventLockWait)
	if code := domain.CodeOf(err); err != nil && (code == domain.CodeHoldExpired || code == domain.CodeHoldNotActive) {
		// The hold has ended. If the booking ended with it (paid, expired, cancelled), that
		// is the more useful thing to say. These refusals leave the transaction usable.
		b, berr := store.LockBooking(ctx, tx, pre.ID)
		if berr != nil {
			return store.Payment{}, fmt.Errorf("lock booking %d: %w", pre.ID, berr)
		}
		if status := domain.BookingStatus(b.Status); status != domain.BookingPendingPayment {
			return store.Payment{}, domain.BookingNotPayable(b.Reference, status)
		}
		var de *domain.Error
		errors.As(err, &de)
		return store.Payment{}, &domain.Error{Code: domain.CodeHoldExpired, Message: "Your hold on these seats has run out.", Details: de.Details}
	}
	if err != nil {
		return store.Payment{}, err
	}

	// 2. Enough time, and every seat.
	if ext.Remaining < s.policy.MinHoldRemaining {
		return store.Payment{}, domain.HoldTooShort(ext.HoldID, domain.HoldActive, ext.ExpiresAt)
	}

	// 3. The booking, under its row lock.
	b, err := store.LockBooking(ctx, tx, pre.ID)
	if err != nil {
		return store.Payment{}, fmt.Errorf("lock booking %d: %w", pre.ID, err)
	}
	if b.UserID != userID {
		return store.Payment{}, domain.ErrNotFound
	}
	if status := domain.BookingStatus(b.Status); status != domain.BookingPendingPayment {
		return store.Payment{}, domain.BookingNotPayable(b.Reference, status)
	}
	items, err := q.CountBookingItems(ctx, b.ID)
	if err != nil {
		return store.Payment{}, fmt.Errorf("count items of booking %d: %w", b.ID, err)
	}
	if items == 0 || int(items) != ext.Seats {
		// The hold no longer has exactly the booking's seats. A payment could not sell them
		// and would end as a refund, so none is started.
		return store.Payment{}, domain.HoldTooShort(ext.HoldID, domain.HoldActive, ext.ExpiresAt)
	}

	// 4. One live payment per booking.
	live, err := q.GetLivePaymentOfBooking(ctx, b.ID)
	switch {
	case err == nil:
		return store.Payment{}, domain.PaymentInProgress(live.ID, live.Status)
	case !errors.Is(err, store.ErrNoRows):
		return store.Payment{}, fmt.Errorf("look for a live payment of booking %d: %w", b.ID, err)
	}

	// 5. The payment row. The amount is the booking's total; the client cannot set it.
	pay, err := q.InsertPayment(ctx, store.InsertPaymentParams{BookingID: b.ID, AmountCents: b.TotalCents, PhoneE164: phone})
	if err != nil {
		return store.Payment{}, fmt.Errorf("insert payment for booking %d: %w", b.ID, err)
	}
	return pay, nil
}

// recordAccepted writes the provider's IDs on a payment and moves it INITIATED -> PENDING.
// It locks the payment row alone (store.LockPaymentAlone says why that is safe).
func (s *Service) recordAccepted(ctx context.Context, paymentID int64, init PaymentInit) error {
	return store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		pay, err := store.LockPaymentAlone(ctx, tx, paymentID)
		if err != nil {
			return fmt.Errorf("lock payment %d: %w", paymentID, err)
		}
		from := domain.PaymentStatus(pay.Status)
		var merchant *string
		if init.MerchantRequestID != "" {
			merchant = &init.MerchantRequestID
		}
		q := store.New(tx)
		if err := domain.TransitionPayment(from, domain.PaymentPending); err != nil {
			// Something closed the payment while the provider was being asked (it should
			// not: InitiateTimeout is shorter than ReconcileAfter). The prompt is out all
			// the same, so the IDs are still recorded: a result that arrives for them must
			// find this payment.
			s.log.ErrorContext(ctx, "payment was closed while the provider was being asked; recording its IDs only",
				"payment_id", paymentID, "status", pay.Status)
			_, err := q.SetPaymentProviderIDs(ctx, store.SetPaymentProviderIDsParams{
				ID: paymentID, CheckoutRequestID: init.CheckoutRequestID, MerchantRequestID: merchant,
			})
			return err
		}
		n, err := q.SetPaymentPending(ctx, store.SetPaymentPendingParams{
			ID: paymentID, FromStatus: string(from), CheckoutRequestID: init.CheckoutRequestID, MerchantRequestID: merchant,
		})
		if err != nil {
			return fmt.Errorf("mark payment %d pending: %w", paymentID, err)
		}
		if n != 1 {
			return fmt.Errorf("mark payment %d pending: changed under its row lock", paymentID)
		}
		return nil
	})
}
