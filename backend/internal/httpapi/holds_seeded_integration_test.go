//go:build integration

package httpapi_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// On the full venue (59,568 seats on sale) none of the hold flow's statements may scan the
// inventory table: every one of them runs for every tap of "hold" during a rush.
func TestSeededVenue_HoldQueriesDoNotScanTheInventoryTable(t *testing.T) {
	sv := seededTalanta(t)
	ctx := context.Background()
	var categoryID, blockID int64
	require.NoError(t, db.Pool.QueryRow(ctx, `
		SELECT category_id, block_id FROM event_seats WHERE event_id = $1 AND status = 'AVAILABLE'
		GROUP BY category_id, block_id ORDER BY count(*) DESC, block_id LIMIT 1`, sv.OnSale).Scan(&categoryID, &blockID))
	seatIDs := collect[int64](t, `
		SELECT id FROM event_seats WHERE event_id = $1 AND block_id = $2 AND category_id = $3 AND status = 'AVAILABLE' ORDER BY id LIMIT 4`,
		sv.OnSale, blockID, categoryID)
	require.Len(t, seatIDs, 4)
	blocks := collect[int64](t, `SELECT DISTINCT block_id FROM event_seats WHERE event_id = $1 AND category_id = $2 ORDER BY 1 LIMIT 11`, sv.OnSale, categoryID)
	exec(t, `ANALYZE event_seats`)

	// EXPLAIN ANALYZE really runs the statement, so the writes are inside a transaction
	// that is rolled back.
	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	explainTx := func(sql string, args ...any) string {
		rows, err := tx.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
		require.NoError(t, err)
		var out string
		for rows.Next() {
			var line string
			require.NoError(t, rows.Scan(&line))
			out += line + "\n"
		}
		require.NoError(t, rows.Err())
		return out
	}
	userID, _ := newEnv(t).newFan()
	var holdID int64
	var expires time.Time
	require.NoError(t, tx.QueryRow(ctx, `INSERT INTO holds (event_id, user_id, expires_at) VALUES ($1, $2, now() + interval '10 minutes') RETURNING id, expires_at`,
		sv.OnSale, userID).Scan(&holdID, &expires))

	plans := []struct{ name, plan string }{
		{"lock lapsed holds of the seats", explainTx(store.SQLLockLapsedHoldsOfSeats, sv.OnSale, seatIDs)},
		{"hold: lock the free seats", explainTx(store.SQLLockFreeSeats, sv.OnSale, seatIDs)},
		{"hold: write the locked seats", explainTx(store.SQLMarkSeatsHeld, seatIDs, holdID, expires)},
		{"best available: candidates, 1 block", explainTx(store.SQLListBestAvailableCandidates, sv.OnSale, []int64{blockID}, categoryID)},
		{"best available: candidates, 11 blocks", explainTx(store.SQLListBestAvailableCandidates, sv.OnSale, blocks, categoryID)},
		{"best available: free seats of the category", explainTx(store.SQLCountFreeSeatsOfCategory, sv.OnSale, blocks, categoryID)},
		{"release / sweep: free the seats of a hold", explainTx(store.SQLFreeSeatsOfHolds, []int64{holdID})},
	}
	for _, p := range plans {
		assert.NotContains(t, p.plan, "Seq Scan on event_seats", "%s:\n%s", p.name, p.plan)
		assert.Contains(t, p.plan, "event_seats", p.name)
		t.Logf("%s:\n%s", p.name, p.plan)
	}
	t.Logf("best available: blocks of the category:\n%s", explainTx(store.SQLListCategoryBlocks, sv.OnSale, categoryID))
}

// Best available on the real venue: the answer is four neighbouring seats in one row, and
// it comes back quickly.
func TestSeededVenue_BestAvailable(t *testing.T) {
	sv := seededTalanta(t)
	e := newEnv(t)
	var categoryID int64
	require.NoError(t, db.Pool.QueryRow(context.Background(), `
		SELECT category_id FROM event_seats WHERE event_id = $1 AND status = 'AVAILABLE' GROUP BY 1 ORDER BY count(*) DESC LIMIT 1`, sv.OnSale).Scan(&categoryID))

	var took []time.Duration
	seen := map[int64]bool{}
	for i := 0; i < 5; i++ {
		_, fan := e.newFan()
		started := time.Now()
		h := want(t, e.best(fan, sv.OnSale, categoryID, 4), http.StatusCreated)
		took = append(took, time.Since(started).Round(time.Millisecond))
		hs := list(h, "seats")
		require.Len(t, hs, 4)
		first := hs[0].(obj)["location"].(obj)
		for k, s := range hs {
			loc := s.(obj)["location"].(obj)
			assert.Equal(t, first["block_id"], loc["block_id"])
			assert.Equal(t, first["row_label"], loc["row_label"])
			assert.EqualValues(t, id64(first["seat_number"])+int64(k), loc["seat_number"])
			assert.Equal(t, "STANDARD", loc["seat_type"])
			id := id64(s.(obj)["event_seat_id"])
			assert.False(t, seen[id], "seat %d given twice", id)
			seen[id] = true
		}
	}
	t.Logf("5 best-available holds of 4 on the full venue took %v (through the router, under -race)", took)
	// Leave the seeded event as it was found for the tests that measure it.
	exec(t, `UPDATE event_seats SET status = 'AVAILABLE', hold_id = NULL, hold_expires_at = NULL WHERE event_id = $1 AND status = 'HELD'`, sv.OnSale)
	exec(t, `UPDATE holds SET status = 'RELEASED' WHERE event_id = $1 AND status = 'ACTIVE'`, sv.OnSale)
	checkInventory(t, sv.OnSale)
}
