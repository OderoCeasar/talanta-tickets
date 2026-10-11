package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

// Many more callers than slots: the number of argon2 computations running at one moment
// never passes the cap, and everyone is served in the end.
func TestPasswordHasher_NeverRunsMoreThanItsCap(t *testing.T) {
	const slots, callers = 3, 60
	h := newPasswordHasher(slots)

	var running, peak atomic.Int32
	work := func() {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(3 * time.Millisecond)
		running.Add(-1)
	}
	h.verify = func(string, string) (bool, error) { work(); return true, nil }
	h.hash = func(string) (string, error) { work(); return "hash", nil }

	var wg sync.WaitGroup
	var served atomic.Int32
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			// Hashing and verifying share the one cap.
			if i%2 == 0 {
				_, err = h.Verify(context.Background(), "pw", "hash")
			} else {
				_, err = h.Hash(context.Background(), "pw")
			}
			if err == nil {
				served.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, int32(slots), peak.Load(), "peak concurrent argon2 runs")
	assert.Equal(t, int32(callers), served.Load())
	assert.Equal(t, uint64(callers/2), h.verifies.Load())
	assert.Empty(t, h.slots, "every slot was given back")
}

// A caller whose request ends while it waits gets RATE_LIMITED with a retry time, and the
// password is not checked.
func TestPasswordHasher_GivesUpWhenTheContextEnds(t *testing.T) {
	h := newPasswordHasher(1)
	release := make(chan struct{})
	started := make(chan struct{})
	var calls atomic.Int32
	h.verify = func(string, string) (bool, error) {
		calls.Add(1)
		close(started)
		<-release
		return true, nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		ok, err := h.Verify(context.Background(), "pw", "hash")
		assert.NoError(t, err)
		assert.True(t, ok)
	}()
	<-started // the one slot is now taken

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ok, err := h.Verify(ctx, "pw", "hash")
	assert.False(t, ok)
	require.Error(t, err)
	assert.Equal(t, domain.CodeRateLimited, domain.CodeOf(err))
	assert.True(t, isHashBusy(err))
	var de *domain.Error
	require.ErrorAs(t, err, &de)
	assert.Equal(t, domain.RateLimitDetails{RetryAfterSeconds: hashBusyRetrySeconds}, de.Details)

	_, err = h.Hash(ctx, "pw")
	assert.True(t, isHashBusy(err))

	close(release)
	<-done
	assert.Equal(t, int32(1), calls.Load(), "the refused caller never reached argon2")
}

func TestPasswordHasher_Defaults(t *testing.T) {
	assert.Equal(t, 4, DefaultArgon2MaxConcurrent)
	assert.Equal(t, DefaultArgon2MaxConcurrent, cap(newPasswordHasher(0).slots))
	assert.Equal(t, 7, cap(newPasswordHasher(7).slots))

	// With nothing swapped out it is the real argon2.
	h := newPasswordHasher(1)
	hash, err := h.Hash(context.Background(), "correct-horse-battery")
	require.NoError(t, err)
	ok, err := h.Verify(context.Background(), "correct-horse-battery", hash)
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = h.Verify(context.Background(), "wrong", hash)
	require.NoError(t, err)
	assert.False(t, ok)
}
