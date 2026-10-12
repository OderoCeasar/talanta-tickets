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
    merchant_request_id = sqlc.narg(merchant_request_id), dispatch = 'ACCEPTED'
WHERE id = sqlc.arg(id) AND status = sqlc.arg(from_status);

-- name: SetPaymentPendingUnknownID :execrows
-- The STK push may have reached M-Pesa but its answer was lost: the payment stays live,
-- PENDING, with no provider ID, marked AMBIGUOUS. While it is live no second payment can start
-- for the booking, so the fan cannot pay twice inside the window. The caller holds the row
-- lock and has asked domain.TransitionPayment with the status it read.
UPDATE payments
SET status = 'PENDING', dispatch = 'AMBIGUOUS', result_desc = sqlc.arg(result_desc)::text
WHERE id = sqlc.arg(id) AND status = sqlc.arg(from_status) AND checkout_request_id IS NULL;

-- name: SetPaymentNotSent :execrows
-- Records that the STK push provably never left us, so the payment may be closed without an
-- alert even if marking it FAILED does not go through.
UPDATE payments SET dispatch = 'NOT_SENT' WHERE id = sqlc.arg(id) AND checkout_request_id IS NULL;

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

-- name: SetPaymentResult :execrows
-- Rewrites the result of a payment that keeps its status: a FAILED payment that M-Pesa later
-- confirms was paid, for an amount that is not the booking's, stays FAILED but now carries
-- the success code and receipt its refund is read with. The caller holds the row lock.
UPDATE payments
SET result_code = sqlc.narg(result_code),
    result_desc = sqlc.narg(result_desc),
    mpesa_receipt = COALESCE(sqlc.narg(mpesa_receipt), mpesa_receipt),
    raw_callback = COALESCE(sqlc.narg(raw_callback)::jsonb, raw_callback)
WHERE id = sqlc.arg(id) AND status = sqlc.arg(status);

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
-- payment_id ties the alert to the payment it is about, so the system can close it when that
-- payment resolves (ResolveAlertsOfPayment).
INSERT INTO organiser_alerts (event_id, booking_id, payment_id, kind, message)
VALUES (sqlc.arg(event_id), sqlc.narg(booking_id), sqlc.narg(payment_id), sqlc.arg(kind), sqlc.arg(message))
RETURNING *;

-- name: ResolveAlertsOfPayment :execrows
-- Closes the open alerts of the given kinds about one payment, with a note saying the system
-- did it and why. An alert a person already acknowledged is left as it is.
UPDATE organiser_alerts
SET acknowledged_at = clock_timestamp(), resolution = sqlc.arg(resolution)::text
WHERE payment_id = sqlc.arg(payment_id)::bigint
  AND kind = ANY(sqlc.arg(kinds)::text[])
  AND acknowledged_at IS NULL;

-- name: SetAlertPayment :execrows
-- Ties an alert raised for an unmatched callback to the payment the callback turned out to
-- be for.
UPDATE organiser_alerts SET payment_id = sqlc.arg(payment_id)::bigint, booking_id = COALESCE(booking_id, sqlc.arg(booking_id)::bigint)
WHERE id = sqlc.arg(id) AND payment_id IS NULL;

-- name: ListAlertViews :many
-- The organiser's alerts, newest first, keyset-paged on (created_at, id).
SELECT sqlc.embed(a), b.reference AS booking_reference, e.title AS event_title
FROM organiser_alerts a
JOIN events e ON e.id = a.event_id
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
SELECT sqlc.embed(a), b.reference AS booking_reference, e.title AS event_title
FROM organiser_alerts a
JOIN events e ON e.id = a.event_id
LEFT JOIN bookings b ON b.id = a.booking_id
WHERE a.id = $1;

-- name: ListRefundViews :many
-- The refund queue, oldest first (the longest-waiting fan on top), keyset-paged on
-- (created_at, id).
SELECT sqlc.embed(r), b.reference AS booking_reference, b.event_id, b.status AS booking_status,
       p.phone_e164 AS payment_phone, p.mpesa_receipt
