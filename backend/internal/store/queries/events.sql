-- name: CreateEvent :one
INSERT INTO events (
    venue_id, title, competition, home_team, away_team, kickoff_at, gates_open_at,
    poster_url, sales_open_at, sales_close_at, max_tickets_per_user
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: GetEvent :one
SELECT * FROM events WHERE id = $1;

-- name: ListEvents :many
-- The organiser's list: every status, newest kickoff first.
SELECT * FROM events ORDER BY kickoff_at DESC, id DESC;

-- name: ListEventsByStatus :many
-- The fan's list passes the statuses a fan may see; soonest kickoff first.
SELECT * FROM events
WHERE status = ANY(sqlc.arg(statuses)::text[])
ORDER BY kickoff_at, id;

-- name: UpdateEvent :one
UPDATE events
SET title = $2,
    competition = $3,
    home_team = $4,
    away_team = $5,
    kickoff_at = $6,
    gates_open_at = $7,
    poster_url = $8,
    sales_open_at = $9,
    sales_close_at = $10,
    max_tickets_per_user = $11
WHERE id = $1
RETURNING *;

-- name: SetEventStatus :execrows
-- Moves an event only from one of the states the caller says is a legal starting point. Zero
-- rows means the event was not in any of them (or does not exist) and nothing changed.
UPDATE events
SET status = sqlc.arg(status),
    published_at = CASE
        WHEN published_at IS NULL AND sqlc.arg(status) IN ('PUBLISHED', 'ON_SALE') THEN now()
        ELSE published_at
    END
WHERE id = sqlc.arg(id) AND status = ANY(sqlc.arg(from_statuses)::text[]);

-- name: DeleteDraftEvent :execrows
-- Only a draft can be deleted: once published an event has inventory and may have sales.
DELETE FROM events WHERE id = $1 AND status = 'DRAFT';
