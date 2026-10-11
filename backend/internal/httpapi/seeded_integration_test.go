//go:build integration

package httpapi_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/events"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi/contracttest"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/venue"
)

// seeded is the real Talanta layout (59,580 seats) with its on-sale and draft demo events,
// put into the shared test database by running the real seeder once. Publishing 60,000
// seats takes about half a minute here, so only the tests that need the real size use it.
type seededVenue struct {
	VenueID int64
	OnSale  int64 // "Harambee Stars vs Uganda Cranes", published by the seeder
	Draft   int64 // "Nairobi Derby", painted but not published
	Seats   int   // active seats
}

var (
	seedOnce sync.Once
	seedData seededVenue
	seedErr  error
)

func seededTalanta(t *testing.T) seededVenue {
	t.Helper()
	seedOnce.Do(func() {
		backendDir := filepath.Join(filepath.Dir(contracttest.SpecPath()), "..")
		cmd := osexec.Command("go", "run", "./cmd/seed")
		cmd.Dir = backendDir
		cmd.Env = append(os.Environ(), "DATABASE_URL="+db.URL, "APP_ENV=development")
		out, err := cmd.CombinedOutput()
		if err != nil {
			seedErr = fmt.Errorf("seeder: %w\n%s", err, out)
			return
		}
		ctx := context.Background()
		seedErr = db.Pool.QueryRow(ctx, `
			SELECT v.id,
			       (SELECT id FROM events WHERE venue_id = v.id AND title = 'Harambee Stars vs Uganda Cranes'),
			       (SELECT id FROM events WHERE venue_id = v.id AND title LIKE 'Nairobi Derby%'),
			       (SELECT count(*) FROM seats s JOIN blocks b ON b.id = s.block_id JOIN stands st ON st.id = b.stand_id
			         WHERE st.venue_id = v.id AND s.is_active)
			FROM venues v WHERE v.name = 'Talanta Stadium'`).Scan(&seedData.VenueID, &seedData.OnSale, &seedData.Draft, &seedData.Seats)
	})
	require.NoError(t, seedErr)
	return seedData
}

// gzipped sends the request the way a phone does, with Accept-Encoding: gzip, through the
// whole router, and returns the compressed and the decompressed size.
func gzipped(t *testing.T, e *env, path, token string) (compressed, raw int) {
	t.Helper()
	req := e.request("GET", path, nil, token)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"), path)
	compressed = rec.Body.Len()
	zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	require.NoError(t, err)
	plain, err := io.ReadAll(zr)
	require.NoError(t, err)
	return compressed, len(plain)
}

