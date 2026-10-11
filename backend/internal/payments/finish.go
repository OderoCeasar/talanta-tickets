package payments

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
)

// Source says who reported a result. It decides how far the result is believed.
type Source int

const (
	// SourceCallback is the provider's callback: an unauthenticated HTTP request. It is
	// believed only as far as it matches a payment we know and carries the right amount.
	SourceCallback Source = iota
	// SourceQuery is the provider's answer to our own, authenticated status query.
	SourceQuery
	// SourceInternal is a verdict of our own: the provider refused the request, or
	// reconciliation gave up waiting.
	SourceInternal
)

// Outcome is what a result says happened.
type Outcome int

const (
	// OutcomeSuccess: the payer paid.
	OutcomeSuccess Outcome = iota
	// OutcomeFailed: the provider reports a final failure.
	OutcomeFailed
	// OutcomeTimedOut: nobody told us anything in time; the payment is closed as TIMEOUT.
	OutcomeTimedOut
)

// Result is one report of how a payment ended, from any source.
type Result struct {
	Source  Source
	Outcome Outcome
	// ResultCode is the provider's code; nil for a verdict of our own.
	ResultCode *int32
	ResultDesc string
	// AmountCents is what was paid, when the report says.
	AmountCents *int64
	Receipt     string
	// Raw is the callback body, valid JSON, or nil.
	Raw []byte
	// Unresolved marks a timeout that happened without the provider ever giving a final
	// answer (as opposed to the request never having been sent). Money may have moved, so
	// the organiser is alerted.
	Unresolved bool
}

// Action is what Finish did with a result.
type Action string

const (
	// ActionNone: the payment was already final, or the result could not be used. Nothing
	// changed (apart from keeping the raw callback when the payment had none).
	ActionNone Action = "none"
	// ActionSold: payment SUCCESS, booking PAID, hold CONVERTED, seats SOLD, tickets issued.
	ActionSold Action = "sold"
	// ActionRefund: the money arrived but bought nothing. The payment is recorded, a refund
	// is queued and the organiser alerted. No seat was sold.
	ActionRefund Action = "refund"
	// ActionFailed: the payment ended FAILED or TIMEOUT.
	ActionFailed Action = "failed"
	// ActionUnverifiable: a success report with no amount from a source that is not
	// believed on its own word. Nothing changed; the caller asks the provider.
	ActionUnverifiable Action = "unverifiable"
)

// Finished is the outcome of Finish.
type Finished struct {
	PaymentID     int64
	BookingID     int64
	EventID       int64
	Action        Action
	PaymentStatus domain.PaymentStatus
	BookingStatus domain.BookingStatus
	// Tickets is how many tickets were issued (ActionSold only).
	Tickets int
	// Reason says why, in plain English, when the result was not a plain sale.
	Reason string
}

// ErrEventBusy is returned by Finish when the payment's event stayed locked by an organiser
// (a publish, a cancel, an edit) for longer than Policy.EventLockWait. Nothing changed; the
// payment is still live and reconciliation will finish it.
var ErrEventBusy = errors.New("payments: the event is locked; the payment was left for reconciliation")

// Why a successful payment bought nothing. These are stored as the refund's reason and shown
// to the organiser.
const (
	reasonEventCancelled = "Paid after the event was cancelled"
	reasonHoldGone       = "Paid after the hold ended; the seats were no longer reserved"
	reasonSeatsResold    = "Paid after the hold expired; seats were resold"
	reasonAlreadyPaid    = "Paid twice: the booking was already paid by another payment"
	reasonBookingClosed  = "Paid after the booking was closed"
)

