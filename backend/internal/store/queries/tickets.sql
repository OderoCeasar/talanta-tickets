-- Tickets: issue, void and the reads behind the ticket endpoints and the web ticket page.
-- The two statements that take explicit row locks are in internal/store/tickets.go.

-- name: GetBookingEventID :one
-- A booking's event never changes, so this plain read is safe to use for deciding which
-- event row to lock before the booking row itself (lock order: event first).
SELECT event_id FROM bookings WHERE id = $1;

-- name: ListBookingSeatsForIssue :many
-- Every item of a booking with what issuing its ticket needs and what the precondition
-- checks: the seat's status, the hold that sold it, its event, and the stand that gives the
-- gate group. The caller holds the booking's row lock.
SELECT bi.event_seat_id, es.event_id, es.status, es.hold_id,
       st.code AS stand_code, st.tier
FROM booking_items bi
JOIN event_seats es ON es.id = bi.event_seat_id
JOIN blocks bl ON bl.id = es.block_id
JOIN stands st ON st.id = bl.stand_id
WHERE bi.booking_id = $1
ORDER BY bi.event_seat_id;

-- name: InsertTickets :execrows
-- One ticket per array position. IDs, nonces and signed tokens are made by the caller (the
-- token signs the ID, so the ID cannot be left to the column default).
-- tickets_live_seat_idx refuses a second live ticket for a seat (SQLSTATE 23505).
INSERT INTO tickets (id, booking_id, event_seat_id, event_id, gate_group, key_id, token_nonce, qr_token, holder_name, holder_phone)
-- Set-returning functions side by side in one select list step together (Postgres 10 and
-- later), so row n takes element n of every array. The caller passes arrays of one length.
SELECT unnest(sqlc.arg(ids)::uuid[]), sqlc.arg(booking_id)::bigint, unnest(sqlc.arg(event_seat_ids)::bigint[]),
       sqlc.arg(event_id)::bigint, unnest(sqlc.arg(gate_groups)::text[]), sqlc.arg(key_id)::text,
       unnest(sqlc.arg(token_nonces)::bytea[]), unnest(sqlc.arg(qr_tokens)::text[]),
       sqlc.narg(holder_name)::text, sqlc.narg(holder_phone)::text;

-- name: VoidTicketsOfBooking :execrows
-- Refund: every ticket of the booking that may become VOID does. from_statuses comes from
-- domain.TicketStatusesThatCanBecome(VOID), so the rule lives in one place.
--
-- Locks: the booking's tickets that will change, FOR UPDATE, in id order, before any is
-- written. Why: the gate's scan upload locks the tickets of a batch in id order too
-- (store/gate.go, lockTicketsForScan). A bare UPDATE takes its rows in whatever order the
-- scan finds them, so a refund of two tickets and an upload holding the same two could each
-- wait for the other.
UPDATE tickets SET status = 'VOID'
WHERE id IN (
    SELECT t.id FROM tickets t
    WHERE t.booking_id = sqlc.arg(booking_id) AND t.status = ANY(sqlc.arg(from_statuses)::text[])
    ORDER BY t.id
    FOR UPDATE
);

-- name: ListTicketViewsOfUser :many
-- One page of a fan's tickets: soonest kickoff first, then seat (inventory id), then ticket
-- id so a seat with a void ticket and its replacement has a fixed order. Keyset paging on
-- exactly those four columns. Ownership is the booking's user.
SELECT sqlc.embed(t), sqlc.embed(e), b.reference AS booking_reference, b.user_id, v.name AS venue_name,
       st.id AS stand_id, st.code AS stand_code, st.name AS stand_name, st.tier,
       bl.id AS block_id, bl.code AS block_code, bl.label AS block_label,
       r.label AS row_label, s.seat_number, s.label AS seat_label, s.seat_type,
       COALESCE(c.name, '')::text AS category_name,
       clock_timestamp()::timestamptz AS now_at
