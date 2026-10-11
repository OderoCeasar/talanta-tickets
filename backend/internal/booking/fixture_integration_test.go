//go:build integration

package booking_test

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/booking"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/testdb"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/testinvariants"
)

// One Postgres for the package. Every test builds its own venue, event and users, so tests
// share no rows and do not depend on the order they run in.
var (
	db *testdb.DB
	// pool is wider than testdb's default so that the concurrency tests really do run many
	// transactions at once: up to 64 connections, each in its own transaction.
	pool *pgxpool.Pool
	// adminPool is the organiser's side (cancel) in the race tests. It is separate so a
	// cancel is not queued behind a thousand hold requests waiting for a connection of the
	// hold pool: what those tests measure is the wait for the event's row lock.
	adminPool *pgxpool.Pool
)

const poolSize = 64

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	var err error
	db, err = testdb.Start(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "testdb:", err)
		return 1
	}
	defer db.Close()
	if err := db.MigrateUp(); err != nil {
		fmt.Fprintln(os.Stderr, "testdb:", err)
		return 1
	}
	cfg, err := pgxpool.ParseConfig(db.URL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pool:", err)
		return 1
	}
	cfg.MaxConns = poolSize
	// The same limits the API runs with: a statement that waits for a lock longer than
	// this fails the test instead of hanging it.
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "15000"
	cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = "15000"
	pool, err = pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pool:", err)
		return 1
	}
	defer pool.Close()
	adminCfg := cfg.Copy()
	adminCfg.MaxConns = 4
	adminPool, err = pgxpool.NewWithConfig(context.Background(), adminCfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pool:", err)
		return 1
	}
	defer adminPool.Close()
	return m.Run()
}

func newService(t testing.TB, mods ...func(*booking.Policy)) *booking.Service {
	t.Helper()
	p := booking.DefaultPolicy()
	for _, m := range mods {
		m(&p)
	}
	svc, err := booking.NewService(booking.Config{DB: pool, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Policy: p})
	require.NoError(t, err)
	return svc
}

func randDigits(t testing.TB, n int) string {
	t.Helper()
	out := make([]byte, n)
	for i := range out {
		d, err := rand.Int(rand.Reader, big.NewInt(10))
		require.NoError(t, err)
		out[i] = byte('0' + d.Int64())
	}
	return string(out)
}

func exec(t testing.TB, sql string, args ...any) pgconn.CommandTag {
	t.Helper()
	tag, err := pool.Exec(context.Background(), sql, args...)
	require.NoError(t, err)
	return tag
}