FROM refunds r
JOIN bookings b ON b.id = r.booking_id
JOIN payments p ON p.id = r.payment_id
WHERE (sqlc.narg(status)::text IS NULL OR r.status = sqlc.narg(status)::text)
  AND (sqlc.narg(event_id)::bigint IS NULL OR b.event_id = sqlc.narg(event_id)::bigint)
  AND (sqlc.narg(booking_reference)::text IS NULL OR b.reference = sqlc.narg(booking_reference)::text)
  AND (NOT sqlc.arg(has_cursor)::boolean
       OR (r.created_at, r.id) > (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::bigint))
ORDER BY r.created_at, r.id
LIMIT sqlc.arg(page_limit);

-- name: GetRefundView :one
SELECT sqlc.embed(r), b.reference AS booking_reference, b.event_id, b.status AS booking_status,
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
SET checkout_request_id = sqlc.arg(checkout_request_id)::text,
    merchant_request_id = COALESCE(sqlc.narg(merchant_request_id), merchant_request_id), dispatch = 'ACCEPTED'
WHERE id = sqlc.arg(id) AND checkout_request_id IS NULL;

-- name: UpsertPaymentCallback :one
-- The callback inbox: written by the webhook before it answers. One row per (payment,
-- result). A repeat of a result that was not applied replaces the stored body and makes the
-- row due again; a repeat of one that was applied changes nothing.
INSERT INTO payment_callbacks (payment_id, checkout_request_id, merchant_request_id, result_code, result_desc,
                               amount_cents, mpesa_receipt, phone_e164, raw)
VALUES (sqlc.arg(payment_id), sqlc.arg(checkout_request_id), sqlc.narg(merchant_request_id), sqlc.arg(result_code),
        sqlc.narg(result_desc), sqlc.narg(amount_cents), sqlc.narg(mpesa_receipt), sqlc.narg(phone_e164), sqlc.arg(raw)::jsonb)
ON CONFLICT ON CONSTRAINT payment_callbacks_payment_result_key DO UPDATE
SET result_desc = CASE WHEN payment_callbacks.verdict = 'APPLIED' THEN payment_callbacks.result_desc ELSE EXCLUDED.result_desc END,
    amount_cents = CASE WHEN payment_callbacks.verdict = 'APPLIED' THEN payment_callbacks.amount_cents ELSE EXCLUDED.amount_cents END,
    mpesa_receipt = CASE WHEN payment_callbacks.verdict = 'APPLIED' THEN payment_callbacks.mpesa_receipt ELSE EXCLUDED.mpesa_receipt END,
    phone_e164 = CASE WHEN payment_callbacks.verdict = 'APPLIED' THEN payment_callbacks.phone_e164 ELSE EXCLUDED.phone_e164 END,
    raw = CASE WHEN payment_callbacks.verdict = 'APPLIED' THEN payment_callbacks.raw ELSE EXCLUDED.raw END,
    next_attempt_at = CASE WHEN payment_callbacks.verdict = 'APPLIED' THEN payment_callbacks.next_attempt_at
                           ELSE LEAST(payment_callbacks.next_attempt_at, clock_timestamp()) END,
    processed_at = CASE WHEN payment_callbacks.verdict = 'APPLIED' THEN payment_callbacks.processed_at END,
    verdict = CASE WHEN payment_callbacks.verdict = 'APPLIED' THEN payment_callbacks.verdict END
RETURNING id;

-- name: FinishPaymentCallback :execrows
-- The inbox row has been dealt with; verdict says how.
UPDATE payment_callbacks
SET processed_at = clock_timestamp(), verdict = sqlc.arg(verdict)::text, last_error = sqlc.narg(last_error)
WHERE id = sqlc.arg(id) AND processed_at IS NULL;

