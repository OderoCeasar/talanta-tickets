//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

func problemCodes(t testing.TB, problems []any) map[string][]obj {
	t.Helper()
	out := map[string][]obj{}
	for _, p := range problems {
		po := p.(obj)
		out[po["code"].(string)] = append(out[po["code"].(string)], po)
	}
	return out
}

func details(t testing.TB, e errorJSON) obj {
	t.Helper()
	var d obj
	require.NoError(t, json.Unmarshal(e.Error.Details, &d))
	return d
}

func TestStaffRoutes_RolesAndAuth(t *testing.T) {
	e := newEnv(t)
	_, fan := e.newFan()
	for _, path := range []string{"/admin/venues", "/admin/events"} {
		want(t, e.do("GET", path, nil, e.staffToken(domain.RoleOrganiser)), http.StatusOK)
		want(t, e.do("GET", path, nil, e.staffToken(domain.RoleAdmin)), http.StatusOK)
		wantError(t, e.do("GET", path, nil, fan), http.StatusForbidden, domain.CodeForbidden)
		wantError(t, e.do("GET", path, nil, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
	}
	// A fan may not change anything either.
	v := e.newVenue(1, 1, 2)
	wantError(t, e.do("POST", "/admin/events", e.eventBody(v), fan), http.StatusForbidden, domain.CodeForbidden)
	wantError(t, e.do("POST", fmt.Sprintf("/admin/blocks/%d/generate-seats", v.BlockIDs[0]), obj{"rows": 1, "seats_per_row": 1}, fan),
		http.StatusForbidden, domain.CodeForbidden)
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM seats WHERE block_id = $1`, v.BlockIDs[0]))
}

func TestCreateEvent_Validation(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 1, 2)
	token := e.staffToken(domain.RoleOrganiser)
	post := func(mod func(obj)) *httptest.ResponseRecorder {
		b := e.eventBody(v)
		mod(b)
		return e.do("POST", "/admin/events", b, token)
	}

	wantFields(t, post(func(b obj) { delete(b, "title"); delete(b, "kickoff_at") }), "title", "kickoff_at")
	wantFields(t, post(func(b obj) { b["title"] = "   " }), "title")
	rules := wantFields(t, post(func(b obj) { b["kickoff_at"] = "next friday" }), "kickoff_at")
	assert.Equal(t, "datetime", rules["kickoff_at"])
	wantFields(t, post(func(b obj) { b["max_tickets_per_user"] = 51 }), "max_tickets_per_user")
	wantFields(t, post(func(b obj) { b["poster_url"] = "javascript:alert(1)" }), "poster_url")
	wantFields(t, post(func(b obj) { b["status"] = "ON_SALE" }), "status")
	// Sales may not close after kickoff, and gates open before it.
	wantFields(t, post(func(b obj) {
		b["sales_close_at"] = time.Now().Add(60 * 24 * time.Hour).UTC().Format(time.RFC3339)
		b["gates_open_at"] = b["sales_close_at"]
	}), "sales_close_at", "gates_open_at")
	wantFields(t, post(func(b obj) { b["sales_close_at"] = b["sales_open_at"] }), "sales_close_at")

	wantError(t, post(func(b obj) { b["venue_id"] = 999_999_999 }), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, post(func(b obj) { b["template_id"] = 999_999_999 }), http.StatusNotFound, domain.CodeNotFound)

	// Only what the contract requires: the rest takes its default.
	o := want(t, e.do("POST", "/admin/events", obj{"venue_id": v.ID, "title": "Bare", "kickoff_at": "2031-01-01T13:00:00Z"}, token), http.StatusCreated)
	assert.EqualValues(t, 6, o["max_tickets_per_user"])
	assert.Nil(t, o["sales_open_at"])
	assert.Equal(t, v.Name, o["venue_name"])
	assert.EqualValues(t, 0, o["seats_total"])
	assert.Nil(t, o["price_from_cents"])
	assert.Nil(t, o["published_at"])
}

func TestTemplates_CreateListAndApply(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(3, 2, 3)
	other := e.newVenue(1, 1, 2)
	src := e.readyEvent(v)
	token := src.Token
	foreign := e.createEvent(other, token)
	templates := fmt.Sprintf("/admin/venues/%d/templates", v.ID)

	want(t, e.do("GET", templates, nil, token), http.StatusOK)
	name := "League match " + randDigits(t, 6)
	tpl := want(t, e.do("POST", templates, obj{"name": name, "event_id": src.ID}, token), http.StatusCreated)
	assert.EqualValues(t, len(v.BlockIDs), tpl["block_assignment_count"])
	assert.EqualValues(t, 1, tpl["row_assignment_count"])
	require.Len(t, list(tpl, "categories"), 2)

	wantError(t, e.do("POST", templates, obj{"name": name, "event_id": src.ID}, token), http.StatusConflict, domain.CodeAlreadyExists)
	wantError(t, e.do("POST", templates, obj{"name": "x", "event_id": foreign.ID}, token), http.StatusUnprocessableEntity, domain.CodeVenueMismatch)
	wantError(t, e.do("POST", templates, obj{"name": "x", "event_id": 999_999_999}, token), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("POST", "/admin/venues/999999999/templates", obj{"name": "x", "event_id": src.ID}, token), http.StatusNotFound, domain.CodeNotFound)
	wantFields(t, e.do("POST", templates, obj{"event_id": src.ID}, token), "name")
	wantError(t, e.do("GET", "/admin/venues/999999999/templates", nil, token), http.StatusNotFound, domain.CodeNotFound)

	listed := items(want(t, e.do("GET", templates, nil, token), http.StatusOK))
	require.Len(t, listed, 1)
	assert.Equal(t, name, listed[0].(obj)["name"])

	// Later changes to the source event do not reach the template.
	want(t, e.do("PATCH", fmt.Sprintf("/admin/categories/%d", src.Cats["VIP"]), obj{"price_cents": 999_900}, token), http.StatusOK)

	// A new event from the template gets the same categories and the same painting.
	created := want(t, e.do("POST", "/admin/events", func() obj {
		b := e.eventBody(v)
		b["template_id"] = tpl["id"]
		return b
	}(), token), http.StatusCreated)
	newID := id64(created["id"])

	cats := items(want(t, e.do("GET", fmt.Sprintf("/admin/events/%d/categories", newID), nil, token), http.StatusOK))
	require.Len(t, cats, 2)
	byName := map[string]obj{}
	nameOf := map[int64]string{}
	for _, c := range cats {
		byName[c.(obj)["name"].(string)] = c.(obj)
		nameOf[id64(c.(obj)["id"])] = c.(obj)["name"].(string)
	}
	assert.EqualValues(t, 100_000, byName["Regular"]["price_cents"])
	assert.EqualValues(t, 500_000, byName["VIP"]["price_cents"], "the price at the time the template was saved")
	assert.Equal(t, "#2563EB", byName["VIP"]["color_hex"])
	assert.NotEqual(t, src.Cats["VIP"], id64(byName["VIP"]["id"]), "categories are copied, not shared")

	a := want(t, e.do("GET", fmt.Sprintf("/admin/events/%d/assignments", newID), nil, token), http.StatusOK)
	require.Len(t, list(a, "blocks"), len(v.BlockIDs))
	for _, b := range list(a, "blocks") {
		assert.Equal(t, "Regular", nameOf[id64(b.(obj)["category_id"])])
	}
	require.Len(t, list(a, "rows"), 1)
	row := list(a, "rows")[0].(obj)
	assert.Equal(t, v.RowIDs[v.BlockIDs[0]][0], id64(row["row_id"]))
	assert.Equal(t, v.BlockIDs[0], id64(row["block_id"]))
	assert.Equal(t, "VIP", nameOf[id64(row["category_id"])])

	// The copy is ready to publish as it stands.
	rd := want(t, e.do("GET", fmt.Sprintf("/admin/events/%d/readiness", newID), nil, token), http.StatusOK)
	assert.Equal(t, true, rd["ready"], "%v", rd["problems"])

	// A template of another venue cannot be applied.
	b := e.eventBody(other)
	b["template_id"] = tpl["id"]
	wantError(t, e.do("POST", "/admin/events", b, token), http.StatusUnprocessableEntity, domain.CodeVenueMismatch)
}

// M4 "done when": readiness lists what is missing, publishing with problems is refused with
// the same list, publishing creates exactly one inventory row per active seat, and
// publishing twice is safe.
func TestReadinessAndPublish(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(3, 2, 4)
	token := e.staffToken(domain.RoleOrganiser)
	// One seat is switched off: it must never get an inventory row.
	exec(t, `UPDATE seats SET is_active = FALSE WHERE id = (SELECT min(id) FROM seats WHERE block_id = $1)`, v.BlockIDs[2])
	active := v.seats() - 1

	ev := e.createEvent(v, token, func(b obj) { delete(b, "sales_open_at"); delete(b, "sales_close_at") })
	readiness := fmt.Sprintf("/admin/events/%d/readiness", ev.ID)

	rd := want(t, e.do("GET", readiness, nil, token), http.StatusOK)
	assert.Equal(t, false, rd["ready"])
	assert.EqualValues(t, active, rd["seat_total"])
	assert.EqualValues(t, 0, rd["seats_with_category"])
	p := problemCodes(t, list(rd, "problems"))
	assert.Len(t, p["NO_CATEGORIES"], 1)
	assert.Len(t, p["SALES_WINDOW_MISSING"], 1)
	require.Len(t, p["SEATS_WITHOUT_CATEGORY"], 3, "one entry per unpainted block")
	perBlock := map[int64]int{}
	for _, pr := range p["SEATS_WITHOUT_CATEGORY"] {
		perBlock[id64(pr["block_id"])] = int(pr["seat_count"].(float64))
		assert.NotEmpty(t, pr["block_code"])
		assert.Contains(t, pr["message"], pr["block_code"])
	}
	assert.Equal(t, map[int64]int{v.BlockIDs[0]: 8, v.BlockIDs[1]: 8, v.BlockIDs[2]: 7}, perBlock)

	// Publishing now is refused with the same problems, and writes nothing.
	refused := wantError(t, e.publish(ev), http.StatusConflict, domain.CodeEventNotReady)
	assert.Equal(t, rd["problems"], details(t, refused)["problems"])
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1`, ev.ID))
	assert.Equal(t, "DRAFT", want(t, e.do("GET", fmt.Sprintf("/admin/events/%d", ev.ID), nil, token), http.StatusOK)["status"])

	// Fix it one thing at a time.
	free := e.addCategory(&ev, "Terrace", 0, "#DB2777")
	regular := e.addCategory(&ev, "Regular", 100_000, "#059669")
	want(t, e.paint(ev, regular, v.BlockIDs[:2], nil), http.StatusOK)
	// The third block is painted row by row, leaving its back row out.
	want(t, e.paint(ev, free, nil, v.RowIDs[v.BlockIDs[2]][:1]), http.StatusOK)
	now := e.clock.Now().UTC()
	want(t, e.do("PATCH", fmt.Sprintf("/admin/events/%d", ev.ID), obj{
		"sales_open_at": now.Add(-time.Hour).Format(time.RFC3339), "sales_close_at": now.Add(24 * time.Hour).Format(time.RFC3339),
	}, token), http.StatusOK)

	rd = want(t, e.do("GET", readiness, nil, token), http.StatusOK)
	p = problemCodes(t, list(rd, "problems"))
	assert.Len(t, p, 2, "%v", rd["problems"])
	require.Len(t, p["CATEGORY_WITHOUT_PRICE"], 1)
	assert.Equal(t, free, id64(p["CATEGORY_WITHOUT_PRICE"][0]["category_id"]))
	assert.Equal(t, "Terrace", p["CATEGORY_WITHOUT_PRICE"][0]["category_name"])
	require.Len(t, p["SEATS_WITHOUT_CATEGORY"], 1)
	assert.EqualValues(t, 4, p["SEATS_WITHOUT_CATEGORY"][0]["seat_count"])
	assert.EqualValues(t, active-4, rd["seats_with_category"])
	wantError(t, e.publish(ev), http.StatusConflict, domain.CodeEventNotReady)

	want(t, e.do("PATCH", fmt.Sprintf("/admin/categories/%d", free), obj{"price_cents": 50_000}, token), http.StatusOK)
	want(t, e.paint(ev, regular, v.BlockIDs[2:], nil), http.StatusOK)
	rd = want(t, e.do("GET", readiness, nil, token), http.StatusOK)
	assert.Equal(t, true, rd["ready"], "%v", rd["problems"])
	assert.Empty(t, list(rd, "problems"))

	// Publish: exactly one inventory row per active seat.
	res := want(t, e.publish(ev), http.StatusOK)
	assert.Equal(t, false, res["already_published"])
	assert.EqualValues(t, active, res["seats_on_sale"])
	published := res["event"].(obj)
	assert.Equal(t, "ON_SALE", published["status"], "the sales window is open now")
	assert.NotNil(t, published["published_at"])
	assert.EqualValues(t, active, published["seats_total"])
	assert.EqualValues(t, active, published["seats_available"])
	assert.EqualValues(t, 50_000, published["price_from_cents"])

	inventory := func() (rows, distinct, missing, wrongBlock int) {
		rows = queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1`, ev.ID)
		distinct = queryInt(t, `SELECT count(DISTINCT seat_id) FROM event_seats WHERE event_id = $1`, ev.ID)
		missing = queryInt(t, `
			SELECT count(*) FROM seats s JOIN blocks b ON b.id = s.block_id JOIN stands st ON st.id = b.stand_id
			WHERE st.venue_id = $1 AND s.is_active
			  AND NOT EXISTS (SELECT 1 FROM event_seats es WHERE es.event_id = $2 AND es.seat_id = s.id)`, v.ID, ev.ID)
		wrongBlock = queryInt(t, `
			SELECT count(*) FROM event_seats es JOIN seats s ON s.id = es.seat_id
			WHERE es.event_id = $1 AND (es.block_id <> s.block_id OR NOT s.is_active OR es.status <> 'AVAILABLE')`, ev.ID)
		return
	}
	rows, distinct, missing, wrong := inventory()
	assert.Equal(t, active, rows)
	assert.Equal(t, active, distinct)
	assert.Zero(t, missing)
	assert.Zero(t, wrong)
	// The row override wins over the block's category.
	assert.Equal(t, 3, queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1 AND category_id = $2`, ev.ID, free),
		"the front row of the third block, less its switched-off seat")

	// Publishing twice is safe: nothing changes, and the answer says so.
	var firstPublishedAt time.Time
	require.NoError(t, db.Pool.QueryRow(context.Background(), `SELECT published_at FROM events WHERE id = $1`, ev.ID).Scan(&firstPublishedAt))
	maxID := queryInt(t, `SELECT max(id) FROM event_seats WHERE event_id = $1`, ev.ID)
	again := want(t, e.publish(ev), http.StatusOK)
	assert.Equal(t, true, again["already_published"])
	assert.EqualValues(t, active, again["seats_on_sale"])
	rows, distinct, missing, wrong = inventory()
	assert.Equal(t, []int{active, active, 0, 0}, []int{rows, distinct, missing, wrong})
	assert.Equal(t, maxID, queryInt(t, `SELECT max(id) FROM event_seats WHERE event_id = $1`, ev.ID), "no row was rewritten")
	var secondPublishedAt time.Time
	require.NoError(t, db.Pool.QueryRow(context.Background(), `SELECT published_at FROM events WHERE id = $1`, ev.ID).Scan(&secondPublishedAt))
	assert.True(t, firstPublishedAt.Equal(secondPublishedAt))

	wantError(t, e.do("POST", "/admin/events/999999999/publish", nil, token), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("GET", "/admin/events/999999999/readiness", nil, token), http.StatusNotFound, domain.CodeNotFound)
	wantFields(t, e.do("POST", "/admin/events/abc/publish", nil, token), "eventId")
}

