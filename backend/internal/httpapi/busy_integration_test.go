//go:build integration

package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/booking"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/events"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// busyWait is the lock wait of the API under test here. Production waits 3 to 5 seconds; the
// rule being tested is "give up after the wait and say so", whatever the wait is.
const busyWait = 400 * time.Millisecond

// busyEnv is an API whose every lock wait is busyWait.
func busyEnv(t *testing.T) *env {
	return newEnv(t,
		withEventPolicy(func(p *events.Policy) { p.LockWait = busyWait }),
		withHoldPolicy(func(p *booking.Policy) { p.EventLockWait = busyWait }),
		func(c *envConfig) { c.venueLockWait = busyWait },
	)
}

// slowPublish stands in for a publish of the full venue, which keeps the event locked for
// 30 to 44 seconds: it takes the very lock publish takes (the LockEvent query: the event's
// gate, then its row) in a transaction of its own and keeps it until the returned function
// is called. Nothing is written.
func slowPublish(t *testing.T, eventID int64) (finish func()) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	_, err = store.New(tx).LockEvent(ctx, eventID)
	require.NoError(t, err)
	done := false
	finish = func() {
		if !done {
			done = true
			require.NoError(t, tx.Rollback(ctx))
		}
	}
	t.Cleanup(finish)
	return finish
}

// wantBusy asserts the answer every request queued behind a publish must get: a prompt 409
// EVENT_BUSY, never a 500 after the statement timeout.
func wantBusy(t *testing.T, name string, call func() *httptest.ResponseRecorder) {
	t.Helper()
	start := time.Now()
	rec := call()
	took := time.Since(start)
	ee := wantError(t, rec, http.StatusConflict, domain.CodeEventBusy)
	assert.Equal(t, "null", string(ee.Error.Details), name)
	assert.GreaterOrEqual(t, took, busyWait, "%s: it must have waited for the lock first", name)
	assert.Less(t, took, 5*time.Second, "%s: the wait is bounded by the lock timeout, not by the statement or request timeout", name)
	t.Logf("%-22s 409 EVENT_BUSY after %s", name, took.Round(10*time.Millisecond))
}

// Fix 1. Everything that needs the event's lock while a publish holds it answers 409
// EVENT_BUSY after a short wait, changes nothing, and works once the publish is over.
func TestEventBusy_StaffRequestsBehindAPublish(t *testing.T) {
	e := busyEnv(t)
	v := e.newVenue(2, 1, 3)
	ev := e.readyEvent(v)
	token := ev.Token
	evPath := fmt.Sprintf("/admin/events/%d", ev.ID)
	regular, vip := ev.Cats["Regular"], ev.Cats["VIP"]
	catPath := func(id int64) string { return fmt.Sprintf("/admin/categories/%d", id) }
	spare := e.addCategory(&ev, "Spare", 70_000, "#111111")

	calls := []struct {
		name string
		do   func() *httptest.ResponseRecorder
		ok   int
	}{
		{"paint", func() *httptest.ResponseRecorder { return e.paint(ev, vip, v.BlockIDs[1:], nil) }, http.StatusOK},
		{"category create", func() *httptest.ResponseRecorder {
			return e.do("POST", evPath+"/categories", obj{"name": "Terrace", "price_cents": 50_000, "color_hex": "#333333"}, token)
		}, http.StatusCreated},
		{"category edit", func() *httptest.ResponseRecorder {
			return e.do("PATCH", catPath(regular), obj{"price_cents": 120_000}, token)
		}, http.StatusOK},
		{"category delete", func() *httptest.ResponseRecorder { return e.do("DELETE", catPath(spare), nil, token) }, http.StatusNoContent},
		{"event PATCH", func() *httptest.ResponseRecorder { return e.do("PATCH", evPath, obj{"title": "Renamed"}, token) }, http.StatusOK},
		{"event DELETE", func() *httptest.ResponseRecorder { return e.do("DELETE", evPath, nil, token) }, 0},
		{"second publish", func() *httptest.ResponseRecorder { return e.publish(ev) }, 0},
		{"cancel", func() *httptest.ResponseRecorder { return e.do("POST", evPath+"/cancel", nil, token) }, http.StatusOK},
	}

	finish := slowPublish(t, ev.ID)
	for _, c := range calls {
		wantBusy(t, c.name, c.do)
	}
	// Nothing got through.
	assert.Equal(t, 100_000, queryInt(t, `SELECT price_cents FROM event_categories WHERE id = $1`, regular))
	assert.Equal(t, 3, queryInt(t, `SELECT count(*) FROM event_categories WHERE event_id = $1`, ev.ID))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM events WHERE id = $1 AND status = 'DRAFT' AND title <> 'Renamed'`, ev.ID))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM event_block_categories WHERE event_id = $1 AND block_id = $2 AND category_id = $3`, ev.ID, v.BlockIDs[1], regular))

	// The publish is over: the same requests go through.
	finish()
	for _, c := range calls {
		if c.ok != 0 {
			want(t, c.do(), c.ok)
		}
	}
}

