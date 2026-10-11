-- name: CreateEventCategory :one
INSERT INTO event_categories (event_id, name, price_cents, color_hex, description)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: GetEventCategory :one
SELECT * FROM event_categories WHERE id = $1 AND event_id = $2;

-- name: ListEventCategories :many
SELECT * FROM event_categories WHERE event_id = $1 ORDER BY price_cents DESC, name;

-- name: UpdateEventCategory :one
-- Changing a price never moves a past booking: booking_items carries its own copy.
UPDATE event_categories
SET name = $3, price_cents = $4, color_hex = $5, description = $6
WHERE id = $1 AND event_id = $2
RETURNING *;

-- name: DeleteEventCategory :execrows
-- Fails with a foreign key error once published seats use the category.
DELETE FROM event_categories WHERE id = $1 AND event_id = $2;

-- name: SetBlockCategories :execrows
-- Paints many blocks with one category (the editor's marquee select).
INSERT INTO event_block_categories (event_id, block_id, category_id)
SELECT sqlc.arg(event_id), b, sqlc.arg(category_id)
FROM unnest(sqlc.arg(block_ids)::bigint[]) AS b
ON CONFLICT (event_id, block_id) DO UPDATE SET category_id = EXCLUDED.category_id;

-- name: ClearBlockCategories :execrows
DELETE FROM event_block_categories
WHERE event_id = sqlc.arg(event_id) AND block_id = ANY(sqlc.arg(block_ids)::bigint[]);

-- name: ListEventBlockCategories :many
SELECT * FROM event_block_categories WHERE event_id = $1 ORDER BY block_id;

-- name: SetRowCategories :execrows
INSERT INTO event_row_categories (event_id, row_id, category_id)
SELECT sqlc.arg(event_id), r, sqlc.arg(category_id)
FROM unnest(sqlc.arg(row_ids)::bigint[]) AS r
ON CONFLICT (event_id, row_id) DO UPDATE SET category_id = EXCLUDED.category_id;

-- name: ClearRowCategories :execrows
DELETE FROM event_row_categories
WHERE event_id = sqlc.arg(event_id) AND row_id = ANY(sqlc.arg(row_ids)::bigint[]);

-- name: ListEventRowCategories :many
SELECT * FROM event_row_categories WHERE event_id = $1 ORDER BY row_id;

-- name: CountUnpaintedActiveSeats :one
-- Active seats of the event's venue that neither a row override nor a block assignment gives
-- a category. Publish would insert these as BLOCKED, so the organiser should see the number.
SELECT count(*)
FROM events e
JOIN stands st ON st.venue_id = e.venue_id
JOIN blocks b ON b.stand_id = st.id
JOIN seats s ON s.block_id = b.id AND s.is_active
LEFT JOIN event_row_categories erc ON erc.event_id = e.id AND erc.row_id = s.row_id
LEFT JOIN event_block_categories ebc ON ebc.event_id = e.id AND ebc.block_id = s.block_id
WHERE e.id = $1 AND erc.category_id IS NULL AND ebc.category_id IS NULL;