// Finish records how a payment ended. It is the ONE function every report goes through: the
// M-Pesa callback, a repeat of that callback, the mock provider, and reconciliation. It is
// one database transaction (§8.3: money moves once).
//
// Locks, in the system's order: the event (shared, bounded wait), the booking's hold, the
// booking, the payment. Then:
//
//  1. The payment's status, read under its lock, decides whether there is anything to do.
//     domain.TransitionPayment refuses a second final state, so a repeated callback, and a
//     callback and a reconciliation run that both carry the same result, change nothing the
//     second time. (A TIMEOUT payment is not final in that sense: TIMEOUT is our verdict,
//     and a late answer from M-Pesa still overrules it.)
//  2. A failure: the payment becomes FAILED or TIMEOUT. The booking stays PENDING_PAYMENT
//     while its hold is there, so the fan can try again, and is closed if the hold is gone.
//  3. A success with the wrong amount: the payment becomes FAILED (result code 0, which is
//     what failure_reason "wrong_amount" is read from), a refund for the amount actually
//     paid is queued, the booking becomes REFUND_REQUIRED and the organiser is alerted.
//  4. A success with the right amount sells the seats if, and only if, the booking is still
//     PENDING_PAYMENT, its event is not cancelled, and its hold's STORED status is ACTIVE.
//     The stored status, not the effective one: a hold whose time has run out but which
//     nobody has taken a seat from still has every seat, and the fan who paid for them gets
//     them (correction 5). The payment becomes SUCCESS, the booking PAID, the hold
//     CONVERTED, and store.SellSeatsOfHold flips the seats where hold_id matches and the
//     status is still HELD. If it reports as many sold as the booking has items, the
//     tickets are issued at once and the transaction commits.
//  5. Otherwise nothing is sold. If the seat statement came up short it sold none; the PAID
//     and CONVERTED writes are undone with a savepoint. The payment is recorded SUCCESS, a
//     refund is queued, the booking becomes REFUND_REQUIRED (a booking that is already PAID
//     by another payment stays PAID and keeps its tickets) and the organiser is alerted.
//     A seat is never sold twice to make the ledger come out right.
//
// The "you're going" SMS is sent after the commit, in the background.
func (s *Service) Finish(ctx context.Context, paymentID int64, res Result) (Finished, error) {
	var out Finished
	err := store.InTxRaw(ctx, s.db, func(tx pgx.Tx) error {
		var err error
		out, err = s.finishTx(ctx, tx, paymentID, res)
		return err
	})
	if err != nil {
		return Finished{}, err
	}

	switch out.Action {
	case ActionSold:
		s.log.InfoContext(ctx, "payment succeeded: seats sold and tickets issued",
			"payment_id", out.PaymentID, "booking_id", out.BookingID, "tickets", out.Tickets)
		bookingID := out.BookingID
		// SendTicketSMS drops the caller's cancellation and has its own timeout.
		if !s.bg.Go(func() { s.tickets.SendTicketSMS(ctx, bookingID) }) {
			s.log.WarnContext(ctx, "ticket sms not sent: too much background work in flight", "booking_id", bookingID)
		}
	case ActionRefund:
		s.log.WarnContext(ctx, "payment received but nothing was sold: refund queued",
			"payment_id", out.PaymentID, "booking_id", out.BookingID, "payment_status", out.PaymentStatus,
			"booking_status", out.BookingStatus, "reason", out.Reason)
	case ActionFailed:
		s.log.InfoContext(ctx, "payment did not succeed",
			"payment_id", out.PaymentID, "booking_id", out.BookingID, "payment_status", out.PaymentStatus, "reason", out.Reason)
	case ActionUnverifiable:
		s.log.WarnContext(ctx, "payment result could not be verified and was not applied",
			"payment_id", out.PaymentID, "reason", out.Reason)
	}
	return out, nil
}

