-- Gate mode (M10): the reads behind the gate event list, the manifest and the scan upload.
-- The statements that take row locks, and the manifest's ticket reads, are hand-written in
-- internal/store/gate.go.

-- name: ListGateEvents :many
-- Events a gate device can scan for: published (in any of its three statuses) and kicking
-- off after the given time, soonest first. The service passes "a day ago", so a match stays
-- on the list while it is being played and for the evening after.
SELECT sqlc.embed(e), v.name AS venue_name, clock_timestamp()::timestamptz AS now_at
FROM events e
JOIN venues v ON v.id = e.venue_id
WHERE e.status IN ('PUBLISHED', 'ON_SALE', 'SOLD_OUT')
  AND e.kickoff_at > sqlc.arg(kickoff_after)::timestamptz
ORDER BY e.kickoff_at, e.id
LIMIT sqlc.arg(row_limit);

-- name: ListGateGroupsOfVenues :many
-- Every stand of the given venues as (venue, code, tier): the gate group is
-- domain.GateGroup(code, tier).
SELECT DISTINCT venue_id, code, tier
FROM stands
WHERE venue_id = ANY(sqlc.arg(venue_ids)::bigint[])
ORDER BY venue_id, code, tier;

-- name: CountTicketsOfEvents :many
-- How many tickets each event has per gate group and status. One pass over the events'
-- tickets through tickets_event_updated_idx; the gate event list builds ticket_count and
-- its change marker from it.
SELECT event_id, gate_group, status, count(*)::bigint AS n
FROM tickets
WHERE event_id = ANY(sqlc.arg(event_ids)::bigint[])
GROUP BY event_id, gate_group, status
ORDER BY event_id, gate_group, status;

-- name: CountLiveTicketsOfEvent :one
-- Tickets a gate may still see (VALID or USED): the manifest's ticket_count.
SELECT count(*)::bigint FROM tickets WHERE event_id = $1 AND status IN ('VALID', 'USED');

-- name: ListScanLogsByClientIDs :many
-- The scans of a batch that are already stored, found by the ID the device made for each.
SELECT * FROM scan_logs WHERE client_scan_id = ANY(sqlc.arg(client_scan_ids)::uuid[]);

-- name: ListAdmittedScansOfTickets :many
-- The scan that currently holds each ticket's admission. There is at most one per ticket:
-- the upload keeps it so under the ticket's row lock.
SELECT ticket_id, client_scan_id, scanned_at
FROM scan_logs
WHERE ticket_id = ANY(sqlc.arg(ticket_ids)::uuid[]) AND result = 'ADMITTED';

-- name: InsertScanLogs :many
-- One row per array position (see InsertTickets for why unnest side by side is safe). A
-- client_scan_id that is already stored is skipped, not overwritten: the first upload of a
-- scan is the record. Returns the IDs actually inserted so the caller can tell.
INSERT INTO scan_logs (client_scan_id, ticket_id, event_id, gate_id, scanned_by, scanned_at, device_id, result)
SELECT unnest(sqlc.arg(client_scan_ids)::uuid[]), unnest(sqlc.arg(ticket_ids)::uuid[]),
       unnest(sqlc.arg(event_ids)::bigint[]), unnest(sqlc.arg(gate_groups)::text[]),
       sqlc.arg(scanned_by)::bigint, unnest(sqlc.arg(scanned_ats)::timestamptz[]),
       sqlc.arg(device_id)::text, unnest(sqlc.arg(results)::text[])
ON CONFLICT (client_scan_id) DO NOTHING
RETURNING client_scan_id;

-- name: DemoteAdmittedScans :execrows
-- An earlier scan of the same ticket has arrived: the scans that held the admission until
-- now become DUPLICATE. The caller holds the row lock of every ticket concerned.
UPDATE scan_logs SET result = 'DUPLICATE'
WHERE client_scan_id = ANY(sqlc.arg(client_scan_ids)::uuid[]) AND result = 'ADMITTED';

-- name: ListTicketStatesByIDs :many
-- A plain read of what the scan answer reports about each ticket.
SELECT id, status, used_at FROM tickets WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ListTicketFactsByIDs :many
-- The columns of a ticket that never change after issue, read without a lock: which event
-- it belongs to decides whether the scan upload locks the row at all.
SELECT id, event_id, gate_group, issued_at, qr_token FROM tickets WHERE id = ANY(sqlc.arg(ids)::uuid[]);
