//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/events"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/seatmap"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/venue"
)

// m4m5Operations is every operation of the events, inventory and seat map milestones. The
// harness requires a contract-checked success answer and error answer for each.
var m4m5Operations = []string{
	"listEvents", "getEvent", "listEventCategories",
	"getEventMap", "listStandBlocks", "getBlockSeats",
	"listVenues", "getVenue", "createBlock", "updateBlock", "deleteBlock",
	"generateBlockSeats", "getBlockLayout", "listVenueTemplates", "createVenueTemplate",
	"listAdminEvents", "createEvent", "getAdminEvent", "updateEvent", "getEventReadiness",
	"publishEvent", "cancelEvent", "listAdminEventCategories", "createCategory",
	"updateCategory", "deleteCategory", "getEventAssignments", "assignCategory",
	"getEventSales", "listEventBookings",
	"deleteEvent", "deleteVenueTemplate",
}

func withEventPolicy(fn func(*events.Policy)) envOption {
	return func(c *envConfig) { fn(&c.eventPolicy) }
}

type obj = map[string]any

func body(t testing.TB, rec *httptest.ResponseRecorder) obj {
	t.Helper()
	var v obj
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v), "body: %s", rec.Body.String())
	return v
}

func want(t testing.TB, rec *httptest.ResponseRecorder, status int) obj {
	t.Helper()
	require.Equal(t, status, rec.Code, "body: %s", rec.Body.String())
	if rec.Body.Len() == 0 {
		return nil
	}
	return body(t, rec)
}

func id64(v any) int64  { return int64(v.(float64)) }
func items(o obj) []any { return o["items"].([]any) }
func list(o obj, k string) []any {
	l, _ := o[k].([]any)
	return l
}

// staffToken mints an access token for a new staff user, skipping the argon2 login that the
// auth tests already cover.
func (e *env) staffToken(role domain.Role) string {
	e.t.Helper()
	u := e.newStaff(role)
	token, err := e.auth.Tokens().IssueAccess(u.ID, role, "test-session")
	require.NoError(e.t, err)
	return token
}

// newFan inserts a fan and mints their access token.
func (e *env) newFan() (int64, string) {
	e.t.Helper()
	phone := randPhone(e.t)
	u, err := e.q.CreateUser(context.Background(), store.CreateUserParams{PhoneE164: &phone, Role: string(domain.RoleFan)})
	require.NoError(e.t, err)
	token, err := e.auth.Tokens().IssueAccess(u.ID, domain.RoleFan, "test-session")
	require.NoError(e.t, err)
	return u.ID, token
}

// tVenue is a small synthetic venue: two stands, blocks alternating between them, every
// block the same grid. The full 60,000-seat layout takes half a minute to publish, so only
// the payload-size and plan tests use it.
type tVenue struct {
	ID          int64
	Name        string
	StandIDs    []int64
	BlockIDs    []int64
	RowIDs      map[int64][]int64 // block id → row ids, front row first
	Rows        int
	SeatsPerRow int
}

func (v tVenue) seatsPerBlock() int { return v.Rows * v.SeatsPerRow }
func (v tVenue) seats() int         { return len(v.BlockIDs) * v.seatsPerBlock() }

func (e *env) newVenue(blocks, rows, seatsPerRow int) tVenue {
	e.t.Helper()
	ctx := context.Background()
	v := tVenue{Name: "Test Ground " + randDigits(e.t, 12), RowIDs: map[int64][]int64{}, Rows: rows, SeatsPerRow: seatsPerRow}
	require.NoError(e.t, db.Pool.QueryRow(ctx,
		`INSERT INTO venues (name, city, capacity, plan_width, plan_height) VALUES ($1, 'Nairobi', 0, 1000, 1000) RETURNING id`,
		v.Name).Scan(&v.ID))
	for i, code := range []string{"N", "S"} {
		st, err := e.q.CreateStand(ctx, store.CreateStandParams{
			VenueID: v.ID, Code: code, Name: code + " Stand", Tier: "LOWER",
			Polygon: json.RawMessage(fmt.Sprintf(`[[0, %d], [1000, %d], [1000, %d], [0, %d]]`, i*500, i*500, i*500+400, i*500+400)), DisplayOrder: int32(i + 1),
		})
		require.NoError(e.t, err)
		v.StandIDs = append(v.StandIDs, st.ID)
	}
	for i := 0; i < blocks; i++ {
		b, err := e.venues.CreateBlock(ctx, venue.CreateBlockInput{
			VenueID: v.ID, StandID: v.StandIDs[i%2], Code: fmt.Sprintf("B%d", i+1), Label: fmt.Sprintf("Block %d", i+1),
			OriginX: float64(50 + (i/2)*200), OriginY: float64(100 + (i%2)*500), SeatPitchX: 5, SeatPitchY: 8,
		})
		require.NoError(e.t, err)
		layout, err := e.venues.GenerateSeats(ctx, b.ID, venue.GenerateInput{
			Rows: rows, SeatsPerRow: seatsPerRow, Labels: seatmap.LabelsAlpha, Numbering: seatmap.LeftToRight,
		})
		require.NoError(e.t, err)
		v.BlockIDs = append(v.BlockIDs, b.ID)
		for _, r := range layout.Rows {
			v.RowIDs[b.ID] = append(v.RowIDs[b.ID], *r.ID)
		}
	}
	return v
}

