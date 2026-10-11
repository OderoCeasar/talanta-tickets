//go:build integration

package httpapi_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
)

func TestSeatMap_ThreeLevels(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(3, 2, 4) // blocks 1 and 3 in the N stand, block 2 in the S stand
	other := e.newVenue(1, 1, 2)
	exec(t, `UPDATE seats SET seat_type = 'ACCESSIBLE' WHERE id = (SELECT min(id) FROM seats WHERE block_id = $1)`, v.BlockIDs[0])
	ev := e.publishedEvent(v)
	e.publishedEvent(other)
	base := fmt.Sprintf("/events/%d", ev.ID)
	// Every seat of the S stand is sold.
	sellSeats(t, eventSeatIDs(t, ev.ID, v.BlockIDs[1])...)
	// The publish answer itself filled the counts cache; step past it.
	e.clock.Advance(4 * time.Second)

	// Level 1.
	m := want(t, e.do("GET", base+"/map", nil, ""), http.StatusOK)
	assert.Equal(t, obj{"id": float64(v.ID), "name": v.Name, "plan_width": 1000.0, "plan_height": 1000.0}, m["venue"])
	stands := list(m, "stands")
	require.Len(t, stands, 2)
	north, south := stands[0].(obj), stands[1].(obj)
	assert.Equal(t, "N", north["code"])
	assert.Equal(t, "N-LOWER", north["gate_group"])
	assert.EqualValues(t, 16, north["seats_total"])
	assert.EqualValues(t, 16, north["seats_available"])
	assert.EqualValues(t, 100_000, north["price_from_cents"], "the cheapest available seat in the stand")
	assert.EqualValues(t, 8, south["seats_total"])
	assert.EqualValues(t, 0, south["seats_available"])
	assert.Nil(t, south["price_from_cents"], "null when nothing is available")
	assert.NotContains(t, e.do("GET", base+"/map", nil, "").Body.String(), ", ", "polygons are sent compact")

	// Level 2.
	sb := want(t, e.do("GET", fmt.Sprintf("%s/stands/%d/blocks", base, v.StandIDs[0]), nil, ""), http.StatusOK)
	cats := list(sb, "categories")
	require.Len(t, cats, 2)
	assert.Equal(t, "Regular", cats[0].(obj)["name"], "cheapest first")
	assert.Equal(t, obj{"id": float64(ev.Cats["VIP"]), "name": "VIP", "price_cents": 500_000.0, "color_hex": "#2563EB"}, cats[1])
	blocks := list(sb, "blocks")
	require.Len(t, blocks, 2, "only this stand's blocks")
	b1 := blocks[0].(obj)
	assert.Equal(t, float64(v.BlockIDs[0]), b1["id"])
	assert.EqualValues(t, 8, b1["seats_total"])
	assert.EqualValues(t, 8, b1["seats_available"])
	assert.EqualValues(t, 100_000, b1["price_from_cents"])
	assert.Equal(t, []any{
		obj{"category_id": float64(ev.Cats["Regular"]), "seats_available": 4.0},
		obj{"category_id": float64(ev.Cats["VIP"]), "seats_available": 4.0},
	}, b1["badges"])
	assert.Len(t, list(b1, "outline"), 4)
	assert.Len(t, list(blocks[1].(obj), "badges"), 1)

	southBlocks := want(t, e.do("GET", fmt.Sprintf("%s/stands/%d/blocks", base, v.StandIDs[1]), nil, ""), http.StatusOK)
	sold := list(southBlocks, "blocks")[0].(obj)
	assert.EqualValues(t, 0, sold["seats_available"])
	assert.Nil(t, sold["price_from_cents"])
	assert.Len(t, list(southBlocks, "categories"), 1)

	// Level 3: one block, its own categories, seat tuples.
	full := want(t, e.seats(ev.ID, v.BlockIDs[0], "", ""), http.StatusOK)
	assert.Equal(t, "full", full["kind"])
	assert.EqualValues(t, 5, full["poll_interval_seconds"])
	assert.NotEmpty(t, full["marker"])
	blk := full["block"].(obj)
	assert.Equal(t, float64(v.BlockIDs[0]), blk["id"])
	assert.Equal(t, float64(v.StandIDs[0]), blk["stand_id"])
	assert.Equal(t, obj{"min_x": 46.0, "min_y": 96.0, "max_x": 69.0, "max_y": 112.0}, blk["bounds"])
	require.Len(t, list(full, "categories"), 2)
	rows := list(full, "rows")
	require.Len(t, rows, 2)
	assert.Equal(t, "A", rows[0].(obj)["label"])
	ids := eventSeatIDs(t, ev.ID, v.BlockIDs[0])
	front := list(rows[0].(obj), "seats")
	require.Len(t, front, 4)
	assert.Equal(t, []any{float64(ids[0]), 1.0, 50.0, 100.0, 1.0, 1.0, "A"}, front[0], "accessible seat, VIP row")
	assert.Equal(t, []any{float64(ids[1]), 2.0, 55.0, 100.0, 0.0, 1.0, "A"}, front[1])
	assert.Equal(t, []any{float64(ids[4]), 1.0, 50.0, 108.0, 0.0, 0.0, "A"}, list(rows[1].(obj), "seats")[0], "back row is Regular")
	assert.Len(t, seatCodes(t, full), 8, "this block's seats and no others")
	assert.Equal(t, "no-store", e.seats(ev.ID, v.BlockIDs[0], "", "").Header().Get("Cache-Control"))

	soldBlock := seatCodes(t, want(t, e.seats(ev.ID, v.BlockIDs[1], "", ""), http.StatusOK))
	for id, code := range soldBlock {
		assert.Equal(t, "S", code, "seat %d", id)
	}

	// A block or stand of another venue is not part of this event.
	wantError(t, e.seats(ev.ID, other.BlockIDs[0], "", ""), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.seats(ev.ID, 999_999_999, "", ""), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.seats(999_999_999, v.BlockIDs[0], "", ""), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("GET", fmt.Sprintf("%s/stands/%d/blocks", base, other.StandIDs[0]), nil, ""), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("GET", base+"/stands/999999999/blocks", nil, ""), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("GET", "/events/999999999/map", nil, ""), http.StatusNotFound, domain.CodeNotFound)
	wantFields(t, e.do("GET", "/events/x/map", nil, ""), "eventId")
	wantFields(t, e.do("GET", base+"/stands/0/blocks", nil, ""), "standId")
	wantFields(t, e.do("GET", base+"/blocks/b/seats", nil, ""), "blockId")
	// A bad token is not quietly treated as "anonymous".
	wantError(t, e.seats(ev.ID, v.BlockIDs[0], "", "not-a-token"), http.StatusUnauthorized, domain.CodeUnauthenticated)
}