func (s *Service) finishTx(ctx context.Context, tx pgx.Tx, paymentID int64, res Result) (Finished, error) {
	q := store.New(tx)
	scope, err := q.GetPaymentScope(ctx, paymentID)
	if errors.Is(err, store.ErrNoRows) {
		return Finished{}, domain.ErrNotFound
	}
	if err != nil {
		return Finished{}, fmt.Errorf("finish payment %d: read scope: %w", paymentID, err)
	}

	// Lock order: event, hold, booking, payment.
	ev, err := store.LockEventForHoldWithin(ctx, tx, scope.EventID, s.policy.EventLockWait)
	if store.IsLockTimeout(err) {
		return Finished{}, fmt.Errorf("%w: payment %d: %w", ErrEventBusy, paymentID, err)
	}
	if err != nil {
		return Finished{}, fmt.Errorf("finish payment %d: lock event %d: %w", paymentID, scope.EventID, err)
	}
	var hold *store.LockedHold
	if scope.HoldID != nil {
		h, err := store.LockHold(ctx, tx, *scope.HoldID)
		if err != nil {
			return Finished{}, fmt.Errorf("finish payment %d: lock hold %d: %w", paymentID, *scope.HoldID, err)
		}
		hold = &h
	}
	b, err := store.LockBooking(ctx, tx, scope.BookingID)
	if err != nil {
		return Finished{}, fmt.Errorf("finish payment %d: lock booking %d: %w", paymentID, scope.BookingID, err)
	}
	pay, err := store.LockPayment(ctx, tx, paymentID)
	if err != nil {
		return Finished{}, fmt.Errorf("finish payment %d: lock payment: %w", paymentID, err)
	}

	f := &finishing{s: s, tx: tx, q: q, res: res, ev: ev, hold: hold, booking: b, pay: pay}
	out := Finished{
		PaymentID: pay.ID, BookingID: b.ID, EventID: b.EventID, Action: ActionNone,
		PaymentStatus: domain.PaymentStatus(pay.Status), BookingStatus: domain.BookingStatus(b.Status),
	}

	// 1. Is there anything to do? The status was read under the row lock.
	target := domain.PaymentSuccess
	switch res.Outcome {
	case OutcomeFailed:
		target = domain.PaymentFailed
	case OutcomeTimedOut:
		target = domain.PaymentTimeout
	}
	if err := domain.TransitionPayment(out.PaymentStatus, target); err != nil {
		return out, f.keepRaw(ctx)
	}

	if res.Outcome != OutcomeSuccess {
		return f.fail(ctx, out, target)
	}

	// A success report. First, can it be believed?
	if res.Receipt != "" {
		used, err := q.ReceiptBelongsToAnotherPayment(ctx, store.ReceiptBelongsToAnotherPaymentParams{MpesaReceipt: res.Receipt, ID: pay.ID})
		if err != nil {
			return Finished{}, fmt.Errorf("finish payment %d: check receipt: %w", paymentID, err)
		}
		if used {
			// One receipt settles one payment. This is an old success replayed against a
			// different payment; it is not applied, and reconciliation asks M-Pesa instead.
			out.Action, out.Reason = ActionUnverifiable, "the M-Pesa receipt already belongs to another payment"
			return out, nil
		}
	}
	paid := res.AmountCents
	if paid == nil {
		if res.Source != SourceQuery {
			out.Action, out.Reason = ActionUnverifiable, "the success report carries no amount"
			return out, f.keepRaw(ctx)
		}
		// Our own authenticated query says "paid" and (as Daraja's does) names no amount.
		// An STK prompt can only be paid in full, so the amount is the one we asked for.
		paid = &pay.AmountCents
	}

	// 3. The amount.
	if *paid != b.TotalCents {
		return f.wrongAmount(ctx, out, *paid)
	}

	// 4. Sell, if the seats are still this booking's to sell.
	reason := f.whyNotSellable()
	if reason == "" {
		tickets, shortReason, err := f.sell(ctx)
		if err != nil {
			return Finished{}, err
		}
		if shortReason == "" {
			out.Action, out.Tickets = ActionSold, tickets
			out.PaymentStatus, out.BookingStatus = domain.PaymentSuccess, domain.BookingPaid
			return out, nil
		}
		reason = shortReason
	}

	// 5. The money is here and bought nothing.
	if err := f.writePayment(ctx, domain.PaymentSuccess, res.ResultCode, res.ResultDesc); err != nil {
		return Finished{}, err
	}
	out.PaymentStatus = domain.PaymentSuccess
	return f.refund(ctx, out, *paid, reason, "REFUND_REQUIRED")
}

// finishing carries what finishTx read under its locks.
type finishing struct {
	s       *Service
	tx      pgx.Tx
	q       *store.Queries
	res     Result
	ev      store.EventForHold
	hold    *store.LockedHold
	booking store.Booking
	pay     store.Payment
}

// keepRaw stores the callback body on a payment that has none, when nothing else is written.
func (f *finishing) keepRaw(ctx context.Context) error {
	if len(f.res.Raw) == 0 || len(f.pay.RawCallback) > 0 {
		return nil
	}
	if _, err := f.q.StoreRawCallback(ctx, store.StoreRawCallbackParams{ID: f.pay.ID, RawCallback: f.res.Raw}); err != nil {
		return fmt.Errorf("finish payment %d: keep raw callback: %w", f.pay.ID, err)
	}
	return nil
}