func TestReadiness_KickoffInPastAndNoSeats(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 1, 2)
	ev := e.readyEvent(v)
	e.clock.Advance(40 * 24 * time.Hour)
	ev.Token = e.staffToken(domain.RoleOrganiser) // the old token ran out with the clock
	rd := want(t, e.do("GET", fmt.Sprintf("/admin/events/%d/readiness", ev.ID), nil, ev.Token), http.StatusOK)
	assert.Len(t, problemCodes(t, list(rd, "problems"))["KICKOFF_IN_PAST"], 1)
	wantError(t, e.publish(ev), http.StatusConflict, domain.CodeEventNotReady)

	empty := e.newVenue(0, 0, 0)
	bare := e.createEvent(empty, ev.Token)
	rd = want(t, e.do("GET", fmt.Sprintf("/admin/events/%d/readiness", bare.ID), nil, ev.Token), http.StatusOK)
	assert.Len(t, problemCodes(t, list(rd, "problems"))["NO_SEATS"], 1)
}

// Rule 3: a draft becomes ON_SALE when published inside its sales window and PUBLISHED
// otherwise; after that every read derives the status from the window, with no background
// job flipping it.
func TestPublish_StatusFollowsSalesWindow(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 1, 2)
	now := e.clock.Now().UTC().Truncate(time.Second)
	ev := e.readyEvent(v, func(b obj) {
		b["sales_open_at"] = now.Add(time.Hour).Format(time.RFC3339)
		b["sales_close_at"] = now.Add(2 * time.Hour).Format(time.RFC3339)
		b["kickoff_at"] = now.Add(3 * time.Hour).Format(time.RFC3339)
		b["gates_open_at"] = now.Add(150 * time.Minute).Format(time.RFC3339)
	})
	res := want(t, e.publish(ev), http.StatusOK)
	assert.Equal(t, "PUBLISHED", res["event"].(obj)["status"], "sales have not opened yet")

	public := fmt.Sprintf("/events/%d", ev.ID)
	window := fmt.Sprintf("&from=%s&to=%s", now.Add(170*time.Minute).Format(time.RFC3339), now.Add(190*time.Minute).Format(time.RFC3339))
	listed := func(status string) bool {
		for _, it := range items(want(t, e.do("GET", "/events?limit=100&status="+status+window, nil, ""), http.StatusOK)) {
			if id64(it.(obj)["id"]) == ev.ID {
				assert.Equal(t, status, it.(obj)["status"])
				return true
			}
		}
		return false
	}
	assert.Equal(t, "PUBLISHED", want(t, e.do("GET", public, nil, ""), http.StatusOK)["status"])
	assert.True(t, listed("PUBLISHED"))
	assert.False(t, listed("ON_SALE"))

	e.clock.Advance(90 * time.Minute) // inside the window; the stored status is untouched
	assert.Equal(t, "ON_SALE", want(t, e.do("GET", public, nil, ""), http.StatusOK)["status"])
	assert.Equal(t, "ON_SALE", want(t, e.do("GET", "/admin"+public, nil, e.staffToken(domain.RoleOrganiser)), http.StatusOK)["status"])
	assert.True(t, listed("ON_SALE"))
	assert.False(t, listed("PUBLISHED"))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM events WHERE id = $1 AND status = 'PUBLISHED'`, ev.ID))

	e.clock.Advance(40 * time.Minute) // past sales_close_at
	assert.Equal(t, "PUBLISHED", want(t, e.do("GET", public, nil, ""), http.StatusOK)["status"])
	assert.True(t, listed("PUBLISHED"))
}

func TestPublish_TwoAtOnce(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(4, 5, 10)
	for round := 0; round < 3; round++ {
		ev := e.readyEvent(v)
		var wg sync.WaitGroup
		results := make([]*httptest.ResponseRecorder, 4)
		for i := range results {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = e.publish(ev)
			}()
		}
		wg.Wait()
		fresh := 0
		for _, rec := range results {
			o := want(t, rec, http.StatusOK)
			if o["already_published"] == false {
				fresh++
			}
			assert.EqualValues(t, v.seats(), o["seats_on_sale"])
		}
		assert.Equal(t, 1, fresh, "exactly one of the calls did the publishing")
		assert.Equal(t, v.seats(), queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1`, ev.ID))
		assert.Equal(t, v.seats(), queryInt(t, `SELECT count(DISTINCT seat_id) FROM event_seats WHERE event_id = $1`, ev.ID))
	}
}

