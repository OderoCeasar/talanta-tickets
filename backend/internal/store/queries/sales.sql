-- name: GetEventSalesTotals :one
-- Money figures of the sales dashboard. Revenue counts PAID bookings only. What is owed back
-- on a REFUND_REQUIRED booking is its refunds still PENDING; a refund that is DONE no longer
-- counts. Only a booking with no refund row at all (none queued yet) counts its whole total.
SELECT
    COALESCE((SELECT sum(b.total_cents) FROM bookings b
               WHERE b.event_id = sqlc.arg(event_id) AND b.status = 'PAID'), 0)::bigint AS revenue_cents,
    (SELECT count(*) FROM bookings b
      WHERE b.event_id = sqlc.arg(event_id) AND b.status = 'REFUND_REQUIRED')::bigint AS refund_required_count,
    COALESCE((SELECT sum(CASE
                  WHEN EXISTS (SELECT 1 FROM refunds r WHERE r.booking_id = b.id)
                  THEN COALESCE((SELECT sum(r.amount_cents) FROM refunds r
                                  WHERE r.booking_id = b.id AND r.status = 'PENDING'), 0)
                  ELSE b.total_cents END)
               FROM bookings b
               WHERE b.event_id = sqlc.arg(event_id) AND b.status = 'REFUND_REQUIRED'), 0)::bigint AS refund_required_cents,
    (SELECT count(*) FROM organiser_alerts a
      WHERE a.event_id = sqlc.arg(event_id) AND a.acknowledged_at IS NULL)::bigint AS open_alert_count;

-- name: SumPaidRevenueByCategory :many
-- The prices actually paid, per category: booking_items carries the price at booking time.
SELECT bi.category_id, COALESCE(sum(bi.unit_price_cents), 0)::bigint AS revenue_cents
FROM bookings b
JOIN booking_items bi ON bi.booking_id = b.id
WHERE b.event_id = $1 AND b.status = 'PAID'
GROUP BY bi.category_id;

-- name: GetFirstPaidAt :one
-- No row when the event has no paid booking.
SELECT paid_at::timestamptz AS first_paid_at
FROM bookings
WHERE event_id = $1 AND status = 'PAID' AND paid_at IS NOT NULL
ORDER BY paid_at
LIMIT 1;

-- name: ListPaidBookingBuckets :many
-- Paid bookings grouped into hour or day buckets. Buckets start on Africa/Nairobi clock
-- boundaries: an organiser's "day" is the Nairobi day, not the UTC one.
SELECT date_trunc(sqlc.arg(bucket)::text, b.paid_at, 'Africa/Nairobi')::timestamptz AS bucket_start,
       count(*)::bigint AS bookings,
       COALESCE(sum(b.total_cents), 0)::bigint AS revenue_cents,
       COALESCE(sum((SELECT count(*) FROM booking_items bi WHERE bi.booking_id = b.id)), 0)::bigint AS tickets
FROM bookings b
WHERE b.event_id = sqlc.arg(event_id) AND b.status = 'PAID'
  AND b.paid_at >= sqlc.arg(from_at)::timestamptz AND b.paid_at < sqlc.arg(to_at)::timestamptz
GROUP BY 1
ORDER BY 1;

-- name: ListEventBookings :many
-- The organiser's booking list: newest first, keyset-paged on (created_at, id).
SELECT sqlc.embed(b), u.name AS user_name, u.phone_e164 AS user_phone
FROM bookings b
JOIN users u ON u.id = b.user_id
WHERE b.event_id = sqlc.arg(event_id)
  AND (sqlc.narg(status)::text IS NULL OR b.status = sqlc.narg(status)::text)
  AND (NOT sqlc.arg(has_cursor)::boolean
       OR (b.created_at, b.id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::bigint))
ORDER BY b.created_at DESC, b.id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListSeatLabelsForBookings :many
SELECT bi.booking_id, bl.code AS block_code, s.label AS seat_label
FROM booking_items bi
JOIN event_seats es ON es.id = bi.event_seat_id
JOIN seats s ON s.id = es.seat_id
JOIN blocks bl ON bl.id = s.block_id
WHERE bi.booking_id = ANY(sqlc.arg(booking_ids)::bigint[])
ORDER BY bi.booking_id, bl.code, s.row_id, s.seat_number;

-- name: ListLatestPaymentsForBookings :many
SELECT DISTINCT ON (p.booking_id) p.*
FROM payments p
WHERE p.booking_id = ANY(sqlc.arg(booking_ids)::bigint[])
ORDER BY p.booking_id, p.created_at DESC, p.id DESC;

-- name: ListLatestRefundsForBookings :many
SELECT DISTINCT ON (r.booking_id) sqlc.embed(r), p.phone_e164 AS payment_phone, p.mpesa_receipt
FROM refunds r
JOIN payments p ON p.id = r.payment_id
WHERE r.booking_id = ANY(sqlc.arg(booking_ids)::bigint[])
ORDER BY r.booking_id, r.created_at DESC, r.id DESC;