// M5 "done when": level 1 and level 2 stay within the §5.1 sizes on the seeded venue, and
// level 3 returns one block only.
func TestSeededVenue_SeatMapPayloadSizes(t *testing.T) {
	sv := seededTalanta(t)
	e := newEnv(t)
	base := fmt.Sprintf("/events/%d", sv.OnSale)
	const kb = 1024.0

	// Level 1.
	m := want(t, e.do("GET", base+"/map", nil, ""), http.StatusOK)
	stands := list(m, "stands")
	require.Len(t, stands, 8)
	total := 0.0
	for _, st := range stands {
		total += st.(obj)["seats_total"].(float64)
		assert.NotNil(t, st.(obj)["price_from_cents"])
	}
	assert.EqualValues(t, sv.Seats, total, "every active seat is in exactly one stand")
	gz1, raw1 := gzipped(t, e, base+"/map", "")
	t.Logf("level 1 (8 stands):            %6d bytes raw (%.1f KB), %5d gzipped", raw1, float64(raw1)/kb, gz1)
	assert.LessOrEqual(t, raw1, 6*1024, "§5.1: about 4 KB")

	// Level 2: every stand, reporting the largest.
	maxRaw, maxGz, maxBlocks := 0, 0, 0
	for _, st := range stands {
		path := fmt.Sprintf("%s/stands/%d/blocks", base, id64(st.(obj)["id"]))
		sb := want(t, e.do("GET", path, nil, ""), http.StatusOK)
		gz, raw := gzipped(t, e, path, "")
		if raw > maxRaw {
			maxRaw, maxGz, maxBlocks = raw, gz, len(list(sb, "blocks"))
		}
	}
	t.Logf("level 2 (largest stand, %2d blocks): %5d bytes raw (%.1f KB), %5d gzipped", maxBlocks, maxRaw, float64(maxRaw)/kb, maxGz)
	assert.LessOrEqual(t, maxRaw, 12*1024, "§5.1: about 10 KB")

	// Level 3: EVERY block of the venue is measured, and the largest answer is the one
	// reported and asserted. Size does not follow the seat count alone: blocks with the
	// same number of seats differ by thousands of bytes (a curved block's coordinates have
	// more digits, and ids, labels and numbers of a later block are longer), so "the block
	// with the most seats, lowest id first" measured a block that was not the largest.
	type blockSize struct {
		id             int64
		seats, raw, gz int
	}
	var sizes []blockSize
	rows, err := db.Pool.Query(context.Background(),
		`SELECT block_id, count(*) FROM event_seats WHERE event_id = $1 GROUP BY block_id ORDER BY block_id`, sv.OnSale)
	require.NoError(t, err)
	for rows.Next() {
		var b blockSize
		require.NoError(t, rows.Scan(&b.id, &b.seats))
		sizes = append(sizes, b)
	}
	require.NoError(t, rows.Err())
	require.Len(t, sizes, queryInt(t, `SELECT count(*) FROM blocks b JOIN stands st ON st.id = b.stand_id WHERE st.venue_id = $1`, sv.VenueID), "every block of the venue")
	var largest, largestGz, mostSeats blockSize
	over40 := 0
	for i := range sizes {
		b := &sizes[i]
		b.gz, b.raw = gzipped(t, e, fmt.Sprintf("%s/blocks/%d/seats", base, b.id), "")
		if b.raw > largest.raw {
			largest = *b
		}
		if b.gz > largestGz.gz {
			largestGz = *b
		}
		if b.seats > mostSeats.seats {
			mostSeats = *b
		}
		if b.raw > 40*1024 {
			over40++
		}
	}
	blockID, blockSeats, raw3, gz3 := largest.id, largest.seats, largest.raw, largest.gz
	t.Logf("level 3, all %d blocks measured: largest answer is block %d (%d seats): %5d bytes raw (%.1f KB), %5d gzipped (%.1f bytes a seat raw)",
		len(sizes), blockID, blockSeats, raw3, float64(raw3)/kb, gz3, float64(raw3)/float64(blockSeats))
	t.Logf("level 3: largest gzipped answer is block %d (%d seats): %d bytes gzipped, %d raw; %d of %d blocks are over 40 KB raw",
		largestGz.id, largestGz.seats, largestGz.gz, largestGz.raw, over40, len(sizes))
	t.Logf("level 3: the first block with the most seats (%d, block %d), which this test used to report: %d bytes raw, %d gzipped",
		mostSeats.seats, mostSeats.id, mostSeats.raw, mostSeats.gz)
	// Reported, not hidden: the tuple layout costs 33 to 35 bytes a seat raw, so the largest
	// blocks are over the 40 KB of §5.1 before compression, and a curved block's coordinates
	// compress to about a third rather than the eighth first estimated. The bounds here are
	// guard rails against a regression, on the true maximum, not the targets.
	assert.LessOrEqual(t, raw3, 48*1024)
	assert.LessOrEqual(t, largestGz.gz, 16*1024, "what travels over the air")

	// The largest block again, contract-checked: one block only.
	seatsPath := fmt.Sprintf("%s/blocks/%d/seats", base, blockID)
	full := want(t, e.do("GET", seatsPath, nil, ""), http.StatusOK)
	codes := seatCodes(t, full)
	assert.Len(t, codes, blockSeats, "one block only")
	inBlock := map[int64]bool{}
	for _, id := range collect[int64](t, `SELECT id FROM event_seats WHERE event_id = $1 AND block_id = $2`, sv.OnSale, blockID) {
		inBlock[id] = true
	}
	for id := range codes {
		require.True(t, inBlock[id], "seat %d is not in block %d", id, blockID)
	}

	// The poll: an empty delta, and one with a handful of changes.
	marker := full["marker"].(string)
	untriggered(t, func(tx pgx.Tx) {
		_, err := tx.Exec(context.Background(), `UPDATE event_seats SET updated_at = now() - interval '1 hour' WHERE event_id = $1 AND block_id = $2`, sv.OnSale, blockID)
		require.NoError(t, err)
		_, err = tx.Exec(context.Background(), `UPDATE events SET updated_at = now() - interval '1 hour' WHERE id = $1`, sv.OnSale)
		require.NoError(t, err)
	})
	empty := e.seats(sv.OnSale, blockID, marker, "")
	assert.Empty(t, changeCodes(t, want(t, empty, http.StatusOK)))
	t.Logf("delta, nothing changed:         %5d bytes raw", empty.Body.Len())
	var some []int64
	for id := range codes {
		if some = append(some, id); len(some) == 6 {
			break
		}
	}
	sellSeats(t, some...)
	delta := e.seats(sv.OnSale, blockID, marker, "")
	assert.Len(t, changeCodes(t, want(t, delta, http.StatusOK)), 6)
	t.Logf("delta, 6 seats changed:         %5d bytes raw", delta.Body.Len())
	assert.Less(t, delta.Body.Len(), 300)
	exec(t, `UPDATE event_seats SET status = 'AVAILABLE' WHERE id = ANY($1)`, some)
}

// explain returns the plan of a store query with its arguments filled in.
func explain(t *testing.T, sql string, args ...any) string {
	t.Helper()
	all := append([]any{pgx.QueryExecModeSimpleProtocol}, args...)
	rows, err := db.Pool.Query(context.Background(), "EXPLAIN (ANALYZE, BUFFERS) "+sql, all...)
	require.NoError(t, err)
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return strings.Join(lines, "\n")
}

