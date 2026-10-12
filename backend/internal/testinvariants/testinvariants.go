// Package testinvariants checks the rules that must hold for an event's inventory at any
// moment no transaction is in flight, whatever happened before: holds, releases, expiries,
// take-overs, sweeps, cancels, sales. Every inventory test calls Check at its end.
//
// There are two sets of rules. Check runs the inventory rules: what must be true of seats
// and holds however the rows got there, including tests that write SOLD seats and bookings
// straight into the tables as fixtures. CheckLedger runs those AND the ledger rules: what
// must be true once seats are only ever sold by the payment flow (a sold seat has a paid
// booking, a converted hold and a ticket; a paid booking has its money). Every payment test
// calls CheckLedger at its end.
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

// ledgerInvariants are the rules of the payment milestone. Seats of a CANCELLED event are left
// out of the rules that tie a sold seat to a paid booking and a live ticket: cancelling an
// event voids its tickets and moves its paid bookings to REFUND_REQUIRED, and its sold seats
// stay SOLD on purpose (nothing goes SOLD -> AVAILABLE).
var ledgerInvariants = []invariant{
	{"a SOLD seat has exactly one PAID booking item", `
		SELECT format('seat %s is SOLD and is in %s PAID bookings', es.id, count(b.id))
		FROM event_seats es
		JOIN events e ON e.id = es.event_id
		LEFT JOIN booking_items bi ON bi.event_seat_id = es.id
		LEFT JOIN bookings b ON b.id = bi.booking_id AND b.status = 'PAID'
		WHERE es.event_id = $1 AND es.status = 'SOLD' AND e.status <> 'CANCELLED'
		GROUP BY es.id HAVING count(b.id) <> 1`},
	{"a SOLD seat was sold through a CONVERTED hold", `
		SELECT format('seat %s is SOLD through hold %s, which is %s', es.id, es.hold_id, COALESCE(h.status, 'missing'))
		FROM event_seats es LEFT JOIN holds h ON h.id = es.hold_id
		WHERE es.event_id = $1 AND es.status = 'SOLD' AND h.status IS DISTINCT FROM 'CONVERTED'`},
	{"a SOLD seat has exactly one live ticket", `
		SELECT format('seat %s is SOLD with %s live tickets', es.id, count(t.id))
		FROM event_seats es
		JOIN events e ON e.id = es.event_id
		LEFT JOIN tickets t ON t.event_seat_id = es.id AND t.status IN ('VALID', 'USED')
		WHERE es.event_id = $1 AND es.status = 'SOLD' AND e.status <> 'CANCELLED'
		GROUP BY es.id HAVING count(t.id) <> 1`},
	{"every seat of a PAID booking is SOLD through the booking's hold", `
		SELECT format('booking %s is PAID but its seat %s is %s (hold %s, booking hold %s)', b.id, es.id, es.status, es.hold_id, b.hold_id)
		FROM bookings b
		JOIN booking_items bi ON bi.booking_id = b.id
		JOIN event_seats es ON es.id = bi.event_seat_id
		WHERE b.event_id = $1 AND b.status = 'PAID'
		  AND (es.status <> 'SOLD' OR es.hold_id IS DISTINCT FROM b.hold_id)`},
	{"no hold has both HELD and SOLD seats", `
		SELECT format('hold %s has %s HELD and %s SOLD seats', hold_id,
		              count(*) FILTER (WHERE status = 'HELD'), count(*) FILTER (WHERE status = 'SOLD'))
		FROM event_seats
		WHERE event_id = $1 AND hold_id IS NOT NULL
		GROUP BY hold_id
		HAVING count(*) FILTER (WHERE status = 'HELD') > 0 AND count(*) FILTER (WHERE status = 'SOLD') > 0`},
	{"a live hold holds exactly the seats of its live booking", `
		SELECT format('hold %s is live with %s HELD seats, but its booking %s has %s items', h.id,
		              (SELECT count(*) FROM event_seats es WHERE es.hold_id = h.id AND es.status = 'HELD'), b.id,
		              (SELECT count(*) FROM booking_items bi WHERE bi.booking_id = b.id))
		FROM holds h
		JOIN bookings b ON b.hold_id = h.id AND b.status IN ('PENDING_PAYMENT', 'PAID')
		WHERE h.event_id = $1 AND h.status = 'ACTIVE' AND h.expires_at > clock_timestamp()
		  AND (SELECT count(*) FROM event_seats es WHERE es.hold_id = h.id AND es.status = 'HELD')
		   <> (SELECT count(*) FROM booking_items bi WHERE bi.booking_id = b.id)`},
	{"a PAID booking's total is its items plus its fee", `
		SELECT format('booking %s: total %s, items %s, fee %s', b.id, b.total_cents, i.sum, b.fee_cents)
		FROM bookings b,
		     LATERAL (SELECT COALESCE(sum(unit_price_cents), 0) AS sum, count(*) AS n FROM booking_items WHERE booking_id = b.id) i
		WHERE b.event_id = $1 AND b.status = 'PAID'
		  AND (b.total_cents <> i.sum + b.fee_cents OR b.subtotal_cents <> i.sum OR i.n = 0)`},
	{"a PAID booking has exactly one SUCCESS payment that bought it, for its total", `
		SELECT format('booking %s (total %s) has %s SUCCESS payments with no refund, amounts %s', b.id, b.total_cents, count(p.id),
		              COALESCE(string_agg(p.amount_cents::text, ','), 'none'))
		FROM bookings b
		LEFT JOIN payments p ON p.booking_id = b.id AND p.status = 'SUCCESS'
		                    AND NOT EXISTS (SELECT 1 FROM refunds r WHERE r.payment_id = p.id)
		WHERE b.event_id = $1 AND b.status = 'PAID'
		GROUP BY b.id
		HAVING count(p.id) <> 1 OR bool_or(p.amount_cents <> b.total_cents)`},
	{"a PAID booking has one live ticket per item", `
		SELECT format('booking %s is PAID but its seat %s has %s live tickets of the booking', b.id, bi.event_seat_id, count(t.id))
		FROM bookings b
		JOIN booking_items bi ON bi.booking_id = b.id
		LEFT JOIN tickets t ON t.booking_id = b.id AND t.event_seat_id = bi.event_seat_id AND t.status IN ('VALID', 'USED')
		WHERE b.event_id = $1 AND b.status = 'PAID'
		GROUP BY b.id, bi.event_seat_id HAVING count(t.id) <> 1`},
	{"no seat has more than one live ticket", `
		SELECT format('seat %s has %s live tickets', t.event_seat_id, count(*))
		FROM tickets t
		WHERE t.event_id = $1 AND t.status IN ('VALID', 'USED')
		GROUP BY t.event_seat_id HAVING count(*) > 1`},
	{"a live ticket belongs to a PAID booking", `
		SELECT format('ticket %s is %s but its booking %s is %s', t.id, t.status, b.id, b.status)
		FROM tickets t JOIN bookings b ON b.id = t.booking_id
		WHERE t.event_id = $1 AND t.status IN ('VALID', 'USED') AND b.status <> 'PAID'`},
	{"a booking that is not PAID owns no SOLD seat", `
		SELECT format('booking %s is %s but its seat %s is SOLD through its hold %s', b.id, b.status, es.id, b.hold_id)
		FROM bookings b
		JOIN events e ON e.id = b.event_id
		JOIN booking_items bi ON bi.booking_id = b.id
		JOIN event_seats es ON es.id = bi.event_seat_id
		WHERE b.event_id = $1 AND e.status <> 'CANCELLED'
		  AND b.status IN ('REFUND_REQUIRED', 'REFUNDED', 'FAILED', 'EXPIRED', 'CANCELLED')
		  AND es.status = 'SOLD' AND es.hold_id IS NOT DISTINCT FROM b.hold_id
		  AND NOT EXISTS (
		      SELECT 1 FROM bookings paid JOIN booking_items pi ON pi.booking_id = paid.id
		      WHERE paid.status = 'PAID' AND paid.hold_id IS NOT DISTINCT FROM b.hold_id AND pi.event_seat_id = es.id)`},
	{"money that bought nothing is queued for refund", `
		SELECT format('payment %s is SUCCESS for booking %s, which is %s, and has no refund', p.id, b.id, b.status)
		FROM payments p JOIN bookings b ON b.id = p.booking_id
		WHERE b.event_id = $1 AND p.status = 'SUCCESS' AND b.status <> 'PAID'
		  AND NOT EXISTS (SELECT 1 FROM refunds r WHERE r.payment_id = p.id)`},
	// The governing rule of the money path: once there is evidence that money moved for a
	// payment, it ends with tickets (SUCCESS, checked above) or with a refund row. A payment
	// that is FAILED or TIMEOUT and shows any of these has dropped money on the floor unless
	// a refund is queued for it:
	//   - its result code is M-Pesa's success code (the wrong-amount case);
	//   - the callback body stored on it says ResultCode 0;
	//   - a row of the callback inbox says it was paid and was either put through the
	//     finishing function (APPLIED) or given up on (ABANDONED). A row that M-Pesa's own
	//     answer contradicted (REFUTED), one left to reconciliation (DEFERRED) and one still
	//     waiting to be worked on (no verdict) are not evidence: the first two are M-Pesa
	//     saying no money moved, the last is work in flight;
	//   - an unmatched "paid" callback was tied to it after M-Pesa confirmed the callback.
	{"a FAILED or TIMEOUT payment with evidence that it was paid has a refund", `
		SELECT format('payment %s is %s with evidence that it was paid (%s) and has no refund', p.id, p.status, ev.what)
		FROM payments p
		JOIN bookings b ON b.id = p.booking_id
		CROSS JOIN LATERAL (
		    SELECT evidence.what FROM (
		        SELECT 'its result code is 0' AS what WHERE p.result_code = 0
		        UNION ALL
		        SELECT 'its stored callback says ResultCode 0' WHERE p.raw_callback #>> '{Body,stkCallback,ResultCode}' = '0'
		        UNION ALL
		        SELECT format('callback inbox row %s, %s', c.id, c.verdict) FROM payment_callbacks c
		         WHERE c.payment_id = p.id AND c.result_code = 0 AND c.verdict IN ('APPLIED', 'ABANDONED')
		        UNION ALL
		        SELECT format('unmatched callback %s was matched to it', u.id) FROM unmatched_callbacks u
		         WHERE u.matched_payment_id = p.id
		    ) evidence LIMIT 1) ev
		WHERE b.event_id = $1 AND p.status IN ('FAILED', 'TIMEOUT')
		  AND NOT EXISTS (SELECT 1 FROM refunds r WHERE r.payment_id = p.id)`},
	{"a booking has at most one live payment", `
		SELECT format('booking %s has %s live payments', p.booking_id, count(*))
		FROM payments p JOIN bookings b ON b.id = p.booking_id
		WHERE b.event_id = $1 AND p.status IN ('INITIATED', 'PENDING')
		GROUP BY p.booking_id HAVING count(*) > 1`},
}

