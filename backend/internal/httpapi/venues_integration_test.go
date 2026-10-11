//go:build integration

package httpapi_test

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

func TestVenues_ListAndGet(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(3, 2, 4)
	token := e.staffToken(domain.RoleOrganiser)
	exec(t, `UPDATE seats SET is_active = FALSE WHERE id = (SELECT min(id) FROM seats WHERE block_id = $1)`, v.BlockIDs[0])
	e.publishedEvent(v)
	// A block added after the publish has no inventory.
	added := want(t, e.do("POST", fmt.Sprintf("/admin/venues/%d/blocks", v.ID), obj{
		"stand_id": v.StandIDs[0], "code": "NEW-1", "label": "New", "origin_x": 700, "origin_y": 100, "seat_pitch_x": 5, "seat_pitch_y": 8,
	}, token), http.StatusCreated)

	found := false
	for _, it := range items(want(t, e.do("GET", "/admin/venues", nil, token), http.StatusOK)) {
		if id64(it.(obj)["id"]) == v.ID {
			found = true
			assert.Equal(t, v.Name, it.(obj)["name"])
			assert.EqualValues(t, 24, it.(obj)["capacity"], "capacity is kept by seat generation, counting every seat generated")
		}
	}
	assert.True(t, found)

	d := want(t, e.do("GET", fmt.Sprintf("/admin/venues/%d", v.ID), nil, token), http.StatusOK)
	assert.EqualValues(t, 23, d["seat_count"], "active seats only")
	stands := list(d, "stands")
	require.Len(t, stands, 2)
	north := stands[0].(obj)
	assert.Equal(t, "N-LOWER", north["gate_group"])
	assert.Len(t, list(north, "polygon"), 4)
	blocks := map[int64]obj{}
	for _, st := range stands {
		for _, b := range list(st.(obj), "blocks") {
			blocks[id64(b.(obj)["id"])] = b.(obj)
		}
	}
	require.Len(t, blocks, 4)
	first := blocks[v.BlockIDs[0]]
	assert.EqualValues(t, 7, first["seat_count"])
	assert.EqualValues(t, 2, first["row_count"])
	assert.EqualValues(t, 4, first["seats_per_row"])
	assert.Equal(t, true, first["has_inventory"])
	assert.Len(t, list(first, "outline"), 4)
	assert.Nil(t, first["arc_radius"])
	fresh := blocks[id64(added["id"])]
	assert.Equal(t, false, fresh["has_inventory"])
	assert.EqualValues(t, 0, fresh["seat_count"])
	assert.Empty(t, list(fresh, "outline"))

	wantError(t, e.do("GET", "/admin/venues/999999999", nil, token), http.StatusNotFound, domain.CodeNotFound)
	wantFields(t, e.do("GET", "/admin/venues/-1", nil, token), "venueId")
}