// Rule 6: level 1 and level 2 counts are reused for a few seconds; level 3 never is.
func TestSeatMap_CountsAreCachedBrieflyAndLevel3IsNot(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 2, 4)
	ev := e.publishedEvent(v)
	mapPath := fmt.Sprintf("/events/%d/map", ev.ID)
	blocksPath := fmt.Sprintf("/events/%d/stands/%d/blocks", ev.ID, v.StandIDs[0])
	available := func(path, key string) float64 {
		o := want(t, e.do("GET", path, nil, ""), http.StatusOK)
		return list(o, key)[0].(obj)["seats_available"].(float64)
	}
	require.EqualValues(t, 8, available(mapPath, "stands"))

	ids := eventSeatIDs(t, ev.ID, v.BlockIDs[0])
	sellSeats(t, ids[0], ids[1])
	assert.EqualValues(t, 8, available(mapPath, "stands"), "served from the cache")
	assert.EqualValues(t, 8, available(blocksPath, "blocks"), "level 2 shares it")
	// Level 3 is read fresh every time.
	codes := seatCodes(t, want(t, e.seats(ev.ID, v.BlockIDs[0], "", ""), http.StatusOK))
	assert.Equal(t, "S", codes[ids[0]])

	e.clock.Advance(2 * time.Second)
	assert.EqualValues(t, 8, available(mapPath, "stands"), "still inside the three seconds")
	e.clock.Advance(1100 * time.Millisecond)
	assert.EqualValues(t, 6, available(mapPath, "stands"))
	assert.EqualValues(t, 6, available(blocksPath, "blocks"))

	// Cancel drops the cached counts at once: the held seat is free in the very next read.
	fanID, _ := e.newFan()
	holdSeats(t, ev.ID, fanID, time.Now().Add(10*time.Minute), ids[2])
	e.clock.Advance(4 * time.Second)
	require.EqualValues(t, 5, available(mapPath, "stands"))
	want(t, e.do("POST", fmt.Sprintf("/admin/events/%d/cancel", ev.ID), nil, e.staffToken(domain.RoleOrganiser)), http.StatusOK)
	assert.EqualValues(t, 6, available(mapPath, "stands"))
}

func forgeMarker(eventID, blockID int64, at time.Time) string {
	return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("m1.%d.%d.%d", eventID, blockID, at.UnixMicro())))
}