// Rule 1: painting and publishing are serialised on the event row. Whichever way the race
// goes, the inventory's category is the painting's category.
func TestPaintRacingPublish_InventoryAlwaysMatchesThePainting(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(4, 5, 10)
	painted, refused := 0, 0
	for round := 0; round < 12; round++ {
		ev := e.readyEvent(v)
		vip := ev.Cats["VIP"]
		var wg sync.WaitGroup
		var paintRec, clearRec, publishRec *httptest.ResponseRecorder
		wg.Add(3)
		go func() { defer wg.Done(); paintRec = e.paint(ev, vip, v.BlockIDs[1:3], v.RowIDs[v.BlockIDs[3]][:2]) }()
		go func() { defer wg.Done(); publishRec = e.publish(ev) }()
		go func() { defer wg.Done(); clearRec = e.paint(ev, nil, nil, v.RowIDs[v.BlockIDs[0]][:1]) }()
		wg.Wait()

		for _, rec := range []*httptest.ResponseRecorder{paintRec, clearRec} {
			switch rec.Code {
			case http.StatusOK:
				painted++
			case http.StatusConflict:
				refused++
				ee := wantError(t, rec, http.StatusConflict, domain.CodeEventNotEditable)
				assert.NotEqual(t, "DRAFT", details(t, ee)["current_status"])
			default:
				t.Fatalf("paint answered %d: %s", rec.Code, rec.Body.String())
			}
		}
		// Clearing the VIP row can leave nothing wrong (the block is still Regular), so
		// publish always succeeds here.
		want(t, publishRec, http.StatusOK)

		mismatched := queryInt(t, `
			SELECT count(*)
			FROM event_seats es
			JOIN seats s ON s.id = es.seat_id
			LEFT JOIN event_row_categories erc ON erc.event_id = es.event_id AND erc.row_id = s.row_id
			LEFT JOIN event_block_categories ebc ON ebc.event_id = es.event_id AND ebc.block_id = s.block_id
			WHERE es.event_id = $1
			  AND es.category_id IS DISTINCT FROM COALESCE(erc.category_id, ebc.category_id)`, ev.ID)
		require.Zero(t, mismatched, "round %d: inventory disagrees with the painting (paint=%d, clear=%d)", round, paintRec.Code, clearRec.Code)
		require.Equal(t, v.seats(), queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1`, ev.ID))
	}
	t.Logf("paints applied before publish: %d, refused after it: %d", painted, refused)
}

func TestDraftHiddenFromPublic(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(2, 1, 2)
	draft := e.readyEvent(v)
	base := fmt.Sprintf("/events/%d", draft.ID)
	for _, path := range []string{
		base, base + "/categories", base + "/map",
		fmt.Sprintf("%s/stands/%d/blocks", base, v.StandIDs[0]),
		fmt.Sprintf("%s/blocks/%d/seats", base, v.BlockIDs[0]),
	} {
		wantError(t, e.do("GET", path, nil, ""), http.StatusNotFound, domain.CodeNotFound)
		// Not even to the organiser on the public routes: drafts live under /admin.
		wantError(t, e.do("GET", path, nil, draft.Token), http.StatusNotFound, domain.CodeNotFound)
	}
	want(t, e.do("GET", "/admin"+base, nil, draft.Token), http.StatusOK)

	inPublicList := func(id int64) bool {
		for _, it := range items(want(t, e.do("GET", "/events?limit=100&from="+e.clock.Now().Add(29*24*time.Hour).UTC().Format(time.RFC3339), nil, ""), http.StatusOK)) {
			if id64(it.(obj)["id"]) == id {
				return true
			}
		}
		return false
	}
	live := e.publishedEvent(v)
	assert.False(t, inPublicList(draft.ID))
	assert.True(t, inPublicList(live.ID))

	// A cancelled event leaves the list but its page still answers, for ticket holders.
	want(t, e.do("POST", fmt.Sprintf("/admin/events/%d/cancel", live.ID), nil, live.Token), http.StatusOK)
	assert.False(t, inPublicList(live.ID))
	assert.Equal(t, "CANCELLED", want(t, e.do("GET", fmt.Sprintf("/events/%d", live.ID), nil, ""), http.StatusOK)["status"])
	want(t, e.do("GET", fmt.Sprintf("/events/%d/categories", live.ID), nil, ""), http.StatusOK)

	wantError(t, e.do("GET", "/events/999999999", nil, ""), http.StatusNotFound, domain.CodeNotFound)
	wantFields(t, e.do("GET", "/events/0", nil, ""), "eventId")
	wantFields(t, e.do("GET", "/events/x/categories", nil, ""), "eventId")
}

func TestEventLists_PagingAndFilters(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 1, 2)
	// A window of its own, far in the future, so other tests' events are not in it.
	base := time.Date(2100+int(time.Now().UnixNano()%4000), 3, 1, 12, 0, 0, 0, time.UTC)
	at := func(day int) string { return base.AddDate(0, 0, day).Format(time.RFC3339) }
	var ids []int64
	var token string
	for day := 1; day <= 5; day++ {
		ev := e.publishedEvent(v, func(b obj) {
			b["kickoff_at"], b["sales_close_at"], b["gates_open_at"] = at(day), at(day), at(day-1)
		})
		ids = append(ids, ev.ID)
		token = ev.Token
	}
	draft := e.createEvent(v, token, func(b obj) { b["kickoff_at"], b["sales_close_at"], b["gates_open_at"] = at(3), at(3), at(2) })
	window := "from=" + at(0) + "&to=" + at(30)

	walk := func(path string) []int64 {
		var got []int64
		cursor := ""
		for pages := 0; pages < 10; pages++ {
			o := want(t, e.do("GET", path+cursor, nil, token), http.StatusOK)
			for _, it := range items(o) {
				got = append(got, id64(it.(obj)["id"]))
			}
			require.LessOrEqual(t, len(items(o)), 2)
			if o["next_cursor"] == nil {
				return got
			}
			cursor = "&cursor=" + o["next_cursor"].(string)
		}
		t.Fatal("paging did not end")
		return nil
	}

	// Public: soonest first, no draft, three pages of at most two.
	assert.Equal(t, ids, walk("/events?limit=2&"+window))
	// to is exclusive, from inclusive.
	assert.Equal(t, ids[1:3], walk("/events?limit=2&from="+at(2)+"&to="+at(4)))
	assert.Equal(t, ids, walk("/events?limit=2&status=ON_SALE&"+window))
	assert.Empty(t, walk("/events?limit=2&status=SOLD_OUT&"+window))

	// Admin: latest first, draft included.
	all := walk("/admin/events?limit=2&" + window)
	assert.Len(t, all, 6)
	assert.Equal(t, []int64{ids[4], ids[3]}, all[:2])
	assert.Contains(t, all, draft.ID)
	assert.Equal(t, []int64{draft.ID}, walk("/admin/events?limit=2&status=DRAFT&"+window))
	assert.Len(t, walk("/admin/events?limit=2&status=ON_SALE&"+window), 5)

	// Without `from` the public list starts now: an event that has kicked off is gone.
	past := e.publishedEvent(v)
	e.clock.Advance(31 * 24 * time.Hour)
	token = e.staffToken(domain.RoleOrganiser) // the old token ran out with the clock
	for _, it := range items(want(t, e.do("GET", "/events?limit=100", nil, ""), http.StatusOK)) {
		assert.NotEqual(t, past.ID, id64(it.(obj)["id"]))
	}

	for _, path := range []string{"/events", "/admin/events"} {
		wantFields(t, e.do("GET", path+"?cursor=garbage", nil, token), "cursor")
		wantFields(t, e.do("GET", path+"?limit=0", nil, token), "limit")
		wantFields(t, e.do("GET", path+"?limit=101", nil, token), "limit")
		wantFields(t, e.do("GET", path+"?from=yesterday", nil, token), "from")
		wantFields(t, e.do("GET", path+"?to=2026-13-01", nil, token), "to")
		wantFields(t, e.do("GET", path+"?status=NOPE", nil, token), "status")
	}
	// DRAFT is not a status the public list knows.
	wantFields(t, e.do("GET", "/events?status=DRAFT", nil, ""), "status")
}

func TestUpdateEvent(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 1, 2)
	ev := e.readyEvent(v)
	path := fmt.Sprintf("/admin/events/%d", ev.ID)
	patch := func(b any) *httptest.ResponseRecorder { return e.do("PATCH", path, b, ev.Token) }

	o := want(t, patch(obj{"title": "  Renamed  ", "competition": nil, "max_tickets_per_user": 4}), http.StatusOK)
	assert.Equal(t, "Renamed", o["title"])
	assert.Nil(t, o["competition"], "null clears a nullable field")
	assert.Equal(t, "Home", o["home_team"], "a field that was not sent is left alone")
	assert.EqualValues(t, 4, o["max_tickets_per_user"])

	wantFields(t, patch(obj{}), "body")
	wantFields(t, patch(obj{"title": ""}), "title")
	wantFields(t, patch(obj{"status": "ON_SALE"}), "status")
	wantFields(t, patch(obj{"max_tickets_per_user": 0}), "max_tickets_per_user")
	// The merged result is checked: closing sales after the existing kickoff is refused.
	wantFields(t, patch(obj{"sales_close_at": "2999-01-01T00:00:00Z"}), "sales_close_at")
	wantFields(t, patch(obj{"kickoff_at": "soon"}), "kickoff_at")
	wantError(t, e.do("PATCH", "/admin/events/999999999", obj{"title": "x"}, ev.Token), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("GET", "/admin/events/999999999", nil, ev.Token), http.StatusNotFound, domain.CodeNotFound)

	// Still editable after publish; not after cancel.
	want(t, e.publish(ev), http.StatusOK)
	want(t, patch(obj{"poster_url": "https://cdn.talanta.test/p.png"}), http.StatusOK)
	want(t, e.do("POST", path+"/cancel", nil, ev.Token), http.StatusOK)
	ee := wantError(t, patch(obj{"title": "Too late"}), http.StatusConflict, domain.CodeEventNotEditable)
	assert.Equal(t, "CANCELLED", details(t, ee)["current_status"])
}

func TestCategories(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(2, 1, 3)
	ev := e.readyEvent(v)
	token := ev.Token
	cats := fmt.Sprintf("/admin/events/%d/categories", ev.ID)
	catPath := func(id int64) string { return fmt.Sprintf("/admin/categories/%d", id) }

	ee := wantError(t, e.do("POST", cats, obj{"name": "VIP", "price_cents": 100, "color_hex": "#000000"}, token), http.StatusConflict, domain.CodeAlreadyExists)
	assert.Contains(t, string(ee.Error.Details), `"name"`)
	wantFields(t, e.do("POST", cats, obj{"name": "X", "price_cents": 100, "color_hex": "red"}, token), "color_hex")
	wantFields(t, e.do("POST", cats, obj{"name": "X", "color_hex": "#000000"}, token), "price_cents")
	wantFields(t, e.do("POST", cats, obj{"name": "X", "price_cents": -5, "color_hex": "#000000"}, token), "price_cents")
	// M-Pesa takes no cents, so a price with cents in it is refused when it is set.
	wantFields(t, e.do("POST", cats, obj{"name": "X", "price_cents": 150_050, "color_hex": "#000000"}, token), "price_cents")
	// Money is whole cents: a fraction is not a price.
	wantFields(t, e.do("POST", cats, obj{"name": "X", "price_cents": 10.5, "color_hex": "#000000"}, token), "price_cents")
	wantError(t, e.do("POST", "/admin/events/999999999/categories", obj{"name": "X", "price_cents": 100, "color_hex": "#000000"}, token), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("GET", "/admin/events/999999999/categories", nil, token), http.StatusNotFound, domain.CodeNotFound)

	// A draft's price changes freely.
	spare := e.addCategory(&ev, "Spare", 70_000, "#111111")
	o := want(t, e.do("PATCH", catPath(spare), obj{"price_cents": 80_000, "description": "Behind the goal"}, token), http.StatusOK)
	assert.EqualValues(t, 80_000, o["price_cents"])
	assert.Equal(t, "Behind the goal", o["description"])
	wantError(t, e.do("PATCH", catPath(spare), obj{"name": "VIP"}, token), http.StatusConflict, domain.CodeAlreadyExists)
	wantFields(t, e.do("PATCH", catPath(spare), obj{}, token), "body")
	wantFields(t, e.do("PATCH", catPath(spare), obj{"color_hex": "#12"}, token), "color_hex")
	wantError(t, e.do("PATCH", "/admin/categories/999999999", obj{"name": "x"}, token), http.StatusNotFound, domain.CodeNotFound)

	// Deleting a draft's category takes its painting with it.
	want(t, e.paint(ev, spare, v.BlockIDs[1:], nil), http.StatusOK)
	want(t, e.do("DELETE", catPath(spare), nil, token), http.StatusNoContent)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM event_block_categories WHERE event_id = $1 AND block_id = $2`, ev.ID, v.BlockIDs[1]))
	wantError(t, e.do("DELETE", catPath(spare), nil, token), http.StatusNotFound, domain.CodeNotFound)
	want(t, e.paint(ev, ev.Cats["Regular"], v.BlockIDs[1:], nil), http.StatusOK)

	want(t, e.publish(ev), http.StatusOK)

	// Public list: cheapest first, with seat counts.
	public := items(want(t, e.do("GET", fmt.Sprintf("/events/%d/categories", ev.ID), nil, ""), http.StatusOK))
	require.Len(t, public, 2)
	assert.Equal(t, "Regular", public[0].(obj)["name"])
	assert.EqualValues(t, 3, public[0].(obj)["seats_total"])
	assert.EqualValues(t, 3, public[1].(obj)["seats_total"], "the VIP row")
	assert.Equal(t, "KES", public[0].(obj)["currency"])
	admin := items(want(t, e.do("GET", cats, nil, token), http.StatusOK))
	assert.Len(t, admin, 2)

	// Rule 8: once the event has left DRAFT, a price change needs confirm, and never
	// touches what was already booked.
	vip := ev.Cats["VIP"]
	seats := eventSeatIDs(t, ev.ID, v.BlockIDs[0])
	fanID, _ := e.newFan()
	sellSeats(t, seats[0])
	holdSeats(t, ev.ID, fanID, time.Now().Add(10*time.Minute), seats[1])
	paidAt := time.Now()
	bookingID, _ := insertBooking(t, ev.ID, fanID, "PAID", paidAt, &paidAt, bookingItem{seats[0], vip, 500_000})

	ee = wantError(t, e.do("PATCH", catPath(vip), obj{"price_cents": 600_000}, token), http.StatusConflict, domain.CodeConfirmationRequired)
	assert.Equal(t, obj{"sold": 1.0, "held": 1.0}, details(t, ee))
	ee = wantError(t, e.do("PATCH", catPath(vip), obj{"price_cents": 600_000, "confirm": false}, token), http.StatusConflict, domain.CodeConfirmationRequired)
	assert.Equal(t, 500_000, queryInt(t, `SELECT price_cents FROM event_categories WHERE id = $1`, vip), "nothing changed")

	// Renaming or recolouring needs no confirmation, nor does sending the same price.
	want(t, e.do("PATCH", catPath(vip), obj{"name": "VIP Gold", "price_cents": 500_000}, token), http.StatusOK)

	o = want(t, e.do("PATCH", catPath(vip), obj{"price_cents": 600_000, "confirm": true}, token), http.StatusOK)
	assert.EqualValues(t, 600_000, o["price_cents"])
	assert.EqualValues(t, 3, o["seats_total"])
	assert.Equal(t, 500_000, queryInt(t, `SELECT unit_price_cents FROM booking_items WHERE booking_id = $1`, bookingID), "a past booking keeps its price")
	assert.Equal(t, 500_000, queryInt(t, `SELECT total_cents FROM bookings WHERE id = $1`, bookingID))

	// Inventory points at the category now.
	ee = wantError(t, e.do("DELETE", catPath(vip), nil, token), http.StatusConflict, domain.CodeCategoryInUse)
	assert.Equal(t, obj{"seats": 3.0, "sold": 1.0, "held": 1.0}, details(t, ee))
	wantFields(t, e.do("DELETE", "/admin/categories/zero", nil, token), "categoryId")
	// A category added after publish has no seats and can still go.
	late := e.addCategory(&ev, "Late", 1_000, "#222222")
	want(t, e.do("DELETE", catPath(late), nil, token), http.StatusNoContent)

	want(t, e.do("POST", fmt.Sprintf("/admin/events/%d/cancel", ev.ID), nil, token), http.StatusOK)
	wantError(t, e.do("POST", cats, obj{"name": "Never", "price_cents": 100, "color_hex": "#000000"}, token), http.StatusConflict, domain.CodeEventNotEditable)
	wantError(t, e.do("PATCH", catPath(vip), obj{"name": "Never"}, token), http.StatusConflict, domain.CodeEventNotEditable)
}

