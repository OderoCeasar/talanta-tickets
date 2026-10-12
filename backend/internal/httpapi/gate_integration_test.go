//go:build integration

package httpapi_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/gate"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
)

// m10GateOperations is every operation of gate mode. The harness requires a
// contract-checked success answer and error answer for each.
var m10GateOperations = []string{"listGateEvents", "getGateManifest", "uploadScans"}

func withGatePolicy(fn func(*gate.Policy)) envOption {
	return func(c *envConfig) { fn(&c.gatePolicy) }
}

// gateSteward inserts a GATE_STAFF user and mints their access token.
func (e *env) gateSteward() (int64, string) {
	e.t.Helper()
	phone, name := randPhone(e.t), "Test Steward"
	u, err := e.q.CreateUser(context.Background(), store.CreateUserParams{PhoneE164: &phone, Name: &name, Role: string(domain.RoleGateStaff)})
	require.NoError(e.t, err)
	token, err := e.auth.Tokens().IssueAccess(u.ID, domain.RoleGateStaff, "test-session")
	require.NoError(e.t, err)
	return u.ID, token
}

func newScanID(t testing.TB) string {
	t.Helper()
	var id [16]byte
	_, err := rand.Read(id[:])
	require.NoError(t, err)
	id[6], id[8] = id[6]&0x0f|0x40, id[8]&0x3f|0x80
	return ticketing.FormatUUID(id)
}

// scanOf is a scan as a device uploads it: a fresh client_scan_id, the device's verdict
// ADMITTED and no token. Tests change what they need.
func scanOf(t testing.TB, ticketID string, eventID int64, gateGroup string, at time.Time) obj {
	return obj{
		"client_scan_id": newScanID(t), "ticket_id": ticketID, "event_id": eventID, "gate_group": gateGroup,
		"scanned_at": at.UTC().Format(time.RFC3339Nano), "result": "ADMITTED",
	}
}

func with(o obj, kv ...any) obj {
	c := obj{}
	for k, v := range o {
		c[k] = v
	}
	for i := 0; i < len(kv); i += 2 {
		c[kv[i].(string)] = kv[i+1]
	}
	return c
}

func (e *env) uploadScans(token, device string, scans ...obj) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("POST", "/gate/scans", obj{"device_id": device, "scans": scans}, token)
}

type scanResultJSON struct {
	ClientScanID    string  `json:"client_scan_id"`
	TicketID        string  `json:"ticket_id"`
	Result          string  `json:"result"`
	Overridden      bool    `json:"overridden"`
	TicketStatus    *string `json:"ticket_status"`
	FirstAdmittedAt *string `json:"first_admitted_at"`
}

func scanResults(t testing.TB, rec *httptest.ResponseRecorder) []scanResultJSON {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return decode[struct {
		Results []scanResultJSON `json:"results"`
	}](t.(*testing.T), rec).Results
}

// one uploads a single scan and returns its result.
func (e *env) one(token, device string, scan obj) scanResultJSON {
	e.t.Helper()
	res := scanResults(e.t, e.uploadScans(token, device, scan))
	require.Len(e.t, res, 1)
	require.Equal(e.t, scan["client_scan_id"], res[0].ClientScanID)
	return res[0]
}