// M5 "done when": the polling endpoint returns only seats that changed.
func TestSeatMap_SinceReturnsOnlyChangedSeats(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(2, 3, 4)
	ev := e.publishedEvent(v)
	// As if published an hour ago: otherwise every seat "changed" in the last seconds.
	age(t, ev.ID, time.Hour)
	block := v.BlockIDs[0]
	ids := eventSeatIDs(t, ev.ID, block)

	full := want(t, e.seats(ev.ID, block, "", ""), http.StatusOK)
	m0 := full["marker"].(string)
	require.Len(t, seatCodes(t, full), 12)

	quiet := want(t, e.seats(ev.ID, block, m0, ""), http.StatusOK)
	assert.Empty(t, changeCodes(t, quiet), "nothing changed: an empty delta is the normal answer")
	assert.NotEqual(t, m0, quiet["marker"], "every answer carries a new marker")
	assert.EqualValues(t, 5, quiet["poll_interval_seconds"])

	fanID, _ := e.newFan()
	sellSeats(t, ids[3])
	holdSeats(t, ev.ID, fanID, time.Now().Add(10*time.Minute), ids[7])
	// A change in another block of the same event is not this block's business.
	sellSeats(t, eventSeatIDs(t, ev.ID, v.BlockIDs[1])[0])

	delta := want(t, e.seats(ev.ID, block, m0, ""), http.StatusOK)
	assert.Equal(t, map[int64]string{ids[3]: "S", ids[7]: "H"}, changeCodes(t, delta), "the two changed seats and nothing else")
	assert.NotContains(t, delta, "rows")
	assert.NotContains(t, delta, "block")

	// A seat that goes back to available is a change too.
	exec(t, `UPDATE event_seats SET status = 'AVAILABLE', hold_id = NULL, hold_expires_at = NULL WHERE id = $1`, ids[7])
	next := changeCodes(t, want(t, e.seats(ev.ID, block, delta["marker"].(string), ""), http.StatusOK))
	assert.Equal(t, "A", next[ids[7]])
	for id := range next {
		assert.Contains(t, []int64{ids[3], ids[7]}, id, "never a seat that did not change")
	}
}

// Rule 4: updated_at is stamped when a row is written, not when it commits. A transaction
// that wrote before a marker was issued and committed after it must still be delivered.
func TestSeatMap_SinceDeliversAChangeThatCommittedLate(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 2, 4)
	ev := e.publishedEvent(v)
	age(t, ev.ID, time.Hour)
	block := v.BlockIDs[0]
	ids := eventSeatIDs(t, ev.ID, block)
	ctx := context.Background()
	m0 := want(t, e.seats(ev.ID, block, "", ""), http.StatusOK)["marker"].(string)

	// Transaction A writes seat 0 first (its stamp is taken now) but does not commit.
	slow, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = slow.Rollback(ctx) }()
	_, err = slow.Exec(ctx, `UPDATE event_seats SET status = 'SOLD' WHERE id = $1`, ids[0])
	require.NoError(t, err)

	// Transaction B writes seat 1 afterwards and commits straight away.
	time.Sleep(20 * time.Millisecond)
	sellSeats(t, ids[1])

	// The poll sees B only, and hands out a marker that is later than A's stamp.
	first := want(t, e.seats(ev.ID, block, m0, ""), http.StatusOK)
	assert.Equal(t, map[int64]string{ids[1]: "S"}, changeCodes(t, first))
	m1 := first["marker"].(string)

	var stampA time.Time
	require.NoError(t, slow.QueryRow(ctx, `SELECT updated_at FROM event_seats WHERE id = $1`, ids[0]).Scan(&stampA))
	time.Sleep(20 * time.Millisecond)
	require.NoError(t, slow.Commit(ctx))

	raw, err := base64.RawURLEncoding.DecodeString(m1)
	require.NoError(t, err)
	var mEv, mBlock, micros int64
	_, err = fmt.Sscanf(string(raw), "m1.%d.%d.%d", &mEv, &mBlock, &micros)
	require.NoError(t, err)
	require.True(t, stampA.Before(time.UnixMicro(micros)), "the test must reproduce the hazard: A's stamp (%s) is older than the marker (%s)", stampA, time.UnixMicro(micros))

	// A is now committed with a stamp older than m1. It must not be lost.
	second := changeCodes(t, want(t, e.seats(ev.ID, block, m1, ""), http.StatusOK))
	assert.Equal(t, "S", second[ids[0]], "the late commit was not delivered: %v", second)
	for id := range second {
		assert.Contains(t, []int64{ids[0], ids[1]}, id, "repeats inside the overlap are fine, strangers are not")
	}
}