func TestAssignCategory(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(3, 2, 2)
	other := e.newVenue(1, 1, 2)
	token := e.staffToken(domain.RoleOrganiser)
	ev := e.createEvent(v, token)
	otherEv := e.createEvent(v, token)
	regular := e.addCategory(&ev, "Regular", 100_000, "#059669")
	vip := e.addCategory(&ev, "VIP", 500_000, "#2563EB")
	foreignCat := e.addCategory(&otherEv, "Regular", 100_000, "#059669")
	assignPath := fmt.Sprintf("/admin/events/%d/assignments", ev.ID)

	a := want(t, e.do("GET", assignPath, nil, token), http.StatusOK)
	assert.Empty(t, list(a, "blocks"))
	assert.Empty(t, list(a, "rows"))

	row := v.RowIDs[v.BlockIDs[0]][1]
	a = want(t, e.paint(ev, regular, v.BlockIDs, []int64{row}), http.StatusOK)
	assert.Len(t, list(a, "blocks"), 3)
	assert.Len(t, list(a, "rows"), 1)
	// Painting again replaces; it does not add.
	a = want(t, e.paint(ev, vip, v.BlockIDs[:1], []int64{row}), http.StatusOK)
	assert.Len(t, list(a, "blocks"), 3)
	assert.Equal(t, vip, id64(list(a, "rows")[0].(obj)["category_id"]))
	assert.Equal(t, a, want(t, e.do("GET", assignPath, nil, token), http.StatusOK))

	// null clears.
	a = want(t, e.paint(ev, nil, v.BlockIDs[2:], []int64{row}), http.StatusOK)
	assert.Len(t, list(a, "blocks"), 2)
	assert.Empty(t, list(a, "rows"))

	wantError(t, e.paint(ev, foreignCat, v.BlockIDs[:1], nil), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("POST", "/admin/events/999999999/assign-category", obj{"category_id": regular, "block_ids": v.BlockIDs, "row_ids": []int64{}}, token),
		http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("GET", "/admin/events/999999999/assignments", nil, token), http.StatusNotFound, domain.CodeNotFound)

	// A block and a row of another venue are named by their position in the request.
	ee := wantError(t, e.paint(ev, regular, []int64{v.BlockIDs[0], other.BlockIDs[0], 999_999_999}, other.RowIDs[other.BlockIDs[0]]),
		http.StatusUnprocessableEntity, domain.CodeVenueMismatch)
	var d struct {
		Fields []struct{ Field string } `json:"fields"`
	}
	require.NoError(t, json.Unmarshal(ee.Error.Details, &d))
	var names []string
	for _, f := range d.Fields {
		names = append(names, f.Field)
	}
	assert.ElementsMatch(t, []string{"block_ids[1]", "block_ids[2]", "row_ids[0]"}, names)
	assert.Len(t, list(want(t, e.do("GET", assignPath, nil, token), http.StatusOK), "blocks"), 2, "nothing was painted by the refused call")

	wantFields(t, e.paint(ev, regular, nil, nil), "block_ids", "row_ids")
	wantFields(t, e.paint(ev, regular, []int64{v.BlockIDs[0], v.BlockIDs[0]}, nil), "block_ids")
	wantFields(t, e.do("POST", fmt.Sprintf("/admin/events/%d/assign-category", ev.ID), obj{"block_ids": v.BlockIDs, "row_ids": []int64{}}, token), "category_id")
	wantFields(t, e.do("POST", fmt.Sprintf("/admin/events/%d/assign-category", ev.ID), obj{"category_id": regular, "block_ids": v.BlockIDs}, token), "row_ids")

	// Only a draft can be painted.
	want(t, e.paint(ev, regular, v.BlockIDs, nil), http.StatusOK)
	want(t, e.publish(ev), http.StatusOK)
	ee = wantError(t, e.paint(ev, vip, v.BlockIDs, nil), http.StatusConflict, domain.CodeEventNotEditable)
	assert.Equal(t, "ON_SALE", details(t, ee)["current_status"])
	assert.Equal(t, v.seats(), queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1 AND category_id = $2`, ev.ID, regular))
}

func TestCancelEvent(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 2, 4)
	ev := e.publishedEvent(v)
	token := ev.Token
	cancel := fmt.Sprintf("/admin/events/%d/cancel", ev.ID)
	seats := eventSeatIDs(t, ev.ID, v.BlockIDs[0])
	regular := ev.Cats["Regular"]
	fanID, _ := e.newFan()
	now := time.Now()

	// One live hold of two seats, one unpaid booking on it, and one paid booking with a
	// successful payment, a failed earlier attempt and a ticket.
	holdID := holdSeats(t, ev.ID, fanID, now.Add(10*time.Minute), seats[4], seats[5])
	pendingID, _ := insertBooking(t, ev.ID, fanID, "PENDING_PAYMENT", now, nil, bookingItem{seats[4], regular, 100_000}, bookingItem{seats[5], regular, 100_000})
	exec(t, `UPDATE bookings SET hold_id = $1 WHERE id = $2`, holdID, pendingID)
	sellSeats(t, seats[6], seats[7])
	paidID, _ := insertBooking(t, ev.ID, fanID, "PAID", now, &now, bookingItem{seats[6], regular, 100_000}, bookingItem{seats[7], regular, 100_000})
	insertPayment(t, paidID, "FAILED", 200_000, now.Add(-time.Minute))
	insertPayment(t, paidID, "SUCCESS", 200_000, now)
	for _, seat := range seats[6:8] {
		exec(t, `INSERT INTO tickets (booking_id, event_seat_id, token_nonce, event_id, gate_group, key_id, qr_token) VALUES ($1, $2, '\x01', $3, 'N-LOWER', 'k1', 'TLT1.dGVzdA.dGVzdA')`, paidID, seat, ev.ID)
	}

	o := want(t, e.do("POST", cancel, nil, token), http.StatusOK)
	assert.Equal(t, "CANCELLED", o["event"].(obj)["status"])
	assert.EqualValues(t, 1, o["holds_released"])
	assert.EqualValues(t, 2, o["tickets_voided"])
	assert.EqualValues(t, 1, o["bookings_cancelled"])
	assert.EqualValues(t, 1, o["refunds_queued"], "one per successful payment, none for the failed attempt")
	assert.EqualValues(t, 200_000, o["refund_total_cents"])

	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM holds WHERE id = $1 AND status = 'RELEASED'`, holdID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1 AND (status = 'HELD' OR hold_id IS NOT NULL)`, ev.ID))
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM tickets WHERE event_id = $1 AND status = 'VOID'`, ev.ID))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM bookings WHERE id = $1 AND status = 'CANCELLED'`, pendingID))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM bookings WHERE id = $1 AND status = 'REFUND_REQUIRED'`, paidID))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM refunds WHERE booking_id = $1 AND status = 'PENDING' AND amount_cents = 200000`, paidID))

	// Safe to repeat: nothing more happens.
	again := want(t, e.do("POST", cancel, nil, token), http.StatusOK)
	for _, k := range []string{"holds_released", "tickets_voided", "bookings_cancelled", "refunds_queued", "refund_total_cents"} {
		assert.EqualValues(t, 0, again[k], k)
	}
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM refunds WHERE booking_id = $1`, paidID))

	// Illegal moves are refused and say where the event is.
	ee := wantError(t, e.publish(ev), http.StatusConflict, domain.CodeInvalidStateTransition)
	assert.Equal(t, "CANCELLED", details(t, ee)["current_status"])

	done := e.publishedEvent(v)
	exec(t, `UPDATE events SET status = 'COMPLETED' WHERE id = $1`, done.ID)
	ee = wantError(t, e.do("POST", fmt.Sprintf("/admin/events/%d/cancel", done.ID), nil, token), http.StatusConflict, domain.CodeInvalidStateTransition)
	assert.Equal(t, "COMPLETED", details(t, ee)["current_status"])
	ee = wantError(t, e.publish(done), http.StatusConflict, domain.CodeInvalidStateTransition)
	assert.Equal(t, "COMPLETED", details(t, ee)["current_status"])

	// A draft can be cancelled too, and then never published.
	draft := e.readyEvent(v)
	want(t, e.do("POST", fmt.Sprintf("/admin/events/%d/cancel", draft.ID), nil, draft.Token), http.StatusOK)
	wantError(t, e.publish(draft), http.StatusConflict, domain.CodeInvalidStateTransition)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1`, draft.ID))

	wantError(t, e.do("POST", "/admin/events/999999999/cancel", nil, token), http.StatusNotFound, domain.CodeNotFound)
}
