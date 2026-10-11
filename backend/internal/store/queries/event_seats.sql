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
-- Levels 1 and 2 of the seat map: availability per block and category. Answered from
-- event_seats_event_block_category_status_idx alone.
SELECT block_id, category_id, status, count(*) AS seats
FROM event_seats
WHERE event_id = $1
GROUP BY block_id, category_id, status;
