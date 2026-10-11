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