func TestBlocks_CreateUpdateDelete(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(2, 2, 3)
	other := e.newVenue(1, 1, 2)
	token := e.staffToken(domain.RoleAdmin)
	create := fmt.Sprintf("/admin/venues/%d/blocks", v.ID)
	block := func(mod func(obj)) obj {
		b := obj{"stand_id": v.StandIDs[0], "code": "X1", "label": "Extra", "origin_x": 600, "origin_y": 200, "seat_pitch_x": 5, "seat_pitch_y": 8}
		if mod != nil {
			mod(b)
		}
		return b
	}

	made := want(t, e.do("POST", create, block(func(b obj) { b["rotation_deg"] = 90; b["arc_radius"] = nil }), token), http.StatusCreated)
	id := id64(made["id"])
	assert.EqualValues(t, 90, made["rotation_deg"])
	assert.EqualValues(t, 0, made["row_count"])
	assert.Equal(t, "LTR", made["numbering_direction"])

	ee := wantError(t, e.do("POST", create, block(nil), token), http.StatusConflict, domain.CodeAlreadyExists)
	assert.Contains(t, string(ee.Error.Details), `"code"`)
	// The same code is fine in another stand.
	want(t, e.do("POST", create, block(func(b obj) { b["stand_id"] = v.StandIDs[1] }), token), http.StatusCreated)
	wantError(t, e.do("POST", create, block(func(b obj) { b["stand_id"] = other.StandIDs[0]; b["code"] = "X2" }), token), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("POST", "/admin/venues/999999999/blocks", block(nil), token), http.StatusNotFound, domain.CodeNotFound)
	wantFields(t, e.do("POST", create, block(func(b obj) { b["code"] = "n 12" }), token), "code")
	wantFields(t, e.do("POST", create, block(func(b obj) { b["seat_pitch_x"] = 0; delete(b, "origin_y"); b["label"] = "" }), token), "seat_pitch_x", "origin_y", "label")
	wantFields(t, e.do("POST", create, block(func(b obj) { b["arc_radius"] = -3; b["rotation_deg"] = 720 }), token), "arc_radius", "rotation_deg")

	// Move a block that has seats: same seat ids, new positions, new outline.
	target := v.BlockIDs[0]
	path := fmt.Sprintf("/admin/blocks/%d", target)
	type seat struct {
		ID   int64
		X, Y float64
	}
	seats := func() []seat {
		rows, err := e.q.ListSeatsByBlock(context.Background(), target)
		require.NoError(t, err)
		var out []seat
		for _, s := range rows {
			out = append(out, seat{s.ID, s.X, s.Y})
		}
		return out
	}
	before := seats()
	moved := want(t, e.do("PATCH", path, obj{"origin_x": 150, "label": "Moved"}, token), http.StatusOK)
	assert.Equal(t, "Moved", moved["label"])
	assert.EqualValues(t, 150, moved["origin_x"])
	assert.EqualValues(t, 6, moved["seat_count"])
	after := seats()
	require.Len(t, after, len(before))
	for i := range before {
		assert.Equal(t, before[i].ID, after[i].ID, "seat ids stay")
		assert.InDelta(t, before[i].X+100, after[i].X, 0.001)
		assert.InDelta(t, before[i].Y, after[i].Y, 0.001)
	}
	assert.EqualValues(t, []any{147.5, 96.0}, list(moved, "outline")[0])

	// Curving a block keeps the seats too, and null straightens it again.
	curved := want(t, e.do("PATCH", path, obj{"arc_radius": 300}, token), http.StatusOK)
	assert.EqualValues(t, 300, curved["arc_radius"])
	straight := want(t, e.do("PATCH", path, obj{"arc_radius": nil}, token), http.StatusOK)
	assert.Nil(t, straight["arc_radius"])
	assert.Equal(t, after, seats())

	// A move that takes the block off the plan is refused, naming what was sent.
	rules := wantFields(t, e.do("PATCH", path, obj{"origin_x": 995}, token), "origin_x")
	assert.Equal(t, "plan_bounds", rules["origin_x"])
	wantFields(t, e.do("PATCH", path, obj{"origin_x": -50, "origin_y": 50}, token), "origin_x", "origin_y")
	assert.Equal(t, after, seats(), "a refused move changes nothing")

	wantError(t, e.do("PATCH", path, obj{"code": "X1"}, token), http.StatusConflict, domain.CodeAlreadyExists)
	wantFields(t, e.do("PATCH", path, obj{}, token), "body")
	wantFields(t, e.do("PATCH", path, obj{"seat_pitch_y": -1}, token), "seat_pitch_y")
	wantFields(t, e.do("PATCH", path, obj{"arc_radius": 0}, token), "arc_radius")
	wantError(t, e.do("PATCH", "/admin/blocks/999999999", obj{"label": "x"}, token), http.StatusNotFound, domain.CodeNotFound)

	// Delete: the empty block goes, and so does one with seats; capacity follows.
	want(t, e.do("DELETE", fmt.Sprintf("/admin/blocks/%d", id), nil, token), http.StatusNoContent)
	wantError(t, e.do("DELETE", fmt.Sprintf("/admin/blocks/%d", id), nil, token), http.StatusNotFound, domain.CodeNotFound)
	wantFields(t, e.do("DELETE", "/admin/blocks/x", nil, token), "blockId")
	assert.Equal(t, 12, queryInt(t, `SELECT capacity FROM venues WHERE id = $1`, v.ID))
	want(t, e.do("DELETE", path, nil, token), http.StatusNoContent)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM seats WHERE block_id = $1`, target))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM rows WHERE block_id = $1`, target))
	assert.Equal(t, 6, queryInt(t, `SELECT capacity FROM venues WHERE id = $1`, v.ID))
}