func TestSeatMap_MarkerThatCannotBeUsedGetsTheFullBlock(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(2, 1, 3)
	ev := e.publishedEvent(v)
	twin := e.publishedEvent(v)
	age(t, ev.ID, time.Hour)
	block := v.BlockIDs[0]
	good := want(t, e.seats(ev.ID, block, "", ""), http.StatusOK)["marker"].(string)
	otherBlock := want(t, e.seats(ev.ID, v.BlockIDs[1], "", ""), http.StatusOK)["marker"].(string)
	otherEvent := want(t, e.seats(twin.ID, block, "", ""), http.StatusOK)["marker"].(string)

	require.Equal(t, "delta", want(t, e.seats(ev.ID, block, good, ""), http.StatusOK)["kind"])
	now := time.Now()
	for name, marker := range map[string]string{
		"garbage":            "not-a-marker",
		"valid base64 only":  base64.RawURLEncoding.EncodeToString([]byte("hello there")),
		"another block":      otherBlock,
		"another event":      otherEvent,
		"an old version":     base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("m0.%d.%d.%d", ev.ID, block, now.UnixMicro()))),
		"too old":            forgeMarker(ev.ID, block, now.Add(-6*time.Minute)),
		"from the future":    forgeMarker(ev.ID, block, now.Add(time.Minute)),
		"a negative time":    base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("m1.%d.%d.-5", ev.ID, block))),
		"a number too large": base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("m1.%d.%d.99999999999999999999999", ev.ID, block))),
	} {
		o := want(t, e.seats(ev.ID, block, marker, ""), http.StatusOK)
		assert.Equal(t, "full", o["kind"], name)
		assert.Len(t, seatCodes(t, o), 3, name)
	}
	// Four minutes old is still inside the bound.
	assert.Equal(t, "delta", want(t, e.seats(ev.ID, block, forgeMarker(ev.ID, block, now.Add(-4*time.Minute)), ""), http.StatusOK)["kind"])

	// A marker of the wrong size is a malformed request, not a stale one.
	wantFields(t, e.do("GET", fmt.Sprintf("/events/%d/blocks/%d/seats?since=", ev.ID, block), nil, ""), "since")
	wantFields(t, e.seats(ev.ID, block, strings.Repeat("a", 129), ""), "since")

	// Something other than a seat status changed (a price): the next poll is a full answer
	// carrying the new category table.
	want(t, e.do("PATCH", fmt.Sprintf("/admin/categories/%d", ev.Cats["VIP"]), obj{"price_cents": 650_000, "confirm": true}, ev.Token), http.StatusOK)
	o := want(t, e.seats(ev.ID, block, good, ""), http.StatusOK)
	require.Equal(t, "full", o["kind"])
	prices := map[string]float64{}
	for _, c := range list(o, "categories") {
		prices[c.(obj)["name"].(string)] = c.(obj)["price_cents"].(float64)
	}
	assert.EqualValues(t, 650_000, prices["VIP"])
}

// Rule 5: a held seat is "H" to everyone except its holder, who sees "M".
func TestSeatMap_HoldIsMineOnlyToItsHolder(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 2, 4)
	ev := e.publishedEvent(v)
	age(t, ev.ID, time.Hour)
	block := v.BlockIDs[0]
	ids := eventSeatIDs(t, ev.ID, block)
	holderID, holder := e.newFan()
	strangerID, stranger := e.newFan()
	marker := want(t, e.seats(ev.ID, block, "", ""), http.StatusOK)["marker"].(string)

	holdSeats(t, ev.ID, holderID, time.Now().Add(10*time.Minute), ids[0], ids[1])
	holdSeats(t, ev.ID, strangerID, time.Now().Add(10*time.Minute), ids[2])

	for name, c := range map[string]struct {
		token string
		want  []string
	}{
		"the holder":   {holder, []string{"M", "M", "H", "A"}},
		"another fan":  {stranger, []string{"H", "H", "M", "A"}},
		"nobody":       {"", []string{"H", "H", "H", "A"}},
		"an organiser": {e.staffToken(domain.RoleOrganiser), []string{"H", "H", "H", "A"}},
	} {
		rec := e.seats(ev.ID, block, "", c.token)
		codes := seatCodes(t, want(t, rec, http.StatusOK))
		assert.Equal(t, c.want, []string{codes[ids[0]], codes[ids[1]], codes[ids[2]], codes[ids[3]]}, "full, seen by %s", name)

		rec = e.seats(ev.ID, block, marker, c.token)
		changes := changeCodes(t, want(t, rec, http.StatusOK))
		assert.Equal(t, map[int64]string{ids[0]: c.want[0], ids[1]: c.want[1], ids[2]: c.want[2]}, changes, "delta, seen by %s", name)
	}

	// Nothing that identifies a holder is in any answer: not the hold, not the user.
	for _, token := range []string{holder, stranger, ""} {
		for _, since := range []string{"", marker} {
			text := e.seats(ev.ID, block, since, token).Body.String()
			assert.NotContains(t, text, "hold")
			assert.NotContains(t, text, "user")
			for _, row := range list(body(t, e.seats(ev.ID, block, "", token)), "rows") {
				for _, s := range list(row.(obj), "seats") {
					require.Len(t, tuple(s), 7)
				}
			}
		}
	}
}

