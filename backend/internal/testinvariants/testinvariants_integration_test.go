//go:build integration

package testinvariants_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/testdb"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/testinvariants"
)

// state is a small event in a sound condition: one paid booking of two sold seats with its
// payment and tickets, and one live hold of two seats with a booking awaiting payment.
type state struct {
	EventID      int64
	Category     int64
	Seats        []int64 // 10 seats: [0,1] sold, [2,3] held, the rest available
	PaidHold     int64
	PaidBooking  int64
	Payment      int64
	LiveHold     int64
	LiveBooking  int64
	User         int64
	TicketOfSeat map[int64]string
}

func build(t *testing.T, ctx context.Context, db *testdb.DB) state {
	t.Helper()
	var s state
	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	one := func(dst any, sql string, args ...any) {
		t.Helper()
		require.NoError(t, tx.QueryRow(ctx, sql, args...).Scan(dst), sql)
	}
	run := func(sql string, args ...any) {
		t.Helper()
		_, err := tx.Exec(ctx, sql, args...)
		require.NoError(t, err, sql)
	}

	var venueID, standID, blockID int64
	one(&venueID, `INSERT INTO venues (name, city, capacity, plan_width, plan_height) VALUES ('Invariant Ground', 'Nairobi', 10, 1000, 1000) RETURNING id`)
	one(&standID, `INSERT INTO stands (venue_id, code, name, tier, display_order) VALUES ($1, 'N', 'North', 'LOWER', 1) RETURNING id`, venueID)
	one(&blockID, `INSERT INTO blocks (stand_id, code, label, origin_x, origin_y, seat_pitch_x, seat_pitch_y, row_count, seats_per_row)
		VALUES ($1, 'N1', 'North 1', 100, 100, 5, 8, 1, 10) RETURNING id`, standID)
	run(`INSERT INTO rows (block_id, label, row_index, seat_count) VALUES ($1, 'A', 1, 10)`, blockID)
	run(`INSERT INTO seats (row_id, block_id, seat_number, label, x, y)
		SELECT r.id, r.block_id, n, 'A' || n, n * 5, 8 FROM rows r, generate_series(1, 10) n WHERE r.block_id = $1`, blockID)
	one(&s.EventID, `INSERT INTO events (venue_id, title, kickoff_at, status, sales_open_at, sales_close_at, published_at)
		VALUES ($1, 'Invariant Match', now() + interval '30 days', 'ON_SALE', now() - interval '1 hour', now() + interval '30 days', now()) RETURNING id`, venueID)
	one(&s.Category, `INSERT INTO event_categories (event_id, name, price_cents, color_hex) VALUES ($1, 'Regular', 100000, '#059669') RETURNING id`, s.EventID)
	run(`INSERT INTO event_block_categories (event_id, block_id, category_id) VALUES ($1, $2, $3)`, s.EventID, blockID, s.Category)
	n, err := store.PublishEventSeats(ctx, tx, s.EventID)
	require.NoError(t, err)
	require.EqualValues(t, 10, n)
	rows, err := tx.Query(ctx, `SELECT id FROM event_seats WHERE event_id = $1 ORDER BY id`, s.EventID)
	require.NoError(t, err)
	s.Seats, err = pgx.CollectRows(rows, pgx.RowTo[int64])
	require.NoError(t, err)
	one(&s.User, `INSERT INTO users (phone_e164, role) VALUES ('+254711000001', 'FAN') RETURNING id`)

	// The paid booking.
	one(&s.PaidHold, `INSERT INTO holds (event_id, user_id, expires_at, status) VALUES ($1, $2, now() + interval '5 minutes', 'CONVERTED') RETURNING id`, s.EventID, s.User)
	run(`UPDATE event_seats SET status = 'SOLD', hold_id = $1 WHERE id = ANY($2)`, s.PaidHold, s.Seats[:2])
	one(&s.PaidBooking, `INSERT INTO bookings (reference, event_id, user_id, hold_id, status, subtotal_cents, fee_cents, total_cents, idempotency_key, paid_at)
		VALUES ('TLT-PAID22', $1, $2, $3, 'PAID', 200000, 0, 200000, 'key-paid-booking', now()) RETURNING id`, s.EventID, s.User, s.PaidHold)
	run(`INSERT INTO booking_items (booking_id, event_seat_id, category_id, unit_price_cents) SELECT $1, unnest($2::bigint[]), $3, 100000`,
		s.PaidBooking, s.Seats[:2], s.Category)
	one(&s.Payment, `INSERT INTO payments (booking_id, amount_cents, phone_e164, status, checkout_request_id, mpesa_receipt, completed_at)
		VALUES ($1, 200000, '+254711000001', 'SUCCESS', 'ws_CO_invariants', 'RINV0001', now()) RETURNING id`, s.PaidBooking)
	s.TicketOfSeat = map[int64]string{}
	for i, seat := range s.Seats[:2] {
		var id string
		one(&id, `INSERT INTO tickets (booking_id, event_seat_id, event_id, gate_group, key_id, token_nonce, qr_token)
			VALUES ($1, $2, $3, 'N-LOWER', 'k1', $4, $5) RETURNING id::text`,
			s.PaidBooking, seat, s.EventID, []byte{byte(i), 1, 2, 3, 4, 5, 6, 7}, fmt.Sprintf("TLT1.payload%d.signature", i))
		s.TicketOfSeat[seat] = id
	}

	// The live hold with a booking awaiting payment.
	one(&s.LiveHold, `INSERT INTO holds (event_id, user_id, expires_at) VALUES ($1, $2, now() + interval '10 minutes') RETURNING id`, s.EventID, s.User)
	run(`UPDATE event_seats es SET status = 'HELD', hold_id = h.id, hold_expires_at = h.expires_at FROM holds h WHERE h.id = $1 AND es.id = ANY($2)`,
		s.LiveHold, s.Seats[2:4])
	one(&s.LiveBooking, `INSERT INTO bookings (reference, event_id, user_id, hold_id, status, subtotal_cents, fee_cents, total_cents, idempotency_key)
		VALUES ('TLT-PEND22', $1, $2, $3, 'PENDING_PAYMENT', 200000, 0, 200000, 'key-pending-booking') RETURNING id`, s.EventID, s.User, s.LiveHold)
	run(`INSERT INTO booking_items (booking_id, event_seat_id, category_id, unit_price_cents) SELECT $1, unnest($2::bigint[]), $3, 100000`,
		s.LiveBooking, s.Seats[2:4], s.Category)
	require.NoError(t, tx.Commit(ctx))
	return s
}