// The level 3 and "changed since" queries run every five seconds for every open block. On
// the full venue (two events, about 120,000 inventory rows) neither may scan event_seats.
func TestSeededVenue_SeatQueriesDoNotScanTheInventoryTable(t *testing.T) {
	sv := seededTalanta(t)
	var blockID int64
	require.NoError(t, db.Pool.QueryRow(context.Background(), `
		SELECT block_id FROM event_seats WHERE event_id = $1 GROUP BY block_id ORDER BY count(*) DESC, block_id LIMIT 1`, sv.OnSale).Scan(&blockID))
	since := time.Now().Add(-30 * time.Second)

	// Named arguments in the order sqlc numbered them for each query.
	plans := map[string]string{
		"level 3 full":  explain(t, store.SQLListBlockSeatsForViewer, int64(42), sv.OnSale, blockID),
		"changed since": explain(t, store.SQLListBlockSeatChangesForViewer, int64(42), sv.OnSale, blockID, since),
		"lapsed holds":  explain(t, store.SQLCountLapsedHoldsByBlock, sv.OnSale),
	}
	for name, plan := range plans {
		assert.NotContains(t, plan, "Seq Scan on event_seats", "%s:\n%s", name, plan)
		assert.Contains(t, plan, "event_seats", name)
		t.Logf("%s:\n%s", name, plan)
	}
	// The availability counts read the whole event by design (which is why they are cached
	// for a few seconds). When one event is most of the table the planner rightly prefers
	// a sequential scan to 60,000 index entries, so the plan is recorded, not asserted.
	t.Logf("counts:\n%s", explain(t, store.SQLCountEventSeatsByBlock, sv.OnSale))
}

// Rule 2: publishing the full venue takes longer than the pool's statement timeout and the
// router's request timeout. It must succeed anyway, through the real middleware, because
// the publish transaction raises its own statement timeout and the route has its own
// request timeout.
func TestSeededVenue_PublishOutlastsTheOrdinaryTimeouts(t *testing.T) {
	sv := seededTalanta(t)
	ctx := context.Background()
	const ordinary = 2 * time.Second

	pool, err := store.OpenWithOptions(ctx, db.URL, store.Options{StatementTimeout: ordinary, IdleInTxTimeout: ordinary})
	require.NoError(t, err)
	defer pool.Close()

	tokens := auth.NewTokens([]byte(testSecret), nil)
	policy := events.DefaultPolicy()
	policy.PublishTimeout = 3 * time.Minute
	svc, err := events.NewService(events.Config{DB: pool, Policy: policy})
	require.NoError(t, err)
	router := httpapi.NewRouter(httpapi.Deps{
		DB: pool, Tokens: tokens, Events: svc, Venues: venue.NewService(pool),
		RequestTimeout: ordinary, PublishTimeout: policy.PublishTimeout,
	})
	token, err := tokens.IssueAccess(1, domain.RoleOrganiser, "test-session")
	require.NoError(t, err)
	contract := contracttest.Load(t)
	call := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/v1"+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		return contract.Do(t, router, req)
	}

	// The ordinary limits are real: a slow statement on this pool is cut off.
	_, err = pool.Exec(ctx, `SELECT pg_sleep(4)`)
	require.Error(t, err, "the pool's statement timeout must be in force for this test to mean anything")

	rd := want(t, call("GET", fmt.Sprintf("/admin/events/%d/readiness", sv.Draft)), http.StatusOK)
	require.Equal(t, true, rd["ready"], "%v", rd["problems"])
	require.EqualValues(t, sv.Seats, rd["seat_total"])

	start := time.Now()
	res := want(t, call("POST", fmt.Sprintf("/admin/events/%d/publish", sv.Draft)), http.StatusOK)
	took := time.Since(start)
	t.Logf("publish of %d seats through the API: %s (statement and request timeouts elsewhere: %s)", sv.Seats, took.Round(100*time.Millisecond), ordinary)
	assert.Equal(t, false, res["already_published"])
	assert.EqualValues(t, sv.Seats, res["seats_on_sale"])
	assert.Greater(t, took, ordinary, "if publish ever gets this fast the test no longer proves anything; lower `ordinary`")
	assert.Equal(t, sv.Seats, queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1`, sv.Draft))
	assert.Equal(t, sv.Seats, queryInt(t, `SELECT count(DISTINCT seat_id) FROM event_seats WHERE event_id = $1`, sv.Draft))

	start = time.Now()
	again := want(t, call("POST", fmt.Sprintf("/admin/events/%d/publish", sv.Draft)), http.StatusOK)
	t.Logf("second publish (no-op): %s", time.Since(start).Round(time.Millisecond))
	assert.Equal(t, true, again["already_published"])
	assert.EqualValues(t, sv.Seats, again["seats_on_sale"])
	assert.Equal(t, sv.Seats, queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1`, sv.Draft))
}
