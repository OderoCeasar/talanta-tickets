package payments

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatKES(t *testing.T) {
	for cents, want := range map[int64]string{
		0: "0.00", 5: "0.05", 100: "1.00", 100_000: "1,000.00", 200_100: "2,001.00",
		123_456_789: "1,234,567.89", 99_999: "999.99", -150: "-1.50",
	} {
		assert.Equal(t, want, formatKES(cents))
	}
}

func TestBackground_BoundedAndWaits(t *testing.T) {
	bg := NewBackground(2)
	release := make(chan struct{})
	var ran atomic.Int64
	work := func() { <-release; ran.Add(1) }
	assert.True(t, bg.Go(work))
	assert.True(t, bg.Go(work))
	assert.False(t, bg.Go(work), "a third piece of work is refused, not queued")
	close(release)
	bg.Wait()
	assert.EqualValues(t, 2, ran.Load())
	assert.True(t, bg.Go(func() { ran.Add(1) }), "room again once the others have finished")
	bg.Wait()
	assert.EqualValues(t, 3, ran.Load())
}

func TestMockOutcomeContext(t *testing.T) {
	ctx := t.Context()
	assert.Equal(t, "", MockOutcomeFrom(ctx))
	assert.Equal(t, "wrong_pin", MockOutcomeFrom(WithMockOutcome(ctx, "wrong_pin")))
	assert.Equal(t, "", MockOutcomeFrom(WithMockOutcome(ctx, "")))
}