// Fix 1, the layout editor. A publish holds FOR KEY SHARE on every block whose seats it
// copies (the event_seats.block_id foreign key) until it commits. A move or a new label must
// not queue behind that; a new code has to, and gives up with EVENT_BUSY.
func TestEventBusy_BlockEditsBehindAPublish(t *testing.T) {
	e := busyEnv(t)
	v := e.newVenue(1, 2, 3)
	token := e.staffToken(domain.RoleOrganiser)
	path := fmt.Sprintf("/admin/blocks/%d", v.BlockIDs[0])
	ctx := context.Background()

	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	// What the foreign-key check of publish's INSERT INTO event_seats does to the block.
	_, err = tx.Exec(ctx, `SELECT 1 FROM blocks WHERE id = $1 FOR KEY SHARE`, v.BlockIDs[0])
	require.NoError(t, err)

	start := time.Now()
	o := want(t, e.do("PATCH", path, obj{"label": "Moved mid-publish", "origin_x": 60}, token), http.StatusOK)
	assert.Equal(t, "Moved mid-publish", o["label"])
	assert.Less(t, time.Since(start), busyWait, "a move does not wait for the publish at all")

	wantBusy(t, "block PATCH (new code)", func() *httptest.ResponseRecorder { return e.do("PATCH", path, obj{"code": "Z9"}, token) })
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM blocks WHERE id = $1 AND code = 'B1'`, v.BlockIDs[0]))

	require.NoError(t, tx.Rollback(ctx))
	want(t, e.do("PATCH", path, obj{"code": "Z9"}, token), http.StatusOK)

	// Two editors on one block: the second gives up the same way instead of hanging.
	tx2, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx2.Rollback(ctx) }()
	_, err = store.New(tx2).LockBlockForEdit(ctx, v.BlockIDs[0])
	require.NoError(t, err)
	wantBusy(t, "block PATCH (two editors)", func() *httptest.ResponseRecorder { return e.do("PATCH", path, obj{"label": "Mine"}, token) })
}

// Fix 1, the fan. A hold request for an event an organiser has locked (a publish, or a
// cancel of a sold-out stadium) waits briefly and answers 409 EVENT_BUSY; no seat is held.
func TestEventBusy_HoldBehindAPublish(t *testing.T) {
	e := busyEnv(t)
	v := e.newVenue(1, 2, 4)
	ev := e.publishedEvent(v)
	seats := eventSeatIDs(t, ev.ID, v.BlockIDs[0])
	_, fan := e.newFan()

	finish := slowPublish(t, ev.ID)
	wantBusy(t, "createHold", func() *httptest.ResponseRecorder { return e.hold(fan, ev.ID, seats[4], seats[5]) })
	wantBusy(t, "best available", func() *httptest.ResponseRecorder { return e.best(fan, ev.ID, ev.Cats["Regular"], 2) })
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM holds WHERE event_id = $1`, ev.ID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1 AND status <> 'AVAILABLE'`, ev.ID))

	finish()
	held := want(t, e.hold(fan, ev.ID, seats[4], seats[5]), http.StatusCreated)
	want(t, e.best(fan, ev.ID, ev.Cats["Regular"], 2), http.StatusCreated)
	checkInventory(t, ev.ID)

	// Giving seats back waits the same bounded time, and keeps the hold when it gives up.
	holdPath := fmt.Sprintf("/holds/%d", id64(held["id"]))
	finish = slowPublish(t, ev.ID)
	wantBusy(t, "releaseHold", func() *httptest.ResponseRecorder { return e.do("DELETE", holdPath, nil, fan) })
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM holds WHERE id = $1 AND status = 'ACTIVE'`, id64(held["id"])))
	finish()
	want(t, e.do("DELETE", holdPath, nil, fan), http.StatusNoContent)
	checkInventory(t, ev.ID)
}

// The bounded wait is lifted again once the event lock is taken, so the rest of a
// transaction waits for row locks exactly as before (a hold queued behind another fan's
// hold of the same seat must wait for it, not give up).
func TestLockTimeoutIsLiftedAfterTheEventLock(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	ev := e.publishedEvent(e.newVenue(1, 1, 2))
	require.NoError(t, store.InTxRaw(ctx, db.Pool, func(tx pgx.Tx) error {
		if _, err := store.New(tx).LockEventWithin(ctx, ev.ID, 250*time.Millisecond); err != nil {
			return err
		}
		var setting string
		if err := tx.QueryRow(ctx, `SHOW lock_timeout`).Scan(&setting); err != nil {
			return err
		}
		assert.Equal(t, "0", setting, "back to the session's own setting")
		return nil
	}))

	// And it is really a timeout while it is set.
	hold := slowPublish(t, ev.ID)
	err := store.InTxRaw(ctx, db.Pool, func(tx pgx.Tx) error {
		_, err := store.New(tx).LockEventWithin(ctx, ev.ID, 250*time.Millisecond)
		return err
	})
	require.ErrorIs(t, err, store.ErrLockTimeout)
	assert.True(t, store.IsLockTimeout(err))
	hold()
}
