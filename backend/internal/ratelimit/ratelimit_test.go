package ratelimit

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestMemoryAllowsUpToLimitThenRefusesUntilWindowEnds(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)}
	l := NewMemory(clock.Now)
	rule := Rule{Limit: 3, Window: time.Minute}

	for i := 0; i < 3; i++ {
		require.True(t, l.Take("k", rule).Allowed, "event %d", i+1)
	}
	clock.Advance(20 * time.Second)
	d := l.Take("k", rule)
	assert.False(t, d.Allowed)
	assert.Equal(t, 40*time.Second, d.RetryAfter)

	// Refused events are not counted: the window still ends when it was going to.
	clock.Advance(39 * time.Second)
	assert.False(t, l.Take("k", rule).Allowed)
	clock.Advance(time.Second)
	assert.True(t, l.Take("k", rule).Allowed, "a new window starts when the old one ends")
}

func TestMemoryKeysAreIndependent(t *testing.T) {
	l := NewMemory(nil)
	rule := Rule{Limit: 1, Window: time.Hour}
	require.True(t, l.Take("a", rule).Allowed)
	assert.False(t, l.Take("a", rule).Allowed)
	assert.True(t, l.Take("b", rule).Allowed)
}

func TestMemoryReset(t *testing.T) {
	l := NewMemory(nil)
	rule := Rule{Limit: 1, Window: time.Hour}
	require.True(t, l.Take("a", rule).Allowed)
	l.Reset("a")
	assert.True(t, l.Take("a", rule).Allowed)
}

func TestMemoryZeroRuleAllowsEverything(t *testing.T) {
	l := NewMemory(nil)
	for i := 0; i < 100; i++ {
		require.True(t, l.Take("a", Rule{}).Allowed)
	}
}

func TestMemorySweepsFinishedWindows(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)}
	l := NewMemory(clock.Now)
	for _, k := range []string{"a", "b", "c"} {
		l.Take(k, Rule{Limit: 1, Window: time.Second})
	}
	require.Equal(t, 3, l.Len())
	clock.Advance(2 * sweepEvery)
	l.Take("d", Rule{Limit: 1, Window: time.Second})
	assert.Equal(t, 1, l.Len())
}

func TestMemoryConcurrentTakesNeverExceedLimit(t *testing.T) {
	l := NewMemory(nil)
	rule := Rule{Limit: 50, Window: time.Hour}
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for i := 0; i < 400; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Take("k", rule).Allowed {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 50, allowed)
}

func TestRetryAfterSeconds(t *testing.T) {
	assert.Equal(t, 1, RetryAfterSeconds(0))
	assert.Equal(t, 1, RetryAfterSeconds(10*time.Millisecond))
	assert.Equal(t, 1, RetryAfterSeconds(time.Second))
	assert.Equal(t, 2, RetryAfterSeconds(time.Second+time.Millisecond))
	assert.Equal(t, 60, RetryAfterSeconds(time.Minute))
}