FROM tickets t
JOIN bookings b ON b.id = t.booking_id
JOIN events e ON e.id = t.event_id
JOIN venues v ON v.id = e.venue_id
JOIN event_seats es ON es.id = t.event_seat_id
JOIN seats s ON s.id = es.seat_id
JOIN rows r ON r.id = s.row_id
JOIN blocks bl ON bl.id = es.block_id
JOIN stands st ON st.id = bl.stand_id
LEFT JOIN booking_items bi ON bi.booking_id = t.booking_id AND bi.event_seat_id = t.event_seat_id
LEFT JOIN event_categories c ON c.id = bi.category_id
WHERE b.user_id = sqlc.arg(user_id)
  AND (sqlc.narg(event_id)::bigint IS NULL OR t.event_id = sqlc.narg(event_id)::bigint)
  AND (sqlc.narg(status)::text IS NULL OR t.status = sqlc.narg(status)::text)
  AND (NOT sqlc.arg(has_cursor)::bool
       OR (e.kickoff_at, t.event_id, t.event_seat_id, t.id)
          > (sqlc.arg(after_kickoff_at)::timestamptz, sqlc.arg(after_event_id)::bigint,
             sqlc.arg(after_event_seat_id)::bigint, sqlc.arg(after_ticket_id)::uuid))
ORDER BY e.kickoff_at, t.event_id, t.event_seat_id, t.id
LIMIT sqlc.arg(row_limit);

-- name: GetTicketViewForUser :one
-- One ticket, only if it belongs to the user: someone else's ticket is "not found".
SELECT sqlc.embed(t), sqlc.embed(e), b.reference AS booking_reference, b.user_id, v.name AS venue_name,
       st.id AS stand_id, st.code AS stand_code, st.name AS stand_name, st.tier,
       bl.id AS block_id, bl.code AS block_code, bl.label AS block_label,
       r.label AS row_label, s.seat_number, s.label AS seat_label, s.seat_type,
       COALESCE(c.name, '')::text AS category_name,
       clock_timestamp()::timestamptz AS now_at
FROM tickets t
JOIN bookings b ON b.id = t.booking_id
JOIN events e ON e.id = t.event_id
JOIN venues v ON v.id = e.venue_id
JOIN event_seats es ON es.id = t.event_seat_id
JOIN seats s ON s.id = es.seat_id
JOIN rows r ON r.id = s.row_id
JOIN blocks bl ON bl.id = es.block_id
JOIN stands st ON st.id = bl.stand_id
LEFT JOIN booking_items bi ON bi.booking_id = t.booking_id AND bi.event_seat_id = t.event_seat_id
LEFT JOIN event_categories c ON c.id = bi.category_id
WHERE t.id = sqlc.arg(id) AND b.user_id = sqlc.arg(user_id);

-- name: ListTicketViewsOfBookings :many
-- Every ticket of the given bookings, in any status, by booking then seat. No owner check:
-- the caller has already decided these bookings may be shown (the owner's booking answer,
-- the web ticket page, the answer of an issue).
SELECT sqlc.embed(t), sqlc.embed(e), b.reference AS booking_reference, b.user_id, v.name AS venue_name,
       st.id AS stand_id, st.code AS stand_code, st.name AS stand_name, st.tier,
       bl.id AS block_id, bl.code AS block_code, bl.label AS block_label,
       r.label AS row_label, s.seat_number, s.label AS seat_label, s.seat_type,
       COALESCE(c.name, '')::text AS category_name,
       clock_timestamp()::timestamptz AS now_at
FROM tickets t
JOIN bookings b ON b.id = t.booking_id
JOIN events e ON e.id = t.event_id
JOIN venues v ON v.id = e.venue_id
JOIN event_seats es ON es.id = t.event_seat_id
JOIN seats s ON s.id = es.seat_id
JOIN rows r ON r.id = s.row_id
JOIN blocks bl ON bl.id = es.block_id
JOIN stands st ON st.id = bl.stand_id
LEFT JOIN booking_items bi ON bi.booking_id = t.booking_id AND bi.event_seat_id = t.event_seat_id
LEFT JOIN event_categories c ON c.id = bi.category_id
WHERE t.booking_id = ANY(sqlc.arg(booking_ids)::bigint[])
ORDER BY t.booking_id, t.event_seat_id, t.issued_at, t.id;

-- name: GetBookingByWebToken :one
-- The web ticket page. web_token is 32 random bytes from the database's CSPRNG
-- (gen_random_bytes, migration 000005), unique, and unrelated to the reference.
SELECT * FROM bookings WHERE web_token = $1;

-- name: GetBookingForTicketSMS :one
-- What the "you're going" SMS needs: where to send it, the reference, the link's token and
-- how many tickets are live.
SELECT b.id, b.reference, b.status, b.web_token, u.phone_e164,
       (SELECT count(*) FROM tickets t WHERE t.booking_id = b.id AND t.status IN ('VALID', 'USED'))::int AS live_tickets
FROM bookings b
JOIN users u ON u.id = b.user_id
WHERE b.id = $1;