type manifestJSON struct {
	Event struct {
		ID        int64  `json:"id"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		VenueName string `json:"venue_name"`
	} `json:"event"`
	Version     string `json:"version"`
	Full        bool   `json:"full"`
	GeneratedAt string `json:"generated_at"`
	Keys        []struct {
		KeyID     string `json:"key_id"`
		Algorithm string `json:"algorithm"`
		PublicKey string `json:"public_key"`
	} `json:"keys"`
	GateGroups  []string `json:"gate_groups"`
	TicketCount int      `json:"ticket_count"`
	Tickets     [][]any  `json:"tickets"`
}

// byTicket maps ticket id to "<status code> <gate group>".
func (m manifestJSON) byTicket(t testing.TB) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, tu := range m.Tickets {
		require.Len(t, tu, 3)
		idx := int(tu[1].(float64))
		require.Less(t, idx, len(m.GateGroups), "gate_group_index points into gate_groups")
		out[tu[0].(string)] = tu[2].(string) + " " + m.GateGroups[idx]
	}
	require.Len(t, out, len(m.Tickets), "a ticket appears once")
	return out
}

func (e *env) manifest(token string, eventID int64, since string) manifestJSON {
	e.t.Helper()
	path := fmt.Sprintf("/gate/events/%d/manifest", eventID)
	if since != "" {
		path += "?since=" + since
	}
	rec := e.do("GET", path, nil, token)
	require.Equal(e.t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return decode[manifestJSON](e.t, rec)
}

// issuedLongAgo moves the issue time of an event's tickets into the past, so that a test can
// scan them "an hour ago": a scan timed before its ticket existed is not believed.
func issuedLongAgo(t testing.TB, eventID int64) {
	t.Helper()
	exec(t, `UPDATE tickets SET issued_at = issued_at - interval '6 hours' WHERE event_id = $1`, eventID)
}

// ageTickets moves the change stamps of an event's tickets into the past. Without it every
// changes-only manifest in the first two minutes of a test would rightly carry every ticket.
func ageTickets(t testing.TB, eventID int64, by time.Duration) {
	t.Helper()
	untriggered(t, func(tx pgx.Tx) {
		_, err := tx.Exec(context.Background(), `UPDATE tickets SET updated_at = updated_at - make_interval(secs => $2) WHERE event_id = $1`, eventID, by.Seconds())
		require.NoError(t, err)
	})
}

type scanRow struct {
	Result, GateID, DeviceID string
	ScannedBy                *int64
	EventID                  int64
	ScannedAt                time.Time
}

func scanLog(t testing.TB, clientScanID any) scanRow {
	t.Helper()
	var r scanRow
	require.NoError(t, db.Pool.QueryRow(context.Background(),
		`SELECT result, gate_id, device_id, scanned_by, event_id, scanned_at FROM scan_logs WHERE client_scan_id = $1`, clientScanID).
		Scan(&r.Result, &r.GateID, &r.DeviceID, &r.ScannedBy, &r.EventID, &r.ScannedAt))
	return r
}

func ticketState(t testing.TB, ticketID string) (status string, usedAt *time.Time) {
	t.Helper()
	require.NoError(t, db.Pool.QueryRow(context.Background(), `SELECT status, used_at FROM tickets WHERE id = $1`, ticketID).Scan(&status, &usedAt))
	return status, usedAt
}

// gateFixture is a published event with issued tickets and a steward.
type gateFixture struct {
	e       *env
	ev      tEvent
	steward int64
	token   string
	// north and south are tickets in gate groups N-LOWER and S-LOWER.
	north, south []ticketing.Ticket
	// past is a time comfortably inside what the server believes: after the tickets were
	// issued (issuedLongAgo) and before now.
	past time.Time
}

func newGateFixture(t *testing.T, nNorth, nSouth int, opts ...envOption) gateFixture {
	t.Helper()
	e := newEnv(t, opts...)
	ev, north, south := e.ticketEvent()
	fanID, _ := e.newFan()
	f := gateFixture{e: e, ev: ev, past: time.Now().Add(-time.Hour).UTC().Truncate(time.Second)}
	if nNorth > 0 {
		_, _, f.north = e.issued(ev.ID, fanID, north[:nNorth]...)
	}
	if nSouth > 0 {
		_, _, f.south = e.issued(ev.ID, fanID, south[:nSouth]...)
	}
	issuedLongAgo(t, ev.ID)
	f.steward, f.token = e.gateSteward()
	return f
}

// ---------------------------------------------------------------------------
// Scan upload: the decision table
// ---------------------------------------------------------------------------

func TestGateScan_ValidTicketIsAdmittedAndTheSecondScanIsADuplicate(t *testing.T) {
	f := newGateFixture(t, 2, 0)
	e, tk := f.e, f.north[0]

	first := with(scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past), "token", tk.QRToken())
	res := e.one(f.token, "gate-phone-01", first)
	assert.Equal(t, "ADMITTED", res.Result)
	assert.False(t, res.Overridden)
	assert.Equal(t, tk.ID(), res.TicketID)
	require.NotNil(t, res.TicketStatus)
	assert.Equal(t, "USED", *res.TicketStatus)
	require.NotNil(t, res.FirstAdmittedAt)
	assert.Equal(t, f.past.Format(time.RFC3339), *res.FirstAdmittedAt)

	status, usedAt := ticketState(t, tk.ID())
	assert.Equal(t, "USED", status)
	require.NotNil(t, usedAt)
	assert.True(t, usedAt.Equal(f.past), "used_at is the scan time, not the upload time: %s", usedAt)

	row := scanLog(t, first["client_scan_id"])
	assert.Equal(t, scanRow{Result: "ADMITTED", GateID: "N-LOWER", DeviceID: "gate-phone-01", ScannedBy: &f.steward, EventID: f.ev.ID, ScannedAt: row.ScannedAt}, row)
	assert.True(t, row.ScannedAt.Equal(f.past))

	// The same QR again, a minute later, on another device.
	second := with(scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past.Add(time.Minute)), "token", tk.QRToken())
	res = e.one(f.token, "gate-phone-02", second)
	assert.Equal(t, "DUPLICATE", res.Result)
	assert.True(t, res.Overridden, "the device said ADMITTED")
	assert.Equal(t, "USED", *res.TicketStatus)
	assert.Equal(t, f.past.Format(time.RFC3339), *res.FirstAdmittedAt, "still the first admission")
	assert.Equal(t, "DUPLICATE", scanLog(t, second["client_scan_id"]).Result)
	assert.Equal(t, "ADMITTED", scanLog(t, first["client_scan_id"]).Result)

	// A device that already knew (its manifest said U) and refused: no override.
	third := with(scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past.Add(2*time.Minute)), "result", "DUPLICATE")
	res = e.one(f.token, "gate-phone-02", third)
	assert.Equal(t, "DUPLICATE", res.Result)
	assert.False(t, res.Overridden)

	// The other ticket of the booking was not touched.
	status, usedAt = ticketState(t, f.north[1].ID())
	assert.Equal(t, "VALID", status)
	assert.Nil(t, usedAt)
}

func TestGateScan_ForgedOrMismatchedTokenIsInvalid(t *testing.T) {
	f := newGateFixture(t, 2, 0)
	e, tk, other := f.e, f.north[0], f.north[1]

	// The same claims, signed by a key the server does not trust under the same key ID.
	claims, err := e.tickets.Verifier().Verify(tk.QRToken())
	require.NoError(t, err)
	stranger, _ := newTicketKeys(t, "k1")
	forged, err := stranger.Signer.Sign(claims)
	require.NoError(t, err)
	// And a key ID the server has never had.
	unknownKey, _ := newTicketKeys(t, "k9")
	unknown, err := unknownKey.Signer.Sign(claims)
	require.NoError(t, err)
	// One character of the payload changed.
	flipped := []byte(tk.QRToken())
	if flipped[20] == 'A' {
		flipped[20] = 'B'
	} else {
		flipped[20] = 'A'
	}

	for name, token := range map[string]string{
		"signed with another key":           forged,
		"signed with an unknown key ID":     unknown,
		"one character changed":             string(flipped),
		"not a token at all":                "hello",
		"a real token, of another ticket":   other.QRToken(),
		"a truncated token":                 tk.QRToken()[:100],
		"the right token with a space":      tk.QRToken() + " ",
		"the test key of the format's spec": "TLT1.AQABCw86UnweTX6aVwwbTV5vcAAAAAAAAAABTi1MT1dFUgAAAAAAAAAAAKGyw9Tl9gcY.O5024bVFl6fq-rPKjqJ15KNls5q3woLuzPEG33mc1oS0w-FSiCHxQXjhzotnEv0Wpfr78tSzzMR8KmOHMyk4Dg",
	} {
		t.Run(name, func(t *testing.T) {
			sc := with(scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past), "token", token)
			res := scanResults(t, e.uploadScans(f.token, "gate-phone-01", sc))[0]
			assert.Equal(t, "INVALID", res.Result)
			assert.True(t, res.Overridden)
			require.NotNil(t, res.TicketStatus, "the ticket ID itself is real")
			assert.Equal(t, "VALID", *res.TicketStatus, "a forged scan does not use up the real ticket")
			assert.Nil(t, res.FirstAdmittedAt)
			assert.Equal(t, "INVALID", scanLog(t, sc["client_scan_id"]).Result)
		})
	}
	status, _ := ticketState(t, tk.ID())
	assert.Equal(t, "VALID", status)

	// After all that the real one still gets in.
	res := e.one(f.token, "gate-phone-01", with(scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past), "token", tk.QRToken()))
	assert.Equal(t, "ADMITTED", res.Result)
	// The key never reached a log line through the refused tokens' errors.
	assert.NotContains(t, e.logs.String(), tk.QRToken())
}

func TestGateScan_TicketOfAnotherEventIsWrongEvent(t *testing.T) {
	f := newGateFixture(t, 1, 0)
	e, tk := f.e, f.north[0]
	// A second event; the device at its gate scans a ticket of the first.
	otherEvent, _, _ := e.ticketEvent()

	for name, mod := range map[string]func(obj) obj{
		"with the token":    func(o obj) obj { return with(o, "token", tk.QRToken()) },
		"without the token": func(o obj) obj { return o },
	} {
		t.Run(name, func(t *testing.T) {
			sc := mod(scanOf(t, tk.ID(), otherEvent.ID, "N-LOWER", f.past))
			res := scanResults(t, e.uploadScans(f.token, "gate-phone-01", sc))[0]
			assert.Equal(t, "WRONG_EVENT", res.Result)
			assert.True(t, res.Overridden)
			assert.Equal(t, "VALID", *res.TicketStatus)
			row := scanLog(t, sc["client_scan_id"])
			assert.Equal(t, "WRONG_EVENT", row.Result)
			assert.Equal(t, otherEvent.ID, row.EventID, "logged under the event the device was scanning for")
		})
	}
	// The device got it right itself: nothing to override.
	res := e.one(f.token, "gate-phone-01", with(scanOf(t, tk.ID(), otherEvent.ID, "N-LOWER", f.past), "result", "WRONG_EVENT", "token", tk.QRToken()))
	assert.Equal(t, "WRONG_EVENT", res.Result)
	assert.False(t, res.Overridden)
	status, _ := ticketState(t, tk.ID())
	assert.Equal(t, "VALID", status)
}

func TestGateScan_TicketOfAnotherStandIsWrongGate(t *testing.T) {
	f := newGateFixture(t, 1, 1)
	e, north, south := f.e, f.north[0], f.south[0]
	require.Equal(t, "N-LOWER", north.Row.Ticket.GateGroup)
	require.Equal(t, "S-LOWER", south.Row.Ticket.GateGroup)

	// The signed claim is checked when the token is sent, the ticket's own row when it is
	// not: either alone must refuse.
	t.Run("with the token", func(t *testing.T) {
		sc := with(scanOf(t, north.ID(), f.ev.ID, "S-LOWER", f.past), "token", north.QRToken())
		res := scanResults(t, e.uploadScans(f.token, "gate-phone-01", sc))[0]
		assert.Equal(t, "WRONG_GATE", res.Result)
		assert.True(t, res.Overridden)
		assert.Equal(t, "S-LOWER", scanLog(t, sc["client_scan_id"]).GateID, "the gate the device stood at")
	})
	t.Run("without the token", func(t *testing.T) {
		sc := scanOf(t, north.ID(), f.ev.ID, "S-LOWER", f.past)
		res := scanResults(t, e.uploadScans(f.token, "gate-phone-01", sc))[0]
		assert.Equal(t, "WRONG_GATE", res.Result)
		assert.True(t, res.Overridden)
	})
	t.Run("the upper tier of the same stand", func(t *testing.T) {
		res := scanResults(t, e.uploadScans(f.token, "gate-phone-01", scanOf(t, north.ID(), f.ev.ID, "N-UPPER", f.past)))[0]
		assert.Equal(t, "WRONG_GATE", res.Result)
	})
	status, usedAt := ticketState(t, north.ID())
	assert.Equal(t, "VALID", status, "a fan sent to the right gate still has a ticket")
	assert.Nil(t, usedAt)

	// At their own gates both get in.
	assert.Equal(t, "ADMITTED", e.one(f.token, "gate-phone-01", scanOf(t, north.ID(), f.ev.ID, "N-LOWER", f.past)).Result)
	assert.Equal(t, "ADMITTED", e.one(f.token, "gate-phone-02", scanOf(t, south.ID(), f.ev.ID, "S-LOWER", f.past)).Result)
}

func TestGateScan_SameBatchTwiceChangesNothing(t *testing.T) {
	f := newGateFixture(t, 3, 1)
	e := f.e
	unknownTicket := newScanID(t)
	batch := []obj{
		with(scanOf(t, f.north[0].ID(), f.ev.ID, "N-LOWER", f.past), "token", f.north[0].QRToken()),
		scanOf(t, f.north[0].ID(), f.ev.ID, "N-LOWER", f.past.Add(time.Second)),
		scanOf(t, f.north[1].ID(), f.ev.ID, "N-LOWER", f.past.Add(2*time.Second)),
		scanOf(t, f.south[0].ID(), f.ev.ID, "N-LOWER", f.past.Add(3*time.Second)),
		scanOf(t, unknownTicket, f.ev.ID, "N-LOWER", f.past.Add(4*time.Second)),
		with(scanOf(t, f.north[2].ID(), f.ev.ID, "N-LOWER", f.past.Add(5*time.Second)), "result", "INVALID"),
	}
	first := e.uploadScans(f.token, "gate-phone-01", batch...)
	res := scanResults(t, first)
	var got []string
	for _, r := range res {
		got = append(got, r.Result)
	}
	assert.Equal(t, []string{"ADMITTED", "DUPLICATE", "ADMITTED", "WRONG_GATE", "INVALID", "INVALID"}, got)

	snapshot := func() string {
		return queryText(t, `
			SELECT (SELECT count(*) || ':' || COALESCE(md5(string_agg(id::text || result || scanned_at::text || synced_at::text, ',' ORDER BY id)), '')
			          FROM scan_logs WHERE event_id = $1)
			    || '/' || (SELECT md5(string_agg(id::text || status || COALESCE(used_at::text, '') || updated_at::text, ',' ORDER BY id))
			          FROM tickets WHERE event_id = $1)`, f.ev.ID)
	}
	before := snapshot()
	require.True(t, strings.HasPrefix(before, "6:"), before)

	for i := 0; i < 3; i++ {
		again := e.uploadScans(f.token, "gate-phone-01", batch...)
		require.Equal(t, http.StatusOK, again.Code)
		assert.JSONEq(t, first.Body.String(), again.Body.String(), "the same answer, upload %d", i+2)
	}
	assert.Equal(t, before, snapshot(), "no row was added and none was rewritten (not even a ticket's updated_at)")

	// Nothing of a stored scan is taken from a later upload, even when the device changes
	// its story: other ticket, other time, other verdict.
	liar := with(batch[0], "ticket_id", f.north[2].ID(), "scanned_at", f.past.Add(-time.Minute).Format(time.RFC3339), "result", "DUPLICATE")
	delete(liar, "token")
	r := e.one(f.token, "gate-phone-09", liar)
	assert.Equal(t, "ADMITTED", r.Result, "the stored result")
	assert.Equal(t, f.north[0].ID(), r.TicketID, "about the stored ticket")
	assert.True(t, r.Overridden, "it differs from what was sent this time")
	assert.Equal(t, before, snapshot())

	// The same client_scan_id twice in one batch is one scan.
	dup := scanOf(t, f.north[2].ID(), f.ev.ID, "N-LOWER", f.past.Add(10*time.Second))
	res = scanResults(t, e.uploadScans(f.token, "gate-phone-01", dup, dup))
	require.Len(t, res, 2)
	assert.Equal(t, "ADMITTED", res[0].Result)
	assert.Equal(t, "ADMITTED", res[1].Result)
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM scan_logs WHERE client_scan_id = $1`, dup["client_scan_id"]))
}