// writePayment moves the payment to its final status. The move was asked of
// domain.TransitionPayment with the status read under the row lock; it is asked again here so
// no path can write without it.
func (f *finishing) writePayment(ctx context.Context, to domain.PaymentStatus, code *int32, desc string) error {
	return writePayment(ctx, f.q, f.pay, to, code, desc, f.res.Receipt, f.res.Raw)
}

func writePayment(ctx context.Context, q *store.Queries, pay store.Payment, to domain.PaymentStatus, code *int32, desc, receipt string, raw []byte) error {
	from := domain.PaymentStatus(pay.Status)
	if err := domain.TransitionPayment(from, to); err != nil {
		return fmt.Errorf("finish payment %d: %w", pay.ID, err)
	}
	arg := store.FinishPaymentParams{ID: pay.ID, FromStatus: string(from), Status: string(to), ResultCode: code, RawCallback: raw}
	if desc != "" {
		arg.ResultDesc = &desc
	}
	if receipt != "" {
		arg.MpesaReceipt = &receipt
	}
	n, err := q.FinishPayment(ctx, arg)
	if err != nil {
		return fmt.Errorf("finish payment %d: write %s: %w", pay.ID, to, err)
	}
	if n != 1 {
		return fmt.Errorf("finish payment %d: changed under its row lock", pay.ID)
	}
	return nil
}

// moveBooking moves the booking from the status read under its lock, if the rule allows it.
func moveBooking(ctx context.Context, q *store.Queries, b store.Booking, to domain.BookingStatus) error {
	from := domain.BookingStatus(b.Status)
	if err := domain.TransitionBooking(from, to); err != nil {
		return fmt.Errorf("booking %d: %w", b.ID, err)
	}
	n, err := q.SetBookingStatus(ctx, store.SetBookingStatusParams{ID: b.ID, Status: string(to), FromStatus: string(from)})
	if err != nil {
		return fmt.Errorf("booking %d: write %s: %w", b.ID, to, err)
	}
	if n != 1 {
		return fmt.Errorf("booking %d: changed under its row lock", b.ID)
	}
	return nil
}

// fail is step 2: the payment ends FAILED or TIMEOUT.
func (f *finishing) fail(ctx context.Context, out Finished, to domain.PaymentStatus) (Finished, error) {
	if err := f.writePayment(ctx, to, f.res.ResultCode, f.res.ResultDesc); err != nil {
		return Finished{}, err
	}
	out.Action, out.PaymentStatus, out.Reason = ActionFailed, to, f.res.ResultDesc

	// The booking stays payable while its hold is there. Judged by the hold's stored
	// status: a hold that has only lapsed is closed, with its booking, by the sweeper or by
	// whoever takes its last seat, both of which now find no live payment in the way.
	if domain.BookingStatus(f.booking.Status) == domain.BookingPendingPayment {
		closeAs := domain.BookingPendingPayment
		switch {
		case f.hold == nil, domain.HoldStatus(f.hold.Status) == domain.HoldExpired:
			closeAs = domain.BookingExpired
		case domain.HoldStatus(f.hold.Status) == domain.HoldReleased:
			closeAs = domain.BookingCancelled
		}
		if closeAs != domain.BookingPendingPayment {
			if err := moveBooking(ctx, f.q, f.booking, closeAs); err != nil {
				return Finished{}, err
			}
			out.BookingStatus = closeAs
		}
	}

	if f.res.Outcome == OutcomeTimedOut && f.res.Unresolved {
		msg := fmt.Sprintf("M-Pesa gave no final answer for a payment of KES %s on booking %s. It was closed as timed out; check the M-Pesa portal for the payment.",
			formatKES(f.pay.AmountCents), f.booking.Reference)
		if err := f.alert(ctx, "PAYMENT_UNRESOLVED", msg); err != nil {
			return Finished{}, err
		}
	}
	return out, nil
}

