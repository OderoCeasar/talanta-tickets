-- Payments, refunds and organiser alerts. The statements that take explicit row locks are in
-- internal/store/payments.go, hand-written, each with a comment saying what it locks and why.

-- name: InsertPayment :one
-- A new payment, INITIATED: the row exists before M-Pesa is asked, so a crash between the
-- two leaves something reconciliation can find. payments_live_booking_idx refuses a second
-- live payment for the booking (SQLSTATE 23505).
INSERT INTO payments (booking_id, provider, amount_cents, phone_e164, status)
VALUES (sqlc.arg(booking_id), 'MPESA', sqlc.arg(amount_cents), sqlc.arg(phone_e164), 'INITIATED')
RETURNING *;

-- name: GetLivePaymentOfBooking :one
SELECT * FROM payments
WHERE booking_id = sqlc.arg(booking_id) AND status IN ('INITIATED', 'PENDING')
ORDER BY id DESC
LIMIT 1;

-- name: GetPaymentByID :one
SELECT * FROM payments WHERE id = $1;

-- name: GetPaymentForUser :one
-- One payment with its booking, only if the booking belongs to the user.
SELECT sqlc.embed(p), sqlc.embed(b)
FROM payments p
JOIN bookings b ON b.id = p.booking_id
WHERE p.id = sqlc.arg(id) AND b.user_id = sqlc.arg(user_id);

-- name: GetPaymentScope :one
-- What a transaction needs to know BEFORE it locks anything for a payment: which event,
-- hold and booking to lock, in that order. None of these columns ever changes after insert,
-- so reading them without a lock is safe.
SELECT p.id AS payment_id, p.booking_id, b.event_id, b.hold_id, b.user_id
FROM payments p
JOIN bookings b ON b.id = p.booking_id
WHERE p.id = $1;

-- name: GetPaymentIDByCheckoutRequestID :one
-- The callback's only key. payments.checkout_request_id is unique.
SELECT id FROM payments WHERE checkout_request_id = sqlc.arg(checkout_request_id)::text;

-- name: SetPaymentPending :execrows
-- M-Pesa took the request: the payment now waits for the fan's PIN. The caller holds the
-- row lock and has asked domain.TransitionPayment with the status it read.
UPDATE payments
SET status = 'PENDING', checkout_request_id = sqlc.arg(checkout_request_id)::text,
    merchant_request_id = sqlc.narg(merchant_request_id)
WHERE id = sqlc.arg(id) AND status = sqlc.arg(from_status);

-- name: FinishPayment :execrows
-- Writes a payment's final state. The caller holds the row lock and has asked
-- domain.TransitionPayment with the status it read; from_status repeats it. The receipt and
-- the raw callback are kept when this call has none (a query result carries neither).
UPDATE payments
SET status = sqlc.arg(status),
    result_code = sqlc.narg(result_code),
    result_desc = sqlc.narg(result_desc),
    mpesa_receipt = COALESCE(sqlc.narg(mpesa_receipt), mpesa_receipt),
    raw_callback = COALESCE(sqlc.narg(raw_callback)::jsonb, raw_callback),
    completed_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND status = sqlc.arg(from_status);

-- name: StoreRawCallback :execrows
-- Keeps a callback body that changed nothing (a repeat, or one that could not be verified)
-- when the payment has none stored yet, for dispute resolution.
UPDATE payments SET raw_callback = sqlc.arg(raw_callback)::jsonb
WHERE id = sqlc.arg(id) AND raw_callback IS NULL;

-- name: ReceiptBelongsToAnotherPayment :one
-- One M-Pesa receipt settles one payment (payments_mpesa_receipt_key). A callback that
-- carries a receipt already recorded on a different payment is a replay.
SELECT EXISTS (
    SELECT 1 FROM payments WHERE mpesa_receipt = sqlc.arg(mpesa_receipt)::text AND id <> sqlc.arg(id)
) AS used;

