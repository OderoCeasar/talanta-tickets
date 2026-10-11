-- name: GetVenue :one
SELECT * FROM venues WHERE id = $1;

-- name: GetVenueByName :one
SELECT * FROM venues WHERE name = $1;

-- name: ListVenues :many
SELECT * FROM venues ORDER BY name;

-- name: CreateVenue :one
INSERT INTO venues (name, city, capacity, plan_width, plan_height)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (name) DO NOTHING
RETURNING *;

-- name: SetVenueCapacityFromSeats :execrows
UPDATE venues v
SET capacity = (
    SELECT count(*)
    FROM seats s
    JOIN blocks b ON b.id = s.block_id
    JOIN stands st ON st.id = b.stand_id
    WHERE st.venue_id = v.id AND s.is_active
)
WHERE v.id = $1;

-- name: GetStand :one
SELECT * FROM stands WHERE id = $1;

-- name: ListStandsByVenue :many
SELECT * FROM stands WHERE venue_id = $1 ORDER BY display_order, id;

-- name: CreateStand :one
INSERT INTO stands (venue_id, code, name, tier, polygon, display_order)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (venue_id, code, tier) DO NOTHING
RETURNING *;

-- name: GetBlock :one
SELECT * FROM blocks WHERE id = $1;

-- name: ListBlocksByStand :many
SELECT * FROM blocks WHERE stand_id = $1 ORDER BY code;

-- name: ListBlocksByVenue :many
SELECT b.*
FROM blocks b
JOIN stands st ON st.id = b.stand_id
WHERE st.venue_id = $1
ORDER BY st.display_order, b.code;

-- name: CreateBlock :one
INSERT INTO blocks (
    stand_id, code, label, origin_x, origin_y, rotation_deg, arc_radius,
    seat_pitch_x, seat_pitch_y, outline, row_count, seats_per_row, numbering_direction
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
ON CONFLICT (stand_id, code) DO NOTHING
RETURNING *;

-- name: ListRowsByBlock :many
SELECT * FROM rows WHERE block_id = $1 ORDER BY row_index;

-- name: ListRowsByBlocks :many
SELECT * FROM rows WHERE block_id = ANY(sqlc.arg(block_ids)::bigint[]) ORDER BY block_id, row_index;

-- name: CopyRows :copyfrom
INSERT INTO rows (block_id, label, row_index, seat_count, x_offset)
VALUES ($1, $2, $3, $4, $5);

-- name: ListSeatsByBlock :many
SELECT s.*
FROM seats s
JOIN rows r ON r.id = s.row_id
WHERE s.block_id = $1
ORDER BY r.row_index, s.seat_number;

-- name: CopySeats :copyfrom
INSERT INTO seats (row_id, block_id, seat_number, label, x, y, seat_type, is_active)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);

-- name: CountActiveSeatsByVenue :one
SELECT count(*)
FROM seats s
JOIN blocks b ON b.id = s.block_id
JOIN stands st ON st.id = b.stand_id
WHERE st.venue_id = $1 AND s.is_active;

-- name: LockVenueForLayoutChange :one
-- Locks: the one venues row, FOR NO KEY UPDATE, until the transaction ends.
-- Why: deleting a block or replacing its seats must not run while an event of the venue is
-- being published. Publish holds this row FOR SHARE while it copies seats into event_seats;
-- without the lock a delete could remove seats that an uncommitted publish has just
-- referenced, and one of the two would die on the foreign key. NO KEY, because nothing here
-- changes the venue's id, so inserts that reference the venue (new events, new stands) are
-- not held up.
SELECT id FROM venues WHERE id = $1 FOR NO KEY UPDATE;

-- name: LockVenueForPublish :one
-- Locks: the one venues row, FOR SHARE, until the transaction ends.
-- Why: the other half of LockVenueForLayoutChange. Publish reads every active seat of the
-- venue and inserts a row per seat; the shared lock keeps blocks and seats from being
-- deleted or regenerated under it. Shared, so several events can be published at once.
SELECT id FROM venues WHERE id = $1 FOR SHARE;

-- name: LockBlock :one
-- Locks: the one blocks row, FOR UPDATE, until the transaction ends.
-- Why: move, regenerate and delete each read the block and then rewrite its seats; two of
-- them at once on one block would interleave their seat writes.
SELECT * FROM blocks WHERE id = $1 FOR UPDATE;