// Two phones at one gate group, both offline, both admit the same ticket. After both have
// synced, exactly one admission stands: the one with the earlier scan time, whichever phone
// reached the server first.
func TestGateScan_TwoDevicesOffline_EarliestScanTimeWinsWhicheverUploadsFirst(t *testing.T) {
	for _, order := range []string{"the earlier scan uploads first", "the later scan uploads first"} {
		t.Run(order, func(t *testing.T) {
			f := newGateFixture(t, 1, 0)
			e, tk := f.e, f.north[0]
			at1900, at1901 := f.past, f.past.Add(time.Minute)
			scanA := with(scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", at1900), "token", tk.QRToken())
			scanB := with(scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", at1901), "token", tk.QRToken())

			if order == "the earlier scan uploads first" {
				a := e.one(f.token, "phone-A", scanA)
				assert.Equal(t, "ADMITTED", a.Result)
				b := e.one(f.token, "phone-B", scanB)
				assert.Equal(t, "DUPLICATE", b.Result)
				assert.True(t, b.Overridden, "phone B is told at once that its admission lost")
			} else {
				b := e.one(f.token, "phone-B", scanB)
				assert.Equal(t, "ADMITTED", b.Result, "nothing earlier is known yet")
				assert.False(t, b.Overridden)
				_, usedAt := ticketState(t, tk.ID())
				assert.True(t, usedAt.Equal(at1901))

				a := e.one(f.token, "phone-A", scanA)
				assert.Equal(t, "ADMITTED", a.Result, "the earlier scan takes the ticket")
				assert.False(t, a.Overridden)
				assert.Equal(t, at1900.Format(time.RFC3339), *a.FirstAdmittedAt)

				// Phone B was told ADMITTED and nothing tells it otherwise, until it asks
				// again: resending the scan answers with the result as it is now.
				b = e.one(f.token, "phone-B", scanB)
				assert.Equal(t, "DUPLICATE", b.Result)
				assert.True(t, b.Overridden)
				assert.Equal(t, at1900.Format(time.RFC3339), *b.FirstAdmittedAt)
			}

			assert.Equal(t, "ADMITTED", scanLog(t, scanA["client_scan_id"]).Result)
			assert.Equal(t, "DUPLICATE", scanLog(t, scanB["client_scan_id"]).Result)
			assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM scan_logs WHERE ticket_id = $1 AND result = 'ADMITTED'`, tk.ID()))
			assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM scan_logs WHERE ticket_id = $1`, tk.ID()))
			status, usedAt := ticketState(t, tk.ID())
			assert.Equal(t, "USED", status)
			assert.True(t, usedAt.Equal(at1900), "used_at is the earlier scan time: %s", usedAt)
		})
	}
}

// The tie-break is fixed: two admissions at the very same instant go to the smaller
// client_scan_id, in either upload order and inside one batch.
func TestGateScan_EqualScanTimesGoToTheSmallerClientScanID(t *testing.T) {
	for _, order := range []string{"small first", "large first", "one batch"} {
		t.Run(order, func(t *testing.T) {
			f := newGateFixture(t, 1, 0)
			e, tk := f.e, f.north[0]
			small := with(scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past), "client_scan_id", "00000000-0000-4000-8000-"+randDigits(t, 12))
			large := with(scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past), "client_scan_id", "ffffffff-ffff-4fff-bfff-"+randDigits(t, 12))
			switch order {
			case "small first":
				e.one(f.token, "phone-A", small)
				e.one(f.token, "phone-B", large)
			case "large first":
				e.one(f.token, "phone-B", large)
				e.one(f.token, "phone-A", small)
			default:
				res := scanResults(t, e.uploadScans(f.token, "phone-A", large, small))
				assert.Equal(t, "DUPLICATE", res[0].Result)
				assert.Equal(t, "ADMITTED", res[1].Result)
			}
			assert.Equal(t, "ADMITTED", scanLog(t, small["client_scan_id"]).Result)
			assert.Equal(t, "DUPLICATE", scanLog(t, large["client_scan_id"]).Result)
		})
	}
}

// Three devices, uploaded latest first: the admission moves back twice and ends with the
// earliest; a re-upload of everything then reports the settled state to each.
func TestGateScan_AdmissionMovesBackAsEarlierScansArrive(t *testing.T) {
	f := newGateFixture(t, 1, 0)
	e, tk := f.e, f.north[0]
	scans := []obj{
		scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past),
		scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past.Add(time.Minute)),
		scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past.Add(2*time.Minute)),
	}
	for i := 2; i >= 0; i-- {
		assert.Equal(t, "ADMITTED", e.one(f.token, fmt.Sprintf("phone-%d", i), scans[i]).Result, "scan %d is the earliest known when it arrives", i)
	}
	res := scanResults(t, e.uploadScans(f.token, "phone-0", scans...))
	assert.Equal(t, "ADMITTED", res[0].Result)
	assert.Equal(t, "DUPLICATE", res[1].Result)
	assert.Equal(t, "DUPLICATE", res[2].Result)
	_, usedAt := ticketState(t, tk.ID())
	assert.True(t, usedAt.Equal(f.past))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM scan_logs WHERE ticket_id = $1 AND result = 'ADMITTED'`, tk.ID()))
}

func TestGateScan_VoidTransferredAndUnknownTicketsAreInvalid(t *testing.T) {
	f := newGateFixture(t, 3, 0)
	e := f.e
	void, transferred, live := f.north[0], f.north[1], f.north[2]
	exec(t, `UPDATE tickets SET status = 'VOID' WHERE id = $1`, void.ID())
	exec(t, `UPDATE tickets SET status = 'TRANSFERRED' WHERE id = $1`, transferred.ID())
	unknown := newScanID(t)

	scans := []obj{
		with(scanOf(t, void.ID(), f.ev.ID, "N-LOWER", f.past), "token", void.QRToken()),
		scanOf(t, transferred.ID(), f.ev.ID, "N-LOWER", f.past),
		scanOf(t, unknown, f.ev.ID, "N-LOWER", f.past),
		with(scanOf(t, unknown, f.ev.ID, "N-LOWER", f.past), "result", "INVALID"),
		scanOf(t, live.ID(), f.ev.ID, "N-LOWER", f.past),
	}
	res := scanResults(t, e.uploadScans(f.token, "gate-phone-01", scans...))
	require.Len(t, res, 5)

	assert.Equal(t, "INVALID", res[0].Result, "a void ticket's signature is still good; only its status refuses it")
	assert.True(t, res[0].Overridden)
	assert.Equal(t, "VOID", *res[0].TicketStatus)
	assert.Equal(t, "INVALID", res[1].Result)
	assert.Equal(t, "TRANSFERRED", *res[1].TicketStatus)

	assert.Equal(t, "INVALID", res[2].Result)
	assert.True(t, res[2].Overridden)
	assert.Nil(t, res[2].TicketStatus, "the server has no such ticket")
	assert.Nil(t, res[2].FirstAdmittedAt)
	assert.Equal(t, unknown, res[2].TicketID)
	assert.False(t, res[3].Overridden, "the device said INVALID itself")
	// Logged all the same: scan_logs.ticket_id has no foreign key for exactly this.
	row := scanLog(t, scans[2]["client_scan_id"])
	assert.Equal(t, "INVALID", row.Result)
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM scan_logs WHERE ticket_id = $1`, unknown))

	assert.Equal(t, "ADMITTED", res[4].Result)
	status, _ := ticketState(t, void.ID())
	assert.Equal(t, "VOID", status, "VOID stays VOID")
	status, _ = ticketState(t, transferred.ID())
	assert.Equal(t, "TRANSFERRED", status)
}

// A scan the steward refused is never turned into an admission by the server, even when
// the server finds nothing wrong with the ticket.
func TestGateScan_ARefusalAtTheGateStaysARefusal(t *testing.T) {
	f := newGateFixture(t, 1, 0)
	e, tk := f.e, f.north[0]
	for _, verdict := range []string{"DUPLICATE", "INVALID", "WRONG_EVENT", "WRONG_GATE"} {
		sc := with(scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past), "result", verdict, "token", tk.QRToken())
		res := e.one(f.token, "gate-phone-01", sc)
		assert.Equal(t, verdict, res.Result)
		assert.False(t, res.Overridden)
		assert.Equal(t, "VALID", *res.TicketStatus)
		assert.Nil(t, res.FirstAdmittedAt)
	}
	status, usedAt := ticketState(t, tk.ID())
	assert.Equal(t, "VALID", status)
	assert.Nil(t, usedAt)
	// And none of them stands in the way of the real admission later, however early they were.
	assert.Equal(t, "ADMITTED", e.one(f.token, "gate-phone-01", scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past.Add(time.Minute))).Result)
}

// A ticket voided (refund, cancelled event) after it was scanned: the stored scan keeps its
// ADMITTED result, the ticket is VOID and keeps used_at, and a scan uploaded after the void
// is INVALID whenever it was made.
func TestGateScan_TicketVoidedAfterItWasScanned(t *testing.T) {
	f := newGateFixture(t, 2, 0)
	e, scanned, late := f.e, f.north[0], f.north[1]
	first := scanOf(t, scanned.ID(), f.ev.ID, "N-LOWER", f.past)
	assert.Equal(t, "ADMITTED", e.one(f.token, "phone-A", first).Result)
	// Phone B admitted the other ticket offline at the same time and has not synced.
	offline := scanOf(t, late.ID(), f.ev.ID, "N-LOWER", f.past)

	want(t, e.do("POST", fmt.Sprintf("/admin/events/%d/cancel", f.ev.ID), nil, f.ev.Token), http.StatusOK)
	status, usedAt := ticketState(t, scanned.ID())
	assert.Equal(t, "VOID", status)
	require.NotNil(t, usedAt, "the record that somebody entered on it is kept")
	assert.True(t, usedAt.Equal(f.past))

	res := e.one(f.token, "phone-A", first)
	assert.Equal(t, "ADMITTED", res.Result, "the stored scan is history; it is not rewritten")
	assert.Equal(t, "VOID", *res.TicketStatus)
	assert.Equal(t, f.past.Format(time.RFC3339), *res.FirstAdmittedAt)

	res = e.one(f.token, "phone-B", offline)
	assert.Equal(t, "INVALID", res.Result, "judged by the ticket's status at upload")
	assert.True(t, res.Overridden)
	assert.Equal(t, "VOID", *res.TicketStatus)
	assert.Nil(t, res.FirstAdmittedAt)
	assert.True(t, scanLog(t, offline["client_scan_id"]).ScannedAt.Equal(f.past), "the log still says when the steward let them in")

	res = e.one(f.token, "phone-B", scanOf(t, scanned.ID(), f.ev.ID, "N-LOWER", f.past.Add(time.Minute)))
	assert.Equal(t, "INVALID", res.Result)
	status, _ = ticketState(t, scanned.ID())
	assert.Equal(t, "VOID", status, "nothing leaves VOID")
}

