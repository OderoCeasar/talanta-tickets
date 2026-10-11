package store

import (
	"context"
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
// LockPaymentAlone (recording that M-Pesa took the request) and ClaimPaymentsForReconcile
// (which never waits at all: SKIP LOCKED).

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
       last_queried_at, query_attempts`

func scanPayment(row pgx.Row) (Payment, error) {
	var p Payment
	err := row.Scan(&p.ID, &p.BookingID, &p.Provider, &p.AmountCents, &p.PhoneE164, &p.Status, &p.MerchantRequestID,
		&p.CheckoutRequestID, &p.MpesaReceipt, &p.ResultCode, &p.ResultDesc, &p.RawCallback, &p.CreatedAt, &p.CompletedAt,
		&p.LastQueriedAt, &p.QueryAttempts)
	return p, err
}

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

// ClaimedPayment is a live payment handed to one reconciliation run.
type ClaimedPayment struct {
	ID                int64
	Status            string
	CheckoutRequestID *string
	// Age is how long ago the payment was created, by the database clock.
	Age time.Duration
	// QueryAttempts counts this claim too.
	QueryAttempts int
}

// Locks: up to $3 live payments older than $1 that no run has looked at for $2, FOR UPDATE
// SKIP LOCKED, for the length of this one statement.
// Why: reconciliation asks M-Pesa about payments whose callback never came. Two workers must
// not both ask about (and then both try to finish) the same payment. The statement stamps
// last_queried_at as it selects, so a claim is a lease: the next run, in this worker or
// another, leaves the payment alone until the lease has passed. SKIP LOCKED means it never
// waits: a payment that a callback is finishing right now is simply not claimed. The lock is
// released when the statement's transaction commits, before any call to M-Pesa is made;
// the finishing function takes the row again, in lock order, and is what decides.
// INITIATED payments are claimed too: one that old was written just before a crash and was
// never sent. Read through payments_live_booking_idx (the live set is small).
const claimPaymentsForReconcile = `
WITH due AS (
    SELECT id FROM payments
    WHERE status IN ('INITIATED', 'PENDING')
      AND created_at <= clock_timestamp() - make_interval(secs => $1::float8)
      AND (last_queried_at IS NULL OR last_queried_at <= clock_timestamp() - make_interval(secs => $2::float8))
    ORDER BY created_at, id
    LIMIT $3
    FOR UPDATE SKIP LOCKED
)
UPDATE payments p
SET last_queried_at = clock_timestamp(), query_attempts = p.query_attempts + 1
FROM due
WHERE p.id = due.id
RETURNING p.id, p.status, p.checkout_request_id,
          extract(epoch FROM clock_timestamp() - p.created_at)::float8, p.query_attempts`

// ClaimPaymentsForReconcile leases a batch of live payments older than minAge to the caller.
func ClaimPaymentsForReconcile(ctx context.Context, db DBTX, minAge, lease time.Duration, limit int) ([]ClaimedPayment, error) {
	rows, err := db.Query(ctx, claimPaymentsForReconcile, minAge.Seconds(), lease.Seconds(), limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ClaimedPayment, error) {
		var p ClaimedPayment
		var age float64
		var attempts int32
		err := row.Scan(&p.ID, &p.Status, &p.CheckoutRequestID, &age, &attempts)
		p.Age, p.QueryAttempts = time.Duration(age*float64(time.Second)), int(attempts)
		return p, err
	})
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
