-- Bookings: created from a hold, read by their owner. The statements that take explicit row
-- locks are in internal/store/payments.go, hand-written, each with a comment saying what it
-- locks and why.

-- name: GetBookingByIdempotencyKey :one
-- The booking a (user, key) pair already made, if any. UNIQUE (user_id, idempotency_key).
SELECT * FROM bookings WHERE user_id = sqlc.arg(user_id) AND idempotency_key = sqlc.arg(idempotency_key);

-- name: GetLiveBookingOfHold :one
-- The booking still attached to a hold: awaiting payment or paid. bookings_live_hold_key
-- allows at most one. The caller holds the hold's row lock.
SELECT * FROM bookings
WHERE hold_id = sqlc.arg(hold_id)::bigint AND status IN ('PENDING_PAYMENT', 'PAID');

-- name: SumHeldSeatsOfHold :one
-- How many seats a hold still has and what they cost at their categories' prices NOW. The
-- caller holds the event shared (so no price can change under it) and the hold's row lock.
SELECT count(*)::int AS seats, COALESCE(sum(c.price_cents), 0)::bigint AS subtotal_cents
FROM event_seats es
JOIN event_categories c ON c.id = es.category_id
WHERE es.hold_id = sqlc.arg(hold_id)::bigint AND es.status = 'HELD';

-- name: InsertBooking :one
-- web_token is not set here: the column default makes it (32 bytes from pgcrypto's CSPRNG).
INSERT INTO bookings (reference, event_id, user_id, hold_id, status, subtotal_cents, fee_cents, total_cents, idempotency_key)
VALUES (sqlc.arg(reference), sqlc.arg(event_id), sqlc.arg(user_id), sqlc.arg(hold_id)::bigint, 'PENDING_PAYMENT',
        sqlc.arg(subtotal_cents), sqlc.arg(fee_cents), sqlc.arg(total_cents), sqlc.arg(idempotency_key))
RETURNING *;

-- name: InsertBookingItemsFromHold :execrows
-- One item per seat the hold still has, with the price copied from the seat's category as it
-- is at this moment. After this nothing reads a booking's prices from event_categories.
INSERT INTO booking_items (booking_id, event_seat_id, category_id, unit_price_cents)
SELECT sqlc.arg(booking_id)::bigint, es.id, c.id, c.price_cents
FROM event_seats es
JOIN event_categories c ON c.id = es.category_id
WHERE es.hold_id = sqlc.arg(hold_id)::bigint AND es.status = 'HELD';

-- name: CountBookingItems :one
SELECT count(*)::int AS items FROM booking_items WHERE booking_id = $1;

-- name: GetBookingByID :one
SELECT * FROM bookings WHERE id = $1;

-- name: GetBookingViewByID :one
-- One booking with its event and the hold it came from. No owner check: for a caller that
-- has just created it or already knows whose it is.
SELECT sqlc.embed(b), sqlc.embed(e), v.name AS venue_name,
       h.expires_at AS hold_expires_at,
       clock_timestamp()::timestamptz AS now_at
FROM bookings b
JOIN events e ON e.id = b.event_id
JOIN venues v ON v.id = e.venue_id
LEFT JOIN holds h ON h.id = b.hold_id
WHERE b.id = sqlc.arg(id);

-- name: GetBookingViewForUser :one
-- One booking by its reference, only if it belongs to the user: someone else's is "not found".
SELECT sqlc.embed(b), sqlc.embed(e), v.name AS venue_name,
       h.expires_at AS hold_expires_at,
       clock_timestamp()::timestamptz AS now_at
FROM bookings b
JOIN events e ON e.id = b.event_id
JOIN venues v ON v.id = e.venue_id
LEFT JOIN holds h ON h.id = b.hold_id
WHERE b.reference = sqlc.arg(reference) AND b.user_id = sqlc.arg(user_id);

-- name: ListBookingViewsOfUser :many
-- One page of a fan's bookings, newest first, keyset-paged on (created_at, id).
SELECT sqlc.embed(b), sqlc.embed(e), v.name AS venue_name,
       h.expires_at AS hold_expires_at,
       clock_timestamp()::timestamptz AS now_at
FROM bookings b
JOIN events e ON e.id = b.event_id
JOIN venues v ON v.id = e.venue_id
LEFT JOIN holds h ON h.id = b.hold_id
WHERE b.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(status)::text IS NULL OR b.status = sqlc.narg(status)::text)
  AND (NOT sqlc.arg(has_cursor)::boolean
       OR (b.created_at, b.id) < (sqlc.arg(cursor_created_at)::timestamptz, sqlc.arg(cursor_id)::bigint))
ORDER BY b.created_at DESC, b.id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListBookingItemViews :many
-- The items of the given bookings with each seat in words, by block, row and seat number.
-- The price and the category are the ones copied onto the item, never the category's now.
SELECT bi.booking_id, bi.event_seat_id, bi.category_id, bi.unit_price_cents, c.name AS category_name,
       st.id AS stand_id, st.code AS stand_code, st.name AS stand_name, st.tier,
       bl.id AS block_id, bl.code AS block_code, bl.label AS block_label,
       r.label AS row_label, s.seat_number, s.label AS seat_label, s.seat_type
FROM booking_items bi
JOIN event_seats es ON es.id = bi.event_seat_id
JOIN seats s ON s.id = es.seat_id
JOIN rows r ON r.id = s.row_id
JOIN blocks bl ON bl.id = es.block_id
JOIN stands st ON st.id = bl.stand_id
JOIN event_categories c ON c.id = bi.category_id
WHERE bi.booking_id = ANY(sqlc.arg(booking_ids)::bigint[])
ORDER BY bi.booking_id, bl.id, r.row_index, s.seat_number;

-- name: SetBookingStatus :execrows
-- Moves one booking, locked by the caller, from the status the caller read under that lock
-- (and asked domain.TransitionBooking about) to the next. paid_at is stamped by the wall
-- clock when the booking becomes PAID.
UPDATE bookings
SET status = sqlc.arg(status),
    paid_at = CASE WHEN sqlc.arg(status) = 'PAID' THEN clock_timestamp() ELSE paid_at END
WHERE id = sqlc.arg(id) AND status = sqlc.arg(from_status);