// The device's clock is bounded, not trusted.
func TestGateScan_ImpossibleScanTimesAreReplacedByTheReceiveTime(t *testing.T) {
	f := newGateFixture(t, 4, 0)
	e := f.e
	now := time.Now().UTC()
	issuedAt := func(id string) time.Time {
		var at time.Time
		require.NoError(t, db.Pool.QueryRow(context.Background(), `SELECT issued_at FROM tickets WHERE id = $1`, id).Scan(&at))
		return at
	}

	cases := []struct {
		name   string
		ticket ticketing.Ticket
		at     time.Time
		kept   bool
	}{
		{"a day in the future", f.north[0], now.Add(24 * time.Hour), false},
		{"the year 2001", f.north[1], time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC), false},
		{"before the ticket was issued", f.north[2], issuedAt(f.north[2].ID()).Add(-time.Minute), false},
		{"an hour ago", f.north[3], f.past, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := time.Now().Add(-2 * time.Second)
			sc := scanOf(t, c.ticket.ID(), f.ev.ID, "N-LOWER", c.at)
			res := e.one(f.token, "gate-phone-01", sc)
			after := time.Now().Add(2 * time.Second)
			assert.Equal(t, "ADMITTED", res.Result, "the scan is kept; only its time is not believed")
			stored := scanLog(t, sc["client_scan_id"]).ScannedAt
			_, usedAt := ticketState(t, c.ticket.ID())
			require.NotNil(t, usedAt)
			assert.True(t, usedAt.Equal(stored))
			if c.kept {
				assert.True(t, stored.Equal(c.at), "%s", stored)
				return
			}
			assert.True(t, stored.After(before) && stored.Before(after), "stored as the receive time, got %s", stored)
		})
	}

	// So a clock set far back cannot take a ticket from an honest scan: its time becomes
	// "now", which is later.
	g := newGateFixture(t, 1, 0)
	tk := g.north[0]
	honest := scanOf(t, tk.ID(), g.ev.ID, "N-LOWER", g.past)
	cheat := scanOf(t, tk.ID(), g.ev.ID, "N-LOWER", time.Date(1999, 12, 31, 23, 59, 0, 0, time.UTC))
	assert.Equal(t, "ADMITTED", g.e.one(g.token, "phone-A", honest).Result)
	assert.Equal(t, "DUPLICATE", g.e.one(g.token, "phone-B", cheat).Result)
	assert.Contains(t, g.e.logs.String(), `"times_replaced":1`)
}

func TestGateScan_BatchLimitsAndValidation(t *testing.T) {
	f := newGateFixture(t, 1, 0)
	e, tk := f.e, f.north[0]
	good := func() obj { return scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past) }
	rows := func() int { return queryInt(t, `SELECT count(*) FROM scan_logs WHERE event_id = $1`, f.ev.ID) }

	t.Run("501 scans", func(t *testing.T) {
		batch := make([]obj, 0, 501)
		for i := 0; i < 501; i++ {
			batch = append(batch, good())
		}
		wantError(t, e.uploadScans(f.token, "gate-phone-01", batch...), http.StatusRequestEntityTooLarge, domain.CodePayloadTooLarge)
		assert.Zero(t, rows())
	})
	t.Run("a body over the size limit", func(t *testing.T) {
		batch := make([]obj, 0, 400)
		for i := 0; i < 400; i++ {
			batch = append(batch, with(good(), "token", strings.Repeat("A", 3000)))
		}
		wantError(t, e.uploadScans(f.token, "gate-phone-01", batch...), http.StatusRequestEntityTooLarge, domain.CodePayloadTooLarge)
		assert.Zero(t, rows())
	})
	t.Run("no scans", func(t *testing.T) {
		wantFields(t, e.do("POST", "/gate/scans", obj{"device_id": "d", "scans": []obj{}}, f.token), "scans")
		wantFields(t, e.do("POST", "/gate/scans", obj{"device_id": "d"}, f.token), "scans")
	})
	t.Run("device_id", func(t *testing.T) {
		wantFields(t, e.do("POST", "/gate/scans", obj{"scans": []obj{good()}}, f.token), "device_id")
		wantFields(t, e.uploadScans(f.token, strings.Repeat("d", 65), good()), "device_id")
	})
	t.Run("bad fields are named by position", func(t *testing.T) {
		bad := obj{"client_scan_id": "nope", "ticket_id": "also-nope", "event_id": 0, "gate_group": "north", "scanned_at": "yesterday", "result": "MAYBE", "token": strings.Repeat("x", 513)}
		rules := wantFields(t, e.uploadScans(f.token, "gate-phone-01", good(), bad),
			"scans[1].client_scan_id", "scans[1].ticket_id", "scans[1].event_id", "scans[1].gate_group", "scans[1].scanned_at", "scans[1].result", "scans[1].token")
		assert.Equal(t, "uuid", rules["scans[1].ticket_id"])
		assert.Equal(t, "gategroup", rules["scans[1].gate_group"])
		assert.Equal(t, "datetime", rules["scans[1].scanned_at"])
		assert.Equal(t, "oneof", rules["scans[1].result"])
		assert.Zero(t, rows(), "one bad scan and none of the batch is stored")
	})
	t.Run("an unknown field", func(t *testing.T) {
		wantFields(t, e.uploadScans(f.token, "gate-phone-01", with(good(), "verdict", "ADMITTED")), "verdict")
	})
	t.Run("an event that does not exist", func(t *testing.T) {
		rules := wantFields(t, e.uploadScans(f.token, "gate-phone-01", good(), with(good(), "event_id", int64(1)<<40)), "scans[1].event_id")
		assert.Equal(t, "event", rules["scans[1].event_id"])
		assert.Zero(t, rows())
	})
	t.Run("500 scans are accepted", func(t *testing.T) {
		batch := make([]obj, 0, 500)
		for i := 0; i < 500; i++ {
			batch = append(batch, scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past.Add(time.Duration(i)*time.Second)))
		}
		res := scanResults(t, e.uploadScans(f.token, "gate-phone-01", batch...))
		require.Len(t, res, 500)
		assert.Equal(t, "ADMITTED", res[0].Result)
		for _, r := range res[1:] {
			require.Equal(t, "DUPLICATE", r.Result)
		}
		assert.Equal(t, 500, rows())
	})
}

func TestGateScan_RateLimitedPerDevice(t *testing.T) {
	f := newGateFixture(t, 1, 0, withLimits(func(l *httpapi.RateLimits) {
		l.ScanUploadPerDevice = ratelimit.Rule{Limit: 2, Window: time.Minute}
		l.ScanUploadPerUser = ratelimit.Rule{Limit: 5, Window: time.Minute}
	}))
	e, tk := f.e, f.north[0]
	sc := func() obj { return scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past) }
	require.Equal(t, http.StatusOK, e.uploadScans(f.token, "phone-A", sc()).Code)
	require.Equal(t, http.StatusOK, e.uploadScans(f.token, "phone-A", sc()).Code)
	rec := e.uploadScans(f.token, "phone-A", sc())
	wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM scan_logs WHERE event_id = $1`, f.ev.ID), "a refused upload stores nothing")

	// Another device of the same steward is not held back by the first one's count…
	require.Equal(t, http.StatusOK, e.uploadScans(f.token, "phone-B", sc()).Code)
	// …and another steward using the same device name has a count of their own.
	_, other := e.gateSteward()
	require.Equal(t, http.StatusOK, e.uploadScans(other, "phone-A", sc()).Code)
	// The per-user limit counts every device of the account: 4 calls so far, the 5th
	// passes, the 6th does not, whatever the device calls itself.
	require.Equal(t, http.StatusOK, e.uploadScans(f.token, "phone-C", sc()).Code)
	wantError(t, e.uploadScans(f.token, "phone-D", sc()), http.StatusTooManyRequests, domain.CodeRateLimited)

	e.clock.Advance(61 * time.Second)
	require.Equal(t, http.StatusOK, e.uploadScans(f.token, "phone-A", sc()).Code)
}

// An organiser's cancel or edit holds the event exclusive. The upload waits a bounded time
// and then says so, storing nothing; sent again afterwards it goes through.
func TestGateScan_EventBusyWhileAnOrganiserHoldsTheEvent(t *testing.T) {
	f := newGateFixture(t, 1, 0, withGatePolicy(func(p *gate.Policy) { p.EventLockWait = 200 * time.Millisecond }))
	e, tk := f.e, f.north[0]
	sc := scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past)

	release := slowPublish(t, f.ev.ID)
	wantError(t, e.uploadScans(f.token, "gate-phone-01", sc), http.StatusConflict, domain.CodeEventBusy)
	assert.Zero(t, queryInt(t, `SELECT count(*) FROM scan_logs WHERE event_id = $1`, f.ev.ID))
	release()
	assert.Equal(t, "ADMITTED", e.one(f.token, "gate-phone-01", sc).Result)
}

// ---------------------------------------------------------------------------
// Scan upload: concurrency
// ---------------------------------------------------------------------------

// serve sends a request straight to the router from a goroutine (the contract check and
// its t.Errorf stay on the test's own goroutine).
func (e *env) serve(method, path string, body any, token string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, e.request(method, path, body, token))
	return rec
}

// Two batches carrying the same tickets, in opposite orders, at the same instant, many
// rounds: each ticket ends with exactly one admission, the one with the earlier scan time,
// and neither upload fails (a deadlock would be a 500).
func TestGateScan_TwoBatchesAtTheSameInstant_OneWinnerNoDeadlock(t *testing.T) {
	const rounds, perRound = 40, 6
	e := newEnv(t)
	v := e.newVenue(2, 10, 30)
	ev := e.publishedEvent(v)
	seats := eventSeatIDs(t, ev.ID, v.BlockIDs[0])
	require.GreaterOrEqual(t, len(seats), rounds*perRound)
	fanID, _ := e.newFan()
	_, tokenA := e.gateSteward()
	_, tokenB := e.gateSteward()
	past := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)

	for round := 0; round < rounds; round++ {
		_, _, tickets := e.issued(ev.ID, fanID, seats[round*perRound:(round+1)*perRound]...)
		issuedLongAgo(t, ev.ID)

		// Device A holds the tickets in one order, device B in the reverse; which of the
		// two scanned a given ticket first alternates.
		var batchA, batchB []obj
		winner := map[string]any{}
		for i, tk := range tickets {
			a := scanOf(t, tk.ID(), ev.ID, "N-LOWER", past.Add(time.Duration(i%2)*time.Second))
			b := scanOf(t, tk.ID(), ev.ID, "N-LOWER", past.Add(time.Duration((i+1)%2)*time.Second))
			batchA = append(batchA, a)
			batchB = append([]obj{b}, batchB...)
			if i%2 == 0 {
				winner[tk.ID()] = a["client_scan_id"]
			} else {
				winner[tk.ID()] = b["client_scan_id"]
			}
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		recs := make([]*httptest.ResponseRecorder, 2)
		for i, up := range []struct {
			token, device string
			batch         []obj
		}{{tokenA, fmt.Sprintf("A-%d", round), batchA}, {tokenB, fmt.Sprintf("B-%d", round), batchB}} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				recs[i] = e.serve("POST", "/gate/scans", obj{"device_id": up.device, "scans": up.batch}, up.token)
			}()
		}
		close(start)
		wg.Wait()

		admittedInAnswers := map[string]int{}
		for _, rec := range recs {
			for _, r := range scanResults(t, rec) {
				if r.Result == "ADMITTED" {
					admittedInAnswers[r.TicketID]++
				}
			}
		}
		for _, tk := range tickets {
			require.Equal(t, 1, queryInt(t, `SELECT count(*) FROM scan_logs WHERE ticket_id = $1 AND result = 'ADMITTED'`, tk.ID()), "round %d: one admission stands", round)
			require.Equal(t, 1, queryInt(t, `SELECT count(*) FROM scan_logs WHERE ticket_id = $1 AND result = 'DUPLICATE'`, tk.ID()), "round %d", round)
			require.Equal(t, winner[tk.ID()], queryText(t, `SELECT client_scan_id::text FROM scan_logs WHERE ticket_id = $1 AND result = 'ADMITTED'`, tk.ID()), "round %d: the earlier scan", round)
			status, usedAt := ticketState(t, tk.ID())
			require.Equal(t, "USED", status)
			require.True(t, usedAt.Equal(past), "round %d: used_at is the earlier of the two scan times", round)
			// The batch that committed first may have been told ADMITTED for a scan the
			// second one then took; never the other way round.
			require.GreaterOrEqual(t, admittedInAnswers[tk.ID()], 1)
			require.LessOrEqual(t, admittedInAnswers[tk.ID()], 2)
		}
	}
	assert.NotContains(t, e.logs.String(), "deadlock")
}

// The same scan (one client_scan_id) sent twice at the same instant, as a device does when
// a request times out and it retries while the first is still running: one row, one answer.
func TestGateScan_TheSameBatchTwiceAtTheSameInstant(t *testing.T) {
	const rounds = 30
	e := newEnv(t)
	v := e.newVenue(2, 4, 20)
	ev := e.publishedEvent(v)
	seats := eventSeatIDs(t, ev.ID, v.BlockIDs[0])
	fanID, _ := e.newFan()
	_, token := e.gateSteward()
	past := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	for round := 0; round < rounds; round++ {
		_, _, tickets := e.issued(ev.ID, fanID, seats[round*2], seats[round*2+1])
		issuedLongAgo(t, ev.ID)
		batch := []obj{
			scanOf(t, tickets[0].ID(), ev.ID, "N-LOWER", past),
			scanOf(t, tickets[1].ID(), ev.ID, "N-LOWER", past),
			scanOf(t, newScanID(t), ev.ID, "N-LOWER", past), // an unknown ticket: no row lock to take turns at
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		recs := make([]*httptest.ResponseRecorder, 3)
		for i := range recs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				recs[i] = e.serve("POST", "/gate/scans", obj{"device_id": "phone-A", "scans": batch}, token)
			}()
		}
		close(start)
		wg.Wait()
		for _, rec := range recs {
			require.Equal(t, http.StatusOK, rec.Code, "round %d: %s", round, rec.Body.String())
			require.JSONEq(t, recs[0].Body.String(), rec.Body.String(), "round %d", round)
		}
		for _, sc := range batch {
			require.Equal(t, 1, queryInt(t, `SELECT count(*) FROM scan_logs WHERE client_scan_id = $1`, sc["client_scan_id"]), "round %d", round)
		}
	}
}

// A refund voids a booking's tickets while a device uploads scans of the same tickets in
// the opposite order. Both take the tickets in id order, so neither waits for the other in
// a circle; whichever goes first, the tickets end VOID.
func TestGateScan_UploadRacingARefundVoid_NoDeadlock(t *testing.T) {
	const rounds, perRound = 30, 4
	ctx := context.Background()
	e := newEnv(t)
	v := e.newVenue(2, 10, 20)
	ev := e.publishedEvent(v)
	seats := eventSeatIDs(t, ev.ID, v.BlockIDs[0])
	fanID, _ := e.newFan()
	_, token := e.gateSteward()
	past := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)

	for round := 0; round < rounds; round++ {
		bookingID, _, tickets := e.issued(ev.ID, fanID, seats[round*perRound:(round+1)*perRound]...)
		issuedLongAgo(t, ev.ID)
		ids := ticketIDs(tickets)
		slices.Sort(ids)
		slices.Reverse(ids)
		var batch []obj
		for _, id := range ids {
			batch = append(batch, scanOf(t, id, ev.ID, "N-LOWER", past))
		}

		start := make(chan struct{})
		var wg sync.WaitGroup
		var rec *httptest.ResponseRecorder
		var voidErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			rec = e.serve("POST", "/gate/scans", obj{"device_id": "phone-A", "scans": batch}, token)
		}()
		go func() {
			defer wg.Done()
			<-start
			// As the refund flow does: the event shared, the booking, then the tickets.
			voidErr = store.InTxRaw(ctx, db.Pool, func(tx pgx.Tx) error {
				if _, err := store.LockEventForHold(ctx, tx, ev.ID); err != nil {
					return err
				}
				if _, err := store.LockBookingForTickets(ctx, tx, bookingID); err != nil {
					return err
				}
				_, err := e.tickets.VoidForBooking(ctx, tx, bookingID)
				return err
			})
		}()
		close(start)
		wg.Wait()
		require.NoError(t, voidErr, "round %d", round)
		for _, r := range scanResults(t, rec) {
			require.Contains(t, []string{"ADMITTED", "INVALID"}, r.Result, "round %d: before the void, or after it", round)
		}
		require.Equal(t, perRound, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1 AND status = 'VOID'`, bookingID), "round %d", round)
	}
	assert.NotContains(t, e.logs.String(), "deadlock")
}

