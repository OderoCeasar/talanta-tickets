package payments

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
)

// Source says who reported a result. It decides how far the result is believed.
type Source int

const (
	// SourceCallback is the provider's callback: an unauthenticated HTTP request. A FAILURE
	// is taken from it (the worst a false one does is close a payment the fan can start
	// again, and a later confirmed success still overrules it). A SUCCESS never is: money is
	// only believed to have moved on the provider's own answer to our query.
	SourceCallback Source = iota
	// SourceQuery is the provider's answer to our own, authenticated status query. It is the
	// only source a success is accepted from.
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
	// AmountCents is what was paid, when the provider's own answer says (SourceQuery only).
	// Nil means the provider names no amount, as Daraja's STK query does not: the payment is
	// then paid in full at the amount we asked for.
	AmountCents *int64
	// ClaimedAmountCents is the amount a callback body named. It is never money: it is only
	// compared with what we asked for, and a difference raises an alert.
	ClaimedAmountCents *int64
	// Receipt is the M-Pesa receipt. From a callback body it is unauthenticated; it is kept
	// for the organiser to look the payment up by, and decides nothing.
	Receipt string
	// Raw is the callback body, valid JSON, or nil.
	Raw []byte
	// Unresolved marks a timeout that happened without the provider ever giving a final
	// answer (as opposed to the request provably never having been sent). Money may have
	// moved, so the organiser is alerted.
	Unresolved bool
}

// Action is what Finish did with a result.
type Action string

