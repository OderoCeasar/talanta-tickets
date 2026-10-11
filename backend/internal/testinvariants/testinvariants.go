// Package testinvariants checks the rules that must hold for an event's inventory at any
// moment no transaction is in flight, whatever happened before: holds, releases, expiries,
// take-overs, sweeps, cancels, sales. Every inventory test calls Check at its end.
//
// It is ordinary Go (no build tag) so any test package can import it; it only reads.
package testinvariants

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Querier is the part of a pool or transaction the checks need.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Counts is an event's inventory by stored status.
type Counts struct {
	Total, Available, Held, Sold, Blocked int
}

// invariant is one rule: a query that returns a row (a short description) per violation.
type invariant struct {
	name string
	sql  string
}

// Every query takes the event id as $1 and returns one text column.
var invariants = []invariant{
	{"a HELD seat belongs to an ACTIVE hold", `
		SELECT format('seat %s is HELD by hold %s, which is %s', es.id, es.hold_id, COALESCE(h.status, 'missing'))
		FROM event_seats es LEFT JOIN holds h ON h.id = es.hold_id
		WHERE es.event_id = $1 AND es.status = 'HELD' AND h.status IS DISTINCT FROM 'ACTIVE'`},
	{"an ACTIVE hold has at least one HELD seat", `
		SELECT format('hold %s is ACTIVE with no seat', h.id)
		FROM holds h
		WHERE h.event_id = $1 AND h.status = 'ACTIVE'
		  AND NOT EXISTS (SELECT 1 FROM event_seats es WHERE es.hold_id = h.id AND es.status = 'HELD')`},
	{"a HELD seat has hold_id and hold_expires_at", `
		SELECT format('seat %s is HELD with hold_id %s and hold_expires_at %s', id, hold_id, hold_expires_at)
		FROM event_seats
		WHERE event_id = $1 AND status = 'HELD' AND (hold_id IS NULL OR hold_expires_at IS NULL)`},
	{"a seat that is not HELD or SOLD points at no hold", `
		SELECT format('seat %s is %s but has hold_id %s / hold_expires_at %s', id, status, hold_id, hold_expires_at)
		FROM event_seats
		WHERE event_id = $1
		  AND ((status IN ('AVAILABLE', 'BLOCKED') AND (hold_id IS NOT NULL OR hold_expires_at IS NOT NULL))
		    OR (status = 'SOLD' AND hold_expires_at IS NOT NULL))`},
	{"a held seat and its hold agree on the event and the expiry", `
		SELECT format('seat %s (event %s, expires %s) is held by hold %s (event %s, expires %s)',
		              es.id, es.event_id, es.hold_expires_at, h.id, h.event_id, h.expires_at)
		FROM event_seats es JOIN holds h ON h.id = es.hold_id
		WHERE es.event_id = $1 AND es.status = 'HELD'
		  AND (h.event_id <> es.event_id OR h.expires_at <> es.hold_expires_at)`},
	{"every seat has one of the four statuses", `
		SELECT format('seat %s has status %s', id, status)
		FROM event_seats
		WHERE event_id = $1 AND status NOT IN ('AVAILABLE', 'HELD', 'SOLD', 'BLOCKED')`},
	{"the inventory is one row per active seat of the venue", `
		SELECT format('%s inventory rows for %s active seats', inv.n, phys.n)
		FROM (SELECT count(*) AS n FROM event_seats WHERE event_id = $1) inv,
		     (SELECT count(*) AS n FROM events e
		        JOIN stands st ON st.venue_id = e.venue_id
		        JOIN blocks b ON b.stand_id = st.id
		        JOIN seats s ON s.block_id = b.id AND s.is_active
		       WHERE e.id = $1) phys
		WHERE inv.n <> phys.n`},
	{"no seat is in two live bookings", `
		SELECT format('seat %s is in %s live bookings', bi.event_seat_id, count(*))
		FROM booking_items bi JOIN bookings b ON b.id = bi.booking_id
		WHERE b.event_id = $1 AND b.status IN ('PENDING_PAYMENT', 'PAID')
		GROUP BY bi.event_seat_id HAVING count(*) > 1`},
	{"a hold has at most one live booking", `
		SELECT format('hold %s has %s live bookings', hold_id, count(*))
		FROM bookings
		WHERE event_id = $1 AND hold_id IS NOT NULL AND status IN ('PENDING_PAYMENT', 'PAID')
		GROUP BY hold_id HAVING count(*) > 1`},
	{"a cancelled event has no HELD seat and no ACTIVE hold", `
		SELECT format('cancelled event %s still has %s', e.id, what.label)
		FROM events e,
		     LATERAL (SELECT 'a HELD seat' AS label WHERE EXISTS (SELECT 1 FROM event_seats es WHERE es.event_id = e.id AND es.status = 'HELD')
		              UNION ALL
		              SELECT 'an ACTIVE hold' WHERE EXISTS (SELECT 1 FROM holds h WHERE h.event_id = e.id AND h.status = 'ACTIVE')) what
		WHERE e.id = $1 AND e.status = 'CANCELLED'`},
}

// Violations runs every rule against one event and returns one line per violation, empty
// when the inventory is sound. A seat cannot be in two holds at once by construction
// (event_seats.hold_id is one column); what is checked instead is everything that would make
// a seat be counted twice: a seat held by a hold that is not live, a live hold with no seat,
// a seat and its hold disagreeing, and a seat in two live bookings.
func Violations(ctx context.Context, db Querier, eventID int64) ([]string, error) {
	var out []string
	for _, inv := range invariants {
		rows, err := db.Query(ctx, inv.sql, eventID)
		if err != nil {
			return nil, fmt.Errorf("invariant %q: %w", inv.name, err)
		}
		found, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, fmt.Errorf("invariant %q: %w", inv.name, err)
		}
		for i, f := range found {
			if i == 5 {
				out = append(out, fmt.Sprintf("%s: … and %d more", inv.name, len(found)-i))
				break
			}
			out = append(out, inv.name+": "+f)
		}
	}
	return out, nil
}

// CountSeats returns the event's inventory by stored status.
func CountSeats(ctx context.Context, db Querier, eventID int64) (Counts, error) {
	var c Counts
	err := db.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE status = 'AVAILABLE'),
		       count(*) FILTER (WHERE status = 'HELD'),
		       count(*) FILTER (WHERE status = 'SOLD'),
		       count(*) FILTER (WHERE status = 'BLOCKED')
		FROM event_seats WHERE event_id = $1`, eventID).
		Scan(&c.Total, &c.Available, &c.Held, &c.Sold, &c.Blocked)
	return c, err
}

// Check fails the test for every broken rule and returns the event's seat counts, having
// also checked that sold + held + available + blocked is every seat of the event.
func Check(t testing.TB, db Querier, eventID int64) Counts {
	t.Helper()
	ctx := context.Background()
	violations, err := Violations(ctx, db, eventID)
	if err != nil {
		t.Fatalf("inventory invariants of event %d: %v", eventID, err)
	}
	for _, v := range violations {
		t.Errorf("inventory invariant broken (event %d): %s", eventID, v)
	}
	c, err := CountSeats(ctx, db, eventID)
	if err != nil {
		t.Fatalf("count seats of event %d: %v", eventID, err)
	}
	if sum := c.Available + c.Held + c.Sold + c.Blocked; sum != c.Total {
		t.Errorf("inventory invariant broken (event %d): sold %d + held %d + available %d + blocked %d = %d, but the event has %d seats",
			eventID, c.Sold, c.Held, c.Available, c.Blocked, sum, c.Total)
	}
	return c
}