// An event cancel voids every ticket of the event in one statement, in no particular order,
// while holding the event exclusive. The upload takes the event (shared) before any ticket,
// so the two can only take turns.
func TestGateScan_UploadRacingAnEventCancel_NoDeadlock(t *testing.T) {
	const rounds = 8
	for round := 0; round < rounds; round++ {
		f := newGateFixture(t, 6, 6)
		e := f.e
		var batch []obj
		for _, tk := range append(append([]ticketing.Ticket{}, f.south...), f.north...) {
			batch = append(batch, scanOf(t, tk.ID(), f.ev.ID, tk.Row.Ticket.GateGroup, f.past))
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		var upload, cancel *httptest.ResponseRecorder
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			upload = e.serve("POST", "/gate/scans", obj{"device_id": "phone-A", "scans": batch}, f.token)
		}()
		go func() {
			defer wg.Done()
			<-start
			cancel = e.serve("POST", fmt.Sprintf("/admin/events/%d/cancel", f.ev.ID), nil, f.ev.Token)
		}()
		close(start)
		wg.Wait()
		require.Equal(t, http.StatusOK, cancel.Code, "round %d: %s", round, cancel.Body.String())
		results := scanResults(t, upload)
		first := results[0].Result
		require.Contains(t, []string{"ADMITTED", "INVALID"}, first)
		for _, r := range results {
			require.Equal(t, first, r.Result, "round %d: the whole batch was before the cancel, or the whole batch after it", round)
		}
		require.Zero(t, queryInt(t, `SELECT count(*) FROM tickets WHERE event_id = $1 AND status <> 'VOID'`, f.ev.ID), "round %d", round)
	}
}

// ---------------------------------------------------------------------------
// Manifest
// ---------------------------------------------------------------------------

func TestGateManifest_Full(t *testing.T) {
	f := newGateFixture(t, 3, 2)
	e := f.e
	// One used, one void: the full manifest carries V and U, and leaves the void one out.
	used, void := f.north[1], f.south[1]
	require.Equal(t, "ADMITTED", e.one(f.token, "gate-phone-01", scanOf(t, used.ID(), f.ev.ID, "N-LOWER", f.past)).Result)
	exec(t, `UPDATE tickets SET status = 'VOID' WHERE id = $1`, void.ID())

	m := e.manifest(f.token, f.ev.ID, "")
	assert.True(t, m.Full)
	assert.Equal(t, f.ev.ID, m.Event.ID)
	assert.Equal(t, "ON_SALE", m.Event.Status)
	assert.Equal(t, f.ev.Venue.Name, m.Event.VenueName)
	assert.NotEmpty(t, m.Version)
	assert.Equal(t, []string{"N-LOWER", "S-LOWER"}, m.GateGroups, "the venue's gate groups, sorted")
	assert.Equal(t, 4, m.TicketCount)
	assert.Equal(t, map[string]string{
		f.north[0].ID(): "V N-LOWER", used.ID(): "U N-LOWER", f.north[2].ID(): "V N-LOWER", f.south[0].ID(): "V S-LOWER",
	}, m.byTicket(t))

	// The key a device verifies with is the public half of the key that signed the tickets.
	require.Len(t, m.Keys, 1)
	assert.Equal(t, "k1", m.Keys[0].KeyID)
	assert.Equal(t, "Ed25519", m.Keys[0].Algorithm)
	public := ed25519.NewKeyFromSeed(e.ticketSeed).Public().(ed25519.PublicKey)
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(public), m.Keys[0].PublicKey)
	// A device built from docs/TICKET_FORMAT.md can check a real ticket with what it got.
	raw, err := base64.RawURLEncoding.DecodeString(m.Keys[0].PublicKey)
	require.NoError(t, err)
	verifier, err := ticketing.NewVerifier(ticketing.PublicKey{KeyID: m.Keys[0].KeyID, Key: raw})
	require.NoError(t, err)
	claims, err := verifier.Verify(f.north[0].QRToken())
	require.NoError(t, err)
	assert.Equal(t, f.north[0].ID(), claims.TicketUUID())
	assert.Contains(t, m.GateGroups, claims.GateGroup)

	// Draft and unknown events have no manifest; a bad id is a bad request.
	draft := e.readyEvent(e.newVenue(2, 1, 2))
	wantError(t, e.do("GET", fmt.Sprintf("/gate/events/%d/manifest", draft.ID), nil, f.token), http.StatusNotFound, domain.CodeNotFound)
	wantError(t, e.do("GET", "/gate/events/999999999999/manifest", nil, f.token), http.StatusNotFound, domain.CodeNotFound)
	wantFields(t, e.do("GET", "/gate/events/zero/manifest", nil, f.token), "eventId")
	wantFields(t, e.do("GET", fmt.Sprintf("/gate/events/%d/manifest?since=", f.ev.ID), nil, f.token), "since")
	wantFields(t, e.do("GET", fmt.Sprintf("/gate/events/%d/manifest?since=%s", f.ev.ID, strings.Repeat("A", 129)), nil, f.token), "since")
}