func queryInt(t testing.TB, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func queryText(t testing.TB, sql string, args ...any) string {
	t.Helper()
	var s string
	require.NoError(t, pool.QueryRow(context.Background(), sql, args...).Scan(&s))
	return s
}

func queryIDs(t testing.TB, sql string, args ...any) []int64 {
	t.Helper()
	rows, err := pool.Query(context.Background(), sql, args...)
	require.NoError(t, err)
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	require.NoError(t, err)
	return ids
}

// fixture is a small venue with one published, on-sale event: `blocks` blocks in one stand,
// each rows × seatsPerRow, every seat in the one category "Regular".
type fixture struct {
	t          testing.TB
	VenueID    int64
	EventID    int64
	CategoryID int64
	BlockIDs   []int64
	// Seats[b][r][s] is the event seat id of block b, row r (front first), seat s (number s+1).
	Seats [][][]int64
}

type fixtureOpts struct {
	Blocks, Rows, SeatsPerRow int
	MaxPerUser                int
}

func newFixture(t testing.TB, o fixtureOpts) *fixture {
	t.Helper()
	ctx := context.Background()
	if o.Blocks == 0 {
		o.Blocks = 1
	}
	if o.MaxPerUser == 0 {
		o.MaxPerUser = 6
	}
	f := &fixture{t: t}
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	require.NoError(t, tx.QueryRow(ctx,
		`INSERT INTO venues (name, city, capacity, plan_width, plan_height) VALUES ($1, 'Nairobi', $2, 1000, 1000) RETURNING id`,
		"Hold Ground "+randDigits(t, 14), o.Blocks*o.Rows*o.SeatsPerRow).Scan(&f.VenueID))
	var standID int64
	require.NoError(t, tx.QueryRow(ctx,
		`INSERT INTO stands (venue_id, code, name, tier, display_order) VALUES ($1, 'N', 'North Lower', 'LOWER', 1) RETURNING id`,
		f.VenueID).Scan(&standID))
	for b := 0; b < o.Blocks; b++ {
		var blockID int64
		require.NoError(t, tx.QueryRow(ctx, `
			INSERT INTO blocks (stand_id, code, label, origin_x, origin_y, seat_pitch_x, seat_pitch_y, row_count, seats_per_row)
			VALUES ($1, $2, $3, $4, 100, 5, 8, $5, $6) RETURNING id`,
			standID, fmt.Sprintf("N%d", b+1), fmt.Sprintf("North %d", b+1), 100+b*200, o.Rows, o.SeatsPerRow).Scan(&blockID))
		f.BlockIDs = append(f.BlockIDs, blockID)
		_, err := tx.Exec(ctx, `
			INSERT INTO rows (block_id, label, row_index, seat_count)
			SELECT $1, chr(64 + i), i, $3 FROM generate_series(1, $2::int) i`, blockID, o.Rows, o.SeatsPerRow)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `
			INSERT INTO seats (row_id, block_id, seat_number, label, x, y)
			SELECT r.id, r.block_id, n, r.label || n, n * 5, r.row_index * 8
			FROM rows r, generate_series(1, $2::int) n WHERE r.block_id = $1`, blockID, o.SeatsPerRow)
		require.NoError(t, err)
	}

	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO events (venue_id, title, kickoff_at, status, sales_open_at, sales_close_at, max_tickets_per_user, published_at)
		VALUES ($1, $2, now() + interval '30 days', 'ON_SALE', now() - interval '1 hour', now() + interval '30 days', $3, now())
		RETURNING id`, f.VenueID, "Hold Match "+randDigits(t, 10), o.MaxPerUser).Scan(&f.EventID))
	require.NoError(t, tx.QueryRow(ctx,
		`INSERT INTO event_categories (event_id, name, price_cents, color_hex) VALUES ($1, 'Regular', 100000, '#059669') RETURNING id`,
		f.EventID).Scan(&f.CategoryID))
	_, err = tx.Exec(ctx, `INSERT INTO event_block_categories (event_id, block_id, category_id) SELECT $1, unnest($2::bigint[]), $3`,
		f.EventID, f.BlockIDs, f.CategoryID)
	require.NoError(t, err)
	n, err := store.PublishEventSeats(ctx, tx, f.EventID)
	require.NoError(t, err)
	require.EqualValues(t, o.Blocks*o.Rows*o.SeatsPerRow, n)

	rows, err := tx.Query(ctx, `
		SELECT es.id, es.block_id, r.row_index FROM event_seats es
		JOIN seats s ON s.id = es.seat_id JOIN rows r ON r.id = s.row_id
		WHERE es.event_id = $1 ORDER BY es.block_id, r.row_index, s.seat_number`, f.EventID)
	require.NoError(t, err)
	blockAt := map[int64]int{}
	for i, id := range f.BlockIDs {
		blockAt[id] = i
	}
	f.Seats = make([][][]int64, o.Blocks)
	for b := range f.Seats {
		f.Seats[b] = make([][]int64, o.Rows)
	}
	for rows.Next() {
		var id, blockID int64
		var rowIndex int
		require.NoError(t, rows.Scan(&id, &blockID, &rowIndex))
		b := blockAt[blockID]
		f.Seats[b][rowIndex-1] = append(f.Seats[b][rowIndex-1], id)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, tx.Commit(ctx))

	// Whatever the test did, the inventory must be sound when it ends.
	t.Cleanup(func() { testinvariants.Check(t, pool, f.EventID) })
	return f
}

// row returns the event seat ids of one row of the first block.
func (f *fixture) row(r int) []int64 { return f.Seats[0][r] }

// all returns every event seat id of the fixture.
func (f *fixture) all() []int64 {
	var out []int64
	for _, b := range f.Seats {
		for _, r := range b {
			out = append(out, r...)
		}
	}
	return out
}

// check runs the invariant checker now and returns the seat counts.
func (f *fixture) check() testinvariants.Counts {
	f.t.Helper()
	return testinvariants.Check(f.t, pool, f.EventID)
}

// phoneSeq hands out phone numbers that are unique within the test binary (the database is
// its own), so two tests can never collide on users.phone_e164.
var phoneSeq atomic.Int64

// newUsers inserts n fans and returns their ids.
func newUsers(t testing.TB, n int) []int64 {
	t.Helper()
	first := phoneSeq.Add(int64(n)) - int64(n)
	return queryIDs(t, `
		INSERT INTO users (phone_e164, role)
		SELECT '+2547' || lpad((i + $1::bigint)::text, 8, '0'), 'FAN' FROM generate_series(0, $2::int - 1) i
		RETURNING id`, first, n)
}

func newUser(t testing.TB) int64 { return newUsers(t, 1)[0] }

// lapse moves a hold's expiry (on the hold and on its seats) into the past, as if its ten
// minutes had run out. Nothing else is touched: the hold stays ACTIVE and its seats HELD,
// exactly what the database looks like before any sweeper has run.
func lapse(t testing.TB, holdID int64) {
	t.Helper()
	setExpiry(t, holdID, -time.Minute)
}

// setExpiry moves a hold's expiry to now + in, on the hold and on its seats.
func setExpiry(t testing.TB, holdID int64, in time.Duration) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	var at time.Time
	require.NoError(t, tx.QueryRow(ctx,
		`UPDATE holds SET expires_at = clock_timestamp() + make_interval(secs => $2) WHERE id = $1 RETURNING expires_at`,
		holdID, in.Seconds()).Scan(&at))
	_, err = tx.Exec(ctx, `UPDATE event_seats SET hold_expires_at = $2 WHERE hold_id = $1 AND status = 'HELD'`, holdID, at)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))
}

func holdStatus(t testing.TB, holdID int64) string {
	t.Helper()
	return queryText(t, `SELECT status FROM holds WHERE id = $1`, holdID)
}

// seatState is what one inventory row says.
type seatState struct {
	Status string
	HoldID *int64
}

func seatStates(t testing.TB, ids []int64) map[int64]seatState {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT id, status, hold_id FROM event_seats WHERE id = ANY($1)`, ids)
	require.NoError(t, err)
	out := map[int64]seatState{}
	for rows.Next() {
		var id int64
		var s seatState
		require.NoError(t, rows.Scan(&id, &s.Status, &s.HoldID))
		out[id] = s
	}
	require.NoError(t, rows.Err())
	return out
}

