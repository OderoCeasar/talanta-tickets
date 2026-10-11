package events

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// nopDB satisfies events.DB for tests that build a Service and never touch the database.
type nopDB struct {
	store.DBTX
	store.TxBeginner
}

func TestMarkerRoundTrip(t *testing.T) {
	at := time.Date(2026, 10, 11, 12, 30, 45, 123456000, time.UTC)
	m := encodeMarker(7, 112, at)
	assert.LessOrEqual(t, len(m), 128, "the contract's bound on ?since=")
	assert.Regexp(t, `^[A-Za-z0-9_-]+$`, m, "safe in a URL without escaping")

	got, ok := decodeMarker(m, 7, 112)
	require.True(t, ok)
	assert.True(t, got.Equal(at), "microseconds survive: %s", got)

	// The largest ids and a far-future time still fit.
	big := encodeMarker(1<<62, 1<<62, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))
	assert.LessOrEqual(t, len(big), 128)

	_, ok = decodeMarker(m, 8, 112)
	assert.False(t, ok, "another event")
	_, ok = decodeMarker(m, 7, 113)
	assert.False(t, ok, "another block")
	for _, bad := range []string{"", "!!!", "bTEuNy4xMTI", "MTc2MDE3NjgwMDEyMzQ1Ng", m + "x"} {
		_, ok := decodeMarker(bad, 7, 112)
		assert.False(t, ok, "%q", bad)
	}
	// A marker cut short in transit can still parse, but only ever as an earlier time,
	// which errs towards sending more (or the full block), never towards losing a change.
	if cut, ok := decodeMarker(m[:len(m)-3], 7, 112); ok {
		assert.True(t, cut.Before(at))
	}
}

func TestMarkerUsable(t *testing.T) {
	s := &Service{policy: DefaultPolicy()}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	assert.True(t, s.markerUsable(now.Add(-5*time.Second), now))
	assert.True(t, s.markerUsable(now.Add(-5*time.Minute), now), "exactly at the bound")
	assert.False(t, s.markerUsable(now.Add(-5*time.Minute-time.Second), now), "too old")
	assert.True(t, s.markerUsable(now.Add(500*time.Millisecond), now), "a small clock step is tolerated")
	assert.False(t, s.markerUsable(now.Add(2*time.Second), now), "from the future")
}

// The overlap must cover the longest time a transaction can sit between writing a seat and
// committing; the pool bounds that with 15-second statement and idle-in-transaction limits.
func TestDefaultPolicy_OverlapCoversTheTransactionLimits(t *testing.T) {
	p := DefaultPolicy()
	assert.Greater(t, p.MarkerOverlap, 15*time.Second)
	assert.Greater(t, p.MarkerMaxAge, p.MarkerOverlap)
	assert.Greater(t, p.MarkerMaxAge, 10*p.PollInterval, "a client that misses a few polls still gets a delta")
	assert.LessOrEqual(t, p.CountsTTL, 5*time.Second, "counts may lag by a few seconds, no more")
	assert.Less(t, p.PublishTimeout, 60*time.Second, "under the HTTP write timeout")
}

func TestCountsCache(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	c := newCountsCache(3*time.Second, func() time.Time { return now })
	loads := 0
	load := func() (*Inventory, error) {
		loads++
		return &Inventory{cells: map[cell]domain.SeatCounts{{block: 1, category: loadsKey(loads)}: {Available: loads}}}, nil
	}
	get := func(event int64) int {
		inv, err := c.get(event, load)
		require.NoError(t, err)
		return inv.Total().Available
	}

	assert.Equal(t, 1, get(7))
	assert.Equal(t, 1, get(7), "second read is served from the cache")
	assert.Equal(t, 1, loads)

	now = now.Add(2999 * time.Millisecond)
	assert.Equal(t, 1, get(7))
	now = now.Add(time.Millisecond)
	assert.Equal(t, 2, get(7), "reloaded once the entry is three seconds old")

	assert.Equal(t, 3, get(8), "each event has its own entry")
	assert.Equal(t, 2, get(7))

	c.invalidate(7)
	assert.Equal(t, 4, get(7), "invalidated on publish: the next read goes to the database")
	assert.Equal(t, 3, get(8), "other events are untouched")

	// A failed load is not cached.
	_, err := c.get(9, func() (*Inventory, error) { return nil, errors.New("down") })
	require.Error(t, err)
	assert.Equal(t, 5, get(9))
}

func loadsKey(n int) int64 { return int64(n) }