func TestGateManifest_SinceReturnsOnlyWhatChanged(t *testing.T) {
	f := newGateFixture(t, 4, 2)
	e := f.e
	ageTickets(t, f.ev.ID, 10*time.Minute)

	full := e.manifest(f.token, f.ev.ID, "")
	require.True(t, full.Full)
	require.Len(t, full.Tickets, 6)

	// Nothing has changed.
	delta := e.manifest(f.token, f.ev.ID, full.Version)
	assert.False(t, delta.Full)
	assert.Empty(t, delta.Tickets)
	assert.Equal(t, 6, delta.TicketCount)
	assert.Equal(t, full.GateGroups, delta.GateGroups, "the whole table, also with nothing to point into it")
	require.Len(t, delta.Keys, 1)
	assert.NotEqual(t, full.Version, delta.Version)

	// One scanned, one voided by a refund, one transferred, one new.
	used, void, transferred := f.north[0], f.north[1], f.south[0]
	require.Equal(t, "ADMITTED", e.one(f.token, "gate-phone-01", scanOf(t, used.ID(), f.ev.ID, "N-LOWER", f.past)).Result)
	exec(t, `UPDATE tickets SET status = 'VOID' WHERE id = $1`, void.ID())
	exec(t, `UPDATE tickets SET status = 'TRANSFERRED' WHERE id = $1`, transferred.ID())
	fanID, _ := e.newFan()
	_, _, fresh := e.issued(f.ev.ID, fanID, eventSeatIDs(t, f.ev.ID, f.ev.Venue.BlockIDs[1])[5])

	next := e.manifest(f.token, f.ev.ID, delta.Version)
	assert.False(t, next.Full)
	assert.Equal(t, map[string]string{
		used.ID(): "U N-LOWER", void.ID(): "X N-LOWER", transferred.ID(): "T S-LOWER", fresh[0].ID(): "V S-LOWER",
	}, next.byTicket(t), "what changed, in any status, and nothing else")
	assert.Equal(t, 5, next.TicketCount, "6, less the void and the transferred one, plus the new one")

	// Applying the changes to the stored set gives exactly the full manifest of now.
	device := full.byTicket(t)
	for id, v := range next.byTicket(t) {
		if strings.HasPrefix(v, "X") || strings.HasPrefix(v, "T") {
			delete(device, id)
			continue
		}
		device[id] = v
	}
	now := e.manifest(f.token, f.ev.ID, "")
	assert.Equal(t, now.byTicket(t), device)
	assert.Len(t, device, next.TicketCount, "ticket_count is the size of the set after applying the answer")

	// The same version asked again gives the same changes: a change is a state, not an event.
	assert.Equal(t, next.byTicket(t), e.manifest(f.token, f.ev.ID, delta.Version).byTicket(t))
	// A fresh version still carries them for the length of the overlap, by design.
	assert.Equal(t, next.byTicket(t), e.manifest(f.token, f.ev.ID, next.Version).byTicket(t))
	// Once they are older than the overlap they are gone from the changes.
	ageTickets(t, f.ev.ID, 10*time.Minute)
	assert.Empty(t, e.manifest(f.token, f.ev.ID, next.Version).Tickets)
}