// Violations runs every rule against one event and returns one line per violation, empty
// when the inventory is sound. A seat cannot be in two holds at once by construction
// (event_seats.hold_id is one column); what is checked instead is everything that would make
// a seat be counted twice: a seat held by a hold that is not live, a live hold with no seat,
// a seat and its hold disagreeing, and a seat in two live bookings.
func Violations(ctx context.Context, db Querier, eventID int64) ([]string, error) {
	return violations(ctx, db, eventID, invariants)
}

// LedgerViolations is Violations plus the ledger rules of the payment milestone.
func LedgerViolations(ctx context.Context, db Querier, eventID int64) ([]string, error) {
	inventory, err := violations(ctx, db, eventID, invariants)
	if err != nil {
		return nil, err
	}
	ledger, err := violations(ctx, db, eventID, ledgerInvariants)
	return append(inventory, ledger...), err
}

// LedgerRuleNames lists the ledger rules, for the test that proves each one can fire.
func LedgerRuleNames() []string {
	names := make([]string, 0, len(ledgerInvariants))
	for _, inv := range ledgerInvariants {
		names = append(names, inv.name)
	}
	return names
}

func violations(ctx context.Context, db Querier, eventID int64, rules []invariant) ([]string, error) {
	var out []string
	for _, inv := range rules {
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
	return check(t, db, eventID, Violations)
}

// CheckLedger is Check with the ledger rules of the payment milestone as well. Every test
// that sells seats through the payment flow ends with it.
func CheckLedger(t testing.TB, db Querier, eventID int64) Counts {
	t.Helper()
	return check(t, db, eventID, LedgerViolations)
}

func check(t testing.TB, db Querier, eventID int64, rules func(context.Context, Querier, int64) ([]string, error)) Counts {
	t.Helper()
	ctx := context.Background()
	violations, err := rules(ctx, db, eventID)
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