// wrongAmount is step 3.
func (f *finishing) wrongAmount(ctx context.Context, out Finished, paid int64) (Finished, error) {
	desc := fmt.Sprintf("Paid KES %s; the booking total is KES %s.", formatKES(paid), formatKES(f.booking.TotalCents))
	zero := int32(domain.ResultSuccess)
	code := f.res.ResultCode
	if code == nil {
		code = &zero
	}
	// FAILED with M-Pesa's success code: failure_reason reads that pair as "wrong_amount".
	if err := f.writePayment(ctx, domain.PaymentFailed, code, desc); err != nil {
		return Finished{}, err
	}
	out.PaymentStatus = domain.PaymentFailed
	return f.refund(ctx, out, paid, fmt.Sprintf("Paid KES %s instead of KES %s", formatKES(paid), formatKES(f.booking.TotalCents)), "AMOUNT_MISMATCH")
}

// whyNotSellable says why this payment cannot sell the booking's seats, or "" when it can try.
func (f *finishing) whyNotSellable() string {
	switch domain.BookingStatus(f.booking.Status) {
	case domain.BookingPendingPayment:
	case domain.BookingPaid:
		return reasonAlreadyPaid
	default:
		if domain.EventStatus(f.ev.Status) == domain.EventCancelled {
			return reasonEventCancelled
		}
		if domain.BookingStatus(f.booking.Status) == domain.BookingExpired {
			return reasonHoldGone
		}
		return reasonBookingClosed
	}
	if domain.EventStatus(f.ev.Status) == domain.EventCancelled {
		return reasonEventCancelled
	}
	// The STORED status. EXPIRED or RELEASED: its seats went back on sale and it has none.
	// ACTIVE, even past its expiry: it has whatever seats nobody has taken, and the seat
	// statement finds out whether that is all of them.
	if f.hold == nil || domain.HoldStatus(f.hold.Status) != domain.HoldActive {
		return reasonHoldGone
	}
	return ""
}

// sell is step 4, under a savepoint. It returns how many tickets were issued, or a reason
// when nothing could be sold; in that case every write it made has been undone.
func (f *finishing) sell(ctx context.Context) (tickets int, shortReason string, err error) {
	items, err := f.q.CountBookingItems(ctx, f.booking.ID)
	if err != nil {
		return 0, "", fmt.Errorf("finish payment %d: count items: %w", f.pay.ID, err)
	}
	n := int(items)
	if n == 0 {
		return 0, reasonBookingClosed, nil
	}
	if err := errors.Join(
		domain.TransitionHold(domain.HoldStatus(f.hold.Status), domain.HoldConverted),
		domain.TransitionSeat(domain.SeatHeld, domain.SeatSold),
	); err != nil {
		return 0, "", fmt.Errorf("finish payment %d: %w", f.pay.ID, err)
	}

	sp, err := f.tx.Begin(ctx)
	if err != nil {
		return 0, "", fmt.Errorf("finish payment %d: savepoint: %w", f.pay.ID, err)
	}
	undo := func(cause error) error {
		if rbErr := sp.Rollback(context.WithoutCancel(ctx)); rbErr != nil {
			return errors.Join(cause, fmt.Errorf("finish payment %d: roll back savepoint: %w", f.pay.ID, rbErr))
		}
		return cause
	}
	spq := store.New(sp)

	// Everything that is not a seat first: the seat write stamps event_seats.updated_at,
	// which the seat map's poll reads, so it goes as late as it can.
	if err := writePayment(ctx, spq, f.pay, domain.PaymentSuccess, f.res.ResultCode, f.res.ResultDesc, f.res.Receipt, f.res.Raw); err != nil {
		return 0, "", undo(err)
	}
	if err := moveBooking(ctx, spq, f.booking, domain.BookingPaid); err != nil {
		return 0, "", undo(err)
	}
	converted, err := spq.SetHoldStatus(ctx, store.SetHoldStatusParams{ID: f.hold.ID, Status: string(domain.HoldConverted)})
	if err != nil || converted != 1 {
		return 0, "", undo(fmt.Errorf("finish payment %d: convert hold %d: %d rows: %w", f.pay.ID, f.hold.ID, converted, err))
	}

	sold, err := store.SellSeatsOfHold(ctx, sp, f.hold.ID, n)
	if err != nil {
		return 0, "", undo(fmt.Errorf("finish payment %d: sell seats of hold %d: %w", f.pay.ID, f.hold.ID, err))
	}
	if sold.Sold != n {
		// Correction 5: the hold no longer has every seat (someone took one over after it
		// lapsed). The statement sold none. PAID and CONVERTED are undone; the caller
		// records the payment and queues the refund.
		if err := undo(nil); err != nil {
			return 0, "", err
		}
		return 0, reasonSeatsResold, nil
	}

	// Immediately after the seat write: nothing that can wait may come between.
	issued, err := f.s.tickets.IssueForBooking(ctx, sp, f.booking.ID)
	switch {
	case errors.Is(err, ticketing.ErrSeatsNotSold), errors.Is(err, ticketing.ErrSeatAlreadyTicketed),
		errors.Is(err, ticketing.ErrEventCancelled), errors.Is(err, ticketing.ErrBookingEmpty):
		// The ticket rules refuse this sale (the seats sold are not exactly the booking's
		// items, or one already carries a live ticket). Tickets cannot be given, so the
		// sale is undone and the money goes back.
		f.s.log.ErrorContext(ctx, "tickets were refused for a payment that sold its seats; the sale was undone and a refund queued",
			"payment_id", f.pay.ID, "booking_id", f.booking.ID, "err", err)
		if err := undo(nil); err != nil {
			return 0, "", err
		}
		return 0, reasonSeatsResold, nil
	case err != nil:
		return 0, "", undo(fmt.Errorf("finish payment %d: issue tickets: %w", f.pay.ID, err))
	case len(issued) != n:
		return 0, "", undo(fmt.Errorf("finish payment %d: issued %d tickets for %d seats", f.pay.ID, len(issued), n))
	}
	if err := sp.Commit(ctx); err != nil {
		return 0, "", fmt.Errorf("finish payment %d: release savepoint: %w", f.pay.ID, err)
	}
	return n, "", nil
}

