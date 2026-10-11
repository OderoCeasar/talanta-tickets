//go:build integration

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/seatmap"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/testdb"
)

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func eventID(t *testing.T, pool *pgxpool.Pool, title string) int64 {
	t.Helper()
	var id int64
	require.NoError(t, pool.QueryRow(context.Background(), `SELECT id FROM events WHERE title = $1`, title).Scan(&id))
	return id
}

// seatsWithoutExactlyOneRow counts the venue's seats whose number of event_seats rows for the
// event is wrong: an active seat must have exactly one, an inactive seat none.
func seatsWithoutExactlyOneRow(t *testing.T, pool *pgxpool.Pool, event int64) int64 {
	t.Helper()
	return count(t, pool, `
		SELECT count(*)
		FROM seats s
		JOIN blocks b ON b.id = s.block_id
		JOIN stands st ON st.id = b.stand_id
		JOIN events e ON e.venue_id = st.venue_id AND e.id = $1
		WHERE (SELECT count(*) FROM event_seats es WHERE es.event_id = e.id AND es.seat_id = s.id)
		      <> CASE WHEN s.is_active THEN 1 ELSE 0 END`, event)
}

// One database for the whole story, because each step needs the state the one before left:
// a production seed, a development seed on top, a repeat, then publishing and the query plans
// on the seeded data, and last the migrations taken down and up with real rows in the tables.
func TestSeed(t *testing.T) {
	ctx := context.Background()
	db := testdb.New(t)
	pool := db.Pool
	now := time.Now()

	prod, err := Seed(ctx, pool, Options{Production: true, Now: now})
	require.NoError(t, err)
	t.Logf("first run: layout %s, total %s, seat checks batched: %v", prod.Layout, prod.Total, prod.SeatChecksBatched)

	t.Run("production run creates the venue but no demo users", func(t *testing.T) {
		assert.True(t, prod.DemoUsersRefused)
		assert.Zero(t, prod.Count("users").Total, "demo users have public passwords and must not exist in production")
		assert.EqualValues(t, 59580, prod.Count("seats").Inserted)
	})

	dev, err := Seed(ctx, pool, Options{Now: now})
	require.NoError(t, err)

	t.Run("development run adds only the users", func(t *testing.T) {
		assert.False(t, dev.DemoUsersRefused)
		assert.EqualValues(t, 4, dev.Count("users").Inserted)
		assert.EqualValues(t, 4, dev.Inserted(), "everything else was already there")
		assert.EqualValues(t, 1, count(t, pool, `SELECT count(*) FROM users WHERE role = 'FAN' AND phone_e164 = $1`, demoFanPhone))
		assert.EqualValues(t, 1, count(t, pool, `SELECT count(*) FROM users WHERE role = 'GATE_STAFF' AND phone_e164 = $1`, demoGatePhone))
		assert.EqualValues(t, 4, count(t, pool, `SELECT count(DISTINCT role) FROM users`))

		admin, err := store.New(pool).GetUserByEmail(ctx, strings.ToUpper(demoAdminEmail))
		require.NoError(t, err, "email lookup must ignore case")
		assert.Equal(t, "ADMIN", admin.Role)
		assert.Nil(t, admin.PhoneE164)
		assert.False(t, admin.TotpEnabled, "TOTP is optional and off for the demo logins")
		assert.Nil(t, admin.TotpSecret)
		require.NotNil(t, admin.PasswordHash)
		ok, err := auth.VerifyPassword(demoAdminPass, *admin.PasswordHash)
		require.NoError(t, err)
		assert.True(t, ok, "the printed password must be the one that was stored")
	})

	again, err := Seed(ctx, pool, Options{Now: now.Add(48 * time.Hour)})
	require.NoError(t, err)

	t.Run("a second run inserts nothing", func(t *testing.T) {
		assert.Zero(t, again.Inserted())
		for _, table := range seededTables {
			assert.Zero(t, again.Count(table).Inserted, table)
			assert.Equal(t, dev.Count(table).Total, again.Count(table).Total, "row count of %s changed", table)
		}
	})

	t.Run("seeded shape", func(t *testing.T) {
		assert.EqualValues(t, 1, dev.Count("venues").Total)
		assert.EqualValues(t, 8, dev.Count("stands").Total)
		assert.EqualValues(t, 96, dev.Count("blocks").Total)
		assert.EqualValues(t, 59580, dev.Count("seats").Total)
		assert.EqualValues(t, 2, dev.Count("events").Total)
		assert.EqualValues(t, 8, dev.Count("event_categories").Total, "four categories for each event")
		assert.EqualValues(t, 59568, count(t, pool, `SELECT capacity FROM venues`), "capacity is the active seats")
		assert.Zero(t, count(t, pool, `SELECT count(*) FROM blocks WHERE jsonb_array_length(outline) < 4`))
		assert.Zero(t, count(t, pool, `SELECT count(*) FROM seats s JOIN rows r ON r.id = s.row_id WHERE r.block_id <> s.block_id`))
		assert.Zero(t, count(t, pool, `SELECT count(*) FROM rows r WHERE seat_count <> (SELECT count(*) FROM seats s WHERE s.row_id = r.id)`))
		assert.Zero(t, count(t, pool, `SELECT count(*) FROM events WHERE kickoff_at <= now()`), "demo matches are in the future")
		assert.EqualValues(t, 1, count(t, pool,
			`SELECT count(*) FROM events WHERE status = 'ON_SALE' AND sales_open_at < now() AND published_at IS NOT NULL`))
		assert.EqualValues(t, 1, count(t, pool, `SELECT count(*) FROM events WHERE status = 'DRAFT' AND published_at IS NULL`))
	})

	onSale := eventID(t, pool, onSaleEventTitle)
	draft := eventID(t, pool, draftEventTitle)
	active := count(t, pool, `SELECT count(*) FROM seats WHERE is_active`)
	require.EqualValues(t, 59568, active)

	t.Run("the seeded on-sale event has one row per active seat", func(t *testing.T) {
		assert.Equal(t, active, count(t, pool, `SELECT count(*) FROM event_seats WHERE event_id = $1`, onSale))
		assert.Zero(t, seatsWithoutExactlyOneRow(t, pool, onSale))
		assert.Zero(t, count(t, pool, `SELECT count(*) FROM event_seats WHERE event_id = $1 AND status <> 'AVAILABLE'`, onSale))
	})

	t.Run("publish creates one row per active seat and a second publish none", func(t *testing.T) {
		require.Zero(t, count(t, pool, `SELECT count(*) FROM event_seats WHERE event_id = $1`, draft))

		start := time.Now()
		inserted, err := store.PublishEventSeats(ctx, pool, draft)
		require.NoError(t, err)
		t.Logf("publish of %d seats took %s", inserted, time.Since(start))
		assert.Equal(t, active, inserted)
		assert.Equal(t, active, count(t, pool, `SELECT count(*) FROM event_seats WHERE event_id = $1`, draft))
		assert.Zero(t, seatsWithoutExactlyOneRow(t, pool, draft))

		inserted, err = store.PublishEventSeats(ctx, pool, draft)
		require.NoError(t, err)
		assert.Zero(t, inserted, "a second publish must insert nothing")
		assert.Equal(t, active, count(t, pool, `SELECT count(*) FROM event_seats WHERE event_id = $1`, draft))
		assert.Zero(t, seatsWithoutExactlyOneRow(t, pool, draft))

		// The other event was not touched.
		assert.Equal(t, active, count(t, pool, `SELECT count(*) FROM event_seats WHERE event_id = $1`, onSale))
	})

	t.Run("publish copies the block and resolves the category", func(t *testing.T) {
		assert.Zero(t, count(t, pool, `
			SELECT count(*) FROM event_seats es JOIN seats s ON s.id = es.seat_id
			WHERE es.event_id = $1 AND es.block_id <> s.block_id`, draft))
		assert.Zero(t, count(t, pool, `
			SELECT count(*) FROM event_seats es JOIN event_categories c ON c.id = es.category_id
			WHERE es.event_id = $1 AND c.event_id <> es.event_id`, draft))
		assert.Zero(t, count(t, pool,
			`SELECT count(*) FROM event_seats WHERE event_id = $1 AND (category_id IS NULL OR status <> 'AVAILABLE')`, draft),
			"the template paints every block")

		byCategory := func(blockCode string, rowIndexBelow int) map[string]int64 {
			rows, err := pool.Query(ctx, `
				SELECT c.name, count(*)
				FROM event_seats es
				JOIN event_categories c ON c.id = es.category_id
				JOIN seats s ON s.id = es.seat_id
				JOIN rows r ON r.id = s.row_id
				JOIN blocks b ON b.id = s.block_id
				WHERE es.event_id = $1 AND b.code = $2 AND r.row_index < $3
				GROUP BY c.name`, draft, blockCode, rowIndexBelow)
			require.NoError(t, err)
			defer rows.Close()
			out := map[string]int64{}
			for rows.Next() {
				var name string
				var n int64
				require.NoError(t, rows.Scan(&name, &n))
				out[name] = n
			}
			require.NoError(t, rows.Err())
			return out
		}

		// W105 and W106 are the centre of the West lower tier; W104 flanks them.
		assert.Equal(t, map[string]int64{catVVIP: 18 * 28}, byCategory("W105", 1000))
		assert.Equal(t, map[string]int64{catVIP: 18 * 28}, byCategory("W104", 1000))
		assert.Equal(t, map[string]int64{catTerrace: 19 * 31}, byCategory("N207", 1000))
		// W205: block is Regular, rows A–E are overridden to VIP. The row wins.
		assert.Equal(t, map[string]int64{catVIP: 18 * 5}, byCategory("W205", 5))
		assert.Equal(t, map[string]int64{catVIP: 18 * 5, catRegular: 18 * 26}, byCategory("W205", 1000))
	})

	t.Run("unpainted seats are published BLOCKED and other venues are left out", func(t *testing.T) {
		q := store.New(pool)
		venue, err := q.CreateVenue(ctx, store.CreateVenueParams{Name: "Training Ground", City: "Nairobi", PlanWidth: 100, PlanHeight: 100})
		require.NoError(t, err)
		stand, err := q.CreateStand(ctx, store.CreateStandParams{VenueID: venue.ID, Code: "M", Name: "Main", Tier: "LOWER", Polygon: []byte(`[]`)})
		require.NoError(t, err)

		params := seatmap.BlockParams{PitchX: 5, PitchY: 8, Rows: 2, SeatsPerRow: 3, Numbering: seatmap.LeftToRight}
		layout, err := seatmap.Generate(params)
		require.NoError(t, err)
		var blockIDs []int64
		for _, code := range []string{"M1", "M2"} {
			block, err := q.CreateBlock(ctx, store.CreateBlockParams{
				StandID: stand.ID, Code: code, Label: code, SeatPitchX: 5, SeatPitchY: 8,
				Outline: []byte(`[]`), RowCount: 2, SeatsPerRow: 3, NumberingDirection: "LTR",
			})
			require.NoError(t, err)
			blockIDs = append(blockIDs, block.ID)
			for _, r := range layout.Rows {
				var rowID int64
				require.NoError(t, pool.QueryRow(ctx,
					`INSERT INTO rows (block_id, label, row_index, seat_count) VALUES ($1, $2, $3, $4) RETURNING id`,
					block.ID, r.Label, r.Index, len(r.Seats)).Scan(&rowID))
				for _, s := range r.Seats {
					_, err := pool.Exec(ctx,
						`INSERT INTO seats (row_id, block_id, seat_number, label, x, y) VALUES ($1, $2, $3, $4, $5, $6)`,
						rowID, block.ID, s.Number, s.Label, s.X, s.Y)
					require.NoError(t, err)
				}
			}
		}

		kickoff := now.Add(72 * time.Hour)
		event, err := q.CreateEvent(ctx, store.CreateEventParams{VenueID: venue.ID, Title: "Friendly", KickoffAt: kickoff, MaxTicketsPerUser: 4})
		require.NoError(t, err)
		cat, err := q.CreateEventCategory(ctx, store.CreateEventCategoryParams{EventID: event.ID, Name: "Regular", PriceCents: 20_000, ColorHex: "#059669"})
		require.NoError(t, err)
		// Paint M1 only; M2 stays without a category.
		_, err = q.SetBlockCategories(ctx, store.SetBlockCategoriesParams{EventID: event.ID, CategoryID: cat.ID, BlockIds: blockIDs[:1]})
		require.NoError(t, err)

		unpainted, err := q.CountUnpaintedActiveSeats(ctx, event.ID)
		require.NoError(t, err)
		assert.EqualValues(t, 6, unpainted)

		inserted, err := store.PublishEventSeats(ctx, pool, event.ID)
		require.NoError(t, err)
		assert.EqualValues(t, 12, inserted, "only this venue's seats, not Talanta's")
		assert.EqualValues(t, 6, count(t, pool,
			`SELECT count(*) FROM event_seats WHERE event_id = $1 AND block_id = $2 AND status = 'AVAILABLE' AND category_id = $3`,
			event.ID, blockIDs[0], cat.ID))
		assert.EqualValues(t, 6, count(t, pool,
			`SELECT count(*) FROM event_seats WHERE event_id = $1 AND block_id = $2 AND status = 'BLOCKED' AND category_id IS NULL`,
			event.ID, blockIDs[1]))

		// The database itself refuses to make an unpriced seat sellable, or to price a seat
		// with another event's category.
		var pgErr *pgconn.PgError
		_, err = pool.Exec(ctx, `UPDATE event_seats SET status = 'AVAILABLE' WHERE event_id = $1 AND block_id = $2`, event.ID, blockIDs[1])
		require.ErrorAs(t, err, &pgErr)
		assert.Equal(t, "event_seats_priced_check", pgErr.ConstraintName)

		_, err = pool.Exec(ctx, `
			UPDATE event_seats SET category_id = (SELECT min(id) FROM event_categories WHERE event_id = $2)
			WHERE event_id = $1 AND block_id = $3`, event.ID, onSale, blockIDs[1])
		require.ErrorAs(t, err, &pgErr)
		assert.Equal(t, "event_seats_event_category_fkey", pgErr.ConstraintName)
	})

	t.Run("hot queries do not scan event_seats sequentially", func(t *testing.T) {
		// The largest block is the worst case for the planner: the more rows a query wants,
		// the more tempting a sequential scan becomes.
		var block int64
		var size int64
		require.NoError(t, pool.QueryRow(ctx, `
			SELECT block_id, count(*) FROM event_seats WHERE event_id = $1
			GROUP BY block_id ORDER BY count(*) DESC, block_id LIMIT 1`, onSale).Scan(&block, &size))
		require.Greater(t, size, int64(1200))
		_, err := pool.Exec(ctx, "ANALYZE event_seats")
		require.NoError(t, err)

		ex := &explainer{pool: pool}
		q := store.New(ex)

		seats, err := q.ListEventSeatsByBlock(ctx, store.ListEventSeatsByBlockParams{EventID: onSale, BlockID: block})
		require.NoError(t, err)
		assert.Len(t, seats, int(size))
		requireNoSeqScan(t, "block seat query", ex.take(t))

		_, err = q.ListEventSeatsChangedSince(ctx, store.ListEventSeatsChangedSinceParams{
			EventID: onSale, BlockID: block, Since: time.Now().Add(-5 * time.Second),
		})
		require.NoError(t, err)
		requireNoSeqScan(t, "changed-since query", ex.take(t))

		counts, err := q.CountEventSeatsByBlock(ctx, onSale)
		require.NoError(t, err)
		assert.NotEmpty(t, counts)
		requireNoSeqScan(t, "availability counts", ex.take(t))

		// The hold statement of docs/ARCHITECTURE.md §6.2, as the booking milestone will run
		// it. EXPLAIN ANALYZE really executes it, so it runs in a transaction that is rolled
		// back.
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()

		var fan, hold int64
		require.NoError(t, tx.QueryRow(ctx, `SELECT id FROM users WHERE role = 'FAN'`).Scan(&fan))
		require.NoError(t, tx.QueryRow(ctx,
			`INSERT INTO holds (event_id, user_id, expires_at) VALUES ($1, $2, now() + interval '10 minutes') RETURNING id`,
			onSale, fan).Scan(&hold))
		var want []int64
		require.NoError(t, tx.QueryRow(ctx, `
			SELECT array_agg(id) FROM (
				SELECT id FROM event_seats WHERE event_id = $1 AND block_id = $2 ORDER BY id LIMIT 6
			) s`, onSale, block).Scan(&want))
		require.Len(t, want, 6)

		plan := explain(t, tx, `
			WITH candidate AS (
				SELECT id
				FROM event_seats
				WHERE event_id = $1
				  AND id = ANY($2::bigint[])
				  AND (status = 'AVAILABLE' OR (status = 'HELD' AND hold_expires_at < now()))
				ORDER BY id
				FOR UPDATE
			)
			UPDATE event_seats es
			SET status = 'HELD', hold_id = $3, hold_expires_at = now() + interval '10 minutes'
			FROM candidate c
			WHERE es.id = c.id
			RETURNING es.id`, onSale, want, hold)
		requireNoSeqScan(t, "hold candidate query", plan)
		assert.EqualValues(t, 6, countTx(t, tx, `SELECT count(*) FROM event_seats WHERE hold_id = $1 AND status = 'HELD'`, hold),
			"the statement under EXPLAIN must really have held the seats")
		assert.EqualValues(t, 6, countTx(t, tx,
			`SELECT count(*) FROM event_seats WHERE hold_id = $1 AND updated_at > now()`, hold),
			"the trigger must stamp updated_at later than the transaction's start")
	})

	t.Run("migrations go down and up again with data in the tables", func(t *testing.T) {
		require.NoError(t, db.MigrateDown())
		require.NoError(t, db.MigrateUp())
		assert.Zero(t, count(t, pool, `SELECT count(*) FROM seats`))
	})
}