func TestBlockDeleteAndRegenerateRefusedWithInventory(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(2, 2, 3)
	first := e.publishedEvent(v)
	second := e.publishedEvent(v)
	draft := e.readyEvent(v)
	token := first.Token
	path := fmt.Sprintf("/admin/blocks/%d", v.BlockIDs[0])

	ee := wantError(t, e.do("DELETE", path, nil, token), http.StatusConflict, domain.CodeBlockHasInventory)
	assert.ElementsMatch(t, []any{float64(first.ID), float64(second.ID)}, details(t, ee)["event_ids"], "published events only, not the draft %d", draft.ID)

	ee = wantError(t, e.do("POST", path+"/generate-seats", obj{"rows": 1, "seats_per_row": 1}, token), http.StatusConflict, domain.CodeBlockHasInventory)
	assert.Len(t, details(t, ee)["event_ids"], 2)
	assert.Equal(t, 6, queryInt(t, `SELECT count(*) FROM seats WHERE block_id = $1`, v.BlockIDs[0]), "nothing was replaced")

	// A preview is always allowed, and a move still is: neither touches seat ids.
	want(t, e.do("POST", path+"/generate-seats", obj{"rows": 1, "seats_per_row": 1, "dry_run": true}, token), http.StatusOK)
	want(t, e.do("PATCH", path, obj{"origin_x": 60}, token), http.StatusOK)
}

// A publish holds the venue row while it copies seats. A delete or regenerate at that venue
// waits a moment and then answers 409, not a timeout.
func TestBlockDeleteWhilePublishHoldsTheVenue(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 1, 2)
	token := e.staffToken(domain.RoleOrganiser)
	ctx := context.Background()

	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = store.New(tx).LockVenueForPublish(ctx, v.ID)
	require.NoError(t, err)

	ee := wantError(t, e.do("DELETE", fmt.Sprintf("/admin/blocks/%d", v.BlockIDs[0]), nil, token), http.StatusConflict, domain.CodeBlockHasInventory)
	assert.Empty(t, details(t, ee)["event_ids"])
	assert.Contains(t, ee.Error.Message, "being published")

	require.NoError(t, tx.Rollback(ctx))
	want(t, e.do("DELETE", fmt.Sprintf("/admin/blocks/%d", v.BlockIDs[0]), nil, token), http.StatusNoContent)
}