// insertBooking writes a booking for a hold's seats directly (the booking flow is the next
// milestone) and returns its id.
func insertBooking(t testing.TB, f *fixture, userID int64, holdID *int64, status string, seatIDs ...int64) int64 {
	t.Helper()
	var id int64
	total := int64(len(seatIDs)) * 100000
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO bookings (reference, event_id, user_id, hold_id, status, subtotal_cents, fee_cents, total_cents, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, $6, 0, $6, $7) RETURNING id`,
		"TLT-"+randDigits(t, 8), f.EventID, userID, holdID, status, total, "key-"+randDigits(t, 16)).Scan(&id))
	for _, seat := range seatIDs {
		exec(t, `INSERT INTO booking_items (booking_id, event_seat_id, category_id, unit_price_cents) VALUES ($1, $2, $3, 100000)`,
			id, seat, f.CategoryID)
	}
	return id
}

func insertPayment(t testing.TB, bookingID int64, status string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(), `
		INSERT INTO payments (booking_id, amount_cents, phone_e164, status, checkout_request_id)
		VALUES ($1, 100000, '+254700000009', $2, $3) RETURNING id`,
		bookingID, status, "ws_CO_"+randDigits(t, 16)).Scan(&id))
	return id
}

// outcome is how one request of a storm ended.
type outcome struct {
	Hold booking.Hold
	Err  error
}

// storm runs n requests at once, each in its own goroutine, released together by one gate so
// they really do overlap, and returns their outcomes in request order. It fails the test if
// they have not all finished within the deadline: a deadlock that Postgres did not detect, or
// a lock wait, shows up here.
func storm(t testing.TB, n int, deadline time.Duration, fn func(i int) (booking.Hold, error)) []outcome {
	t.Helper()
	out := make([]outcome, n)
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			h, err := fn(i)
			out[i] = outcome{Hold: h, Err: err}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	close(gate)
	select {
	case <-done:
	case <-time.After(deadline):
		t.Fatalf("%d parallel requests did not finish within %s: something is blocked", n, deadline)
	}
	return out
}

// tally counts outcomes by error code ("" for success).
func tally(outs []outcome) map[domain.Code]int {
	m := map[domain.Code]int{}
	for _, o := range outs {
		if o.Err == nil {
			m[""]++
			continue
		}
		m[domain.CodeOf(o.Err)]++
	}
	return m
}

// unavailable returns the unavailable_seat_ids of a SEATS_UNAVAILABLE error.
func unavailable(t testing.TB, err error) []int64 {
	t.Helper()
	var de *domain.Error
	require.True(t, errors.As(err, &de), "want a domain error, got %v", err)
	require.Equal(t, domain.CodeSeatsUnavailable, de.Code, "%v", err)
	d, ok := de.Details.(domain.SeatsUnavailableDetails)
	require.True(t, ok, "details are %T", de.Details)
	return d.UnavailableSeatIDs
}

// noDeadlocks fails the test if any outcome is Postgres's "deadlock detected".
func noDeadlocks(t testing.TB, outs []outcome) {
	t.Helper()
	for i, o := range outs {
		if store.IsDeadlock(o.Err) {
			t.Fatalf("request %d: deadlock: %v", i, o.Err)
		}
	}
}

func timeout(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}