func countTx(t *testing.T, tx pgx.Tx, sql string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, tx.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func explain(t *testing.T, db querier, sql string, args ...any) string {
	t.Helper()
	rows, err := db.Query(context.Background(), "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
	require.NoError(t, err)
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		b.WriteString(line)
		b.WriteByte('\n')
	}
	require.NoError(t, rows.Err())
	return b.String()
}

func requireNoSeqScan(t *testing.T, name, plan string) {
	t.Helper()
	t.Logf("%s:\n%s", name, plan)
	require.NotEmpty(t, plan, "%s: no plan was captured", name)
	require.NotContains(t, plan, "Seq Scan on event_seats", "%s scans event_seats sequentially", name)
	require.Contains(t, plan, "event_seats", "%s: the plan does not mention event_seats at all", name)
}

// explainer is a store.DBTX that records the plan of each query it is asked to run and then
// runs it. Handing it to store.New means the plan checked is the plan of the SQL the store
// really ships, not of a copy pasted into a test.
type explainer struct {
	pool *pgxpool.Pool
	plan string
}

func (e *explainer) take(t *testing.T) string {
	t.Helper()
	plan := e.plan
	e.plan = ""
	return plan
}

func (e *explainer) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	rows, err := e.pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			rows.Close()
			return nil, err
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	e.plan = b.String()
	return e.pool.Query(ctx, sql, args...)
}

func (e *explainer) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return e.pool.Exec(ctx, sql, args...)
}

func (e *explainer) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return e.pool.QueryRow(ctx, sql, args...)
}

func (e *explainer) CopyFrom(ctx context.Context, table pgx.Identifier, cols []string, src pgx.CopyFromSource) (int64, error) {
	return e.pool.CopyFrom(ctx, table, cols, src)
}