// Fix 9. The event list reads the counts of every event on its page. It accepts counts up
// to ListCountsTTL old, so a list polled every few seconds costs one aggregate per event per
// 20 seconds instead of one per 3; a single event's own page keeps the 3 seconds.
func TestCountsCache_TheListAcceptsOlderCounts(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	const single, listing = 3 * time.Second, 20 * time.Second
	c := newCountsCache(single, func() time.Time { return now })
	loads := 0
	load := func() (*Inventory, error) {
		loads++
		return &Inventory{cells: map[cell]domain.SeatCounts{{block: 1}: {Available: loads}}}, nil
	}
	list := func() int {
		inv, err := c.getWithin(7, listing, load)
		require.NoError(t, err)
		return inv.Total().Available
	}
	one := func() int {
		inv, err := c.get(7, load)
		require.NoError(t, err)
		return inv.Total().Available
	}

	// A list polled every 3 seconds for a minute (21 polls): 3 loads, at 0, 21 and 42
	// seconds. With the 3-second limit it was 21.
	for i := 0; i <= 20; i++ {
		list()
		if i < 20 {
			now = now.Add(3 * time.Second)
		}
	}
	assert.Equal(t, 3, loads, "one aggregate per 20 seconds for the list, not one per poll")

	// The entry is 18 seconds old now: good enough for the list, too old for a single
	// event's page or seat map, which keeps its 3 seconds.
	assert.Equal(t, 3, list())
	assert.Equal(t, 4, one(), "a seat map or event page reloads counts older than 3 seconds")
	assert.Equal(t, 4, list(), "and the list gets that fresher answer for free")
	assert.Equal(t, 4, loads)
	now = now.Add(single)
	assert.Equal(t, 4, list())
	assert.Equal(t, 5, one())

	// The list's own limit is exact.
	now = now.Add(listing - time.Millisecond)
	assert.Equal(t, 5, list())
	now = now.Add(time.Millisecond)
	assert.Equal(t, 6, list())

	// Publish and cancel still drop the entry for everyone.
	c.invalidate(7)
	assert.Equal(t, 7, list())
}

func TestPolicy_ListCountsAreNeverFresherThanSingleCounts(t *testing.T) {
	def := DefaultPolicy()
	assert.Equal(t, 3*time.Second, def.CountsTTL, "the per-event seat map keeps 3 seconds")
	assert.Equal(t, 20*time.Second, def.ListCountsTTL)

	svc, err := NewService(Config{DB: nopDB{}, Policy: Policy{CountsTTL: 30 * time.Second, ListCountsTTL: time.Second}})
	require.NoError(t, err)
	assert.Equal(t, 30*time.Second, svc.policy.ListCountsTTL)
}

// A burst of requests for one event runs one load; the rest wait for it.
func TestCountsCache_OneLoadForABurst(t *testing.T) {
	c := newCountsCache(3*time.Second, time.Now)
	var mu sync.Mutex
	loads := 0
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.get(1, func() (*Inventory, error) {
				mu.Lock()
				loads++
				mu.Unlock()
				time.Sleep(20 * time.Millisecond)
				return &Inventory{}, nil
			})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	assert.Equal(t, 1, loads)
}

func TestInventory_PriceFromAndTotals(t *testing.T) {
	inv := &Inventory{cells: map[cell]domain.SeatCounts{
		{block: 1, category: 10}: {Available: 0, Sold: 5},    // VIP, sold out
		{block: 1, category: 20}: {Available: 3, Held: 1},    // Regular
		{block: 2, category: 30}: {Available: 2, Blocked: 1}, // Terrace
		{block: 2}:               {Blocked: 4},               // unpainted
	}}
	prices := map[int64]int64{10: 500_000, 20: 100_000, 30: 50_000}

	assert.Equal(t, domain.SeatCounts{Sold: 5, Held: 1, Available: 5, Blocked: 5}, inv.Total())
	assert.Equal(t, 16, inv.Total().Total())
	assert.Equal(t, 11, inv.Total().Sellable())
	assert.EqualValues(t, 50_000, *inv.PriceFrom(prices, nil))
	assert.EqualValues(t, 100_000, *inv.PriceFrom(prices, map[int64]bool{1: true}), "the sold-out VIP does not count, nor does block 2")
	assert.Nil(t, inv.PriceFrom(prices, map[int64]bool{3: true}))
	assert.Equal(t, domain.SeatCounts{Sold: 5, Held: 1, Available: 3}, inv.ByBlock()[1])
	assert.Equal(t, domain.SeatCounts{Blocked: 4}, inv.ByCategory()[0])
	assert.Len(t, inv.BlockCategories(2), 2)
	// An empty inventory (a draft) answers zeros.
	assert.Zero(t, (&Inventory{}).Total().Total())
	assert.Nil(t, (&Inventory{}).PriceFrom(prices, nil))
}