// recorder is a testing.TB that keeps what Check reports instead of failing the real test.
type recorder struct {
	testing.TB
	errors []string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
func (r *recorder) Fatalf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

// Every ledger rule must be able to fire: a checker that cannot see a broken state would
// pass every payment test for nothing. Each case breaks one thing in an otherwise sound
// event, inside a transaction that is rolled back, and requires the rule that guards it to
// report it.
func TestLedgerRulesEachFireOnACorruptState(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	s := build(t, ctx, db)

	// The sound state passes both sets of rules.
	sound, err := testinvariants.LedgerViolations(ctx, db.Pool, s.EventID)
	require.NoError(t, err)
	require.Empty(t, sound, "the fixture itself must be sound")
	c := testinvariants.CheckLedger(t, db.Pool, s.EventID)
	assert.Equal(t, testinvariants.Counts{Total: 10, Available: 6, Held: 2, Sold: 2}, c)

	cases := []struct {
		rule    string
		corrupt []string
		args    [][]any
	}{
		{"a SOLD seat has exactly one PAID booking item",
			[]string{`DELETE FROM tickets WHERE booking_id = $1`, `DELETE FROM booking_items WHERE booking_id = $1 AND event_seat_id = $2`},
			[][]any{{s.PaidBooking}, {s.PaidBooking, s.Seats[0]}}},
		{"a SOLD seat was sold through a CONVERTED hold",
			[]string{`UPDATE holds SET status = 'EXPIRED' WHERE id = $1`}, [][]any{{s.PaidHold}}},
		{"a SOLD seat has exactly one live ticket",
			[]string{`UPDATE tickets SET status = 'VOID' WHERE id = $1::uuid`}, [][]any{{s.TicketOfSeat[s.Seats[0]]}}},
		{"every seat of a PAID booking is SOLD through the booking's hold",
			[]string{`UPDATE event_seats SET status = 'AVAILABLE', hold_id = NULL WHERE id = $1`}, [][]any{{s.Seats[1]}}},
		{"no hold has both HELD and SOLD seats",
			[]string{`UPDATE event_seats SET status = 'SOLD', hold_expires_at = NULL WHERE id = $1`}, [][]any{{s.Seats[2]}}},
		{"a live hold holds exactly the seats of its live booking",
			[]string{`UPDATE event_seats SET status = 'AVAILABLE', hold_id = NULL, hold_expires_at = NULL WHERE id = $1`}, [][]any{{s.Seats[3]}}},
		{"a PAID booking's total is its items plus its fee",
			[]string{`UPDATE booking_items SET unit_price_cents = 90000 WHERE booking_id = $1 AND event_seat_id = $2`}, [][]any{{s.PaidBooking, s.Seats[0]}}},
		{"a PAID booking has exactly one SUCCESS payment that bought it, for its total",
			[]string{`UPDATE payments SET amount_cents = 190000 WHERE id = $1`}, [][]any{{s.Payment}}},
		{"a PAID booking has exactly one SUCCESS payment that bought it, for its total",
			[]string{`INSERT INTO payments (booking_id, amount_cents, phone_e164, status, checkout_request_id) VALUES ($1, 200000, '+254711000001', 'SUCCESS', 'ws_CO_second')`},
			[][]any{{s.PaidBooking}}},
		{"a PAID booking has exactly one SUCCESS payment that bought it, for its total",
			[]string{`UPDATE payments SET status = 'FAILED' WHERE id = $1`}, [][]any{{s.Payment}}},
		{"a PAID booking has one live ticket per item",
			[]string{`UPDATE tickets SET booking_id = $1 WHERE id = $2::uuid`}, [][]any{{s.LiveBooking, s.TicketOfSeat[s.Seats[0]]}}},
		{"no seat has more than one live ticket",
			[]string{`DROP INDEX tickets_live_seat_idx`,
				`INSERT INTO tickets (booking_id, event_seat_id, event_id, gate_group, key_id, token_nonce, qr_token) VALUES ($1, $2, $3, 'N-LOWER', 'k1', '\x00', 'TLT1.dup.sig')`},
			[][]any{{}, {s.PaidBooking, s.Seats[0], s.EventID}}},
		{"a live ticket belongs to a PAID booking",
			[]string{`UPDATE bookings SET status = 'REFUND_REQUIRED' WHERE id = $1`}, [][]any{{s.PaidBooking}}},
		{"a booking that is not PAID owns no SOLD seat",
			[]string{`UPDATE bookings SET status = 'EXPIRED' WHERE id = $1`}, [][]any{{s.PaidBooking}}},
		{"money that bought nothing is queued for refund",
			[]string{`UPDATE bookings SET status = 'REFUND_REQUIRED' WHERE id = $1`}, [][]any{{s.PaidBooking}}},
		{"a booking has at most one live payment",
			[]string{`DROP INDEX payments_live_booking_idx`,
				`INSERT INTO payments (booking_id, amount_cents, phone_e164, status) VALUES ($1, 200000, '+254711000001', 'PENDING'), ($1, 200000, '+254711000001', 'INITIATED')`},
			[][]any{{}, {s.LiveBooking}}},
	}

	proven := map[string]bool{}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("%02d %s", i, tc.rule), func(t *testing.T) {
			tx, err := db.Pool.Begin(ctx)
			require.NoError(t, err)
			defer func() { _ = tx.Rollback(ctx) }()
			for k, sql := range tc.corrupt {
				_, err := tx.Exec(ctx, sql, tc.args[k]...)
				require.NoError(t, err, sql)
			}
			found, err := testinvariants.LedgerViolations(ctx, tx, s.EventID)
			require.NoError(t, err)
			fired := false
			for _, v := range found {
				fired = fired || strings.HasPrefix(v, tc.rule+": ")
			}
			require.True(t, fired, "rule %q did not report the corrupt state; reported: %v", tc.rule, found)
			proven[tc.rule] = true

			// And CheckLedger turns it into a test failure.
			rec := &recorder{TB: t}
			testinvariants.CheckLedger(rec, tx, s.EventID)
			require.NotEmpty(t, rec.errors)
			// The plain inventory checker does not look at the ledger rules.
			plain, err := testinvariants.Violations(ctx, tx, s.EventID)
			require.NoError(t, err)
			for _, v := range plain {
				assert.False(t, strings.HasPrefix(v, tc.rule+": "), "inventory rules must not include ledger rule %q", tc.rule)
			}
		})
	}
	for _, name := range testinvariants.LedgerRuleNames() {
		assert.True(t, proven[name], "ledger rule %q has no corrupt state that proves it fires", name)
	}

	// sold + held + available + blocked = total: a seat in none of the four statuses.
	t.Run("the four counts add up to every seat", func(t *testing.T) {
		tx, err := db.Pool.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = tx.Exec(ctx, `ALTER TABLE event_seats DROP CONSTRAINT event_seats_status_check`)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `UPDATE event_seats SET status = 'LOST' WHERE id = $1`, s.Seats[9])
		require.NoError(t, err)
		rec := &recorder{TB: t}
		testinvariants.CheckLedger(rec, tx, s.EventID)
		joined := strings.Join(rec.errors, "\n")
		assert.Contains(t, joined, "but the event has 10 seats")
		assert.Contains(t, joined, "every seat has one of the four statuses")
	})

	// A cancelled event keeps its sold seats SOLD with void tickets and bookings owed a
	// refund; the ledger rules know that and do not report it.
	t.Run("a cancelled event's sold seats are not reported", func(t *testing.T) {
		tx, err := db.Pool.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		for _, sql := range []string{
			`UPDATE events SET status = 'CANCELLED' WHERE id = $1`,
			`UPDATE holds SET status = 'RELEASED' WHERE event_id = $1 AND status = 'ACTIVE'`,
			`UPDATE event_seats SET status = 'AVAILABLE', hold_id = NULL, hold_expires_at = NULL WHERE event_id = $1 AND status = 'HELD'`,
			`UPDATE tickets SET status = 'VOID' WHERE event_id = $1`,
			`UPDATE bookings SET status = 'CANCELLED' WHERE event_id = $1 AND status = 'PENDING_PAYMENT'`,
			`INSERT INTO refunds (booking_id, payment_id, amount_cents, reason) SELECT p.booking_id, p.id, p.amount_cents, 'cancelled' FROM payments p JOIN bookings b ON b.id = p.booking_id WHERE b.event_id = $1`,
			`UPDATE bookings SET status = 'REFUND_REQUIRED' WHERE event_id = $1 AND status = 'PAID'`,
		} {
			_, err := tx.Exec(ctx, sql, s.EventID)
			require.NoError(t, err, sql)
		}
		found, err := testinvariants.LedgerViolations(ctx, tx, s.EventID)
		require.NoError(t, err)
		assert.Empty(t, found)
	})
}