-- name: QueueRefund :execrows
-- Money that has to go back. refunds.payment_id is unique, so a payment is queued once
-- however many times this runs (event cancel relies on the same key).
INSERT INTO refunds (booking_id, payment_id, amount_cents, reason)
VALUES (sqlc.arg(booking_id), sqlc.arg(payment_id), sqlc.arg(amount_cents), sqlc.arg(reason))
ON CONFLICT (payment_id) DO NOTHING;

-- name: InsertAlert :one
INSERT INTO organiser_alerts (event_id, booking_id, kind, message)
VALUES (sqlc.arg(event_id), sqlc.narg(booking_id), sqlc.arg(kind), sqlc.arg(message))
RETURNING *;

-- name: ListAlertViews :many
-- The organiser's alerts, newest first, keyset-paged on (created_at, id).
SELECT sqlc.embed(a), b.reference AS booking_reference
FROM organiser_alerts a
LEFT JOIN bookings b ON b.id = a.booking_id
WHERE (sqlc.narg(event_id)::bigint IS NULL OR a.event_id = sqlc.narg(event_id)::bigint)
  AND (sqlc.narg(acknowledged)::boolean IS NULL
       OR (a.acknowledged_at IS NOT NULL) = sqlc.narg(acknowledged)::boolean)
  AND (NOT sqlc.arg(has_cursor)::boolean
       OR (a.created_at, a.id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::bigint))
ORDER BY a.created_at DESC, a.id DESC
LIMIT sqlc.arg(page_limit);

-- name: AckAlert :execrows
-- Safe to repeat: the first acknowledgement time is kept.
UPDATE organiser_alerts SET acknowledged_at = COALESCE(acknowledged_at, clock_timestamp()) WHERE id = $1;

-- name: GetAlertView :one
SELECT sqlc.embed(a), b.reference AS booking_reference
FROM organiser_alerts a
LEFT JOIN bookings b ON b.id = a.booking_id
WHERE a.id = $1;

-- name: ListRefundViews :many
-- The refund queue, oldest first (the longest-waiting fan on top), keyset-paged on
-- (created_at, id).
SELECT sqlc.embed(r), b.reference AS booking_reference, b.event_id,
       p.phone_e164 AS payment_phone, p.mpesa_receipt
FROM refunds r
JOIN bookings b ON b.id = r.booking_id
JOIN payments p ON p.id = r.payment_id
WHERE (sqlc.narg(status)::text IS NULL OR r.status = sqlc.narg(status)::text)
  AND (sqlc.narg(event_id)::bigint IS NULL OR b.event_id = sqlc.narg(event_id)::bigint)
  AND (NOT sqlc.arg(has_cursor)::boolean
       OR (r.created_at, r.id) > (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::bigint))
ORDER BY r.created_at, r.id
LIMIT sqlc.arg(page_limit);

-- name: GetRefundView :one
SELECT sqlc.embed(r), b.reference AS booking_reference, b.event_id,
       p.phone_e164 AS payment_phone, p.mpesa_receipt
FROM refunds r
JOIN bookings b ON b.id = r.booking_id
JOIN payments p ON p.id = r.payment_id
WHERE r.id = $1;

-- name: GetRefundScope :one
-- Which event, hold and booking to lock, in that order, before a refund row is locked.
-- These columns never change after insert.
SELECT r.id AS refund_id, r.booking_id, b.event_id, b.hold_id
FROM refunds r
JOIN bookings b ON b.id = r.booking_id
WHERE r.id = $1;

-- name: CompleteRefund :execrows
-- The caller holds the refund's row lock and has read it PENDING.
UPDATE refunds SET status = 'DONE', completed_at = clock_timestamp() WHERE id = $1 AND status = 'PENDING';

-- name: CountPendingRefundsOfBooking :one
SELECT count(*)::int AS pending FROM refunds WHERE booking_id = $1 AND status = 'PENDING';

-- name: SetPaymentProviderIDs :execrows
-- Records the provider's IDs on a payment that is no longer INITIATED, without touching its
-- status, so that a result arriving for those IDs can still be matched to it.
UPDATE payments
SET checkout_request_id = sqlc.arg(checkout_request_id)::text, merchant_request_id = sqlc.narg(merchant_request_id)
WHERE id = sqlc.arg(id) AND checkout_request_id IS NULL;