// tickets.updated_at is stamped when the row is written, not when its transaction commits.
// Transaction 1 writes a ticket and stays open; transaction 2 writes another, later, and
// commits; a device downloads the manifest; then transaction 1 commits a stamp that is
// OLDER than the version the device now holds. The look-back is what delivers it.
func TestGateManifest_SinceDoesNotLoseAChangeCommittedOutOfOrder(t *testing.T) {
	ctx := context.Background()
	f := newGateFixture(t, 3, 0)
	e := f.e
	ageTickets(t, f.ev.ID, 10*time.Minute)
	slow, fast, untouched := f.north[0], f.north[1], f.north[2]

	tx1, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx1.Rollback(ctx) }()
	_, err = tx1.Exec(ctx, `UPDATE tickets SET status = 'VOID' WHERE id = $1`, slow.ID())
	require.NoError(t, err)

	tx2, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx2.Rollback(ctx) }()
	_, err = tx2.Exec(ctx, `UPDATE tickets SET status = 'USED', used_at = now() WHERE id = $1`, fast.ID())
	require.NoError(t, err)
	require.NoError(t, tx2.Commit(ctx))

	// The device syncs between the two commits. It sees transaction 2's ticket and, rightly,
	// not transaction 1's.
	held := e.manifest(f.token, f.ev.ID, "")
	assert.Equal(t, map[string]string{slow.ID(): "V N-LOWER", fast.ID(): "U N-LOWER", untouched.ID(): "V N-LOWER"}, held.byTicket(t))

	require.NoError(t, tx1.Commit(ctx))
	var slowStamp, versionTime time.Time
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT updated_at FROM tickets WHERE id = $1`, slow.ID()).Scan(&slowStamp))
	raw, err := base64.RawURLEncoding.DecodeString(held.Version)
	require.NoError(t, err)
	var eventID, micros int64
	_, err = fmt.Sscanf(string(raw), "g1.%d.%d", &eventID, &micros)
	require.NoError(t, err)
	versionTime = time.UnixMicro(micros)
	require.True(t, slowStamp.Before(versionTime), "the scenario is real: the late commit's stamp (%s) is older than the version the device holds (%s)", slowStamp, versionTime)

	delta := e.manifest(f.token, f.ev.ID, held.Version)
	require.False(t, delta.Full)
	got := delta.byTicket(t)
	assert.Equal(t, "X N-LOWER", got[slow.ID()], "the void that committed late reaches the device")
	assert.NotContains(t, got, untouched.ID())
	assert.Equal(t, 2, delta.TicketCount)
}

func TestGateManifest_UnusableVersionGetsTheFullManifest(t *testing.T) {
	f := newGateFixture(t, 2, 0)
	e := f.e
	ageTickets(t, f.ev.ID, 10*time.Minute)
	other := newGateFixture(t, 1, 0)
	current := e.manifest(f.token, f.ev.ID, "")
	foreign := other.e.manifest(other.token, other.ev.ID, "")
	version := func(format string, eventID int64, at time.Time) string {
		return base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("%s.%d.%d", format, eventID, at.UnixMicro())))
	}
	listed := ""
	for _, it := range items(want(t, e.do("GET", "/gate/events", nil, f.token), http.StatusOK)) {
		if id64(it.(obj)["event"].(obj)["id"]) == f.ev.ID {
			listed = it.(obj)["manifest_version"].(string)
		}
	}

	for name, since := range map[string]string{
		"not base64":                     "!!!",
		"base64 of nothing we wrote":     base64.RawURLEncoding.EncodeToString([]byte("hello")),
		"another event's version":        foreign.Version,
		"an old format":                  version("g0", f.ev.ID, time.Now()),
		"a seat map marker":              base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("m1.%d.1.%d", f.ev.ID, time.Now().UnixMicro()))),
		"from the future":                version("g1", f.ev.ID, time.Now().Add(time.Hour)),
		"older than seven days":          version("g1", f.ev.ID, time.Now().Add(-8*24*time.Hour)),
		"a negative time":                base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("g1.%d.-5", f.ev.ID))),
		"the list's change marker":       listed,
		"128 characters of nothing":      strings.Repeat("A", 128),
		"a version with padding":         current.Version + "=",
		"a version with a trailing part": base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("g1.%d.%d.9", f.ev.ID, time.Now().UnixMicro()))),
	} {
		t.Run(name, func(t *testing.T) {
			require.NotEmpty(t, since)
			m := e.manifest(f.token, f.ev.ID, since)
			assert.True(t, m.Full, "full: true")
			assert.Len(t, m.Tickets, 2)
			assert.Equal(t, 2, m.TicketCount)
		})
	}
	// Six days old is still answered with changes only.
	m := e.manifest(f.token, f.ev.ID, version("g1", f.ev.ID, time.Now().Add(-6*24*time.Hour)))
	assert.False(t, m.Full)
	assert.Len(t, m.Tickets, 2, "both tickets changed since then")
	// And the real one.
	assert.False(t, e.manifest(f.token, f.ev.ID, current.Version).Full)
}

// A cancelled event leaves the gate list, but its manifest still answers: the device that
// holds its tickets hears that every one is void.
func TestGateManifest_CancelledEventVoidsEverythingOnTheDevice(t *testing.T) {
	f := newGateFixture(t, 2, 1)
	e := f.e
	ageTickets(t, f.ev.ID, 10*time.Minute)
	held := e.manifest(f.token, f.ev.ID, "")
	require.Len(t, held.Tickets, 3)

	want(t, e.do("POST", fmt.Sprintf("/admin/events/%d/cancel", f.ev.ID), nil, f.ev.Token), http.StatusOK)

	delta := e.manifest(f.token, f.ev.ID, held.Version)
	assert.False(t, delta.Full)
	assert.Equal(t, "CANCELLED", delta.Event.Status)
	assert.Zero(t, delta.TicketCount)
	assert.Equal(t, map[string]string{f.north[0].ID(): "X N-LOWER", f.north[1].ID(): "X N-LOWER", f.south[0].ID(): "X S-LOWER"}, delta.byTicket(t))
	full := e.manifest(f.token, f.ev.ID, "")
	assert.True(t, full.Full)
	assert.Empty(t, full.Tickets)
	assert.Equal(t, []string{"N-LOWER", "S-LOWER"}, full.GateGroups)
}

// ---------------------------------------------------------------------------
// Gate events
// ---------------------------------------------------------------------------

type gateEventItemJSON struct {
	Event struct {
		ID        int64  `json:"id"`
		Title     string `json:"title"`
		Status    string `json:"status"`
		KickoffAt string `json:"kickoff_at"`
	} `json:"event"`
	GateGroups      []string `json:"gate_groups"`
	TicketCount     int      `json:"ticket_count"`
	ManifestVersion string   `json:"manifest_version"`
}

func (e *env) gateEvents(token string) map[int64]gateEventItemJSON {
	e.t.Helper()
	rec := e.do("GET", "/gate/events", nil, token)
	require.Equal(e.t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	out := map[int64]gateEventItemJSON{}
	var last string
	for _, it := range decode[page[gateEventItemJSON]](e.t, rec).Items {
		out[it.Event.ID] = it
		require.LessOrEqual(e.t, last, it.Event.KickoffAt, "soonest first")
		last = it.Event.KickoffAt
	}
	return out
}

func TestGateEvents(t *testing.T) {
	// The database is shared with every other test's events; lift the cap so this test's
	// own events are certainly on the list.
	f := newGateFixture(t, 2, 1, withGatePolicy(func(p *gate.Policy) { p.MaxEvents = 1_000_000 }))
	e := f.e
	draft := e.readyEvent(e.newVenue(2, 1, 2))
	cancelled := e.publishedEvent(e.newVenue(2, 1, 2))
	want(t, e.do("POST", fmt.Sprintf("/admin/events/%d/cancel", cancelled.ID), nil, cancelled.Token), http.StatusOK)
	playing := e.publishedEvent(e.newVenue(2, 1, 2))
	exec(t, `UPDATE events SET kickoff_at = now() - interval '2 hours', sales_close_at = now() - interval '2 hours', gates_open_at = now() - interval '5 hours' WHERE id = $1`, playing.ID)
	yesterday := e.publishedEvent(e.newVenue(2, 1, 2))
	exec(t, `UPDATE events SET kickoff_at = now() - interval '25 hours', sales_close_at = now() - interval '25 hours', gates_open_at = now() - interval '28 hours' WHERE id = $1`, yesterday.ID)
	soldOut := e.publishedEvent(e.newVenue(2, 1, 2))
	exec(t, `UPDATE events SET status = 'SOLD_OUT' WHERE id = $1`, soldOut.ID)

	list := e.gateEvents(f.token)
	require.Contains(t, list, f.ev.ID)
	mine := list[f.ev.ID]
	assert.Equal(t, "ON_SALE", mine.Event.Status)
	assert.Equal(t, []string{"N-LOWER", "S-LOWER"}, mine.GateGroups)
	assert.Equal(t, 3, mine.TicketCount)
	assert.NotEmpty(t, mine.ManifestVersion)

	assert.NotContains(t, list, draft.ID, "a draft")
	assert.NotContains(t, list, cancelled.ID, "a cancelled event")
	assert.NotContains(t, list, yesterday.ID, "kicked off more than a day ago")
	require.Contains(t, list, playing.ID, "kicked off two hours ago: the gates are busy")
	assert.Equal(t, "PUBLISHED", list[playing.ID].Event.Status, "sales have closed; it is still a match to scan for")
	require.Contains(t, list, soldOut.ID)
	assert.Equal(t, "SOLD_OUT", list[soldOut.ID].Event.Status)
	assert.Zero(t, list[soldOut.ID].TicketCount)

	// The change marker: the same while nothing changes, different after a scan, a void and
	// an issue, and never usable as a manifest version.
	assert.Equal(t, mine.ManifestVersion, e.gateEvents(f.token)[f.ev.ID].ManifestVersion)
	seen := map[string]bool{mine.ManifestVersion: true}
	changed := func(what string) {
		t.Helper()
		got := e.gateEvents(f.token)[f.ev.ID].ManifestVersion
		assert.False(t, seen[got], "the marker changes after %s", what)
		seen[got] = true
	}
	require.Equal(t, "ADMITTED", e.one(f.token, "gate-phone-01", scanOf(t, f.north[0].ID(), f.ev.ID, "N-LOWER", f.past)).Result)
	changed("a scan")
	exec(t, `UPDATE tickets SET status = 'VOID' WHERE id = $1`, f.north[1].ID())
	changed("a void")
	fanID, _ := e.newFan()
	e.issued(f.ev.ID, fanID, eventSeatIDs(t, f.ev.ID, f.ev.Venue.BlockIDs[0])[7])
	changed("an issue")
	assert.Equal(t, 3, e.gateEvents(f.token)[f.ev.ID].TicketCount, "3, less the void one, plus the new one")
}

// ---------------------------------------------------------------------------
// Who may call
// ---------------------------------------------------------------------------

func TestGateRoutes_OnlyGateStaffAndAdmin(t *testing.T) {
	f := newGateFixture(t, 1, 0)
	e, tk := f.e, f.north[0]
	_, fan := e.newFan()
	organiser := e.staffToken(domain.RoleOrganiser)
	admin := e.staffToken(domain.RoleAdmin)
	manifestPath := fmt.Sprintf("/gate/events/%d/manifest", f.ev.ID)
	calls := func(token string) []*httptest.ResponseRecorder {
		return []*httptest.ResponseRecorder{
			e.do("GET", "/gate/events", nil, token),
			e.do("GET", manifestPath, nil, token),
			e.uploadScans(token, "gate-phone-01", scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past)),
		}
	}
	for _, rec := range calls("") {
		wantError(t, rec, http.StatusUnauthorized, domain.CodeUnauthenticated)
	}
	for _, rec := range calls("not-a-token") {
		wantError(t, rec, http.StatusUnauthorized, domain.CodeUnauthenticated)
	}
	for name, token := range map[string]string{"fan": fan, "organiser": organiser} {
		for _, rec := range calls(token) {
			wantError(t, rec, http.StatusForbidden, domain.CodeForbidden)
			assert.NotContains(t, rec.Body.String(), tk.ID(), name)
		}
	}
	assert.Zero(t, queryInt(t, `SELECT count(*) FROM scan_logs WHERE event_id = $1`, f.ev.ID), "a refused caller's scan is not stored")
	status, _ := ticketState(t, tk.ID())
	assert.Equal(t, "VALID", status)
	// A path under /gate that does not exist says no more to a fan than a real one.
	assert.Equal(t, http.StatusForbidden, httptestDo(e, "GET", "/gate/secrets", fan).Code)

	for _, token := range []string{f.token, admin} {
		for _, rec := range calls(token) {
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Contains(t, rec.Header().Get("Cache-Control"), "no-store")
		}
	}
	// The admin's scan is recorded under the admin.
	assert.Equal(t, 2, queryInt(t, `SELECT count(DISTINCT scanned_by) FROM scan_logs WHERE event_id = $1`, f.ev.ID))
}

// ---------------------------------------------------------------------------
// The private key
// ---------------------------------------------------------------------------

func TestGate_PrivateKeyAppearsInNoGateResponseOrLog(t *testing.T) {
	// A second, retired key in the verify set, as after a rotation: the manifest then
	// carries two public keys and still nothing private.
	retiredRing, retiredSeed := newTicketKeys(t, "k1")
	ring, seed := newTicketKeys(t, "k2", ticketing.PublicKey{KeyID: "k1", Key: retiredRing.Signer.PublicKey()})
	f := newGateFixture(t, 2, 1, withTicketKeys(ring))
	e, tk := f.e, f.north[0]

	var out bytes.Buffer
	collect := func(rec *httptest.ResponseRecorder) {
		out.Write(rec.Body.Bytes())
		for k, vs := range rec.Header() {
			out.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
		}
	}
	manifestPath := fmt.Sprintf("/gate/events/%d/manifest", f.ev.ID)
	full := e.do("GET", manifestPath, nil, f.token)
	collect(full)
	version := decode[manifestJSON](t, full).Version
	collect(e.do("GET", manifestPath+"?since="+version, nil, f.token))
	collect(e.do("GET", manifestPath+"?since=garbage", nil, f.token))
	collect(e.do("GET", "/gate/events/999999999999/manifest", nil, f.token))
	collect(e.do("GET", manifestPath, nil, ""))
	collect(e.do("GET", "/gate/events", nil, f.token))
	collect(e.uploadScans(f.token, "gate-phone-01",
		with(scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past), "token", tk.QRToken()),
		with(scanOf(t, tk.ID(), f.ev.ID, "N-LOWER", f.past), "token", "TLT1.AAAA.BBBB"),
		with(scanOf(t, tk.ID(), f.ev.ID, "S-LOWER", f.past), "token", tk.QRToken()),
		scanOf(t, newScanID(t), f.ev.ID, "N-LOWER", f.past)))
	collect(e.uploadScans(f.token, "gate-phone-01", obj{"client_scan_id": "x"}))
	e.logger.Debug("service dump", "gate", fmt.Sprintf("%+v %#v", e.gate, e.gate))
	out.WriteString(fmt.Sprintf("%v %+v %#v", e.gate, e.gate, e.gate))

	haystack := out.String() + "\n" + e.logs.String()
	require.Contains(t, haystack, `"keys":[`, "the search covers a real manifest")
	require.Contains(t, haystack, `"msg":"gate scans uploaded"`, "and the gate's own log lines")

	for _, s := range [][]byte{seed, retiredSeed} {
		private := ed25519.NewKeyFromSeed(s)
		for _, secret := range [][]byte{s, private} {
			for name, spelling := range map[string]string{
				"raw bytes":        string(secret),
				"hex":              hex.EncodeToString(secret),
				"HEX":              strings.ToUpper(hex.EncodeToString(secret)),
				"base64":           base64.StdEncoding.EncodeToString(secret),
				"base64 raw":       base64.RawStdEncoding.EncodeToString(secret),
				"base64url":        base64.URLEncoding.EncodeToString(secret),
				"base64url raw":    base64.RawURLEncoding.EncodeToString(secret),
				"go byte slice":    fmt.Sprintf("%v", secret),
				"first half, hex":  hex.EncodeToString(secret[:16]),
				"second half, hex": hex.EncodeToString(secret[16:32]),
				"first half, b64":  base64.RawURLEncoding.EncodeToString(secret[:16])[:20],
			} {
				assert.NotContains(t, haystack, spelling, "a private key (%s, %d bytes) is in a gate response or a log line", name, len(secret))
			}
		}
	}
	// The public halves are there, and only in the manifest's keys.
	m := decode[manifestJSON](t, full)
	require.Len(t, m.Keys, 2)
	assert.Equal(t, "k1", m.Keys[0].KeyID)
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(retiredRing.Signer.PublicKey()), m.Keys[0].PublicKey)
	assert.Equal(t, "k2", m.Keys[1].KeyID)
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(ring.Signer.PublicKey()), m.Keys[1].PublicKey)
	// Every key of the manifest is exactly 32 bytes: there is no room for a private key
	// (64 bytes) or a seed sent by mistake under another name.
	var generic map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(full.Body.Bytes(), &generic))
	assert.ElementsMatch(t, []string{"event", "version", "full", "generated_at", "keys", "gate_groups", "ticket_count", "tickets"}, keysOf(generic))
	var keys []map[string]string
	require.NoError(t, json.Unmarshal(generic["keys"], &keys))
	for _, k := range keys {
		assert.ElementsMatch(t, []string{"key_id", "algorithm", "public_key"}, keysOf(k))
		raw, err := base64.RawURLEncoding.DecodeString(k["public_key"])
		require.NoError(t, err)
		assert.Len(t, raw, ed25519.PublicKeySize)
	}
	// Logs never carry a token either: it is a bearer credential for a seat.
	assert.NotContains(t, e.logs.String(), tk.QRToken())
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---------------------------------------------------------------------------
// End to end
// ---------------------------------------------------------------------------

// A ticket bought through the API (hold, booking, mock M-Pesa payment, callback) shows up in
// the manifest and is admitted at its gate with the QR text the fan was given.
func TestGate_TicketFromARealPaymentIsInTheManifestAndScansAdmitted(t *testing.T) {
	e := newEnv(t)
	ev, north, south := e.ticketEvent()
	_, steward := e.gateSteward()
	before := e.manifest(steward, ev.ID, "")
	require.Empty(t, before.Tickets)

	c, _ := e.paid(ev.ID, north[0], south[0])
	booking := e.getBooking(c.Token, c.Ref)
	require.Equal(t, "PAID", booking["status"])
	tickets := list(booking, "tickets")
	require.Len(t, tickets, 2)
	byGroup := map[string]obj{}
	for _, tk := range tickets {
		byGroup[tk.(obj)["location"].(obj)["gate_group"].(string)] = tk.(obj)
	}
	northTicket, southTicket := byGroup["N-LOWER"], byGroup["S-LOWER"]
	require.NotNil(t, northTicket)
	require.NotNil(t, southTicket)

	// The device's next sync picks both up (they were issued inside the overlap, so the
	// changes-only answer carries them whatever the version).
	delta := e.manifest(steward, ev.ID, before.Version)
	assert.False(t, delta.Full)
	assert.Equal(t, map[string]string{northTicket["id"].(string): "V N-LOWER", southTicket["id"].(string): "V S-LOWER"}, delta.byTicket(t))
	assert.Equal(t, 2, delta.TicketCount)

	// At the gate, now. (No backdating here: the scan is timed after the real issue.)
	token := northTicket["qr_token"].(string)
	scan := with(scanOf(t, northTicket["id"].(string), ev.ID, "N-LOWER", time.Now()), "token", token)
	res := e.one(steward, "gate-phone-01", scan)
	assert.Equal(t, "ADMITTED", res.Result)
	assert.False(t, res.Overridden)
	assert.Equal(t, "USED", *res.TicketStatus)

	// The fan's own view and the next manifest agree.
	mine := want(t, e.do("GET", "/tickets/"+northTicket["id"].(string), nil, c.Token), http.StatusOK)
	assert.Equal(t, "USED", mine["status"])
	assert.NotNil(t, mine["used_at"])
	after := e.manifest(steward, ev.ID, delta.Version).byTicket(t)
	assert.Equal(t, "U N-LOWER", after[northTicket["id"].(string)])
	assert.Equal(t, "V S-LOWER", after[southTicket["id"].(string)])

	// The second fan of the booking walks to the wrong stand, then the right one.
	wrong := with(scanOf(t, southTicket["id"].(string), ev.ID, "N-LOWER", time.Now()), "token", southTicket["qr_token"])
	assert.Equal(t, "WRONG_GATE", e.one(steward, "gate-phone-01", wrong).Result)
	right := with(scanOf(t, southTicket["id"].(string), ev.ID, "S-LOWER", time.Now()), "token", southTicket["qr_token"])
	assert.Equal(t, "ADMITTED", e.one(steward, "gate-phone-02", right).Result)

	// Scanning changed tickets only: seats, bookings, payments and their sums still agree.
	counts := ledger(t, ev.ID)
	assert.Equal(t, 2, counts.Sold)
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM tickets WHERE event_id = $1 AND status = 'USED'`, ev.ID))
}