// Rule 5: a hold whose time has run out is reported as available, sweeper or no sweeper.
func TestSeatMap_LapsedHoldIsAvailable(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 2, 4)
	ev := e.publishedEvent(v)
	block := v.BlockIDs[0]
	ids := eventSeatIDs(t, ev.ID, block)
	holderID, holder := e.newFan()

	holdSeats(t, ev.ID, holderID, time.Now().Add(-time.Minute), ids[0])
	holdSeats(t, ev.ID, holderID, time.Now().Add(10*time.Minute), ids[1])
	require.Equal(t, 2, queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1 AND status = 'HELD'`, ev.ID), "nothing has swept the lapsed hold")

	for _, token := range []string{"", holder} {
		codes := seatCodes(t, want(t, e.seats(ev.ID, block, "", token), http.StatusOK))
		assert.Equal(t, "A", codes[ids[0]], "lapsed: available, even to the fan who held it")
	}
	assert.Equal(t, "M", seatCodes(t, want(t, e.seats(ev.ID, block, "", holder), http.StatusOK))[ids[1]])

	// The counts agree: 6 never held, plus the lapsed one. (Step past the counts cached by
	// the publish answer.)
	e.clock.Advance(4 * time.Second)
	m := want(t, e.do("GET", fmt.Sprintf("/events/%d/map", ev.ID), nil, ""), http.StatusOK)
	assert.EqualValues(t, 7, list(m, "stands")[0].(obj)["seats_available"])
	sales := want(t, e.do("GET", fmt.Sprintf("/admin/events/%d/sales", ev.ID), nil, ev.Token), http.StatusOK)
	assert.Equal(t, obj{"sold": 0.0, "held": 1.0, "available": 7.0, "blocked": 0.0, "total": 8.0}, sales["seats"])

	// A hold that lapses while a block is open reaches the poll although no row is
	// written when it lapses. The row's own stamp is put in the past so that only the
	// expiry can be what the poll finds.
	holdSeats(t, ev.ID, holderID, time.Now().Add(1500*time.Millisecond), ids[2])
	age(t, ev.ID, time.Hour)
	live := want(t, e.seats(ev.ID, block, "", ""), http.StatusOK)
	require.Equal(t, "H", seatCodes(t, live)[ids[2]])
	time.Sleep(1700 * time.Millisecond)
	changes := changeCodes(t, want(t, e.seats(ev.ID, block, live["marker"].(string), ""), http.StatusOK))
	assert.Equal(t, map[int64]string{ids[2]: "A"}, changes)
}

func TestPublicReads_RateLimited(t *testing.T) {
	e := newEnv(t, withLimits(func(l *httpapi.RateLimits) {
		l.PublicReadPerIP = ratelimit.Rule{Limit: 2, Window: time.Minute}
	}))
	want(t, e.do("GET", "/events", nil, ""), http.StatusOK)
	want(t, e.do("GET", "/events", nil, ""), http.StatusOK)
	for _, path := range []string{"/events", "/events/1", "/events/1/categories", "/events/1/map", "/events/1/stands/1/blocks", "/events/1/blocks/1/seats"} {
		rec := e.do("GET", path, nil, "")
		wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
		assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	}
	// Another address has its own allowance.
	e.remoteAddr = "198.51.100.99:40000"
	want(t, e.do("GET", "/events", nil, ""), http.StatusOK)
}

// collect is a tiny helper for one-column queries.
func collect[T any](t testing.TB, sql string, args ...any) []T {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(), sql, args...)
	require.NoError(t, err)
	out, err := pgx.CollectRows(rows, pgx.RowTo[T])
	require.NoError(t, err)
	return out
}