func TestBucketStart_NairobiBoundaries(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		require.NoError(t, err)
		return v
	}
	// 20:59 UTC is 23:59 in Nairobi; 21:00 UTC is the next Nairobi day.
	assert.Equal(t, at("2026-10-09T21:00:00Z"), bucketStart(BucketDay, at("2026-10-10T20:59:59Z")))
	assert.Equal(t, at("2026-10-10T21:00:00Z"), bucketStart(BucketDay, at("2026-10-10T21:00:00Z")))
	assert.Equal(t, at("2026-10-10T20:00:00Z"), bucketStart(BucketHour, at("2026-10-10T20:59:59Z")))
	assert.Equal(t, at("2026-10-11T21:00:00Z"), nextBucket(BucketDay, at("2026-10-10T21:00:00Z")))
	assert.Equal(t, at("2026-10-10T22:00:00Z"), nextBucket(BucketHour, at("2026-10-10T21:00:00Z")))
}

func TestTimeProblems(t *testing.T) {
	kickoff := time.Date(2026, 11, 14, 13, 0, 0, 0, time.UTC)
	p := func(d time.Duration) *time.Time { v := kickoff.Add(d); return &v }
	names := func(gates, open, closeAt *time.Time) []string {
		var out []string
		for _, f := range timeProblems(kickoff, gates, open, closeAt) {
			out = append(out, f.Field)
		}
		return out
	}
	assert.Empty(t, names(nil, nil, nil))
	assert.Empty(t, names(p(-3*time.Hour), p(-240*time.Hour), p(0)), "closing exactly at kickoff is allowed")
	assert.Equal(t, []string{"gates_open_at"}, names(p(0), nil, nil))
	assert.Equal(t, []string{"sales_close_at"}, names(nil, p(-time.Hour), p(-time.Hour)))
	assert.Equal(t, []string{"sales_close_at"}, names(nil, nil, p(time.Second)))
}

func TestSeriesRange_DefaultsAlwaysFit(t *testing.T) {
	to := time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)
	const maxPoints = 2000
	ago := func(d time.Duration) time.Time { return to.Add(-d) }
	day := 24 * time.Hour
	count := func(bucket string, from time.Time) (n int) {
		for start := bucketStart(bucket, from); start.Before(to); start = nextBucket(bucket, start) {
			n++
		}
		return n
	}

	// Nothing sent: hours for a short history, days for a long one, never more than the cap.
	for _, c := range []struct {
		first  time.Duration
		bucket string
		points int
	}{
		{time.Hour, BucketHour, 2},
		{48 * time.Hour, BucketHour, 49},
		{48*time.Hour + time.Second, BucketDay, 3},
		{83 * day, BucketDay, 84},
		{200 * day, BucketDay, 201},
		{1999 * day, BucketDay, 2000},
		{2000 * day, BucketDay, 2000},
		{20000 * day, BucketDay, 2000},
	} {
		bucket, from, err := seriesRange("", nil, ago(c.first), to, maxPoints)
		require.NoError(t, err, "first sale %s ago", c.first)
		assert.Equal(t, c.bucket, bucket, "first sale %s ago", c.first)
		assert.Equal(t, c.points, count(bucket, from), "first sale %s ago", c.first)
	}

	// A bucket chosen by the caller is kept; a default `from` is moved forward to fit it.
	bucket, from, err := seriesRange(BucketHour, nil, ago(200*day), to, maxPoints)
	require.NoError(t, err)
	assert.Equal(t, BucketHour, bucket)
	assert.Equal(t, maxPoints, count(bucket, from))
	assert.Equal(t, bucketStart(BucketHour, to).Add(-1999*time.Hour), from, "the most recent buckets, ending with the current one")
	_, from, err = seriesRange(BucketHour, nil, ago(time.Hour), to, maxPoints)
	require.NoError(t, err)
	assert.Equal(t, ago(time.Hour), from, "a range that fits is left alone")

	// `to` is exclusive: ending exactly on a boundary does not add an empty bucket.
	onTheHour := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	_, from, err = seriesRange(BucketHour, nil, onTheHour.Add(-5000*time.Hour), onTheHour, maxPoints)
	require.NoError(t, err)
	assert.Equal(t, onTheHour.Add(-2000*time.Hour), from)

	// A `from` the caller sent is never moved: too far back is their mistake to fix.
	given := ago(200 * day)
	_, _, err = seriesRange(BucketHour, &given, time.Time{}, to, maxPoints)
	require.ErrorIs(t, err, domain.Invalid())
	bucket, from, err = seriesRange("", &given, time.Time{}, to, maxPoints)
	require.NoError(t, err)
	assert.Equal(t, BucketDay, bucket)
	assert.Equal(t, given, from)
	tooFar := ago(2001 * day)
	_, _, err = seriesRange("", &tooFar, time.Time{}, to, maxPoints)
	require.ErrorIs(t, err, domain.Invalid())
}
