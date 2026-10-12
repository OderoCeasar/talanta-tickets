package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The locking statements of bookings, payments and refunds (docs/ARCHITECTURE.md §8).
//
// They extend the lock order written at the top of holds.go. Every transaction here takes:
//
//	0/1. the event, shared           (LockEventForHoldWithin or LockEventOfHoldWithin)
//	3.   the hold row                (LockHold), when the booking has a hold
//	4.   the booking row, then the payment row, then a refund row
//
// and writes event_seats last (SellSeatsOfHold), followed only by ticket issue, which waits
// for nothing (see docs/DECISIONS.md). A booking, its payments and its refunds are only
// touched by a transaction that already has the booking's hold locked, so "booking before
// payment before refund" is an order among rows that one transaction at a time can reach.
//
// Two statements are outside that rule on purpose, each a single row lock held by a
// transaction that waits for nothing else afterwards, so neither can be part of a deadlock:
// LockPaymentAlone (recording that M-Pesa took the request) and the claim statements of
// reconciliation and the callback inbox (which never wait at all: SKIP LOCKED).

const bookingColumns = `id, reference, event_id, user_id, hold_id, status, subtotal_cents, fee_cents, total_cents,
       idempotency_key, created_at, paid_at, web_token, updated_at`

func scanBooking(row pgx.Row) (Booking, error) {
	var b Booking
	err := row.Scan(&b.ID, &b.Reference, &b.EventID, &b.UserID, &b.HoldID, &b.Status, &b.SubtotalCents, &b.FeeCents,
		&b.TotalCents, &b.IdempotencyKey, &b.CreatedAt, &b.PaidAt, &b.WebToken, &b.UpdatedAt)
	return b, err
}

// Locks: the one bookings row, FOR UPDATE, until the transaction ends.
// Why: starting a payment, finishing one and completing a refund each read the booking's
// status and then act on it. The row lock (taken after the hold's, lock order) makes them
// take turns on a booking even when it has no hold row to take turns on, and the status read
// under it is the one domain.TransitionBooking is asked about. Ticket issue takes the same
// lock (LockBookingForTickets); for a caller that already has it that is free.
const lockBooking = `SELECT ` + bookingColumns + ` FROM bookings WHERE id = $1 FOR UPDATE`

// LockBooking takes a booking's row lock and returns the row. ErrNoRows when there is none.
func LockBooking(ctx context.Context, db DBTX, bookingID int64) (Booking, error) {
	return scanBooking(db.QueryRow(ctx, lockBooking, bookingID))
}

const paymentColumns = `id, booking_id, provider, amount_cents, phone_e164, status, merchant_request_id,
       checkout_request_id, mpesa_receipt, result_code, result_desc, raw_callback, created_at, completed_at,
       last_queried_at, query_attempts, dispatch`

func scanPayment(row pgx.Row) (Payment, error) {
	var p Payment
	err := row.Scan(&p.ID, &p.BookingID, &p.Provider, &p.AmountCents, &p.PhoneE164, &p.Status, &p.MerchantRequestID,
		&p.CheckoutRequestID, &p.MpesaReceipt, &p.ResultCode, &p.ResultDesc, &p.RawCallback, &p.CreatedAt, &p.CompletedAt,
		&p.LastQueriedAt, &p.QueryAttempts, &p.Dispatch)
	return p, err
}

// What a payment's dispatch column says about whether its STK push reached M-Pesa
// (migration 000009).
const (
	DispatchUnknown   = "UNKNOWN"
	DispatchNotSent   = "NOT_SENT"
	DispatchAmbiguous = "AMBIGUOUS"
	DispatchAccepted  = "ACCEPTED"
)

// Locks: the one payments row, FOR UPDATE, until the transaction ends.
// Why: this is what makes a payment finish exactly once. The callback, a repeat of the
// callback, the mock and reconciliation all end in the same function, which takes this lock
// and then reads the status: the first one through writes the final state, every later one
// reads it and stops. Without the lock two of them could both read PENDING and both sell.
// It is the last lock in the order: the caller already has the event (shared), the
// booking's hold and the booking.
const lockPayment = `SELECT ` + paymentColumns + ` FROM payments WHERE id = $1 FOR UPDATE`

// LockPayment takes a payment's row lock and returns the row. ErrNoRows when there is none.
func LockPayment(ctx context.Context, db DBTX, paymentID int64) (Payment, error) {
	return scanPayment(db.QueryRow(ctx, lockPayment, paymentID))
}

