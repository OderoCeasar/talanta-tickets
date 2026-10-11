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

-- name: GetEventWithVenue :one
SELECT sqlc.embed(e), v.name AS venue_name
FROM events e
JOIN venues v ON v.id = e.venue_id
WHERE e.id = $1;

-- name: LockEvent :one
-- Locks: the one events row, FOR UPDATE, until the transaction ends.
-- Why: an event's inventory is a copy of its painting, made at publish. Publish and
-- everything that edits what publish copies (assign-category, category create/update/delete,
-- applying a template, editing the event, cancel) take this lock first and then check the
-- status they just read. So a paint either commits before publish reads the painting, or
-- waits, sees the event is no longer DRAFT and is refused; event_seats.category_id can never
-- differ from the painting. Two publishes of one event also queue here, and the second finds
-- the event already published.
SELECT * FROM events WHERE id = $1 FOR UPDATE;

-- name: ListPublicEvents :many
-- The fan's list: published events only, soonest kickoff first, keyset-paged on
-- (kickoff_at, id). The CASE is domain.EffectiveEventStatus in SQL: a published event is
-- ON_SALE exactly while sqlc.arg(now_at) is inside its sales window. Change both together.
SELECT sqlc.embed(e), v.name AS venue_name
FROM events e
JOIN venues v ON v.id = e.venue_id
WHERE e.status IN ('PUBLISHED', 'ON_SALE', 'SOLD_OUT')
  AND e.kickoff_at >= sqlc.arg(from_at)::timestamptz
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR e.kickoff_at < sqlc.narg(to_at)::timestamptz)
  AND (sqlc.narg(effective_status)::text IS NULL OR sqlc.narg(effective_status)::text =
       CASE WHEN e.status IN ('PUBLISHED', 'ON_SALE') THEN
                CASE WHEN (e.sales_open_at IS NULL OR e.sales_open_at <= sqlc.arg(now_at)::timestamptz)
                      AND (e.sales_close_at IS NULL OR sqlc.arg(now_at)::timestamptz < e.sales_close_at)
                     THEN 'ON_SALE' ELSE 'PUBLISHED' END
            ELSE e.status END)
  AND (NOT sqlc.arg(has_cursor)::boolean
       OR (e.kickoff_at, e.id) > (sqlc.arg(cursor_kickoff_at)::timestamptz, sqlc.arg(cursor_id)::bigint))
ORDER BY e.kickoff_at, e.id
LIMIT sqlc.arg(page_limit);

-- name: ListAdminEvents :many
-- The organiser's list: every status, latest kickoff first, keyset-paged on (kickoff_at, id).
-- The status filter uses the same effective status as ListPublicEvents.
SELECT sqlc.embed(e), v.name AS venue_name
FROM events e
JOIN venues v ON v.id = e.venue_id
WHERE (sqlc.narg(from_at)::timestamptz IS NULL OR e.kickoff_at >= sqlc.narg(from_at)::timestamptz)
  AND (sqlc.narg(to_at)::timestamptz IS NULL OR e.kickoff_at < sqlc.narg(to_at)::timestamptz)
  AND (sqlc.narg(effective_status)::text IS NULL OR sqlc.narg(effective_status)::text =
       CASE WHEN e.status IN ('PUBLISHED', 'ON_SALE') THEN
                CASE WHEN (e.sales_open_at IS NULL OR e.sales_open_at <= sqlc.arg(now_at)::timestamptz)
                      AND (e.sales_close_at IS NULL OR sqlc.arg(now_at)::timestamptz < e.sales_close_at)
                     THEN 'ON_SALE' ELSE 'PUBLISHED' END
            ELSE e.status END)
  AND (NOT sqlc.arg(has_cursor)::boolean
       OR (e.kickoff_at, e.id) < (sqlc.arg(cursor_kickoff_at)::timestamptz, sqlc.arg(cursor_id)::bigint))
ORDER BY e.kickoff_at DESC, e.id DESC
LIMIT sqlc.arg(page_limit);

-- name: TouchEvent :exec
-- Stamps events.updated_at (the trigger sets it). The seat map poll answers "full" instead
-- of "delta" when the event changed since the caller's marker, which is how a price or
-- colour change reaches an open block.
UPDATE events SET updated_at = clock_timestamp() WHERE id = $1;

-- name: ReleaseHoldsOfEvent :execrows
-- Cancel: every live hold of the event ends.
UPDATE holds SET status = 'RELEASED' WHERE event_id = $1 AND status = 'ACTIVE';

-- name: FreeHeldSeatsOfEvent :execrows
-- Cancel: seats of the holds just released go back to AVAILABLE, so a payment that lands
-- afterwards finds no held seat to sell (correction 5) and is refunded.
UPDATE event_seats
SET status = 'AVAILABLE', hold_id = NULL, hold_expires_at = NULL, version = version + 1
WHERE event_id = $1 AND status = 'HELD';

-- name: VoidTicketsOfEvent :execrows
UPDATE tickets SET status = 'VOID' WHERE event_id = $1 AND status IN ('VALID', 'USED');

-- name: CancelUnpaidBookingsOfEvent :execrows
UPDATE bookings SET status = 'CANCELLED' WHERE event_id = $1 AND status = 'PENDING_PAYMENT';

-- name: QueueRefundsForEvent :many
-- Cancel: one refund per successful payment of the event. payment_id is unique in refunds,
-- so a payment that already has a refund (queued by the payment flow) is skipped.
INSERT INTO refunds (booking_id, payment_id, amount_cents, reason)
SELECT p.booking_id, p.id, p.amount_cents, sqlc.arg(reason)::text
FROM payments p
JOIN bookings b ON b.id = p.booking_id
WHERE b.event_id = sqlc.arg(event_id) AND p.status = 'SUCCESS' AND p.amount_cents > 0
ON CONFLICT (payment_id) DO NOTHING
RETURNING amount_cents;

-- name: MarkPaidBookingsRefundRequired :execrows
-- Cancel: a paid booking of a cancelled event is owed its money back.
UPDATE bookings SET status = 'REFUND_REQUIRED' WHERE event_id = $1 AND status = 'PAID';