-- name: LockBlockForEdit :one
-- Locks: the one blocks row, FOR NO KEY UPDATE, until the transaction ends.
-- Why: a rename or a move reads the block and then rewrites it and its seats' positions; two
-- edits of one block at once would interleave. NO KEY UPDATE still shuts out the other
-- edits, regenerate and delete (they take NO KEY UPDATE or FOR UPDATE on the same row), but
-- unlike FOR UPDATE it does not conflict with the FOR KEY SHARE locks a publish takes on
-- every block it copies seats of (the event_seats.block_id foreign key) and keeps until it
-- commits, most of a minute later. A move changes no key column, so it has no reason to
-- queue behind a publish. Changing the block's code is the exception: (stand_id, code) is a
-- unique key, so that UPDATE itself needs the stronger lock and waits; UpdateBlock runs
-- under a short lock_timeout for that case.
SELECT * FROM blocks WHERE id = $1 FOR NO KEY UPDATE;

-- name: GetBlockWithVenue :one
SELECT sqlc.embed(b), st.venue_id, v.plan_width, v.plan_height
FROM blocks b
JOIN stands st ON st.id = b.stand_id
JOIN venues v ON v.id = st.venue_id
WHERE b.id = $1;

-- name: ListBlockStandsByVenue :many
-- Which stand each block is in, without the outlines.
SELECT b.id AS block_id, b.stand_id
FROM blocks b
JOIN stands st ON st.id = b.stand_id
WHERE st.venue_id = $1;

-- name: CountActiveSeatsByBlockForVenue :many
SELECT s.block_id, count(*)::bigint AS seats
FROM seats s
JOIN blocks b ON b.id = s.block_id
JOIN stands st ON st.id = b.stand_id
WHERE st.venue_id = $1 AND s.is_active
GROUP BY s.block_id;

-- name: CountActiveSeatsByBlock :one
SELECT count(*)::bigint FROM seats WHERE block_id = $1 AND is_active;

-- name: ListBlockIDsWithInventoryByVenue :many
-- Blocks that some event was published with. Probes event_seats once per (event, block)
-- through its (event_id, block_id, …) index instead of scanning it.
SELECT b.id
FROM blocks b
JOIN stands st ON st.id = b.stand_id
WHERE st.venue_id = $1
  AND EXISTS (
      SELECT 1 FROM events e
      WHERE e.venue_id = st.venue_id
        AND EXISTS (SELECT 1 FROM event_seats es WHERE es.event_id = e.id AND es.block_id = b.id));

-- name: ListEventIDsWithInventoryForBlock :many
SELECT e.id
FROM events e
WHERE e.venue_id = sqlc.arg(venue_id)
  AND EXISTS (SELECT 1 FROM event_seats es WHERE es.event_id = e.id AND es.block_id = sqlc.arg(block_id))
ORDER BY e.id;

-- name: UpdateBlock :one
UPDATE blocks
SET code = $2, label = $3, origin_x = $4, origin_y = $5, rotation_deg = $6, arc_radius = $7,
    seat_pitch_x = $8, seat_pitch_y = $9, outline = $10
WHERE id = $1
RETURNING *;

-- name: SetBlockLayout :one
UPDATE blocks
SET outline = $2, row_count = $3, seats_per_row = $4, numbering_direction = $5
WHERE id = $1
RETURNING *;

-- name: DeleteBlock :execrows
DELETE FROM blocks WHERE id = $1;

-- name: DeleteRowsByBlock :execrows
-- Seats and row category overrides go with their rows (ON DELETE CASCADE).
DELETE FROM rows WHERE block_id = $1;

-- name: UpdateSeatPositions :execrows
-- Moves many seats at once: the three arrays are parallel.
UPDATE seats s
SET x = u.x, y = u.y
FROM (
    -- Set-returning functions side by side in one SELECT list are zipped row by row.
    SELECT unnest(sqlc.arg(ids)::bigint[]) AS id,
           unnest(sqlc.arg(xs)::float8[]) AS x,
           unnest(sqlc.arg(ys)::float8[]) AS y
) AS u
WHERE s.id = u.id;