// LockPaymentAlone is LockPayment for the one transaction that takes it WITHOUT the event,
// hold and booking locks: recording that the provider accepted the request (INITIATED to
// PENDING, with the provider's IDs). That transaction locks this single row, writes it and
// commits; it never waits for another lock while holding it, so it cannot deadlock with a
// finishing transaction, which only makes it wait its turn.
func LockPaymentAlone(ctx context.Context, db DBTX, paymentID int64) (Payment, error) {
	return LockPayment(ctx, db, paymentID)
}

// Locks: the one refunds row, FOR UPDATE, until the transaction ends.
// Why: two organisers pressing "refund paid" at once must mark it once and move the booking
// to REFUNDED once. Taken after the event, the hold and the booking (lock order).
const lockRefund = `
SELECT id, booking_id, payment_id, amount_cents, reason, status, created_at, completed_at
FROM refunds WHERE id = $1 FOR UPDATE`

// LockRefund takes a refund's row lock and returns the row. ErrNoRows when there is none.
func LockRefund(ctx context.Context, db DBTX, refundID int64) (Refund, error) {
	var r Refund
	err := db.QueryRow(ctx, lockRefund, refundID).
		Scan(&r.ID, &r.BookingID, &r.PaymentID, &r.AmountCents, &r.Reason, &r.Status, &r.CreatedAt, &r.CompletedAt)
	return r, err
}

// ClaimedPayment is a payment handed to one reconciliation run.
type ClaimedPayment struct {
	ID                int64
	Status            string
	CheckoutRequestID *string
	// Dispatch is what is known about whether the STK push reached M-Pesa (Dispatch*).
	Dispatch string
	// CreatedAt and Age (how long ago that was) are by the database clock.
	CreatedAt time.Time
	Age       time.Duration
	// QueryAttempts counts this claim too.
	QueryAttempts int
}

// PaymentCursor is the position after the last payment a reconciliation run claimed. A run
// claims in chunks and passes the cursor back, so one run never claims the same payment
// twice, whatever the lease. The zero value is the start.
type PaymentCursor struct {
	CreatedAt time.Time
	ID        int64
}

func collectClaimedPayments(rows pgx.Rows, err error) ([]ClaimedPayment, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClaimedPayment, error) {
		var p ClaimedPayment
		var age float64
		var attempts int32
		err := row.Scan(&p.ID, &p.Status, &p.CheckoutRequestID, &p.Dispatch, &p.CreatedAt, &age, &attempts)
		p.Age, p.QueryAttempts = time.Duration(age*float64(time.Second)), int(attempts)
		return p, err
	})
}

// Locks: up to $3 live payments older than $1 that no run has looked at for $2, FOR UPDATE
// SKIP LOCKED, for the length of this one statement. $4 chooses the payments that have a
// provider ID (true) or those that have none (false); ($5, $6) is where the run's previous
// chunk ended.
// Why: reconciliation asks M-Pesa about payments whose callback never came. Two workers must
// not both ask about (and then both try to finish) the same payment. The statement stamps
// last_queried_at as it selects, so a claim is a lease: the next run, in this worker or
// another, leaves the payment alone until the lease has passed. SKIP LOCKED means it never
// waits: a payment that a callback is finishing right now is simply not claimed. The lock is
// released when the statement's transaction commits, before any call to M-Pesa is made;
// the finishing function takes the row again, in lock order, and is what decides.
// Payments with no provider ID are claimed separately and first: they need no call to M-Pesa,
// so a slow M-Pesa cannot keep them waiting. Read through payments_live_booking_idx (the
// live set is small).
const claimPaymentsForReconcile = `
WITH due AS (
    SELECT id FROM payments
    WHERE status IN ('INITIATED', 'PENDING')
      AND created_at <= clock_timestamp() - make_interval(secs => $1::float8)
      AND (last_queried_at IS NULL OR last_queried_at <= clock_timestamp() - make_interval(secs => $2::float8))
      AND (checkout_request_id IS NOT NULL) = $4::boolean
      AND (created_at, id) > ($5::timestamptz, $6::bigint)
    ORDER BY created_at, id
    LIMIT $3
    FOR UPDATE SKIP LOCKED
)
UPDATE payments p
SET last_queried_at = clock_timestamp(), query_attempts = p.query_attempts + 1
FROM due
WHERE p.id = due.id
RETURNING p.id, p.status, p.checkout_request_id, p.dispatch, p.created_at,
          extract(epoch FROM clock_timestamp() - p.created_at)::float8, p.query_attempts`