const (
	// ActionNone: the result changed nothing (the payment was already settled that way, or
	// the report is one that a settled payment ignores).
	ActionNone Action = "none"
	// ActionSold: payment SUCCESS, booking PAID, hold CONVERTED, seats SOLD, tickets issued.
	ActionSold Action = "sold"
	// ActionRefund: the money arrived but bought nothing. The payment is recorded, a refund
	// is queued and the organiser alerted. No seat was sold.
	ActionRefund Action = "refund"
	// ActionFailed: the payment ended FAILED or TIMEOUT.
	ActionFailed Action = "failed"
	// ActionUnverifiable: a success report from a source that is not believed on its own
	// word. Nothing changed; the caller asks the provider.
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
// (a publish, a cancel, an edit) for longer than Policy.EventLockWait. Nothing changed. The
// caller retries: a stored callback is retried by the drain job, a query result by the next
// reconciliation run.
var ErrEventBusy = errors.New("payments: the event is locked; the payment was not finished and will be tried again")

// Why a successful payment bought nothing. These are stored as the refund's reason and shown
// to the organiser.
const (
	reasonEventCancelled = "Paid after the event was cancelled"
	reasonHoldGone       = "Paid after the hold ended; the seats were no longer reserved"
	reasonSeatsResold    = "Paid after the hold expired; seats were resold"
	reasonAlreadyPaid    = "Paid twice: the booking was already paid by another payment"
	reasonBookingClosed  = "Paid after the booking was closed"
	reasonNoTickets      = "Paid, but the tickets could not be issued"
)

// Finish records how a payment ended. It is the ONE function every report goes through: the
// M-Pesa callback (once the provider has confirmed it), a repeat of that callback, the mock
// provider, and reconciliation. It is one database transaction (§8.3: money moves once).
//
// The rule it keeps: once there is evidence that money moved for a payment, the payment ends
// in exactly one of (a) seats SOLD and tickets issued, or (b) a refund row and an organiser
// alert. Never neither, never both.
//
// Locks, in the system's order: the event (shared, bounded wait), the booking's hold, the
// booking, the payment. Then:
//
//  1. A success is accepted only from the provider's own answer (SourceQuery). A success
//     from a callback body is not applied at all (ActionUnverifiable): the caller asks the
//     provider and comes back with its answer.
//  2. The payment's status, read under its lock, decides whether there is anything to do.
//     domain.TransitionPayment refuses a repeat, so a repeated callback, and a callback and a
//     reconciliation run that both carry the same result, change nothing the second time. It
//     ALLOWS a success for a payment that is TIMEOUT or FAILED: both can be recorded before
//     M-Pesa's last word (TIMEOUT is our own verdict; FAILED can come from a callback body),
//     and money that then turns out to have arrived must not be dropped. A failure reported
//     after a SUCCESS is refused and ignored.
//  3. A failure: the payment becomes FAILED or TIMEOUT. The booking stays PENDING_PAYMENT
//     while its hold is there, so the fan can try again, and is closed if the hold is gone.
//  4. The amount. It is the one the provider's answer names, or, when it names none, the
//     amount we asked for: an STK prompt cannot be paid in part or in excess. If it is not
//     the booking's total, the payment is FAILED (result code 0, which is what
//     failure_reason "wrong_amount" is read from), a refund for the amount the PROVIDER named
//     is queued, the booking becomes REFUND_REQUIRED and the organiser is alerted. An amount
//     named only by a callback body is never refunded; when it differs from what we asked
//     for it raises an AMOUNT_MISMATCH alert and nothing else.
//  5. A success with the right amount sells the seats if, and only if, the booking is still
//     PENDING_PAYMENT, its event is not cancelled, and its hold's STORED status is ACTIVE.
//     The stored status, not the effective one: a hold whose time has run out but which
//     nobody has taken a seat from still has every seat, and the fan who paid for them gets
//     them (correction 5). The payment becomes SUCCESS, the booking PAID, the hold
//     CONVERTED, and store.SellSeatsOfHold flips the seats where hold_id matches and the
//     status is still HELD. If it reports as many sold as the booking has items, the
//     tickets are issued at once and the transaction commits.
//  6. Otherwise nothing is sold. If the seat statement came up short it sold none, and if
//     the tickets could not be made for any reason that will not go away by trying again,
//     the sale is undone: the PAID, CONVERTED and SOLD writes are rolled back to a savepoint.
//     The payment is recorded SUCCESS, a refund is queued, the booking becomes
//     REFUND_REQUIRED (a booking that is already PAID by another payment stays PAID and keeps
//     its tickets) and the organiser is alerted. A seat is never sold twice to make the
//     ledger come out right.
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

	// 1. A success is only believed from the provider's own answer.
	if res.Outcome == OutcomeSuccess && res.Source != SourceQuery {
		out.Action, out.Reason = ActionUnverifiable, "a success is only accepted from the provider's own answer to a query"
		return out, nil
	}

	// 2. Is there anything to do? The status was read under the row lock.
	target := domain.PaymentSuccess
	switch res.Outcome {
	case OutcomeFailed:
		target = domain.PaymentFailed
	case OutcomeTimedOut:
		target = domain.PaymentTimeout
	}
	if err := domain.TransitionPayment(out.PaymentStatus, target); err != nil {
		return out, nil
	}

	// 3. A failure.
	if res.Outcome != OutcomeSuccess {
		return f.fail(ctx, out, target)
	}

	// A confirmed success. Money moved; from here the payment ends with tickets or a refund.
	if res.Receipt != "" {
		used, err := q.ReceiptBelongsToAnotherPayment(ctx, store.ReceiptBelongsToAnotherPaymentParams{MpesaReceipt: res.Receipt, ID: pay.ID})
		if err != nil {
			return Finished{}, fmt.Errorf("finish payment %d: check receipt: %w", paymentID, err)
		}
		if used {
			// One receipt names one payment. The provider confirmed this payment, so the
			// money is real; the receipt, which came with a callback body, is not this
			// payment's and is left off it.
			s.log.ErrorContext(ctx, "the M-Pesa receipt already belongs to another payment; it was not recorded on this one",
				"payment_id", pay.ID, "mpesa_receipt", res.Receipt)
			f.res.Receipt = ""
		}
	}

	// 4. The amount.
	paid := pay.AmountCents
	if res.AmountCents != nil {
		paid = *res.AmountCents
	}
	if paid != b.TotalCents {
		return f.wrongAmount(ctx, out, paid)
	}

	// 5. Sell, if the seats are still this booking's to sell.
	reason := f.whyNotSellable()
	if reason == "" {
		tickets, shortReason, err := f.sell(ctx)
		if err != nil {
			return Finished{}, err
		}
		if shortReason == "" {
			out.Action, out.Tickets = ActionSold, tickets
			out.PaymentStatus, out.BookingStatus = domain.PaymentSuccess, domain.BookingPaid
			if err := f.resolveUnresolvedAlerts(ctx, resolutionSold); err != nil {
				return Finished{}, err
			}
			return out, f.flagClaimedAmount(ctx)
		}
		reason = shortReason
	}

	// 6. The money is here and bought nothing.
	if err := f.writePayment(ctx, domain.PaymentSuccess, res.ResultCode, res.ResultDesc); err != nil {
		return Finished{}, err
	}
	out.PaymentStatus = domain.PaymentSuccess
	out, err = f.refund(ctx, out, paid, reason, "REFUND_REQUIRED")
	if err != nil {
		return Finished{}, err
	}
	return out, f.flagClaimedAmount(ctx)
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

// flagClaimedAmount raises an alert when a callback body named an amount other than the one
// we asked M-Pesa to charge, for a payment the provider confirmed without naming an amount.
// An STK prompt cannot be paid in part or in excess, so the body is forged or malformed: the
// payment was treated as paid in full, no refund is queued for the body's figure, and a
// person is asked to look at the statement.
func (f *finishing) flagClaimedAmount(ctx context.Context) error {
	claimed := f.res.ClaimedAmountCents
	if claimed == nil || f.res.AmountCents != nil || *claimed == f.pay.AmountCents {
		return nil
	}
	f.s.log.ErrorContext(ctx, "a payment callback named a different amount from the one requested; M-Pesa confirmed the payment and it was treated as paid in full",
		"payment_id", f.pay.ID, "claimed_cents", *claimed, "requested_cents", f.pay.AmountCents)
	return f.alert(ctx, "AMOUNT_MISMATCH", fmt.Sprintf(
		"The M-Pesa callback for booking %s named KES %s, but KES %s was requested and M-Pesa confirmed the payment. It was treated as paid in full and no refund was queued for the callback's figure. Check the M-Pesa statement (receipt %s).",
		f.booking.Reference, formatKES(*claimed), formatKES(f.pay.AmountCents), orDash(f.res.Receipt)))
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

// fail is step 3: the payment ends FAILED or TIMEOUT.
func (f *finishing) fail(ctx context.Context, out Finished, to domain.PaymentStatus) (Finished, error) {
	if err := f.writePayment(ctx, to, f.res.ResultCode, f.res.ResultDesc); err != nil {
		return Finished{}, err
	}
	out.Action, out.PaymentStatus, out.Reason = ActionFailed, to, f.res.ResultDesc
	closed, err := f.closeBookingIfHoldGone(ctx)
	if err != nil {
		return Finished{}, err
	}
	if closed != "" {
		out.BookingStatus = closed
	}

	if f.res.Outcome == OutcomeTimedOut && f.res.Unresolved {
		msg := fmt.Sprintf("M-Pesa gave no final answer for a payment of KES %s on booking %s. It was closed as timed out; check the M-Pesa portal for the payment.",
			formatKES(f.pay.AmountCents), f.booking.Reference)
		if f.pay.CheckoutRequestID == nil {
			msg = fmt.Sprintf("A payment request of KES %s from %s for booking %s may have reached M-Pesa, but we never learned its M-Pesa ID and cannot ask about it. It was closed as timed out. Check the M-Pesa statement for that amount from that phone; if the money is there, refund it.",
				formatKES(f.pay.AmountCents), f.pay.PhoneE164, f.booking.Reference)
		}
		if err := f.alert(ctx, "PAYMENT_UNRESOLVED", msg); err != nil {
			return Finished{}, err
		}
	}
	return out, nil
}

// closeBookingIfHoldGone closes a booking that is still awaiting payment when its hold is no
// longer there to pay on, and returns the status it gave it ("" when it left it alone).
//
// The booking stays payable while its hold is there. Judged by the hold's stored status: a
// hold that has only lapsed is closed, with its booking, by the sweeper or by whoever takes
// its last seat, both of which now find no live payment in the way.
func (f *finishing) closeBookingIfHoldGone(ctx context.Context) (domain.BookingStatus, error) {
	if domain.BookingStatus(f.booking.Status) != domain.BookingPendingPayment {
		return "", nil
	}
	closeAs := domain.BookingPendingPayment
	switch {
	case f.hold == nil, domain.HoldStatus(f.hold.Status) == domain.HoldExpired:
		closeAs = domain.BookingExpired
	case domain.HoldStatus(f.hold.Status) == domain.HoldReleased:
		closeAs = domain.BookingCancelled
	}
	if closeAs == domain.BookingPendingPayment {
		return "", nil
	}
	if err := moveBooking(ctx, f.q, f.booking, closeAs); err != nil {
		return "", err
	}
	return closeAs, nil
}

// wrongAmount is step 4: the provider itself says the fan paid, and names an amount that is
// not the booking's total.
func (f *finishing) wrongAmount(ctx context.Context, out Finished, paid int64) (Finished, error) {
	from := domain.PaymentStatus(f.pay.Status)

	if paid <= 0 {
		// "Paid", and nothing was paid. There is no money to give back, so there is no
		// refund, and a booking is not moved to REFUND_REQUIRED with nothing to refund: this
		// is a failed payment. It is odd enough for a person to look at.
		desc := "M-Pesa reported a payment of KES 0.00; nothing was received."
		if from != domain.PaymentFailed {
			// Written without the callback's body and receipt: the row must not carry a
			// "paid" it did not get (the body stays in the callback inbox).
			if err := writePayment(ctx, f.q, f.pay, domain.PaymentFailed, nil, desc, "", nil); err != nil {
				return Finished{}, err
			}
			closed, err := f.closeBookingIfHoldGone(ctx)
			if err != nil {
				return Finished{}, err
			}
			if closed != "" {
				out.BookingStatus = closed
			}
		}
		out.Action, out.PaymentStatus, out.Reason = ActionFailed, domain.PaymentFailed, desc
		return out, f.alert(ctx, "AMOUNT_MISMATCH", fmt.Sprintf(
			"M-Pesa reported booking %s as paid with an amount of KES 0.00 (receipt %s). Nothing was sold and no refund was queued. Check the M-Pesa statement.",
			f.booking.Reference, orDash(f.res.Receipt)))
	}

	desc := fmt.Sprintf("Paid KES %s; the booking total is KES %s.", formatKES(paid), formatKES(f.booking.TotalCents))
	zero := int32(domain.ResultSuccess)
	code := f.res.ResultCode
	if code == nil {
		code = &zero
	}
	// FAILED with M-Pesa's success code: failure_reason reads that pair as "wrong_amount".
	if from == domain.PaymentFailed {
		// Already FAILED (on an earlier report). It stays FAILED; only its result changes.
		arg := store.SetPaymentResultParams{ID: f.pay.ID, Status: string(from), ResultCode: code, ResultDesc: &desc, RawCallback: f.res.Raw}
		if f.res.Receipt != "" {
			arg.MpesaReceipt = &f.res.Receipt
		}
		if n, err := f.q.SetPaymentResult(ctx, arg); err != nil || n != 1 {
			return Finished{}, fmt.Errorf("finish payment %d: rewrite result: %d rows: %w", f.pay.ID, n, err)
		}
	} else if err := f.writePayment(ctx, domain.PaymentFailed, code, desc); err != nil {
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

// isTransient reports whether err is the kind that may not happen again if the same work is
// tried later: the request was cancelled or ran out of time, the connection to the database
// broke, or the database refused for a reason that passes (a lock or serialisation failure,
// no resources, a shutdown). Everything else (a rule of the ticket service, a token that
// cannot be signed, a constraint the row breaks) is deterministic: trying again gives the
// same error.
func isTransient(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if len(pgErr.Code) < 2 {
			return true
		}
		switch pgErr.Code[:2] {
		// Connection, transaction rollback (serialisation, deadlock), insufficient
		// resources, operator intervention (query cancelled, shutdown), system error.
		case "08", "40", "53", "57", "58":
			return true
		}
		return pgErr.Code == "55P03" // lock_not_available
	}
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, pgx.ErrTxClosed) || pgconn.SafeToRetry(err)
}

// sell is step 5, under a savepoint. It returns how many tickets were issued, or a reason
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
	if err == nil && len(issued) != n {
		err = fmt.Errorf("issued %d tickets for %d seats", len(issued), n)
	}
	switch {
	case err == nil:
	case isTransient(err):
		// It may work next time: the whole transaction is given up and the caller retries.
		return 0, "", undo(fmt.Errorf("finish payment %d: issue tickets: %w", f.pay.ID, err))
	default:
		// The tickets cannot be made, and trying again will give the same answer: the ticket
		// rules refuse this sale, or a ticket cannot be signed (a stand code that does not
		// fit a gate group, for one). Rolling the whole transaction back, as an unknown error
		// used to, would leave the fan's money with us and the payment open until it timed
		// out, every time. A sale without tickets is not a sale: it is undone, and the money
		// goes back.
		f.s.log.ErrorContext(ctx, "tickets could not be issued for a payment that sold its seats; the sale was undone and a refund queued",
			"payment_id", f.pay.ID, "booking_id", f.booking.ID, "err", err)
		if err := undo(nil); err != nil {
			return 0, "", err
		}
		if errors.Is(err, ticketing.ErrSeatsNotSold) || errors.Is(err, ticketing.ErrSeatAlreadyTicketed) {
			return 0, reasonSeatsResold, nil
		}
		if errors.Is(err, ticketing.ErrEventCancelled) {
			return 0, reasonEventCancelled, nil
		}
		return 0, reasonNoTickets, nil
	}
	if err := sp.Commit(ctx); err != nil {
		return 0, "", fmt.Errorf("finish payment %d: release savepoint: %w", f.pay.ID, err)
	}
	return n, "", nil
}

// refund is step 6 (and the tail of step 4): queue the money to go back, mark the booking
// and tell the organiser. The payment's own status has been written by the caller. paid is
// always above zero and always an amount the provider's own answer stands behind.
func (f *finishing) refund(ctx context.Context, out Finished, paid int64, reason, alertKind string) (Finished, error) {
	out.Action, out.Reason = ActionRefund, reason
	if paid <= 0 {
		return Finished{}, fmt.Errorf("finish payment %d: refusing to queue a refund of %d cents", f.pay.ID, paid)
	}
	// ON CONFLICT (payment_id) DO NOTHING: one payment is queued once. Event cancel relies
	// on the same key.
	if _, err := f.q.QueueRefund(ctx, store.QueueRefundParams{
		BookingID: f.booking.ID, PaymentID: f.pay.ID, AmountCents: paid, Reason: reason,
	}); err != nil {
		return Finished{}, fmt.Errorf("finish payment %d: queue refund: %w", f.pay.ID, err)
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
	return out, f.resolveUnresolvedAlerts(ctx, resolutionRefundQueued)
}

// What the system writes on an alert it closes itself (organiser_alerts.resolution).
const (
	resolutionSold         = "Resolved automatically: the payment succeeded and the tickets were issued."
	resolutionRefundQueued = "Resolved automatically: the payment was received and a refund is queued for it."
	resolutionRefundPaid   = "Resolved automatically: the refund was marked as paid back."
)

// resolveUnresolvedAlerts closes the open PAYMENT_UNRESOLVED alerts about this payment. Such
// an alert says "we do not know what became of this payment; check the M-Pesa portal". Once
// the payment has ended in tickets or in a queued refund that is no longer true, and leaving
// it open sends staff to look for a problem that is solved. It is closed in the transaction
// that settles the payment.
func (f *finishing) resolveUnresolvedAlerts(ctx context.Context, resolution string) error {
	if _, err := f.q.ResolveAlertsOfPayment(ctx, store.ResolveAlertsOfPaymentParams{
		PaymentID: f.pay.ID, Kinds: []string{"PAYMENT_UNRESOLVED"}, Resolution: resolution,
	}); err != nil {
		return fmt.Errorf("finish payment %d: resolve alerts: %w", f.pay.ID, err)
	}
	return nil
}

func (f *finishing) alert(ctx context.Context, kind, message string) error {
	bookingID, paymentID := f.booking.ID, f.pay.ID
	if _, err := f.q.InsertAlert(ctx, store.InsertAlertParams{
		EventID: f.booking.EventID, BookingID: &bookingID, PaymentID: &paymentID, Kind: kind, Message: message,
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
