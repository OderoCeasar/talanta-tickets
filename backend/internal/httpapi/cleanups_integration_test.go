//go:build integration

package httpapi_test

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

// Fix 6. The public reads look at an Authorization header when one is sent (a signed-in fan
// sees their own holds), so a malformed or dead one answers 401. That answer is in the
// contract now; e.do checks every one of these against it.
func TestPublicReads_BadAuthorizationHeaderIs401(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 1, 2)
	ev := e.publishedEvent(v)
	base := fmt.Sprintf("/events/%d", ev.ID)
	paths := []string{
		"/events", base, base + "/categories", base + "/map",
		fmt.Sprintf("%s/stands/%d/blocks", base, v.StandIDs[0]),
		fmt.Sprintf("%s/blocks/%d/seats", base, v.BlockIDs[0]),
	}
	for _, path := range paths {
		// Without a header the call is anonymous and fine.
		want(t, e.do("GET", path, nil, ""), http.StatusOK)
		// "Bearer" followed by something that is not a token.
		wantError(t, e.do("GET", path, nil, "not-a-token"), http.StatusUnauthorized, domain.CodeUnauthenticated)
		// A header that is not a bearer token at all.
		req := e.request("GET", path, nil, "")
		req.Header.Set("Authorization", "Basic dGFsYW50YTp0aWNrZXRz")
		rec := e.contract.Do(t, e.router, req)
		wantError(t, rec, http.StatusUnauthorized, domain.CodeUnauthenticated)
		assert.NotEmpty(t, rec.Header().Get("WWW-Authenticate"), path)
	}
}

// Fix 10(c). A draft can be deleted, with its categories and painting; nothing else can.
func TestDeleteEvent_DraftOnly(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(2, 1, 3)
	draft := e.readyEvent(v)
	token := draft.Token
	path := func(id int64) string { return fmt.Sprintf("/admin/events/%d", id) }
	require.Equal(t, 2, queryInt(t, `SELECT count(*) FROM event_categories WHERE event_id = $1`, draft.ID))
	require.Equal(t, 2, queryInt(t, `SELECT count(*) FROM event_block_categories WHERE event_id = $1`, draft.ID))
	// A template saved from the draft is a copy and outlives it.
	templates := fmt.Sprintf("/admin/venues/%d/templates", v.ID)
	tpl := want(t, e.do("POST", templates, obj{"name": "Kept " + randDigits(t, 6), "event_id": draft.ID}, token), http.StatusCreated)

	_, fan := e.newFan()
	wantError(t, e.do("DELETE", path(draft.ID), nil, fan), http.StatusForbidden, domain.CodeForbidden)
	wantError(t, e.do("DELETE", path(draft.ID), nil, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
	wantFields(t, e.do("DELETE", "/admin/events/zero", nil, token), "eventId")

	want(t, e.do("DELETE", path(draft.ID), nil, token), http.StatusNoContent)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM events WHERE id = $1`, draft.ID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM event_categories WHERE event_id = $1`, draft.ID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM event_block_categories WHERE event_id = $1`, draft.ID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM event_row_categories WHERE event_id = $1`, draft.ID))
	wantError(t, e.do("GET", path(draft.ID), nil, token), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("DELETE", path(draft.ID), nil, token), http.StatusNotFound, domain.CodeNotFound)
	assert.Len(t, items(want(t, e.do("GET", templates, nil, token), http.StatusOK)), 1, "the template is still there")

	// Published, cancelled (even a draft that was cancelled) and completed events stay.
	live := e.publishedEvent(v)
	ee := wantError(t, e.do("DELETE", path(live.ID), nil, token), http.StatusConflict, domain.CodeEventNotEditable)
	assert.Equal(t, "ON_SALE", details(t, ee)["current_status"], "the stored status: an event that was published has inventory whatever its window says")
	assert.Equal(t, v.seats(), queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1`, live.ID), "nothing was removed")

	cancelled := e.readyEvent(v)
	want(t, e.do("POST", path(cancelled.ID)+"/cancel", nil, token), http.StatusOK)
	ee = wantError(t, e.do("DELETE", path(cancelled.ID), nil, token), http.StatusConflict, domain.CodeEventNotEditable)
	assert.Equal(t, "CANCELLED", details(t, ee)["current_status"])

	exec(t, `UPDATE events SET status = 'COMPLETED' WHERE id = $1`, live.ID)
	ee = wantError(t, e.do("DELETE", path(live.ID), nil, token), http.StatusConflict, domain.CodeEventNotEditable)
	assert.Equal(t, "COMPLETED", details(t, ee)["current_status"])
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM events WHERE id = ANY($1)`, []int64{live.ID, cancelled.ID}))

	// --- Templates --------------------------------------------------------------------------
	other := e.newVenue(1, 1, 2)
	tplPath := func(venueID int64, id any) string { return fmt.Sprintf("/admin/venues/%d/templates/%v", venueID, id) }
	// An event made from the template keeps its categories when the template goes.
	b := e.eventBody(v)
	b["template_id"] = tpl["id"]
	made := id64(want(t, e.do("POST", "/admin/events", b, token), http.StatusCreated)["id"])

	wantError(t, e.do("DELETE", tplPath(other.ID, tpl["id"]), nil, token), http.StatusNotFound, domain.CodeNotFound)
	assert.Len(t, items(want(t, e.do("GET", templates, nil, token), http.StatusOK)), 1, "a template is only deleted through its own venue")
	wantError(t, e.do("DELETE", tplPath(v.ID, tpl["id"]), nil, fan), http.StatusForbidden, domain.CodeForbidden)
	wantFields(t, e.do("DELETE", tplPath(v.ID, "x"), nil, token), "templateId")

	want(t, e.do("DELETE", tplPath(v.ID, tpl["id"]), nil, token), http.StatusNoContent)
	assert.Empty(t, items(want(t, e.do("GET", templates, nil, token), http.StatusOK)))
	wantError(t, e.do("DELETE", tplPath(v.ID, tpl["id"]), nil, token), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("DELETE", tplPath(999_999_999, 1), nil, token), http.StatusNotFound, domain.CodeNotFound)
	assert.Len(t, items(want(t, e.do("GET", fmt.Sprintf("/admin/events/%d/categories", made), nil, token), http.StatusOK)), 2)
}

// Fix 10(b). CATEGORY_IN_USE says how many seats were published in the category as well as
// how many are sold or held, so "the event is published" can be told from "tickets are out".
func TestDeleteCategory_InUseDetailsSayWhy(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(2, 1, 3)
	ev := e.publishedEvent(v)
	regular := fmt.Sprintf("/admin/categories/%d", ev.Cats["Regular"])

	// Published, nothing sold: only the inventory stands in the way.
	ee := wantError(t, e.do("DELETE", regular, nil, ev.Token), http.StatusConflict, domain.CodeCategoryInUse)
	assert.Equal(t, obj{"seats": 3.0, "sold": 0.0, "held": 0.0}, details(t, ee))

	seats := eventSeatIDs(t, ev.ID, v.BlockIDs[1])
	sellSeats(t, seats[0])
	ee = wantError(t, e.do("DELETE", regular, nil, ev.Token), http.StatusConflict, domain.CodeCategoryInUse)
	assert.Equal(t, obj{"seats": 3.0, "sold": 1.0, "held": 0.0}, details(t, ee))
}