func TestGenerateSeats(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 2, 3)
	token := e.staffToken(domain.RoleOrganiser)
	blockID := v.BlockIDs[0]
	gen := fmt.Sprintf("/admin/blocks/%d/generate-seats", blockID)
	layoutPath := fmt.Sprintf("/admin/blocks/%d/seats", blockID)
	oldSeats := queryInt(t, `SELECT max(id) FROM seats WHERE block_id = $1`, blockID)

	// Dry run: the preview, with null ids, and nothing written.
	preview := want(t, e.do("POST", gen, obj{
		"rows": 3, "seats_per_row": 4, "row_label_scheme": "NUMERIC", "numbering_direction": "RTL", "row_offsets": []float64{0, 2.5, 5}, "dry_run": true,
	}, token), http.StatusOK)
	assert.Equal(t, true, preview["dry_run"])
	assert.EqualValues(t, 12, preview["seat_count"])
	rows := list(preview, "rows")
	require.Len(t, rows, 3)
	assert.Nil(t, rows[0].(obj)["id"])
	assert.Equal(t, "2", rows[1].(obj)["label"])
	assert.EqualValues(t, 2.5, rows[1].(obj)["x_offset"])
	seat := tuple(list(rows[1].(obj), "seats")[0])
	assert.Nil(t, seat[0], "no seat id in a preview")
	assert.EqualValues(t, 4, seat[1], "right-to-left numbering starts at the far end")
	assert.EqualValues(t, 52.5, seat[2])
	assert.EqualValues(t, 108, seat[3])
	assert.NotNil(t, preview["bounds"])
	assert.Equal(t, 6, queryInt(t, `SELECT count(*) FROM seats WHERE block_id = $1`, blockID))
	assert.Equal(t, oldSeats, queryInt(t, `SELECT max(id) FROM seats WHERE block_id = $1`, blockID))
	assert.Equal(t, 6, queryInt(t, `SELECT capacity FROM venues WHERE id = $1`, v.ID))

	// A draft's row override on the old rows goes with them.
	ev := e.readyEvent(v)
	require.Equal(t, 1, queryInt(t, `SELECT count(*) FROM event_row_categories WHERE event_id = $1`, ev.ID))

	// Real run: the block's rows and seats are replaced.
	saved := want(t, e.do("POST", gen, obj{"rows": 3, "seats_per_row": 4, "row_label_scheme": "NUMERIC", "numbering_direction": "RTL"}, token), http.StatusOK)
	assert.Equal(t, false, saved["dry_run"])
	assert.EqualValues(t, 12, saved["seat_count"])
	rows = list(saved, "rows")
	require.Len(t, rows, 3)
	assert.NotNil(t, rows[0].(obj)["id"])
	firstSeat := tuple(list(rows[0].(obj), "seats")[0])
	assert.Greater(t, id64(firstSeat[0]), int64(oldSeats), "new seats, new ids")
	assert.EqualValues(t, 1, firstSeat[5])
	assert.Equal(t, 12, queryInt(t, `SELECT count(*) FROM seats WHERE block_id = $1`, blockID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM seats WHERE block_id = $1 AND id <= $2`, blockID, oldSeats))
	assert.Equal(t, 3, queryInt(t, `SELECT count(*) FROM rows WHERE block_id = $1`, blockID))
	assert.Equal(t, 12, queryInt(t, `SELECT capacity FROM venues WHERE id = $1`, v.ID), "capacity follows the seats")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM seats WHERE block_id = $1 AND label = '3-4'`, blockID), "numeric rows put a dash in the seat label")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM blocks WHERE id = $1 AND row_count = 3 AND seats_per_row = 4 AND numbering_direction = 'RTL'`, blockID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM event_row_categories WHERE event_id = $1`, ev.ID))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM event_block_categories WHERE event_id = $1`, ev.ID), "the block's own painting stays")

	// The saved layout reads back the same.
	read := want(t, e.do("GET", layoutPath, nil, token), http.StatusOK)
	assert.Equal(t, saved["rows"], read["rows"])
	assert.Equal(t, saved["outline"], read["outline"])
	assert.Equal(t, saved["bounds"], read["bounds"])

	// A layout that leaves the plan is refused, naming the fields, and writes nothing.
	rules := wantFields(t, e.do("POST", gen, obj{"rows": 2, "seats_per_row": 400}, token), "rows", "seats_per_row")
	assert.Equal(t, "plan_bounds", rules["seats_per_row"])
	wantFields(t, e.do("POST", gen, obj{"rows": 2, "seats_per_row": 400, "dry_run": true}, token), "rows", "seats_per_row")
	assert.Equal(t, 12, queryInt(t, `SELECT count(*) FROM seats WHERE block_id = $1`, blockID))

	wantFields(t, e.do("POST", gen, obj{"rows": 3, "seats_per_row": 4, "row_offsets": []float64{1, 2}}, token), "row_offsets")
	wantFields(t, e.do("POST", gen, obj{"rows": 201, "seats_per_row": 0}, token), "rows", "seats_per_row")
	// Each number is within its own limit, but together they ask for 100,000 seats (and
	// 3,050): refused on both fields, for a preview too, and nothing is written.
	for _, b := range []obj{
		{"rows": 200, "seats_per_row": 500}, {"rows": 200, "seats_per_row": 500, "dry_run": true}, {"rows": 61, "seats_per_row": 50, "dry_run": true},
	} {
		rules = wantFields(t, e.do("POST", gen, b, token), "rows", "seats_per_row")
		assert.Equal(t, "max_seats", rules["rows"])
		assert.Equal(t, "max_seats", rules["seats_per_row"])
	}
	assert.Equal(t, 12, queryInt(t, `SELECT count(*) FROM seats WHERE block_id = $1`, blockID))
	wantFields(t, e.do("POST", gen, obj{"rows": 1, "seats_per_row": 1, "row_label_scheme": "ROMAN", "numbering_direction": "UP"}, token), "row_label_scheme", "numbering_direction")
	wantError(t, e.do("POST", "/admin/blocks/999999999/generate-seats", obj{"rows": 1, "seats_per_row": 1}, token), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("GET", "/admin/blocks/999999999/seats", nil, token), http.StatusNotFound, domain.CodeNotFound)

	// A curve too tight for the row is a validation error from the seatmap package.
	want(t, e.do("PATCH", fmt.Sprintf("/admin/blocks/%d", blockID), obj{"origin_x": 500, "origin_y": 500}, token), http.StatusOK)
	exec(t, `UPDATE blocks SET arc_radius = 10 WHERE id = $1`, blockID)
	rules = wantFields(t, e.do("POST", gen, obj{"rows": 1, "seats_per_row": 100, "dry_run": true}, token), "seats_per_row")
	assert.Equal(t, "layout", rules["seats_per_row"])
}
