package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// The gate's hand-written statements (M10): the two that lock and write tickets for a scan
// upload, and the manifest's ticket reads, which stream rows instead of building a slice of
// generated structs for a whole stadium.
//
// LOCK ORDER, extended for M10. Ticket rows come after everything in the order at the top
// of holds.go, and among themselves they are always taken in id order:
//
//	0, 1. the event (gate, then row), shared, for every event a batch scans for
//	...
//	last. tickets rows, in id order
//
// A scan upload takes the event shared first because (a) cancel holds the event exclusive
// while it voids every ticket of the event in one statement, in no particular order, and
// (b) inserting a scan_logs row takes a key-share lock on its event row through the foreign
// key, which a cancel's FOR UPDATE blocks. Without the event lock an upload could hold
// tickets and wait for the event while a cancel holds the event and waits for those
// tickets. An upload only ever locks tickets of events it has taken this way.
//
// TICKET WRITES COME LAST. tickets.updated_at is stamped when the row is written, and the
// manifest's change poll looks back a fixed span from its version (gate.Policy
// .VersionOverlap). The statements that write tickets are the last ones of the upload's
// transaction, after every lock and every scan_logs write.

// LockedTicket is the changeable part of a ticket, read under its row lock.
type LockedTicket struct {
	ID     [16]byte
	Status string
	UsedAt *time.Time
}

// Locks: the given tickets rows, FOR UPDATE, in id order, until the transaction ends.
// Why: deciding a scan is "read the ticket's status and who holds its admission, then write
// both". Two uploads carrying the same ticket at the same moment would both read VALID and
// both admit. With the row lock they take turns: the second waits, then reads USED and the
// first one's scan, and is judged against it. The id order is what lets two batches that
// share several tickets, in different orders, never wait for each other in a circle; a
// refund's void takes its tickets in the same order (VoidTicketsOfBooking).
const lockTicketsForScan = `
SELECT id, status, used_at FROM tickets WHERE id = ANY($1::uuid[]) ORDER BY id FOR UPDATE`

// LockTicketsForScan takes the row locks of the given tickets and returns each one's status
// and used_at as they are now. IDs with no ticket are simply absent from the answer.
func LockTicketsForScan(ctx context.Context, db DBTX, ids [][16]byte) ([]LockedTicket, error) {
	rows, err := db.Query(ctx, lockTicketsForScan, uuids(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]LockedTicket, 0, len(ids))
	for rows.Next() {
		var t LockedTicket
		if err := rows.Scan(&t.ID, &t.Status, &t.UsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Writes tickets whose rows the caller has locked with LockTicketsForScan. No new lock.
// from_statuses is domain.TicketStatusesThatCanBecome(USED): which tickets may become USED
// is the ticket status rule's to say, not this statement's.
const markTicketsUsed = `
UPDATE tickets t SET status = 'USED', used_at = u.used_at
FROM unnest($1::uuid[], $2::timestamptz[]) AS u(id, used_at)
WHERE t.id = u.id AND t.status = ANY($3::text[])`

// MarkTicketsUsed moves tickets to USED with the scan time of the admission that won them.
// ids and usedAt are parallel. It returns how many rows changed; the caller expects all.
func MarkTicketsUsed(ctx context.Context, db DBTX, ids [][16]byte, usedAt []time.Time, fromStatuses []string) (int64, error) {
	if len(ids) != len(usedAt) {
		return 0, fmt.Errorf("mark tickets used: %d ids for %d times", len(ids), len(usedAt))
	}
	tag, err := db.Exec(ctx, markTicketsUsed, uuids(ids), usedAt, fromStatuses)
	return tag.RowsAffected(), err
}

// Writes tickets whose rows the caller has locked with LockTicketsForScan. No new lock.
// Not a status change: the ticket stays USED. An earlier scan of it has been uploaded after
// a later one, and used_at follows the admission that now holds the ticket.
const moveTicketsUsedAt = `
UPDATE tickets t SET used_at = u.used_at
FROM unnest($1::uuid[], $2::timestamptz[]) AS u(id, used_at)
WHERE t.id = u.id AND t.status = 'USED'`

// MoveTicketsUsedAt sets used_at of tickets that are already USED. ids and usedAt are parallel.
func MoveTicketsUsedAt(ctx context.Context, db DBTX, ids [][16]byte, usedAt []time.Time) (int64, error) {
	if len(ids) != len(usedAt) {
		return 0, fmt.Errorf("move tickets used_at: %d ids for %d times", len(ids), len(usedAt))
	}
	tag, err := db.Exec(ctx, moveTicketsUsedAt, uuids(ids), usedAt)
	return tag.RowsAffected(), err
}

// Plain reads, no locks. The full manifest: every ticket of the event a gate may still see.
const manifestTickets = `
SELECT id, gate_group, status FROM tickets WHERE event_id = $1 AND status IN ('VALID', 'USED')`

// The changes-only manifest: every ticket of the event written after a time, in any status,
// so a device also hears of the ones it must drop. Served by tickets_event_updated_idx.
const manifestTicketChanges = `
SELECT id, gate_group, status FROM tickets WHERE event_id = $1 AND updated_at > $2`

// ForEachManifestTicket calls fn for every ticket of the event that is VALID or USED, in no
// particular order. gateGroup and status are only valid during the call.
func ForEachManifestTicket(ctx context.Context, db DBTX, eventID int64, fn func(id [16]byte, gateGroup, status []byte)) error {
	rows, err := db.Query(ctx, manifestTickets, pgx.QueryResultFormats{pgx.BinaryFormatCode}, eventID)
	if err != nil {
		return err
	}
	return eachManifestRow(rows, fn)
}

// ForEachManifestTicketChange calls fn for every ticket of the event whose row was written
// after since, whatever its status now.
func ForEachManifestTicketChange(ctx context.Context, db DBTX, eventID int64, since time.Time, fn func(id [16]byte, gateGroup, status []byte)) error {
	rows, err := db.Query(ctx, manifestTicketChanges, pgx.QueryResultFormats{pgx.BinaryFormatCode}, eventID, since)
	if err != nil {
		return err
	}
	return eachManifestRow(rows, fn)
}

// eachManifestRow reads (uuid, text, text) rows straight from the wire buffer: a full
// stadium is 60,000 rows, and scanning each into strings would allocate twice per row for
// values that are looked up once and thrown away.
func eachManifestRow(rows pgx.Rows, fn func(id [16]byte, gateGroup, status []byte)) error {
	defer rows.Close()
	for rows.Next() {
		raw := rows.RawValues()
		if len(raw) != 3 || len(raw[0]) != 16 {
			return fmt.Errorf("manifest row: unexpected shape (%d columns)", len(raw))
		}
		fn([16]byte(raw[0]), raw[1], raw[2])
	}
	return rows.Err()
}

func uuids(ids [][16]byte) []pgtype.UUID {
	out := make([]pgtype.UUID, len(ids))
	for i, id := range ids {
		out[i] = pgtype.UUID{Bytes: id, Valid: true}
	}
	return out
}
