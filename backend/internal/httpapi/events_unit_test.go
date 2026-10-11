package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/events"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/venue"
)

// One route can run longer than the router-wide timeout, and still ends when the client
// goes away.
func TestRouteTimeoutReplacesTheRouterWideOne(t *testing.T) {
	type key struct{}
	var seen time.Duration
	var value any
	var hasDeadline bool
	record := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		deadline, ok := r.Context().Deadline()
		hasDeadline = ok
		seen = time.Until(deadline)
		value = r.Context().Value(key{})
	})
	withValue := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), key{}, "kept")))
		})
	}
	req := httptest.NewRequest("POST", "/", nil)

	timeout(50*time.Millisecond)(record).ServeHTTP(httptest.NewRecorder(), req)
	require.True(t, hasDeadline)
	assert.LessOrEqual(t, seen, 50*time.Millisecond)

	// Values set after the router-wide timeout (logger, principal) survive the swap.
	timeout(50*time.Millisecond)(withValue(routeTimeout(time.Minute)(record))).ServeHTTP(httptest.NewRecorder(), req)
	require.True(t, hasDeadline)
	assert.Greater(t, seen, 50*time.Second, "the route's own limit, not the router's")
	assert.Equal(t, "kept", value)

	// The long handler outlives the short limit…
	outlived := false
	slow := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(150 * time.Millisecond):
			outlived = true
		}
	})
	timeout(30*time.Millisecond)(routeTimeout(time.Minute)(slow)).ServeHTTP(httptest.NewRecorder(), req)
	assert.True(t, outlived)

	// …but not its client.
	ctx, cancel := context.WithCancel(context.Background())
	var err error
	gone := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		cancel()
		<-r.Context().Done()
		err = r.Context().Err()
	})
	timeout(time.Minute)(routeTimeout(time.Minute)(gone)).ServeHTTP(httptest.NewRecorder(), req.WithContext(ctx))
	assert.ErrorIs(t, err, context.Canceled)

	// And it still times out by its own clock.
	routeTimeout(20*time.Millisecond)(gone).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil))
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestSeatTuplesAreCompactArrays(t *testing.T) {
	b, err := json.Marshal(seatTuple(events.Seat{EventSeatID: 910002, Number: 2, X: 4017.5, Y: 594, TypeCode: 1, CategoryIndex: 1, Code: domain.SeatCodeMine}))
	require.NoError(t, err)
	assert.Equal(t, `[910002,2,4017.5,594,1,1,"M"]`, string(b), "the contract's own example")

	b, err = json.Marshal(seatTuple(events.Seat{EventSeatID: 1, Number: 10, X: 0.25, Y: 1234.56, CategoryIndex: -1, Code: domain.SeatCodeBlocked}))
	require.NoError(t, err)
	assert.Equal(t, `[1,10,0.25,1234.56,0,-1,"B"]`, string(b))

	b, err = json.Marshal(seatChangeTuple(events.SeatChange{EventSeatID: 910001, Code: domain.SeatCodeHeld}))
	require.NoError(t, err)
	assert.Equal(t, `[910001,"H"]`, string(b))

	id := int64(7)
	b, err = json.Marshal([]physicalSeatTuple{
		physicalSeatTuple(venue.LayoutSeat{Number: 1, X: 4012.5, Y: 594, Type: "STANDARD", Active: true}),
		physicalSeatTuple(venue.LayoutSeat{ID: &id, Number: 2, X: 1, Y: 2, Type: "COMPANION"}),
	})
	require.NoError(t, err)
	assert.Equal(t, `[[null,1,4012.5,594,0,1],[7,2,1,2,2,0]]`, string(b))
}

func TestOptionalTellsAbsentFromNull(t *testing.T) {
	var body struct {
		A optional[string] `json:"a"`
		B optional[string] `json:"b"`
		C optional[string] `json:"c"`
	}
	require.NoError(t, json.Unmarshal([]byte(`{"a":null,"b":"x"}`), &body))
	assert.True(t, body.A.Set)
	assert.Nil(t, body.A.Value, "null clears")
	assert.True(t, body.B.Set)
	assert.Equal(t, "x", *body.B.Value)
	assert.False(t, body.C.Set, "absent leaves alone")
	assert.Error(t, json.Unmarshal([]byte(`{"a":5}`), &body))
}

func TestCompactJSON(t *testing.T) {
	assert.Equal(t, `[[1,2],[3.5,4]]`, string(compactJSON(json.RawMessage(`[[1, 2], [3.5, 4]]`))))
	assert.Equal(t, `not json`, string(compactJSON(json.RawMessage(`not json`))), "left as it is rather than lost")
}
