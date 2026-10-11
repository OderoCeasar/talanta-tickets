-- Plain reads and single-row writes of the hold flow. Every statement that LOCKS rows is in
-- internal/store/holds.go, hand-written, each with a comment saying what it locks and why.

-- name: InsertHold :one
-- The expiry is the database's wall clock plus the hold's life, not now(): now() is the
-- transaction's start, and a request that waited for a lock would get a shorter hold.
INSERT INTO holds (event_id, user_id, expires_at)
VALUES (sqlc.arg(event_id), sqlc.arg(user_id), clock_timestamp() + make_interval(secs => sqlc.arg(ttl_seconds)::float8))
RETURNING *;

-- name: CountUserSeatsForEvent :one
-- How many seats of one event a user holds or has bought, for max_tickets_per_user. The
-- caller holds the (user, event) advisory lock, so no other hold of the same user for the
-- same event can be between its count and its insert.
-- Three sets, joined with UNION so a seat that is in two of them is counted once:
--   - seats HELD by the user's ACTIVE, unexpired holds;
--   - seats of the user's PAID bookings;
--   - seats of a PENDING_PAYMENT booking with a payment still live. Its hold is normally
--     still active (same seats, counted once); if the hold ran out while M-Pesa is still
--     deciding, the fan may yet end up owning those seats, so they keep counting.
SELECT count(*)::int AS seats FROM (
    SELECT es.id
    FROM holds h
    JOIN event_seats es ON es.hold_id = h.id AND es.status = 'HELD'
    WHERE h.user_id = sqlc.arg(user_id) AND h.event_id = sqlc.arg(event_id)
      AND h.status = 'ACTIVE' AND h.expires_at > clock_timestamp()
    UNION
    SELECT bi.event_seat_id
    FROM bookings b
    JOIN booking_items bi ON bi.booking_id = b.id
    WHERE b.user_id = sqlc.arg(user_id) AND b.event_id = sqlc.arg(event_id)
      AND (b.status = 'PAID'
           OR (b.status = 'PENDING_PAYMENT' AND EXISTS (
                   SELECT 1 FROM payments p WHERE p.booking_id = b.id AND p.status IN ('INITIATED', 'PENDING'))))
) counted;

-- name: GetHoldForUser :one
-- One hold of one user, with the database clock it is judged against and the booking made
-- from it (the live one if there is one, else the newest).
SELECT sqlc.embed(h),
       clock_timestamp()::timestamptz AS now_at,
       -- '' when the hold has no booking.
       COALESCE(bk.reference, '')::text AS booking_reference
FROM holds h
LEFT JOIN LATERAL (
    SELECT b.reference FROM bookings b WHERE b.hold_id = h.id
    ORDER BY (b.status IN ('PENDING_PAYMENT', 'PAID')) DESC, b.id DESC LIMIT 1
) bk ON TRUE
WHERE h.id = sqlc.arg(id) AND h.user_id = sqlc.arg(user_id);

-- name: ListActiveHoldsOfUser :many
-- The caller's live holds, newest first. "Live" is judged by the database clock, so a hold
-- the sweeper has not closed yet is already left out.
SELECT sqlc.embed(h),
       clock_timestamp()::timestamptz AS now_at,
       -- '' when the hold has no booking.
       COALESCE(bk.reference, '')::text AS booking_reference
FROM holds h
LEFT JOIN LATERAL (
    SELECT b.reference FROM bookings b WHERE b.hold_id = h.id
    ORDER BY (b.status IN ('PENDING_PAYMENT', 'PAID')) DESC, b.id DESC LIMIT 1
) bk ON TRUE
WHERE h.user_id = sqlc.arg(user_id) AND h.status = 'ACTIVE' AND h.expires_at > clock_timestamp()
  AND (sqlc.narg(event_id)::bigint IS NULL OR h.event_id = sqlc.narg(event_id)::bigint)
ORDER BY h.created_at DESC, h.id DESC;

-- name: ListSeatsOfHolds :many
-- The seats that belong to the given holds now: still HELD by them, or SOLD through them.
-- A seat that was released, swept or taken over no longer points at its old hold, so an
-- ended hold lists no seats.
SELECT es.hold_id::bigint AS hold_id, es.id AS event_seat_id,
       st.id AS stand_id, st.code AS stand_code, st.name AS stand_name, st.tier,
       bl.id AS block_id, bl.code AS block_code, bl.label AS block_label,
       r.label AS row_label, s.seat_number, s.label AS seat_label, s.seat_type,
       c.id AS category_id, c.name AS category_name, c.price_cents
FROM event_seats es
JOIN seats s ON s.id = es.seat_id
JOIN rows r ON r.id = s.row_id
JOIN blocks bl ON bl.id = es.block_id
JOIN stands st ON st.id = bl.stand_id
JOIN event_categories c ON c.id = es.category_id
WHERE es.hold_id = ANY(sqlc.arg(hold_ids)::bigint[]) AND es.status IN ('HELD', 'SOLD')
ORDER BY es.hold_id, bl.id, r.row_index, s.seat_number;

