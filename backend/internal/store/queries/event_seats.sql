-- name: InsertEventSeatsForEvent :execrows
-- Publish: one event_seats row per active seat of the event's venue, in one statement.
-- A seat's category is its row's override, else its block's. A seat with neither is inserted
-- BLOCKED, because an unpriced seat must never be sellable.
-- Safe to run twice: seats that already have a row are skipped, so a second run only adds
-- seats created since the first.
-- The ORDER BY lays each block's seats next to each other on disk, which is what keeps the
-- block seat query to a handful of pages.
INSERT INTO event_seats (event_id, seat_id, block_id, category_id, status)
SELECT e.id,
       s.id,
       s.block_id,
       COALESCE(erc.category_id, ebc.category_id),
       CASE WHEN COALESCE(erc.category_id, ebc.category_id) IS NULL THEN 'BLOCKED' ELSE 'AVAILABLE' END
FROM events e
JOIN stands st ON st.venue_id = e.venue_id
JOIN blocks b ON b.stand_id = st.id
JOIN seats s ON s.block_id = b.id AND s.is_active
LEFT JOIN event_row_categories erc ON erc.event_id = e.id AND erc.row_id = s.row_id
LEFT JOIN event_block_categories ebc ON ebc.event_id = e.id AND ebc.block_id = s.block_id
WHERE e.id = $1
ORDER BY s.block_id, s.row_id, s.seat_number
ON CONFLICT (event_id, seat_id) DO NOTHING;

-- name: ListEventSeatsByBlock :many
-- Level 3 of the seat map: every seat of one block for one event, with its geometry.
SELECT es.id, es.seat_id, es.category_id, es.status, es.version, es.updated_at,
       s.row_id, s.seat_number, s.label, s.x, s.y, s.seat_type
FROM event_seats es
JOIN seats s ON s.id = es.seat_id
-- The second block_id condition is redundant by construction; it is there so the planner
-- reads the block's seats through seats_block_id_idx instead of hashing all 60,000.
WHERE es.event_id = $1 AND es.block_id = $2 AND s.block_id = es.block_id
ORDER BY s.row_id, s.seat_number;

-- name: ListEventSeatsChangedSince :many
-- The poll behind an open block (correction 9). updated_at is stamped when the row is
-- written, not when its transaction commits, so the caller must ask from a little before the
-- time it last saw, or a change that commits late can be missed.
SELECT id, seat_id, category_id, status, version, updated_at
FROM event_seats
WHERE event_id = $1 AND block_id = $2 AND updated_at > sqlc.arg(since)
ORDER BY updated_at, id;

-- name: CountEventSeatsByBlock :many
-- Levels 1 and 2 of the seat map: availability per block and category. It reads every
-- inventory row of the event (about 60,000): from event_seats_event_block_category_status_idx
-- alone when the planner finds that cheaper, by a sequential scan when one event is most of
-- the table. Either way the caller keeps the answer for a few seconds (events.countsCache).
SELECT block_id, category_id, status, count(*) AS seats
FROM event_seats
WHERE event_id = $1
GROUP BY block_id, category_id, status;

-- name: DatabaseClock :one
-- The database's wall clock. The seat map reads it BEFORE it selects seats and builds the
-- change marker from it, so the marker is never later than the data it was issued with.
SELECT clock_timestamp()::timestamptz AS now_at;

-- name: ListBlockSeatsForViewer :many
-- Level 3 of the seat map: every seat of one block for one event, with its geometry.
-- Who holds a seat never leaves the database: the only thing selected about a hold is
-- whether it has lapsed and whether it belongs to sqlc.arg(viewer_id) (0 for an anonymous
-- caller, which matches nobody).
SELECT es.id, s.row_id, s.seat_number, s.x, s.y, s.seat_type, es.category_id, es.status,
       (es.status = 'HELD' AND es.hold_expires_at <= now())::boolean AS hold_lapsed,
       (es.status = 'HELD' AND EXISTS (
           SELECT 1 FROM holds h WHERE h.id = es.hold_id AND h.user_id = sqlc.arg(viewer_id)::bigint
       ))::boolean AS held_by_viewer
FROM event_seats es
JOIN seats s ON s.id = es.seat_id
-- The second block_id condition is redundant by construction; it is there so the planner
-- reads the block's seats through seats_block_id_idx instead of hashing all 60,000.
WHERE es.event_id = sqlc.arg(event_id) AND es.block_id = sqlc.arg(block_id) AND s.block_id = es.block_id
ORDER BY s.row_id, s.seat_number;

-- name: ListBlockSeatChangesForViewer :many
-- The poll behind an open block (correction 9): seats of the block whose row was written
-- after sqlc.arg(since), plus seats whose hold ran out after it. A lapsed hold changes what
-- the seat is shown as without any write to the row, so the second branch is what tells an
-- open seat map that the seat is free again, sweeper or no sweeper.
-- The caller passes a `since` that is earlier than the marker it was given (see
-- events.SeatMap): updated_at is stamped when the row is written, not when its transaction
-- commits.
SELECT es.id, es.status,
       (es.status = 'HELD' AND es.hold_expires_at <= now())::boolean AS hold_lapsed,
       (es.status = 'HELD' AND EXISTS (
           SELECT 1 FROM holds h WHERE h.id = es.hold_id AND h.user_id = sqlc.arg(viewer_id)::bigint
       ))::boolean AS held_by_viewer
FROM event_seats es
WHERE es.event_id = sqlc.arg(event_id) AND es.block_id = sqlc.arg(block_id)
  AND es.updated_at > sqlc.arg(since)::timestamptz
UNION
SELECT es.id, es.status, TRUE, FALSE
FROM event_seats es
WHERE es.event_id = sqlc.arg(event_id) AND es.block_id = sqlc.arg(block_id)
  AND es.status = 'HELD'
  AND es.hold_expires_at > sqlc.arg(since)::timestamptz AND es.hold_expires_at <= now()
ORDER BY 1;

-- name: CountLapsedHoldsByBlock :many
-- Seats still stored as HELD whose hold has run out, per block and category. The
-- availability counts add these back to "available". Read through the partial index on
-- hold_expires_at, which only holds HELD rows.
SELECT block_id, category_id, count(*)::bigint AS seats
FROM event_seats
WHERE event_id = $1 AND status = 'HELD' AND hold_expires_at <= now()
GROUP BY block_id, category_id;