-- name: RetryPaymentCallback :execrows
-- The inbox row could not be dealt with now (the event was busy, the database or M-Pesa did
-- not answer): it becomes due again after delay_seconds, by the database clock.
UPDATE payment_callbacks
SET next_attempt_at = clock_timestamp() + make_interval(secs => sqlc.arg(delay_seconds)::float8),
    last_error = sqlc.narg(last_error)
WHERE id = sqlc.arg(id) AND processed_at IS NULL;

-- name: UpsertUnmatchedCallback :one
-- A "paid" callback whose CheckoutRequestID no payment has. Kept once per ID.
INSERT INTO unmatched_callbacks (checkout_request_id, merchant_request_id, result_code, result_desc,
                                 amount_cents, mpesa_receipt, phone_e164, raw)
VALUES (sqlc.arg(checkout_request_id), sqlc.narg(merchant_request_id), sqlc.arg(result_code),
        sqlc.narg(result_desc), sqlc.narg(amount_cents), sqlc.narg(mpesa_receipt), sqlc.narg(phone_e164), sqlc.arg(raw)::jsonb)
ON CONFLICT ON CONSTRAINT unmatched_callbacks_checkout_request_id_key DO UPDATE
SET attempts = unmatched_callbacks.attempts
RETURNING id;

-- name: FinishUnmatchedCallback :execrows
UPDATE unmatched_callbacks
SET processed_at = clock_timestamp(), verdict = sqlc.arg(verdict)::text,
    matched_payment_id = sqlc.narg(matched_payment_id), last_error = sqlc.narg(last_error)
WHERE id = sqlc.arg(id) AND processed_at IS NULL;

-- name: RetryUnmatchedCallback :execrows
UPDATE unmatched_callbacks
SET next_attempt_at = clock_timestamp() + make_interval(secs => sqlc.arg(delay_seconds)::float8),
    last_error = sqlc.narg(last_error)
WHERE id = sqlc.arg(id) AND processed_at IS NULL;

-- name: SetUnmatchedCallbackAlert :execrows
-- Remembers the alert raised for an unmatched callback, so it is raised once.
UPDATE unmatched_callbacks SET alert_id = sqlc.arg(alert_id)::bigint WHERE id = sqlc.arg(id) AND alert_id IS NULL;

-- name: ListIDLessPaymentCandidates :many
-- Payments an unknown-ID "paid" callback could belong to: no provider ID on the row, not
-- provably unsent, not already settled, the same phone and the same amount, and started no
-- more than window_seconds before the callback arrived. The caller acts only on exactly one.
SELECT p.id AS payment_id, p.booking_id, b.event_id
FROM payments p
JOIN bookings b ON b.id = p.booking_id
WHERE p.checkout_request_id IS NULL
  AND p.dispatch IN ('UNKNOWN', 'AMBIGUOUS')
  AND p.status IN ('INITIATED', 'PENDING', 'TIMEOUT')
  AND p.phone_e164 = sqlc.arg(phone_e164)
  AND p.amount_cents = sqlc.arg(amount_cents)
  AND p.created_at <= sqlc.arg(received_at)::timestamptz
  AND p.created_at >= sqlc.arg(received_at)::timestamptz - make_interval(secs => sqlc.arg(window_seconds)::float8)
ORDER BY p.created_at DESC, p.id DESC
LIMIT 2;

-- name: GuessEventForUnmatchedCallback :one
-- organiser_alerts needs an event. An unmatched callback has none, so its alert is filed
-- under the event of the newest payment from the same phone, or failing that the newest
-- event. No row when there is no event at all.
SELECT x.event_id::bigint AS event_id FROM (
    SELECT b.event_id, 1 AS priority, p.created_at AS at
    FROM payments p JOIN bookings b ON b.id = p.booking_id
    WHERE p.phone_e164 = sqlc.arg(phone_e164)::text
    UNION ALL
    SELECT e.id, 2, e.created_at FROM events e
) x
ORDER BY x.priority, x.at DESC
LIMIT 1;