// ClaimPaymentsForReconcile leases a batch of live payments older than minAge to the caller:
// those with a provider ID, or those without one.
func ClaimPaymentsForReconcile(ctx context.Context, db DBTX, minAge, lease time.Duration, limit int, withProviderID bool, after PaymentCursor) ([]ClaimedPayment, error) {
	return collectClaimedPayments(db.Query(ctx, claimPaymentsForReconcile, minAge.Seconds(), lease.Seconds(), limit, withProviderID, after.CreatedAt, after.ID))
}

// RequerySchedule is how often, and for how long, M-Pesa keeps being asked about a payment
// that was closed as TIMEOUT: every Every while the payment is younger than DenseFor, then
// every SparseEvery until it is Until old. All by the database clock.
type RequerySchedule struct {
	Every, DenseFor, SparseEvery, Until time.Duration
}

// Locks: up to $5 TIMEOUT payments that have a provider ID, are younger than $1 and are due
// under the schedule, FOR UPDATE SKIP LOCKED, for the length of this one statement.
// Why: TIMEOUT is our verdict, not M-Pesa's. A payment we stopped waiting for may still have
// been paid, and without this only a callback could ever say so. The statement stamps
// last_queried_at as it selects (the same lease as ClaimPaymentsForReconcile), so two workers
// never ask about the same payment, and a payment being finished right now is skipped. The
// lock is gone before M-Pesa is called. Read through payments_timeout_requery_idx.
const claimTimedOutPaymentsForRequery = `
WITH due AS (
    SELECT id FROM payments
    WHERE status = 'TIMEOUT' AND checkout_request_id IS NOT NULL
      AND created_at > clock_timestamp() - make_interval(secs => $1::float8)
      AND (last_queried_at IS NULL OR last_queried_at <= clock_timestamp() - make_interval(secs =>
            CASE WHEN created_at > clock_timestamp() - make_interval(secs => $2::float8) THEN $3::float8 ELSE $4::float8 END))
      AND (created_at, id) > ($6::timestamptz, $7::bigint)
    ORDER BY created_at, id
    LIMIT $5
    FOR UPDATE SKIP LOCKED
)
UPDATE payments p
SET last_queried_at = clock_timestamp(), query_attempts = p.query_attempts + 1
FROM due
WHERE p.id = due.id
RETURNING p.id, p.status, p.checkout_request_id, p.dispatch, p.created_at,
          extract(epoch FROM clock_timestamp() - p.created_at)::float8, p.query_attempts`

// ClaimTimedOutPaymentsForRequery leases a batch of TIMEOUT payments that are due to be asked
// about again.
func ClaimTimedOutPaymentsForRequery(ctx context.Context, db DBTX, s RequerySchedule, limit int, after PaymentCursor) ([]ClaimedPayment, error) {
	return collectClaimedPayments(db.Query(ctx, claimTimedOutPaymentsForRequery,
		s.Until.Seconds(), s.DenseFor.Seconds(), s.Every.Seconds(), s.SparseEvery.Seconds(), limit, after.CreatedAt, after.ID))
}

// ClaimedCallback is a stored callback handed to one worker: a row of payment_callbacks (the
// inbox, PaymentID set) or of unmatched_callbacks (PaymentID nil).
type ClaimedCallback struct {
	ID                int64
	PaymentID         *int64
	CheckoutRequestID string
	MerchantRequestID *string
	ResultCode        int32
	ResultDesc        *string
	// AmountCents, MpesaReceipt and PhoneE164 are what the body claims.
	AmountCents  *int64
	MpesaReceipt *string
	PhoneE164    *string
	Raw          []byte
	ReceivedAt   time.Time
	// Age is how long ago the callback arrived, by the database clock.
	Age time.Duration
	// Attempts counts this claim too.
	Attempts int
	// AlertID is the alert already raised for an unmatched callback, if any.
	AlertID *int64
}

// Locks: up to $3 stored callbacks that are due and not dealt with, FOR UPDATE SKIP LOCKED,
// for the length of this one statement. $1 narrows it to one row (0: any).
// Why: a callback is written to the inbox before the webhook answers, and then worked on by
// whoever gets to it: the API's background runner straight away, or the worker's drain job
// after a failure or a restart. They must not both work on the same row. The statement moves
// next_attempt_at forward as it selects, so a claim is a lease of $2 seconds; whoever holds it
// either marks the row done or sets when it is due again. SKIP LOCKED: it never waits. The
// lock is released with the statement, before M-Pesa is asked and before the finishing
// function takes its locks, so this is outside the lock order by construction.
// %[1]s is the table and %[2]s the two columns the tables do not share.
const claimCallbacks = `
WITH due AS (
    SELECT id FROM %[1]s
    WHERE processed_at IS NULL AND next_attempt_at <= clock_timestamp()
      AND ($1::bigint = 0 OR id = $1::bigint)
    ORDER BY next_attempt_at, id
    LIMIT $3
    FOR UPDATE SKIP LOCKED
)
UPDATE %[1]s c
SET attempts = c.attempts + 1, next_attempt_at = clock_timestamp() + make_interval(secs => $2::float8)
FROM due
WHERE c.id = due.id
RETURNING c.id, %[2]s, c.checkout_request_id, c.merchant_request_id, c.result_code, c.result_desc,
          c.amount_cents, c.mpesa_receipt, c.phone_e164, c.raw, c.received_at,
          extract(epoch FROM clock_timestamp() - c.received_at)::float8, c.attempts`