// tEvent is an event made through the API.
type tEvent struct {
	ID    int64
	Venue tVenue
	// Cats maps category name to id.
	Cats  map[string]int64
	Token string
}

// eventBody is a valid CreateEvent body: kickoff in 30 days, sales open an hour ago.
func (e *env) eventBody(v tVenue) obj {
	now := e.clock.Now().UTC().Truncate(time.Second)
	kickoff := now.Add(30 * 24 * time.Hour)
	return obj{
		"venue_id": v.ID, "title": "Test Match " + randDigits(e.t, 8),
		"competition": "Test Cup", "home_team": "Home", "away_team": "Away",
		"kickoff_at":     kickoff.Format(time.RFC3339),
		"gates_open_at":  kickoff.Add(-3 * time.Hour).Format(time.RFC3339),
		"sales_open_at":  now.Add(-time.Hour).Format(time.RFC3339),
		"sales_close_at": kickoff.Format(time.RFC3339),
	}
}

func (e *env) createEvent(v tVenue, token string, mods ...func(obj)) tEvent {
	e.t.Helper()
	b := e.eventBody(v)
	for _, m := range mods {
		m(b)
	}
	o := want(e.t, e.do("POST", "/admin/events", b, token), http.StatusCreated)
	require.Equal(e.t, "DRAFT", o["status"])
	return tEvent{ID: id64(o["id"]), Venue: v, Cats: map[string]int64{}, Token: token}
}

func (e *env) addCategory(ev *tEvent, name string, priceCents int64, color string) int64 {
	e.t.Helper()
	o := want(e.t, e.do("POST", fmt.Sprintf("/admin/events/%d/categories", ev.ID),
		obj{"name": name, "price_cents": priceCents, "color_hex": color}, ev.Token), http.StatusCreated)
	ev.Cats[name] = id64(o["id"])
	return ev.Cats[name]
}

func (e *env) paint(ev tEvent, categoryID any, blockIDs, rowIDs []int64) *httptest.ResponseRecorder {
	e.t.Helper()
	if blockIDs == nil {
		blockIDs = []int64{}
	}
	if rowIDs == nil {
		rowIDs = []int64{}
	}
	return e.do("POST", fmt.Sprintf("/admin/events/%d/assign-category", ev.ID),
		obj{"category_id": categoryID, "block_ids": blockIDs, "row_ids": rowIDs}, ev.Token)
}

// readyEvent is a draft that passes readiness: Regular on every block, VIP on the front row
// of the first block.
func (e *env) readyEvent(v tVenue, mods ...func(obj)) tEvent {
	e.t.Helper()
	ev := e.createEvent(v, e.staffToken(domain.RoleOrganiser), mods...)
	regular := e.addCategory(&ev, "Regular", 100_000, "#059669")
	vip := e.addCategory(&ev, "VIP", 500_000, "#2563EB")
	want(e.t, e.paint(ev, regular, v.BlockIDs, nil), http.StatusOK)
	want(e.t, e.paint(ev, vip, nil, v.RowIDs[v.BlockIDs[0]][:1]), http.StatusOK)
	return ev
}

func (e *env) publish(ev tEvent) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("POST", fmt.Sprintf("/admin/events/%d/publish", ev.ID), nil, ev.Token)
}

func (e *env) publishedEvent(v tVenue, mods ...func(obj)) tEvent {
	e.t.Helper()
	ev := e.readyEvent(v, mods...)
	o := want(e.t, e.publish(ev), http.StatusOK)
	require.Equal(e.t, false, o["already_published"])
	return ev
}

