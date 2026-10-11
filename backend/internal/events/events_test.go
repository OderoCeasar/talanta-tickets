package events

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

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