var (
	claimPaymentCallbacks   = fmt.Sprintf(claimCallbacks, "payment_callbacks", "c.payment_id, NULL::bigint")
	claimUnmatchedCallbacks = fmt.Sprintf(claimCallbacks, "unmatched_callbacks", "NULL::bigint, c.alert_id")
)

func collectClaimedCallbacks(rows pgx.Rows, err error) ([]ClaimedCallback, error) {
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClaimedCallback, error) {
		var c ClaimedCallback
		var age float64
		var attempts int32
		err := row.Scan(&c.ID, &c.PaymentID, &c.AlertID, &c.CheckoutRequestID, &c.MerchantRequestID, &c.ResultCode, &c.ResultDesc,
			&c.AmountCents, &c.MpesaReceipt, &c.PhoneE164, &c.Raw, &c.ReceivedAt, &age, &attempts)
		c.Age, c.Attempts = time.Duration(age*float64(time.Second)), int(attempts)
		return c, err
	})
}

// ClaimPaymentCallbacks leases up to limit due rows of the callback inbox for lease. onlyID
// narrows it to one row; zero means any.
func ClaimPaymentCallbacks(ctx context.Context, db DBTX, onlyID int64, lease time.Duration, limit int) ([]ClaimedCallback, error) {
	return collectClaimedCallbacks(db.Query(ctx, claimPaymentCallbacks, onlyID, lease.Seconds(), limit))
}

// ClaimUnmatchedCallbacks is ClaimPaymentCallbacks for unmatched_callbacks.
func ClaimUnmatchedCallbacks(ctx context.Context, db DBTX, onlyID int64, lease time.Duration, limit int) ([]ClaimedCallback, error) {
	return collectClaimedCallbacks(db.Query(ctx, claimUnmatchedCallbacks, onlyID, lease.Seconds(), limit))
}

// Locks: every bookings row of one event, FOR UPDATE, until the transaction ends.
// Why: cancelling an event rewrites its bookings in bulk (unpaid ones CANCELLED, paid ones
// REFUND_REQUIRED). The rule for which booking may become what is domain.TransitionBooking,
// and it has to be asked with the statuses the rows really have, not with the ones the
// UPDATEs hope to find. This statement locks the rows and reports how many there are in each
// status, so the caller can ask the rule once per status actually present before it writes.
// The caller holds the event exclusively (LockEvent), so no payment or booking transaction
// of the event is in flight and nothing here waits. FOR UPDATE sits in a sub-select because
// Postgres does not allow it beside GROUP BY.
const lockBookingsOfEvent = `
SELECT locked.status, count(*)::int
FROM (SELECT status FROM bookings WHERE event_id = $1 FOR UPDATE) locked
GROUP BY locked.status`

// LockBookingsOfEvent locks every booking of an event and returns how many it has per status.
func LockBookingsOfEvent(ctx context.Context, db DBTX, eventID int64) (map[string]int, error) {
	rows, err := db.Query(ctx, lockBookingsOfEvent, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		out[status] = n
	}
	return out, rows.Err()
}

// Names of the unique constraints the booking and payment flows tell apart.
const (
	ConstraintBookingReference   = "bookings_reference_key"
	ConstraintBookingIdempotency = "bookings_user_id_idempotency_key_key"
	ConstraintBookingLiveHold    = "bookings_live_hold_key"
	ConstraintPaymentLiveBooking = "payments_live_booking_idx"
	ConstraintPaymentReceipt     = "payments_mpesa_receipt_key"
)

// BookingView is a booking with its event and its hold's expiry. The three booking view
// queries select the same columns, so their row types convert to this one.
type BookingView = GetBookingViewByIDRow

// RefundView is a refund with its booking's reference and event and the payment it returns.
type RefundView = GetRefundViewRow

// AlertView is an organiser alert with its booking's reference, when it has a booking.
type AlertView = GetAlertViewRow