// untriggered runs fn in a transaction where triggers do not fire, so a test can write an
// updated_at in the past: the set_updated_at trigger would otherwise overwrite it.
func untriggered(t testing.TB, fn func(tx pgx.Tx)) {
	t.Helper()
	ctx := context.Background()
	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SET LOCAL session_replication_role = replica`)
	require.NoError(t, err)
	fn(tx)
	require.NoError(t, tx.Commit(ctx))
}

// age moves an event's and its seats' change stamps into the past, as if it had been
// published that long ago. Without it every poll in the first seconds of a test would
// rightly be told that everything just changed.
func age(t testing.TB, eventID int64, by time.Duration) {
	t.Helper()
	untriggered(t, func(tx pgx.Tx) {
		ctx := context.Background()
		secs := by.Seconds()
		_, err := tx.Exec(ctx, `UPDATE event_seats SET updated_at = updated_at - make_interval(secs => $2) WHERE event_id = $1`, eventID, secs)
		require.NoError(t, err)
		_, err = tx.Exec(ctx, `UPDATE events SET updated_at = updated_at - make_interval(secs => $2) WHERE id = $1`, eventID, secs)
		require.NoError(t, err)
	})
}

// eventSeatIDs lists the inventory ids of one block, in row then seat order.
func eventSeatIDs(t testing.TB, eventID, blockID int64) []int64 {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(), `
		SELECT es.id FROM event_seats es JOIN seats s ON s.id = es.seat_id JOIN rows r ON r.id = s.row_id
		WHERE es.event_id = $1 AND es.block_id = $2 ORDER BY r.row_index, s.seat_number`, eventID, blockID)
	require.NoError(t, err)
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	require.NoError(t, err)
	require.NotEmpty(t, ids)
	return ids
}

// holdSeats makes a hold for a user directly in the database (the hold endpoints are a
// later milestone) and marks the seats HELD by it.
func holdSeats(t testing.TB, eventID, userID int64, expiresAt time.Time, seatIDs ...int64) int64 {
	t.Helper()
	ctx := context.Background()
	var holdID int64
	require.NoError(t, db.Pool.QueryRow(ctx,
		`INSERT INTO holds (event_id, user_id, expires_at) VALUES ($1, $2, $3) RETURNING id`, eventID, userID, expiresAt).Scan(&holdID))
	tag, err := db.Pool.Exec(ctx,
		`UPDATE event_seats SET status = 'HELD', hold_id = $1, hold_expires_at = $2 WHERE id = ANY($3) AND event_id = $4`,
		holdID, expiresAt, seatIDs, eventID)
	require.NoError(t, err)
	require.EqualValues(t, len(seatIDs), tag.RowsAffected())
	return holdID
}

func sellSeats(t testing.TB, seatIDs ...int64) {
	t.Helper()
	exec(t, `UPDATE event_seats SET status = 'SOLD', hold_id = NULL, hold_expires_at = NULL WHERE id = ANY($1)`, seatIDs)
}

type bookingItem struct {
	SeatID, CategoryID, PriceCents int64
}

// insertBooking writes a booking and its items directly.
func insertBooking(t testing.TB, eventID, userID int64, status string, createdAt time.Time, paidAt *time.Time, its ...bookingItem) (int64, string) {
	t.Helper()
	ctx := context.Background()
	var subtotal int64
	for _, it := range its {
		subtotal += it.PriceCents
	}
	ref := "TLT-" + strings.ToUpper(randDigits(t, 6))
	var id int64
	require.NoError(t, db.Pool.QueryRow(ctx, `
		INSERT INTO bookings (reference, event_id, user_id, status, subtotal_cents, fee_cents, total_cents, idempotency_key, created_at, paid_at)
		VALUES ($1, $2, $3, $4, $5, 0, $5, $6, $7, $8) RETURNING id`,
		ref, eventID, userID, status, subtotal, "key-"+randDigits(t, 16), createdAt, paidAt).Scan(&id))
	for _, it := range its {
		exec(t, `INSERT INTO booking_items (booking_id, event_seat_id, category_id, unit_price_cents) VALUES ($1, $2, $3, $4)`,
			id, it.SeatID, it.CategoryID, it.PriceCents)
	}
	return id, ref
}

func insertPayment(t testing.TB, bookingID int64, status string, amount int64, createdAt time.Time) int64 {
	t.Helper()
	var id int64
	require.NoError(t, db.Pool.QueryRow(context.Background(), `
		INSERT INTO payments (booking_id, amount_cents, phone_e164, status, checkout_request_id, mpesa_receipt, created_at)
		VALUES ($1, $2, '+254700000009', $3, $4, CASE WHEN $3 = 'SUCCESS' THEN $5 END, $6) RETURNING id`,
		bookingID, amount, status, "ws_CO_"+randDigits(t, 16), "R"+randDigits(t, 9), createdAt).Scan(&id))
	return id
}

// tuple reads a seat tuple from a decoded level-3 answer.
func tuple(v any) []any { return v.([]any) }

// seatCodes maps event seat id to status letter for a full level-3 answer.
func seatCodes(t testing.TB, full obj) map[int64]string {
	t.Helper()
	require.Equal(t, "full", full["kind"])
	out := map[int64]string{}
	for _, r := range list(full, "rows") {
		for _, s := range list(r.(obj), "seats") {
			tp := tuple(s)
			require.Len(t, tp, 7)
			out[id64(tp[0])] = tp[6].(string)
		}
	}
	return out
}

// changeCodes maps event seat id to status letter for a delta answer.
func changeCodes(t testing.TB, delta obj) map[int64]string {
	t.Helper()
	require.Equal(t, "delta", delta["kind"], "expected a delta answer")
	out := map[int64]string{}
	for _, c := range list(delta, "changes") {
		tp := tuple(c)
		require.Len(t, tp, 2)
		out[id64(tp[0])] = tp[1].(string)
	}
	return out
}

func (e *env) seats(eventID, blockID int64, since, token string) *httptest.ResponseRecorder {
	e.t.Helper()
	path := fmt.Sprintf("/events/%d/blocks/%d/seats", eventID, blockID)
	if since != "" {
		path += "?since=" + since
	}
	return e.do("GET", path, nil, token)
}