-- name: GetLivePaymentOfHold :one
-- The payment still in flight (INITIATED or PENDING) for the unpaid booking of a hold, if
-- there is one. payments_live_booking_idx allows at most one per booking.
SELECT p.id, p.status
FROM bookings b
JOIN payments p ON p.booking_id = b.id AND p.status IN ('INITIATED', 'PENDING')
WHERE b.hold_id = sqlc.arg(hold_id)::bigint AND b.status = 'PENDING_PAYMENT'
ORDER BY p.id DESC
LIMIT 1;

-- name: SetHoldStatus :execrows
-- Moves one ACTIVE hold to an end state. The caller holds the row lock and has asked
-- domain.TransitionHold; the status condition repeats the rule so a stale caller changes
-- nothing.
UPDATE holds SET status = sqlc.arg(status) WHERE id = sqlc.arg(id) AND status = 'ACTIVE';

-- name: ExtendHold :execrows
-- Correction 4: pushes one ACTIVE, never-extended hold's expiry out and records that it was
-- done. extended_at IS NULL is what makes a second extension impossible. The expiry
-- condition is judged by the clock at the moment of the write: a hold whose time has run out
-- is never revived, whatever an earlier read made of it (zero rows -> HOLD_EXPIRED).
UPDATE holds SET expires_at = sqlc.arg(expires_at), extended_at = clock_timestamp()
WHERE id = sqlc.arg(id) AND status = 'ACTIVE' AND extended_at IS NULL
  AND expires_at > clock_timestamp();

-- name: ListCategoryBlocks :many
-- Best available, step 1: the blocks that have seats of one category, with what ranks a
-- block ("central in its stand"). It reads the event's painting (a row per painted block and
-- per overridden row), not the inventory: publish copied the painting onto event_seats and
-- painting is frozen from then on, so the two always agree, and this costs a few dozen rows
-- instead of a count over tens of thousands of seats.
SELECT bl.id AS block_id, st.tier, st.display_order,
       -- Squared distance of the block from the middle of its stand, in plan units.
       (SELECT (bl.origin_x - avg(o.origin_x)) ^ 2 + (bl.origin_y - avg(o.origin_y)) ^ 2
          FROM blocks o WHERE o.stand_id = bl.stand_id)::float8 AS off_centre
FROM blocks bl
JOIN stands st ON st.id = bl.stand_id
WHERE bl.id IN (
    SELECT ebc.block_id FROM event_block_categories ebc
    WHERE ebc.event_id = sqlc.arg(event_id) AND ebc.category_id = sqlc.arg(category_id)
    UNION
    SELECT r.block_id FROM event_row_categories erc JOIN rows r ON r.id = erc.row_id
    WHERE erc.event_id = sqlc.arg(event_id) AND erc.category_id = sqlc.arg(category_id));

-- name: ListBestAvailableCandidates :many
-- Best available, step 2: the free seats of one category in the given blocks, in row then
-- seat order within each block. A plain read: no locks, so the answer can be stale by the
-- time a run is tried; the hold statement is what decides.
-- "Free" is AVAILABLE, or HELD by a hold that has run out (the hold statement reclaims it).
-- The block conditions on seats and rows are redundant by construction. They are what keeps
-- the plan to the blocks asked for: the planner reads those blocks' seats through
-- seats_block_id_idx and probes event_seats by (event_id, seat_id), or reads event_seats
-- through event_seats_event_block_category_status_idx. Without them it hashes all 60,000
-- seats. Either way the work is bounded by the size of the blocks, never by the event.
SELECT es.id, es.block_id, s.row_id, r.row_index, r.seat_count, s.seat_number, s.seat_type
FROM event_seats es
JOIN seats s ON s.id = es.seat_id
JOIN rows r ON r.id = s.row_id
WHERE es.event_id = sqlc.arg(event_id) AND es.block_id = ANY(sqlc.arg(block_ids)::bigint[])
  AND s.block_id = ANY(sqlc.arg(block_ids)::bigint[]) AND r.block_id = ANY(sqlc.arg(block_ids)::bigint[])
  AND es.category_id = sqlc.arg(category_id)::bigint
  AND es.status IN ('AVAILABLE', 'HELD')
  AND (es.status = 'AVAILABLE' OR es.hold_expires_at <= now())
ORDER BY es.block_id, r.row_index, s.seat_number;

-- name: CountFreeSeatsOfCategory :one
-- How many seats of a category are free in the given blocks, for the NO_ADJACENT_SEATS
-- answer only (the failure path). Same index and same meaning of "free" as the candidates.
SELECT count(*)::int AS free_seats
FROM event_seats es
WHERE es.event_id = sqlc.arg(event_id) AND es.block_id = ANY(sqlc.arg(block_ids)::bigint[])
  AND es.category_id = sqlc.arg(category_id)::bigint
  AND es.status IN ('AVAILABLE', 'HELD')
  AND (es.status = 'AVAILABLE' OR es.hold_expires_at <= now());
