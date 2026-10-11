//go:build integration

package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/events"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi/contracttest"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/testinvariants"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/venue"
)

// cloneSeededEvent makes a second on-sale event at the seeded venue with the same categories
// and the same inventory as the seeder's published one, every priced seat AVAILABLE. Copying
// 60,000 rows takes a second or two; publishing them through the API takes half a minute.
// The tests that sell or cancel a whole stadium use a clone so that the seeder's own event
// stays as the other tests expect it.
func cloneSeededEvent(t *testing.T, sv seededVenue) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO events (venue_id, title, competition, home_team, away_team, kickoff_at, gates_open_at,
		                    sales_open_at, sales_close_at, max_tickets_per_user, status, published_at)
		SELECT venue_id, 'Sold-out stand-in ' || $2, competition, home_team, away_team, now() + interval '30 days',
		       now() + interval '30 days' - interval '3 hours', now() - interval '1 day', now() + interval '30 days', 6, 'ON_SALE', now()
		FROM events WHERE id = $1 RETURNING id`, sv.OnSale, randDigits(t, 8)).Scan(&id))
	exec(t, `INSERT INTO event_categories (event_id, name, price_cents, color_hex, description)
	         SELECT $2, name, price_cents, color_hex, description FROM event_categories WHERE event_id = $1`, sv.OnSale, id)
	exec(t, `
		INSERT INTO event_seats (event_id, seat_id, block_id, category_id, status)
		SELECT $2, es.seat_id, es.block_id, nc.id, CASE WHEN nc.id IS NULL THEN 'BLOCKED' ELSE 'AVAILABLE' END
		FROM event_seats es
		LEFT JOIN event_categories oc ON oc.id = es.category_id
		LEFT JOIN event_categories nc ON nc.event_id = $2 AND nc.name = oc.name
		WHERE es.event_id = $1`, sv.OnSale, id)
	return id
}

// soldOut is what sellOut wrote.
type soldOut struct {
	Sold, PaidBookings, Held, Holds int
	PaidCents                       int64
}

// sellOut sells `sold` seats of an event the way the payment flow leaves them: SOLD seats,
// PAID bookings of four seats with their items, one SUCCESS payment per booking and one VALID
// ticket per seat. It then puts `holds` live holds of two seats each on top, each with an
// unpaid booking. Everything is INSERT … SELECT, so a whole stadium takes seconds.
func sellOut(t *testing.T, eventID, userID int64, sold, holds int) soldOut {
	t.Helper()
	exec(t, `UPDATE event_seats SET status = 'SOLD' WHERE id IN (
	             SELECT id FROM event_seats WHERE event_id = $1 AND status = 'AVAILABLE' ORDER BY id LIMIT $2)`, eventID, sold)
	const groups = `
		WITH s AS (
		    SELECT es.id, es.category_id, ec.price_cents, (row_number() OVER (ORDER BY es.id) - 1) / 4 AS g
		    FROM event_seats es JOIN event_categories ec ON ec.id = es.category_id
		    WHERE es.event_id = $1 AND es.status = 'SOLD')`
	exec(t, groups+`
		INSERT INTO bookings (reference, event_id, user_id, status, subtotal_cents, fee_cents, total_cents, idempotency_key, paid_at)
		SELECT 'SC' || $1::bigint::text || '-' || g, $1, $2, 'PAID', sum(price_cents), 0, sum(price_cents), 'scale-' || $1::bigint::text || '-' || g, now()
		FROM s GROUP BY g`, eventID, userID)
	exec(t, groups+`
		INSERT INTO booking_items (booking_id, event_seat_id, category_id, unit_price_cents)
		SELECT b.id, s.id, s.category_id, s.price_cents
		FROM s JOIN bookings b ON b.reference = 'SC' || $1::bigint::text || '-' || s.g`, eventID)
	exec(t, `
		INSERT INTO payments (booking_id, amount_cents, phone_e164, status, checkout_request_id, mpesa_receipt, completed_at)
		SELECT id, total_cents, '+254700000009', 'SUCCESS', 'ws_CO_scale_' || id, 'RSC' || id, now()
		FROM bookings WHERE event_id = $1 AND status = 'PAID'`, eventID)
	exec(t, `
		INSERT INTO tickets (booking_id, event_seat_id, token_nonce, event_id, gate_group, key_id)
		SELECT bi.booking_id, bi.event_seat_id, '\x01', $1, 'N-LOWER', 'k1'
		FROM booking_items bi JOIN bookings b ON b.id = bi.booking_id
		WHERE b.event_id = $1`, eventID)

	exec(t, `INSERT INTO holds (event_id, user_id, expires_at) SELECT $1, $2, now() + interval '10 minutes' FROM generate_series(1, $3)`, eventID, userID, holds)
	exec(t, `
		WITH h AS (
		    SELECT id, row_number() OVER (ORDER BY id) - 1 AS n FROM holds WHERE event_id = $1 AND status = 'ACTIVE'
		), s AS (
		    SELECT id, (row_number() OVER (ORDER BY id) - 1) / 2 AS n
		    FROM (SELECT id FROM event_seats WHERE event_id = $1 AND status = 'AVAILABLE' ORDER BY id LIMIT $2) free
		)
		UPDATE event_seats es SET status = 'HELD', hold_id = h.id, hold_expires_at = now() + interval '10 minutes'
		FROM s JOIN h ON h.n = s.n WHERE es.id = s.id`, eventID, holds*2)
	exec(t, `
		INSERT INTO bookings (reference, event_id, user_id, hold_id, status, subtotal_cents, fee_cents, total_cents, idempotency_key)
		SELECT 'SP' || $1::bigint::text || '-' || id, $1, user_id, id, 'PENDING_PAYMENT', 200000, 0, 200000, 'scale-pending-' || id
		FROM holds WHERE event_id = $1 AND status = 'ACTIVE'`, eventID)
	// A production database has statistics for tables this size; a freshly filled test one
	// would plan the cancel statements as if they were empty.
	exec(t, `ANALYZE event_seats, holds, bookings, booking_items, payments, tickets, refunds`)

	out := soldOut{
		Sold:         queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1 AND status = 'SOLD'`, eventID),
		PaidBookings: queryInt(t, `SELECT count(*) FROM bookings WHERE event_id = $1 AND status = 'PAID'`, eventID),
		Held:         queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1 AND status = 'HELD'`, eventID),
		Holds:        queryInt(t, `SELECT count(*) FROM holds WHERE event_id = $1 AND status = 'ACTIVE'`, eventID),
	}
	require.NoError(t, db.Pool.QueryRow(context.Background(),
		`SELECT COALESCE(sum(total_cents), 0) FROM bookings WHERE event_id = $1 AND status = 'PAID'`, eventID).Scan(&out.PaidCents))
	require.Equal(t, sold, out.Sold)
	require.Equal(t, holds, out.Holds)
	require.Equal(t, holds*2, out.Held)
	require.Equal(t, sold, queryInt(t, `SELECT count(*) FROM tickets WHERE event_id = $1 AND status = 'VALID'`, eventID))
	return out
}

// Fix 3. Cancelling a sold-out stadium: every seat of the seeded venue sold or held, with
// its bookings, payments and tickets. The cancel is one transaction, is measured, and must
// get through the real router on a pool whose statement and request limits are far below
// what it takes, because (like publish) it runs under its own.
func TestSeededVenue_CancelASoldOutEvent(t *testing.T) {
	sv := seededTalanta(t)
	ctx := context.Background()
	e := newEnv(t)
	fanID, _ := e.newFan()
	eventID := cloneSeededEvent(t, sv)
	const holds = 1000
	sellable := queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1 AND status = 'AVAILABLE'`, eventID)
	require.Greater(t, sellable, 50_000, "the full venue")
	setup := time.Now()
	was := sellOut(t, eventID, fanID, sellable-2*holds, holds)
	t.Logf("set up %d sold seats in %d paid bookings, %d tickets, %d live holds on %d seats: %s",
		was.Sold, was.PaidBookings, was.Sold, was.Holds, was.Held, time.Since(setup).Round(100*time.Millisecond))

	// The ordinary limits, well under the measured cancel (see docs/DECISIONS.md), on a pool
	// and a router of their own. Cancel must outlast both.
	const ordinary = 1500 * time.Millisecond
	pool, err := store.OpenWithOptions(ctx, db.URL, store.Options{StatementTimeout: ordinary, IdleInTxTimeout: ordinary})
	require.NoError(t, err)
	defer pool.Close()
	tokens := auth.NewTokens([]byte(testSecret), nil)
	policy := events.DefaultPolicy()
	policy.CancelTimeout = 3 * time.Minute
	svc, err := events.NewService(events.Config{DB: pool, Policy: policy})
	require.NoError(t, err)
	router := httpapi.NewRouter(httpapi.Deps{
		DB: pool, Tokens: tokens, Events: svc, Venues: venue.NewService(pool),
		RequestTimeout: ordinary, PublishTimeout: policy.CancelTimeout,
	})
	token, err := tokens.IssueAccess(1, domain.RoleOrganiser, "test-session")
	require.NoError(t, err)
	contract := contracttest.Load(t)
	cancel := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", fmt.Sprintf("/v1/admin/events/%d/cancel", eventID), nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return contract.Do(t, router, req)
	}
	_, err = pool.Exec(ctx, `SELECT pg_sleep(3)`)
	require.Error(t, err, "the pool's statement timeout must be in force for this test to mean anything")

	start := time.Now()
	o := want(t, cancel(), http.StatusOK)
	took := time.Since(start)
	t.Logf("cancel of a sold-out event (%d tickets, %d bookings, %d holds) through the API: %s (statement and request timeouts elsewhere: %s)",
		was.Sold, was.PaidBookings, was.Holds, took.Round(10*time.Millisecond), ordinary)
	assert.Greater(t, took, ordinary, "if cancel ever gets this fast the test no longer proves anything; lower `ordinary`")

	assert.Equal(t, "CANCELLED", o["event"].(obj)["status"])
	assert.EqualValues(t, was.Sold, o["tickets_voided"])
	assert.EqualValues(t, was.Holds, o["holds_released"])
	assert.EqualValues(t, was.Holds, o["bookings_cancelled"], "the unpaid booking of each hold")
	assert.EqualValues(t, was.PaidBookings, o["refunds_queued"], "one per successful payment")
	assert.EqualValues(t, was.PaidCents, o["refund_total_cents"])

	// All of it happened, in the one transaction: nothing is left half done.
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM tickets WHERE event_id = $1 AND status <> 'VOID'`, eventID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM holds WHERE event_id = $1 AND status = 'ACTIVE'`, eventID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1 AND (status = 'HELD' OR (hold_id IS NOT NULL AND status <> 'SOLD'))`, eventID))
	assert.Equal(t, was.Held, queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1 AND status = 'AVAILABLE'`, eventID), "the held seats went back")
	assert.Equal(t, was.PaidBookings, queryInt(t, `SELECT count(*) FROM bookings WHERE event_id = $1 AND status = 'REFUND_REQUIRED'`, eventID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM bookings WHERE event_id = $1 AND status IN ('PAID', 'PENDING_PAYMENT')`, eventID))
	assert.Equal(t, was.PaidBookings, queryInt(t, `SELECT count(*) FROM refunds r JOIN bookings b ON b.id = r.booking_id WHERE b.event_id = $1 AND r.status = 'PENDING'`, eventID))
	var refundCents int64
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT COALESCE(sum(r.amount_cents), 0) FROM refunds r JOIN bookings b ON b.id = r.booking_id WHERE b.event_id = $1`, eventID).Scan(&refundCents))
	assert.Equal(t, was.PaidCents, refundCents, "money in integer cents: every shilling paid is queued for refund")
	testinvariants.Check(t, db.Pool, eventID)

	// Safe to repeat, and the repeat is quick: nothing more happens.
	start = time.Now()
	again := want(t, cancel(), http.StatusOK)
	t.Logf("second cancel (no-op): %s", time.Since(start).Round(time.Millisecond))
	for _, k := range []string{"holds_released", "tickets_voided", "bookings_cancelled", "refunds_queued", "refund_total_cents"} {
		assert.EqualValues(t, 0, again[k], k)
	}
	assert.Equal(t, was.PaidBookings, queryInt(t, `SELECT count(*) FROM refunds r JOIN bookings b ON b.id = r.booking_id WHERE b.event_id = $1`, eventID), "no refund is queued twice")
}
