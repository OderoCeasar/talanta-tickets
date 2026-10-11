-- name: CreateVenueTemplate :one
INSERT INTO venue_templates (venue_id, name, categories, assignments)
VALUES ($1, $2, $3, $4)
ON CONFLICT (venue_id, name) DO NOTHING
RETURNING *;

-- name: GetVenueTemplate :one
SELECT * FROM venue_templates WHERE id = $1;

-- name: GetVenueTemplateByName :one
SELECT * FROM venue_templates WHERE venue_id = $1 AND name = $2;

-- name: ListVenueTemplates :many
SELECT * FROM venue_templates WHERE venue_id = $1 ORDER BY name;

-- name: DeleteVenueTemplate :execrows
DELETE FROM venue_templates WHERE id = $1;

-- name: CreateCategoriesFromTemplate :execrows
INSERT INTO event_categories (event_id, name, price_cents, color_hex, description)
SELECT sqlc.arg(event_id)::bigint,
       c ->> 'name',
       (c ->> 'price_cents')::bigint,
       c ->> 'color_hex',
       c ->> 'description'
FROM venue_templates t
CROSS JOIN LATERAL jsonb_array_elements(t.categories) AS c
WHERE t.id = sqlc.arg(template_id)
ON CONFLICT (event_id, name) DO NOTHING;

-- name: AssignBlockCategoriesFromTemplate :execrows
-- The join to blocks drops assignments for blocks deleted since the template was saved, and
-- limits them to the event's own venue.
INSERT INTO event_block_categories (event_id, block_id, category_id)
SELECT e.id, b.id, ec.id
FROM venue_templates t
JOIN events e ON e.id = sqlc.arg(event_id) AND e.venue_id = t.venue_id
CROSS JOIN LATERAL jsonb_array_elements(COALESCE(t.assignments -> 'blocks', '[]'::jsonb)) AS a
JOIN event_categories ec ON ec.event_id = e.id AND ec.name = a ->> 'category'
JOIN blocks b ON b.id = (a ->> 'block_id')::bigint
JOIN stands st ON st.id = b.stand_id AND st.venue_id = t.venue_id
WHERE t.id = sqlc.arg(template_id)
ON CONFLICT (event_id, block_id) DO NOTHING;

-- name: AssignRowCategoriesFromTemplate :execrows
INSERT INTO event_row_categories (event_id, row_id, category_id)
SELECT e.id, r.id, ec.id
FROM venue_templates t
JOIN events e ON e.id = sqlc.arg(event_id) AND e.venue_id = t.venue_id
CROSS JOIN LATERAL jsonb_array_elements(COALESCE(t.assignments -> 'rows', '[]'::jsonb)) AS a
JOIN event_categories ec ON ec.event_id = e.id AND ec.name = a ->> 'category'
JOIN rows r ON r.id = (a ->> 'row_id')::bigint
JOIN blocks b ON b.id = r.block_id
JOIN stands st ON st.id = b.stand_id AND st.venue_id = t.venue_id
WHERE t.id = sqlc.arg(template_id)
ON CONFLICT (event_id, row_id) DO NOTHING;