// refund is step 5 (and the tail of step 3): queue the money to go back, mark the booking
// and tell the organiser. The payment's own status has been written by the caller.
func (f *finishing) refund(ctx context.Context, out Finished, paid int64, reason, alertKind string) (Finished, error) {
	out.Action, out.Reason = ActionRefund, reason
	if paid > 0 {
		// ON CONFLICT (payment_id) DO NOTHING: one payment is queued once. Event cancel
		// relies on the same key.
		if _, err := f.q.QueueRefund(ctx, store.QueueRefundParams{
			BookingID: f.booking.ID, PaymentID: f.pay.ID, AmountCents: paid, Reason: reason,
		}); err != nil {
			return Finished{}, fmt.Errorf("finish payment %d: queue refund: %w", f.pay.ID, err)
		}
	}

	switch from := domain.BookingStatus(f.booking.Status); from {
	case domain.BookingPaid:
		// Another payment bought these seats. The booking and its tickets are not touched;
		// only this payment goes back.
	case domain.BookingRefundRequired:
	default:
		if err := moveBooking(ctx, f.q, f.booking, domain.BookingRefundRequired); err != nil {
			return Finished{}, err
		}
		out.BookingStatus = domain.BookingRefundRequired
	}

	msg := fmt.Sprintf("%s. Booking %s: KES %s received (receipt %s) and no seats given. Refund it from the refund queue.",
		reason, f.booking.Reference, formatKES(paid), orDash(f.res.Receipt))
	if domain.BookingStatus(f.booking.Status) == domain.BookingPaid {
		msg = fmt.Sprintf("%s. Booking %s keeps its tickets; the extra KES %s (receipt %s) must be refunded.",
			reason, f.booking.Reference, formatKES(paid), orDash(f.res.Receipt))
	}
	if err := f.alert(ctx, alertKind, msg); err != nil {
		return Finished{}, err
	}
	return out, nil
}

func (f *finishing) alert(ctx context.Context, kind, message string) error {
	bookingID := f.booking.ID
	if _, err := f.q.InsertAlert(ctx, store.InsertAlertParams{
		EventID: f.booking.EventID, BookingID: &bookingID, Kind: kind, Message: message,
	}); err != nil {
		return fmt.Errorf("finish payment %d: raise %s alert: %w", f.pay.ID, kind, err)
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// formatKES writes cents as shillings with two decimals and thousands separators, using
// integer arithmetic only.
func formatKES(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	whole := fmt.Sprintf("%d", cents/100)
	for i := len(whole) - 3; i > 0; i -= 3 {
		whole = whole[:i] + "," + whole[i:]
	}
	return fmt.Sprintf("%s%s.%02d", sign, whole, cents%100)
}
