//go:build integration

package httpapi_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

// Fix 2. Publish refuses an event with no sales window, a kickoff in the past or a free
// category. An edit made after publish must not be a way to get there anyway.
func TestEditsAfterPublish_KeepWhatReadinessEnforced(t *testing.T) {
	e := newEnv(t)
	v := e.newVenue(1, 1, 3)
	ev := e.publishedEvent(v)
	token := ev.Token
	path := fmt.Sprintf("/admin/events/%d", ev.ID)
	patch := func(b any) *httptest.ResponseRecorder { return e.do("PATCH", path, b, token) }
	rfc := func(d time.Duration) string {
		return e.clock.Now().UTC().Add(d).Truncate(time.Second).Format(time.RFC3339)
	}
	stored := func() string {
		var s string
		assert.NoError(t, db.Pool.QueryRow(t.Context(), `SELECT (kickoff_at, sales_open_at, sales_close_at)::text FROM events WHERE id = $1`, ev.ID).Scan(&s))
		return s
	}
	before := stored()

	// Sales that never close, or never opened.
	rules := wantFields(t, patch(obj{"sales_close_at": nil}), "sales_close_at")
	assert.Equal(t, "required", rules["sales_close_at"])
	rules = wantFields(t, patch(obj{"sales_open_at": nil}), "sales_open_at")
	assert.Equal(t, "required", rules["sales_open_at"])
	wantFields(t, patch(obj{"sales_open_at": nil, "sales_close_at": nil}), "sales_open_at", "sales_close_at")

	// A kickoff moved into the past. Sales are moved with it so that the kickoff itself is
	// the only thing wrong.
	rules = wantFields(t, patch(obj{
		"kickoff_at": rfc(-time.Hour), "gates_open_at": rfc(-4 * time.Hour),
		"sales_open_at": rfc(-48 * time.Hour), "sales_close_at": rfc(-2 * time.Hour),
	}), "kickoff_at")
	assert.Equal(t, "future", rules["kickoff_at"])
	// On its own it also leaves sales closing after kickoff: both are reported.
	wantFields(t, patch(obj{"kickoff_at": rfc(-time.Hour)}), "kickoff_at", "sales_close_at", "gates_open_at")
	assert.Equal(t, before, stored(), "nothing was written")

	// Legitimate edits of a published event still work: a later kickoff, a new window.
	o := want(t, patch(obj{"kickoff_at": rfc(40 * 24 * time.Hour), "sales_close_at": rfc(39 * 24 * time.Hour)}), http.StatusOK)
	assert.Equal(t, "ON_SALE", o["status"])

	// A draft is still free to have no window: that is what readiness is for.
	draft := e.readyEvent(v)
	want(t, e.do("PATCH", fmt.Sprintf("/admin/events/%d", draft.ID), obj{"sales_close_at": nil, "sales_open_at": nil}, draft.Token), http.StatusOK)

	// An event whose kickoff has simply come and gone stays editable, as long as the edit
	// does not move the kickoff to another moment in the past.
	exec(t, `UPDATE events SET kickoff_at = now() - interval '2 hours', gates_open_at = now() - interval '5 hours',
	                           sales_open_at = now() - interval '30 days', sales_close_at = now() - interval '3 hours' WHERE id = $1`, ev.ID)
	want(t, patch(obj{"title": "Played yesterday"}), http.StatusOK)
	wantFields(t, patch(obj{"kickoff_at": rfc(-90 * time.Minute)}), "kickoff_at")

	// --- Categories -----------------------------------------------------------------------
	live := e.publishedEvent(v)
	cat := fmt.Sprintf("/admin/categories/%d", live.Cats["Regular"])
	for _, b := range []obj{{"price_cents": 0, "confirm": true}, {"price_cents": 0}} {
		rules = wantFields(t, e.do("PATCH", cat, b, live.Token), "price_cents")
		assert.Equal(t, "gt", rules["price_cents"])
	}
	assert.Equal(t, 100_000, queryInt(t, `SELECT price_cents FROM event_categories WHERE id = $1`, live.Cats["Regular"]))
	// A real price change still goes through with confirm.
	want(t, e.do("PATCH", cat, obj{"price_cents": 150_000, "confirm": true}, live.Token), http.StatusOK)
	// A free category cannot be added to a published event either.
	wantFields(t, e.do("POST", fmt.Sprintf("/admin/events/%d/categories", live.ID), obj{"name": "Free", "price_cents": 0, "color_hex": "#000000"}, live.Token), "price_cents")

	// A draft may still hold a zero price; it blocks publishing instead.
	d2 := e.readyEvent(v)
	want(t, e.do("PATCH", fmt.Sprintf("/admin/categories/%d", d2.Cats["Regular"]), obj{"price_cents": 0}, d2.Token), http.StatusOK)
	wantError(t, e.publish(d2), http.StatusConflict, domain.CodeEventNotReady)
}