// ---------------------------------------------------------------------------
// A full stadium
// ---------------------------------------------------------------------------

// The manifest of a sold-out Talanta: every seat of the seeded venue with a ticket, written
// set-based. Measures the full manifest (raw, gzipped, server time) and a small changes-only
// answer, and looks at the plans.
func TestSeededVenue_GateManifestOfAFullStadium(t *testing.T) {
	sv := seededTalanta(t)
	ctx := context.Background()
	e := newEnv(t)
	fanID, _ := e.newFan()
	_, steward := e.gateSteward()
	eventID := cloneSeededEvent(t, sv)
	sellable := queryInt(t, `SELECT count(*) FROM event_seats WHERE event_id = $1 AND status = 'AVAILABLE'`, eventID)
	require.Greater(t, sellable, 50_000, "the full venue")
	sellOut(t, eventID, fanID, sellable, 0)
	// sellOut writes one gate group for every ticket; give each the group of its own stand,
	// as issue does, and a token of the real length so row widths are the real ones.
	exec(t, `
		UPDATE tickets t SET gate_group = st.code || '-' || st.tier,
		       qr_token = 'TLT1.' || repeat('A', 68) || '.' || repeat('B', 86)
		FROM event_seats es JOIN blocks b ON b.id = es.block_id JOIN stands st ON st.id = b.stand_id
		WHERE t.event_id = $1 AND es.id = t.event_seat_id`, eventID)
	exec(t, `VACUUM (ANALYZE) tickets`)
	ageTickets(t, eventID, time.Hour)
	exec(t, `VACUUM (ANALYZE) tickets`)

	// fetch asks the way a phone does (gzip) through the whole router and returns the size
	// on the wire, the size unpacked and how long the server took.
	fetch := func(path string) (gz, raw int, took time.Duration, body []byte) {
		req := e.request("GET", path, nil, steward)
		req.Header.Set("Accept-Encoding", "gzip")
		rec := httptest.NewRecorder()
		start := time.Now()
		e.router.ServeHTTP(rec, req)
		took = time.Since(start)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.Equal(t, "gzip", rec.Header().Get("Content-Encoding"), path)
		gz = rec.Body.Len()
		zr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
		require.NoError(t, err)
		body, err = io.ReadAll(zr)
		require.NoError(t, err)
		return gz, len(body), took, body
	}
	path := fmt.Sprintf("/gate/events/%d/manifest", eventID)

	// Full, five times: the first run warms the cache. The service alone (query and
	// building the ticket list) is timed too, to tell it from JSON and gzip.
	var times, serviceTimes []time.Duration
	var gz, raw int
	var fullBody []byte
	for i := 0; i < 5; i++ {
		var took time.Duration
		gz, raw, took, fullBody = fetch(path)
		times = append(times, took)
		start := time.Now()
		m, err := e.gate.Manifest(ctx, eventID, "")
		require.NoError(t, err)
		require.Len(t, m.Tickets, sellable)
		serviceTimes = append(serviceTimes, time.Since(start))
	}
	slices.Sort(times)
	slices.Sort(serviceTimes)
	var full manifestJSON
	require.NoError(t, json.Unmarshal(fullBody, &full))
	{
		// One uncompressed answer through the contract check: 60,000 tuples of the right shape.
		req := e.request("GET", path, nil, steward)
		rec := httptest.NewRecorder()
		e.router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code)
		e.contract.Check(t, req, nil, rec)
	}
	require.True(t, full.Full)
	require.Len(t, full.Tickets, sellable)
	require.Equal(t, sellable, full.TicketCount)
	require.Greater(t, len(full.GateGroups), 2)
	groups := map[string]int{}
	for _, v := range full.byTicket(t) {
		groups[v]++
	}
	require.Equal(t, queryInt(t, `SELECT count(DISTINCT gate_group) FROM tickets WHERE event_id = $1`, eventID), len(groups))
	t.Logf("FULL MANIFEST: %d tickets, %d gate groups: raw %d bytes (%.2f MB), gzip %d bytes (%.2f MB, %.1f bytes a ticket); whole request with gzip min %s median %s max %s; of which query and building the list min %s median %s",
		sellable, len(full.GateGroups), raw, float64(raw)/1e6, gz, float64(gz)/1e6, float64(gz)/float64(sellable), times[0], times[2], times[4], serviceTimes[0], serviceTimes[2])
	assert.Less(t, gz, 2_000_000, "a full stadium stays under 2 MB on the wire")
	// Loose on purpose: under the race detector gzip alone takes seconds. The numbers that
	// are reported are from a run without it.
	assert.Less(t, times[2], 30*time.Second)

	// A small delta: 100 tickets scanned since the device's version.
	rows, err := db.Pool.Query(ctx, `SELECT id::text, gate_group FROM tickets WHERE event_id = $1 ORDER BY id LIMIT 100`, eventID)
	require.NoError(t, err)
	type idGroup struct{ ID, Group string }
	some, err := pgx.CollectRows(rows, pgx.RowToStructByPos[idGroup])
	require.NoError(t, err)
	batch := make([]obj, 0, len(some))
	for _, s := range some {
		batch = append(batch, scanOf(t, s.ID, eventID, s.Group, time.Now()))
	}
	uploadStart := time.Now()
	res := scanResults(t, e.uploadScans(steward, "gate-phone-01", batch...))
	uploadTook := time.Since(uploadStart)
	for _, r := range res {
		require.Equal(t, "ADMITTED", r.Result)
	}
	var deltaTimes []time.Duration
	var deltaGz, deltaRaw int
	for i := 0; i < 5; i++ {
		var took time.Duration
		deltaGz, deltaRaw, took, _ = fetch(path + "?since=" + full.Version)
		deltaTimes = append(deltaTimes, took)
	}
	slices.Sort(deltaTimes)
	delta := e.manifest(steward, eventID, full.Version)
	require.False(t, delta.Full)
	require.Len(t, delta.Tickets, 100)
	require.Equal(t, sellable, delta.TicketCount)
	t.Logf("DELTA (100 changed of %d): raw %d bytes, gzip %d bytes; whole request min %s median %s max %s; uploading the 100 scans took %s",
		sellable, deltaRaw, deltaGz, deltaTimes[0], deltaTimes[2], deltaTimes[4], uploadTook)
	assert.Less(t, deltaGz, 10_000)

	// Plans. The changes-only read must go through tickets_event_updated_idx, not over the
	// table; the full read and the count touch only this event's rows.
	plan := func(sql string, args ...any) string {
		rows, err := db.Pool.Query(ctx, "EXPLAIN (ANALYZE, BUFFERS, COSTS OFF, TIMING OFF) "+sql, args...)
		require.NoError(t, err)
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		require.NoError(t, err)
		return strings.Join(lines, "\n")
	}
	since := time.Now().Add(-3 * time.Minute)
	deltaPlan := plan(`SELECT id, gate_group, status FROM tickets WHERE event_id = $1 AND updated_at > $2`, eventID, since)
	t.Logf("PLAN, changes since:\n%s", deltaPlan)
	assert.Contains(t, deltaPlan, "tickets_event_updated_idx")
	assert.NotContains(t, deltaPlan, "Seq Scan")
	fullPlan := plan(`SELECT id, gate_group, status FROM tickets WHERE event_id = $1 AND status IN ('VALID', 'USED')`, eventID)
	t.Logf("PLAN, full manifest:\n%s", fullPlan)
	countPlan := plan(`SELECT count(*)::bigint FROM tickets WHERE event_id = $1 AND status IN ('VALID', 'USED')`, eventID)
	t.Logf("PLAN, ticket_count of a changes-only answer:\n%s", countPlan)
	total := queryInt(t, `SELECT count(*) FROM tickets`)
	t.Logf("tickets in the table: %d (this event: %d)", total, sellable)
	lockPlan := plan(`SELECT id, status, used_at FROM tickets WHERE id = ANY($1::uuid[]) ORDER BY id FOR UPDATE`, []string{some[0].ID, some[1].ID, some[2].ID})
	t.Logf("PLAN, locking a batch's tickets:\n%s", lockPlan)
	assert.NotContains(t, lockPlan, "Seq Scan")
	scanPlan := plan(`SELECT ticket_id, client_scan_id, scanned_at FROM scan_logs WHERE ticket_id = ANY($1::uuid[]) AND result = 'ADMITTED'`, []string{some[0].ID, some[1].ID})
	t.Logf("PLAN, admitted scans of tickets:\n%s", scanPlan)
}
