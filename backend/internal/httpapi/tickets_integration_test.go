//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/makiuchi-d/gozxing"
	zxingqr "github.com/makiuchi-d/gozxing/qrcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/notify"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
)

// m7TicketOperations is every operation of the ticket half of M7. The harness requires a
// contract-checked success answer and error answer for each.
var m7TicketOperations = []string{"listMyTickets", "getTicket", "getTicketQr", "getWebTicket"}

const testPublicBaseURL = "https://tickets.talanta.test"

// newTicketKeys makes a keyring around a fresh random key and returns its private seed.
func newTicketKeys(t testing.TB, keyID string, retired ...ticketing.PublicKey) (*ticketing.Keyring, []byte) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ticketing.NewSigner(keyID, private.Seed())
	require.NoError(t, err)
	ring, err := ticketing.NewKeyring(signer, retired...)
	require.NoError(t, err)
	return ring, private.Seed()
}

func withTicketKeys(k *ticketing.Keyring) envOption {
	return func(c *envConfig) { c.ticketKeys = k }
}

// ticketEvent is a published event on a venue of two blocks, one in stand N and one in
// stand S (both LOWER), three rows of ten seats each.
func (e *env) ticketEvent(mods ...func(obj)) (tEvent, []int64, []int64) {
	e.t.Helper()
	v := e.newVenue(2, 3, 10)
	ev := e.publishedEvent(v, mods...)
	return ev, eventSeatIDs(e.t, ev.ID, v.BlockIDs[0]), eventSeatIDs(e.t, ev.ID, v.BlockIDs[1])
}

// kickoffIn moves an event's kickoff (and the times that hang off it) to now + d.
func (e *env) kickoffIn(d time.Duration) func(obj) {
	return func(b obj) {
		kickoff := e.clock.Now().UTC().Truncate(time.Second).Add(d)
		b["kickoff_at"] = kickoff.Format(time.RFC3339)
		b["gates_open_at"] = kickoff.Add(-3 * time.Hour).Format(time.RFC3339)
		b["sales_close_at"] = kickoff.Format(time.RFC3339)
	}
}

// insertBookingOf writes a booking of the given seats at their categories' prices.
func insertBookingOf(t testing.TB, eventID, userID int64, status string, seatIDs ...int64) (int64, string) {
	t.Helper()
	var its []bookingItem
	for _, seat := range seatIDs {
		var it bookingItem
		it.SeatID = seat
		require.NoError(t, db.Pool.QueryRow(context.Background(),
			`SELECT es.category_id, c.price_cents FROM event_seats es JOIN event_categories c ON c.id = es.category_id WHERE es.id = $1`,
			seat).Scan(&it.CategoryID, &it.PriceCents))
		its = append(its, it)
	}
	now := time.Now()
	var paidAt *time.Time
	if status == "PAID" {
		paidAt = &now
	}
	return insertBooking(t, eventID, userID, status, now, paidAt, its...)
}

// paidBooking puts a booking in the state the payment transaction leaves it in just before
// it issues tickets: seats SOLD, booking PAID.
func paidBooking(t testing.TB, eventID, userID int64, seatIDs ...int64) (int64, string) {
	t.Helper()
	sellSeats(t, seatIDs...)
	return insertBookingOf(t, eventID, userID, "PAID", seatIDs...)
}

// issue runs IssueForBooking in its own transaction, as the payment flow will inside its.
func (e *env) issue(bookingID int64) ([]ticketing.Ticket, error) {
	var tickets []ticketing.Ticket
	err := store.InTxRaw(context.Background(), db.Pool, func(tx pgx.Tx) (err error) {
		tickets, err = e.tickets.IssueForBooking(context.Background(), tx, bookingID)
		return err
	})
	return tickets, err
}

func (e *env) issued(eventID, userID int64, seatIDs ...int64) (int64, string, []ticketing.Ticket) {
	e.t.Helper()
	id, ref := paidBooking(e.t, eventID, userID, seatIDs...)
	tickets, err := e.issue(id)
	require.NoError(e.t, err)
	require.Len(e.t, tickets, len(seatIDs))
	return id, ref, tickets
}

func ticketIDs(tickets []ticketing.Ticket) []string {
	out := make([]string, 0, len(tickets))
	for _, t := range tickets {
		out = append(out, t.ID())
	}
	return out
}

func queryText(t testing.TB, sql string, args ...any) string {
	t.Helper()
	var s string
	require.NoError(t, db.Pool.QueryRow(context.Background(), sql, args...).Scan(&s))
	return s
}

func webToken(t testing.TB, bookingID int64) string {
	return queryText(t, `SELECT web_token FROM bookings WHERE id = $1`, bookingID)
}

func sha256Base64(s string) string {
	sum := sha256.Sum256([]byte(s))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func decodeQR(t testing.TB, pngBytes []byte) string {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(pngBytes))
	require.NoError(t, err)
	bitmap, err := gozxing.NewBinaryBitmapFromImage(img)
	require.NoError(t, err)
	result, err := zxingqr.NewQRCodeReader().Decode(bitmap, nil)
	require.NoError(t, err)
	return result.GetText()
}

type ticketJSON struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Event  struct {
		ID        int64  `json:"id"`
		Title     string `json:"title"`
		KickoffAt string `json:"kickoff_at"`
		VenueName string `json:"venue_name"`
		Status    string `json:"status"`
	} `json:"event"`
	BookingReference string `json:"booking_reference"`
	Location         struct {
		StandCode  string `json:"stand_code"`
		StandName  string `json:"stand_name"`
		Tier       string `json:"tier"`
		GateGroup  string `json:"gate_group"`
		BlockCode  string `json:"block_code"`
		RowLabel   string `json:"row_label"`
		SeatNumber int    `json:"seat_number"`
		SeatLabel  string `json:"seat_label"`
	} `json:"location"`
	CategoryName string  `json:"category_name"`
	HolderName   *string `json:"holder_name"`
	HolderPhone  *string `json:"holder_phone"`
	QRToken      string  `json:"qr_token"`
	IssuedAt     string  `json:"issued_at"`
	UsedAt       *string `json:"used_at"`
	UpdatedAt    string  `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// Issue
// ---------------------------------------------------------------------------

func TestIssue_OneTicketPerItemWithTheRightGateGroup(t *testing.T) {
	e := newEnv(t)
	ev, north, south := e.ticketEvent()
	fanID, _ := e.newFan()
	exec(t, `UPDATE users SET name = 'Wanjiru Otieno' WHERE id = $1`, fanID)
	phone := queryText(t, `SELECT phone_e164 FROM users WHERE id = $1`, fanID)

	bookingID, ref, tickets := e.issued(ev.ID, fanID, north[0], north[11], south[5])

	require.Equal(t, 3, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, bookingID), "exactly one ticket per booking item")
	require.Equal(t, 3, queryInt(t, `SELECT count(DISTINCT event_seat_id) FROM tickets WHERE booking_id = $1`, bookingID))

	bySeat := map[int64]ticketing.Ticket{}
	nonces := map[string]bool{}
	for _, tk := range tickets {
		row := tk.Row.Ticket
		bySeat[row.EventSeatID] = tk
		nonces[hex.EncodeToString(row.TokenNonce)] = true

		assert.Equal(t, domain.TicketValid, tk.Status())
		assert.Equal(t, ev.ID, row.EventID)
		assert.Equal(t, bookingID, row.BookingID)
		assert.Equal(t, "k1", row.KeyID)
		assert.Equal(t, e.tickets.SigningKeyID(), row.KeyID)
		assert.Len(t, row.TokenNonce, ticketing.NonceSize)
		require.NotNil(t, row.HolderName)
		assert.Equal(t, "Wanjiru Otieno", *row.HolderName, "holder name from the booking's user")
		require.NotNil(t, row.HolderPhone)
		assert.Equal(t, phone, *row.HolderPhone)
		assert.Equal(t, ref, tk.Row.BookingReference)
		assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, tk.ID(), "a random (version 4) UUID")

		// The stored token is a real signature over exactly what the row says.
		assert.Len(t, tk.QRToken(), 160)
		claims, err := e.tickets.Verifier().Verify(tk.QRToken())
		require.NoError(t, err)
		assert.Equal(t, tk.ID(), claims.TicketUUID())
		assert.Equal(t, ev.ID, claims.EventID)
		assert.Equal(t, row.GateGroup, claims.GateGroup)
		assert.Equal(t, row.TokenNonce, claims.Nonce[:])
		assert.Equal(t, "k1", claims.KeyID)
		assert.NoError(t, claims.CheckEvent(ev.ID))
		assert.ErrorIs(t, claims.CheckEvent(ev.ID+1), ticketing.ErrWrongEvent)
	}
	assert.Len(t, nonces, 3, "every ticket has its own random nonce")

	// Gate group = the seat's stand code + tier. Block 1 is in stand N, block 2 in stand S.
	require.Len(t, bySeat, 3)
	assert.Equal(t, "N-LOWER", bySeat[north[0]].Row.Ticket.GateGroup)
	assert.Equal(t, "N-LOWER", bySeat[north[11]].Row.Ticket.GateGroup)
	assert.Equal(t, "S-LOWER", bySeat[south[5]].Row.Ticket.GateGroup)
	northClaims, err := e.tickets.Verifier().Verify(bySeat[north[0]].QRToken())
	require.NoError(t, err)
	assert.NoError(t, northClaims.CheckGate("N-LOWER"))
	assert.ErrorIs(t, northClaims.CheckGate("S-LOWER"), ticketing.ErrWrongGate, "a north ticket at a south gate")

	// What the row says is what the database stored.
	assert.Equal(t, 3, queryInt(t, `
		SELECT count(*) FROM tickets t
		JOIN event_seats es ON es.id = t.event_seat_id JOIN blocks bl ON bl.id = es.block_id JOIN stands st ON st.id = bl.stand_id
		WHERE t.booking_id = $1 AND t.gate_group = st.code || '-' || st.tier AND t.event_id = es.event_id
		  AND t.status = 'VALID' AND octet_length(t.token_nonce) = 8`, bookingID))
}

func TestIssue_TwiceReturnsTheSameTickets(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	fanID, _ := e.newFan()
	bookingID, _, first := e.issued(ev.ID, fanID, north[0], north[1])

	for i := 0; i < 3; i++ {
		again, err := e.issue(bookingID)
		require.NoError(t, err)
		assert.Equal(t, ticketIDs(first), ticketIDs(again), "the same tickets, in the same order")
		for j := range again {
			assert.Equal(t, first[j].QRToken(), again[j].QRToken(), "and the same signed text: nothing is signed twice")
		}
	}
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, bookingID), "never a second set")

	// Still the same answer, and still no second set, after the tickets were voided (a
	// refund) or the booking is no longer PAID.
	exec(t, `UPDATE tickets SET status = 'VOID' WHERE booking_id = $1`, bookingID)
	exec(t, `UPDATE bookings SET status = 'REFUND_REQUIRED' WHERE id = $1`, bookingID)
	again, err := e.issue(bookingID)
	require.NoError(t, err)
	assert.Equal(t, ticketIDs(first), ticketIDs(again))
	assert.Equal(t, domain.TicketVoid, again[0].Status())
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, bookingID))
}

// Two (here twelve) issues of one booking at the same moment, each in its own transaction:
// one set of tickets, and every caller gets that set.
func TestIssue_ConcurrentCallsMakeOneSet(t *testing.T) {
	e := newEnv(t)
	ev, north, south := e.ticketEvent()
	fanID, _ := e.newFan()

	const rounds, callers = 5, 12
	for round := 0; round < rounds; round++ {
		seats := []int64{north[round*3], north[round*3+1], south[round*3]}
		bookingID, _ := paidBooking(t, ev.ID, fanID, seats...)

		results := make([][]string, callers)
		errs := make([]error, callers)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				tickets, err := e.issue(bookingID)
				results[i], errs[i] = ticketIDs(tickets), err
			}()
		}
		close(start)
		wg.Wait()

		for i := 0; i < callers; i++ {
			require.NoError(t, errs[i], "round %d caller %d", round, i)
			require.Len(t, results[i], len(seats))
			assert.Equal(t, results[0], results[i], "round %d: every caller gets the one set", round)
		}
		assert.Equal(t, len(seats), queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, bookingID), "round %d", round)
		assert.Equal(t, len(seats), queryInt(t, `SELECT count(DISTINCT event_seat_id) FROM tickets WHERE booking_id = $1`, bookingID))
	}
	assert.Equal(t, rounds*3, queryInt(t, `SELECT count(*) FROM tickets WHERE event_id = $1`, ev.ID))
}

func TestIssue_RefusedUnlessThePreconditionHolds(t *testing.T) {
	e := newEnv(t)
	ev, north, south := e.ticketEvent()
	fanID, _ := e.newFan()
	noTickets := func(bookingID int64) {
		t.Helper()
		assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, bookingID))
	}

	t.Run("no such booking", func(t *testing.T) {
		_, err := e.issue(1 << 40)
		assert.ErrorIs(t, err, ticketing.ErrBookingNotFound)
	})

	for i, status := range []string{"PENDING_PAYMENT", "FAILED", "EXPIRED", "CANCELLED", "REFUND_REQUIRED", "REFUNDED"} {
		t.Run("booking is "+status, func(t *testing.T) {
			sellSeats(t, north[i])
			bookingID, _ := insertBookingOf(t, ev.ID, fanID, status, north[i])
			_, err := e.issue(bookingID)
			require.ErrorIs(t, err, ticketing.ErrBookingNotPaid)
			assert.NotErrorIs(t, err, ticketing.ErrSeatsNotSold)
			noTickets(bookingID)
		})
	}

	t.Run("a seat is still only held", func(t *testing.T) {
		holdID := holdSeats(t, ev.ID, fanID, time.Now().Add(10*time.Minute), north[10], north[11])
		bookingID, _ := insertBookingOf(t, ev.ID, fanID, "PAID", north[10], north[11])
		exec(t, `UPDATE bookings SET hold_id = $1 WHERE id = $2`, holdID, bookingID)
		// One of the two is sold, the other not: still refused, and nothing is issued for
		// the one that is.
		exec(t, `UPDATE event_seats SET status = 'SOLD', hold_expires_at = NULL WHERE id = $1`, north[10])
		_, err := e.issue(bookingID)
		require.ErrorIs(t, err, ticketing.ErrSeatsNotSold)
		noTickets(bookingID)
	})

	t.Run("a seat is available", func(t *testing.T) {
		bookingID, _ := insertBookingOf(t, ev.ID, fanID, "PAID", north[12])
		_, err := e.issue(bookingID)
		require.ErrorIs(t, err, ticketing.ErrSeatsNotSold)
		noTickets(bookingID)
	})

	t.Run("a seat was sold through someone else's hold", func(t *testing.T) {
		otherFan, _ := e.newFan()
		theirHold := holdSeats(t, ev.ID, otherFan, time.Now().Add(10*time.Minute), north[13])
		exec(t, `UPDATE event_seats SET status = 'SOLD', hold_expires_at = NULL WHERE id = $1`, north[13])
		myHold := holdSeats(t, ev.ID, fanID, time.Now().Add(10*time.Minute), north[14])
		exec(t, `UPDATE event_seats SET status = 'SOLD', hold_expires_at = NULL WHERE id = $1`, north[14])
		bookingID, _ := insertBookingOf(t, ev.ID, fanID, "PAID", north[13], north[14])
		exec(t, `UPDATE bookings SET hold_id = $1 WHERE id = $2`, myHold, bookingID)
		require.NotEqual(t, theirHold, myHold)
		_, err := e.issue(bookingID)
		require.ErrorIs(t, err, ticketing.ErrSeatsNotSold)
		noTickets(bookingID)
	})

	t.Run("a seat already has a live ticket of another booking", func(t *testing.T) {
		_, _, _ = e.issued(ev.ID, fanID, south[0])
		second, _ := insertBookingOf(t, ev.ID, fanID, "PAID", south[0], south[1])
		sellSeats(t, south[1])
		_, err := e.issue(second)
		require.ErrorIs(t, err, ticketing.ErrSeatAlreadyTicketed)
		noTickets(second)
		assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM tickets WHERE event_seat_id = $1 AND status = 'VALID'`, south[0]), "the seat keeps its one live ticket")
	})

	t.Run("booking has no items", func(t *testing.T) {
		bookingID, _ := insertBooking(t, ev.ID, fanID, "PAID", time.Now(), nil)
		_, err := e.issue(bookingID)
		require.ErrorIs(t, err, ticketing.ErrBookingEmpty)
	})

	// A refusal leaves the caller's transaction usable: the payment flow can then mark the
	// booking REFUND_REQUIRED in the same transaction and commit that.
	t.Run("the caller's transaction survives a refusal", func(t *testing.T) {
		_, _, _ = e.issued(ev.ID, fanID, south[10])
		second, _ := insertBookingOf(t, ev.ID, fanID, "PAID", south[10])
		err := store.InTxRaw(context.Background(), db.Pool, func(tx pgx.Tx) error {
			_, err := e.tickets.IssueForBooking(context.Background(), tx, second)
			require.ErrorIs(t, err, ticketing.ErrSeatAlreadyTicketed, "a unique violation, which aborts a transaction that has no savepoint")
			_, err = tx.Exec(context.Background(), `UPDATE bookings SET status = 'REFUND_REQUIRED' WHERE id = $1`, second)
			return err
		})
		require.NoError(t, err)
		assert.Equal(t, "REFUND_REQUIRED", queryText(t, `SELECT status FROM bookings WHERE id = $1`, second))
		noTickets(second)
	})

	t.Run("event is cancelled", func(t *testing.T) {
		ev2, seats, _ := e.ticketEvent()
		bookingID, _ := paidBooking(t, ev2.ID, fanID, seats[0])
		// The booking is left PAID on purpose: the event's status alone must stop the issue.
		exec(t, `UPDATE events SET status = 'CANCELLED' WHERE id = $1`, ev2.ID)
		_, err := e.issue(bookingID)
		require.ErrorIs(t, err, ticketing.ErrEventCancelled)
		noTickets(bookingID)
	})
}

// The whole payment-success transaction in the order the payment flow will run it, with a
// real hold made through the API: event shared, hold locked, booking PAID, hold CONVERTED,
// seats sold, tickets issued, commit.
func TestIssue_InsideAPaymentShapedTransaction(t *testing.T) {
	e := newEnv(t)
	ev, north, south := e.ticketEvent()
	fanID, fanToken := e.newFan()
	ctx := context.Background()

	seats := []int64{north[3], north[4], south[7]}
	h := want(t, e.hold(fanToken, ev.ID, seats...), http.StatusCreated)
	holdID := id64(h["id"])
	bookingID, _ := insertBookingOf(t, ev.ID, fanID, "PENDING_PAYMENT", seats...)
	exec(t, `UPDATE bookings SET hold_id = $1 WHERE id = $2`, holdID, bookingID)

	var tickets []ticketing.Ticket
	var issueTook time.Duration
	err := store.InTxRaw(ctx, db.Pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if _, err := store.LockEventOfHoldWithin(ctx, tx, holdID, 3*time.Second); err != nil {
			return err
		}
		if _, err := store.LockHold(ctx, tx, holdID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE bookings SET status = 'PAID', paid_at = clock_timestamp() WHERE id = $1 AND status = 'PENDING_PAYMENT'`, bookingID); err != nil {
			return err
		}
		if n, err := q.SetHoldStatus(ctx, store.SetHoldStatusParams{ID: holdID, Status: string(domain.HoldConverted)}); err != nil || n != 1 {
			return fmt.Errorf("convert hold: %d rows, %w", n, err)
		}
		sold, err := store.SellSeatsOfHold(ctx, tx, holdID, len(seats))
		if err != nil || sold.Sold != len(seats) {
			return fmt.Errorf("sell seats: %+v, %w", sold, err)
		}
		started := time.Now()
		tickets, err = e.tickets.IssueForBooking(ctx, tx, bookingID)
		issueTook = time.Since(started)
		return err
	})
	require.NoError(t, err)
	require.Len(t, tickets, 3)
	t.Logf("IssueForBooking for %d seats took %s inside the payment transaction", len(seats), issueTook)

	assert.Equal(t, 3, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1 AND status = 'VALID'`, bookingID))
	assert.Equal(t, 3, queryInt(t, `SELECT count(*) FROM event_seats WHERE id = ANY($1) AND status = 'SOLD' AND hold_id = $2`, seats, holdID))

	// The fan sees them through the API, each with a token that verifies.
	page := decode[page[ticketJSON]](t, e.do("GET", "/me/tickets", nil, fanToken))
	require.Len(t, page.Items, 3)
	for _, it := range page.Items {
		_, err := e.tickets.Verifier().Verify(it.QRToken)
		require.NoError(t, err)
	}

	// A payment that rolls back leaves no ticket behind: issue is part of the transaction.
	seats2 := []int64{north[20], north[21]}
	h2 := want(t, e.hold(fanToken, ev.ID, seats2...), http.StatusCreated)
	booking2, _ := insertBookingOf(t, ev.ID, fanID, "PENDING_PAYMENT", seats2...)
	exec(t, `UPDATE bookings SET hold_id = $1 WHERE id = $2`, id64(h2["id"]), booking2)
	boom := errors.New("the payment transaction failed after issuing")
	err = store.InTxRaw(ctx, db.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE bookings SET status = 'PAID' WHERE id = $1`, booking2); err != nil {
			return err
		}
		if _, err := store.SellSeatsOfHold(ctx, tx, id64(h2["id"]), 2); err != nil {
			return err
		}
		issued, err := e.tickets.IssueForBooking(ctx, tx, booking2)
		require.NoError(t, err)
		require.Len(t, issued, 2)
		return boom
	})
	require.ErrorIs(t, err, boom)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1`, booking2), "rolled back with the payment")
	assert.Equal(t, "PENDING_PAYMENT", queryText(t, `SELECT status FROM bookings WHERE id = $1`, booking2))
}

// The most one booking holds is 50 seats. Issue runs after the seats are written SOLD (the
// seat map's poll goes by that write's time), so how long it takes is worth knowing.
func TestIssue_FiftySeats(t *testing.T) {
	e := newEnv(t)
	ev, north, south := e.ticketEvent()
	fanID, _ := e.newFan()
	seats := append(append([]int64{}, north[:30]...), south[:20]...)
	bookingID, _ := paidBooking(t, ev.ID, fanID, seats...)

	// Once before, on another booking, so the timing below is not the first-use cost of
	// preparing the statements.
	_, _, _ = e.issued(ev.ID, fanID, south[25])

	started := time.Now()
	tickets, err := e.issue(bookingID)
	took := time.Since(started)
	require.NoError(t, err)
	require.Len(t, tickets, 50)
	t.Logf("IssueForBooking for 50 seats (begin, issue, commit) took %s", took)
	assert.Less(t, took, 5*time.Second)

	groups := map[string]int{}
	seen := map[string]bool{}
	for _, tk := range tickets {
		groups[tk.Row.Ticket.GateGroup]++
		seen[tk.QRToken()] = true
		_, err := e.tickets.Verifier().Verify(tk.QRToken())
		require.NoError(t, err)
	}
	assert.Equal(t, map[string]int{"N-LOWER": 30, "S-LOWER": 20}, groups)
	assert.Len(t, seen, 50, "fifty different tokens")
}

// A payment transaction holds the event shared. An organiser's cancel that arrives meanwhile
// queues behind it. Issue re-takes the event's row lock; that must not queue behind the
// waiting cancel (which would be a deadlock: the cancel waits for this transaction).
func TestIssue_DoesNotWaitBehindAQueuedCancel(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	fanID, _ := e.newFan()
	bookingID, _ := paidBooking(t, ev.ID, fanID, north[0], north[1])
	ctx := context.Background()

	tx, err := db.Pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = store.LockEventForHoldWithin(ctx, tx, ev.ID, time.Second)
	require.NoError(t, err)

	waitingBefore := queryInt(t, `SELECT count(*) FROM pg_locks WHERE NOT granted`)
	cancelled := make(chan error, 1)
	go func() {
		_, err := e.events.Cancel(ctx, ev.ID)
		cancelled <- err
	}()
	require.Eventually(t, func() bool {
		return queryInt(t, `SELECT count(*) FROM pg_locks WHERE NOT granted`) > waitingBefore
	}, 5*time.Second, 20*time.Millisecond, "the cancel should be waiting for the event")

	issueCtx, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	started := time.Now()
	tickets, err := e.tickets.IssueForBooking(issueCtx, tx, bookingID)
	require.NoError(t, err, "issue must not wait for the cancel that is waiting for it")
	require.Len(t, tickets, 2)
	assert.Less(t, time.Since(started), 2*time.Second)
	require.NoError(t, tx.Commit(ctx))

	// The cancel then runs and voids the tickets that were just issued: none is left valid
	// for an event that is off.
	require.NoError(t, <-cancelled)
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1 AND status = 'VOID'`, bookingID))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM tickets WHERE event_id = $1 AND status IN ('VALID', 'USED')`, ev.ID))
}

func TestVoidForBooking(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	fanID, _ := e.newFan()
	bookingID, _, tickets := e.issued(ev.ID, fanID, north[0], north[1], north[2])
	otherBooking, _, _ := e.issued(ev.ID, fanID, north[5])
	ctx := context.Background()

	// One of the three was already scanned in: a refund voids it too.
	exec(t, `UPDATE tickets SET status = 'USED', used_at = now() WHERE id = $1`, tickets[0].ID())

	void := func(id int64) (n int) {
		require.NoError(t, store.InTxRaw(ctx, db.Pool, func(tx pgx.Tx) (err error) {
			n, err = e.tickets.VoidForBooking(ctx, tx, id)
			return err
		}))
		return n
	}
	assert.Equal(t, 3, void(bookingID))
	assert.Equal(t, 3, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1 AND status = 'VOID'`, bookingID))
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM tickets WHERE booking_id = $1 AND status = 'VALID'`, otherBooking), "another booking's ticket is untouched")
	assert.Equal(t, 0, void(bookingID), "a second void changes nothing")

	// A void ticket no longer occupies its seat: the seat can be ticketed for a new booking,
	// and the old one never comes back.
	newBooking, _ := insertBookingOf(t, ev.ID, fanID, "PAID", north[0])
	reissued, err := e.issue(newBooking)
	require.NoError(t, err)
	require.Len(t, reissued, 1)
	assert.NotEqual(t, tickets[0].ID(), reissued[0].ID())
	assert.NotEqual(t, tickets[0].QRToken(), reissued[0].QRToken())
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM tickets WHERE event_seat_id = $1 AND status = 'VALID'`, north[0]))

	// Event cancel voids through the same status rule.
	assert.Equal(t, []string{"VALID", "USED"}, domain.TicketStatusesThatCanBecome(domain.TicketVoid))
	exec(t, `UPDATE tickets SET status = 'USED', used_at = now() WHERE booking_id = $1`, otherBooking)
	o := want(t, e.do("POST", fmt.Sprintf("/admin/events/%d/cancel", ev.ID), nil, ev.Token), http.StatusOK)
	assert.EqualValues(t, 2, o["tickets_voided"], "the reissued VALID one and the USED one; the three already VOID are not counted again")
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM tickets WHERE event_id = $1 AND status <> 'VOID'`, ev.ID))
}

// ---------------------------------------------------------------------------
// GET /me/tickets, GET /tickets/{ticketId}
// ---------------------------------------------------------------------------

func TestListMyTickets(t *testing.T) {
	e := newEnv(t)
	later, laterNorth, laterSouth := e.ticketEvent() // kickoff in 30 days
	sooner, soonerNorth, _ := e.ticketEvent(e.kickoffIn(10 * 24 * time.Hour))
	fanID, token := e.newFan()
	otherID, otherToken := e.newFan()
	exec(t, `UPDATE users SET name = 'Achieng' WHERE id = $1`, fanID)

	_, laterRef, _ := e.issued(later.ID, fanID, laterNorth[2], laterNorth[0], laterSouth[4])
	_, _, soonTickets := e.issued(sooner.ID, fanID, soonerNorth[1], soonerNorth[0])
	_, _, _ = e.issued(later.ID, otherID, laterNorth[9])

	rec := e.do("GET", "/me/tickets", nil, token)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	all := decode[page[ticketJSON]](t, rec)
	require.Len(t, all.Items, 5, "only the caller's own tickets")
	assert.Nil(t, all.NextCursor)

	// Soonest kickoff first, then seat.
	var gotEvents []int64
	for _, it := range all.Items {
		gotEvents = append(gotEvents, it.Event.ID)
	}
	assert.Equal(t, []int64{sooner.ID, sooner.ID, later.ID, later.ID, later.ID}, gotEvents)
	assert.Equal(t, 1, all.Items[0].Location.SeatNumber)
	assert.Equal(t, 2, all.Items[1].Location.SeatNumber)

	// Everything the app needs to show a ticket with no network.
	first := all.Items[2]
	assert.Equal(t, "VALID", first.Status)
	assert.Equal(t, laterRef, first.BookingReference)
	assert.Equal(t, later.Venue.Name, first.Event.VenueName)
	assert.Equal(t, "ON_SALE", first.Event.Status, "the effective status, as everywhere else")
	assert.Equal(t, "N", first.Location.StandCode)
	assert.Equal(t, "N-LOWER", first.Location.GateGroup)
	assert.Equal(t, "B1", first.Location.BlockCode)
	assert.Equal(t, "A", first.Location.RowLabel)
	assert.Equal(t, "A1", first.Location.SeatLabel)
	assert.Equal(t, "VIP", first.CategoryName, "the front row of the first block is VIP")
	assert.Equal(t, "Regular", all.Items[4].CategoryName)
	assert.Equal(t, "S-LOWER", all.Items[4].Location.GateGroup)
	require.NotNil(t, first.HolderName)
	assert.Equal(t, "Achieng", *first.HolderName)
	require.NotNil(t, first.HolderPhone)
	assert.Nil(t, first.UsedAt)
	claims, err := e.tickets.Verifier().Verify(first.QRToken)
	require.NoError(t, err)
	assert.Equal(t, first.ID, claims.TicketUUID())
	assert.Equal(t, first.Location.GateGroup, claims.GateGroup, "the gate shown is the gate signed")

	// Paging: 2 + 2 + 1, no ticket twice, none missed, same order.
	var paged []string
	path := "/me/tickets?limit=2"
	for pages := 0; ; pages++ {
		require.Less(t, pages, 4)
		p := decode[page[ticketJSON]](t, e.do("GET", path, nil, token))
		for _, it := range p.Items {
			paged = append(paged, it.ID)
		}
		if p.NextCursor == nil {
			assert.Len(t, p.Items, 1, "the last page")
			break
		}
		assert.Len(t, p.Items, 2)
		path = "/me/tickets?limit=2&cursor=" + *p.NextCursor
	}
	var allIDs []string
	for _, it := range all.Items {
		allIDs = append(allIDs, it.ID)
	}
	assert.Equal(t, allIDs, paged)

	// A page that ends exactly at the last ticket has no next page.
	exact := decode[page[ticketJSON]](t, e.do("GET", "/me/tickets?limit=5", nil, token))
	assert.Len(t, exact.Items, 5)
	assert.Nil(t, exact.NextCursor)

	// Filters.
	byEvent := decode[page[ticketJSON]](t, e.do("GET", fmt.Sprintf("/me/tickets?event_id=%d", sooner.ID), nil, token))
	assert.Len(t, byEvent.Items, 2)
	exec(t, `UPDATE tickets SET status = 'VOID' WHERE id = $1`, soonTickets[0].ID())
	exec(t, `UPDATE tickets SET status = 'USED', used_at = now() WHERE id = $1`, soonTickets[1].ID())
	void := decode[page[ticketJSON]](t, e.do("GET", "/me/tickets?status=VOID", nil, token))
	require.Len(t, void.Items, 1)
	assert.Equal(t, soonTickets[0].ID(), void.Items[0].ID)
	used := decode[page[ticketJSON]](t, e.do("GET", fmt.Sprintf("/me/tickets?status=USED&event_id=%d", sooner.ID), nil, token))
	require.Len(t, used.Items, 1)
	assert.NotNil(t, used.Items[0].UsedAt)
	valid := decode[page[ticketJSON]](t, e.do("GET", "/me/tickets?status=VALID", nil, token))
	assert.Len(t, valid.Items, 3)
	none := decode[page[ticketJSON]](t, e.do("GET", "/me/tickets?status=TRANSFERRED", nil, token))
	assert.Empty(t, none.Items)
	assert.NotNil(t, none.Items, "an empty list is [], not null")

	// The other fan sees one ticket; a fan with none sees an empty page.
	theirs := decode[page[ticketJSON]](t, e.do("GET", "/me/tickets", nil, otherToken))
	assert.Len(t, theirs.Items, 1)
	_, nobody := e.newFan()
	assert.Empty(t, decode[page[ticketJSON]](t, e.do("GET", "/me/tickets", nil, nobody)).Items)

	// Errors.
	wantError(t, e.do("GET", "/me/tickets", nil, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
	wantError(t, e.do("GET", "/me/tickets", nil, e.staffToken(domain.RoleOrganiser)), http.StatusForbidden, domain.CodeForbidden)
	wantFields(t, e.do("GET", "/me/tickets?limit=0", nil, token), "limit")
	wantFields(t, e.do("GET", "/me/tickets?limit=101", nil, token), "limit")
	wantFields(t, e.do("GET", "/me/tickets?status=LOST", nil, token), "status")
	wantFields(t, e.do("GET", "/me/tickets?event_id=abc", nil, token), "event_id")
	wantFields(t, e.do("GET", "/me/tickets?cursor=not-a-cursor!", nil, token), "cursor")
	wantFields(t, e.do("GET", "/me/tickets?cursor="+base64.RawURLEncoding.EncodeToString([]byte(`{"k":"2026-01-01T00:00:00Z","e":1,"s":1,"t":"nope"}`)), nil, token), "cursor")
}

func TestGetTicket_OwnerOnly(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	ownerID, ownerToken := e.newFan()
	_, strangerToken := e.newFan()
	_, _, tickets := e.issued(ev.ID, ownerID, north[0])
	path := "/tickets/" + tickets[0].ID()

	rec := e.do("GET", path, nil, ownerToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	got := decode[ticketJSON](t, rec)
	assert.Equal(t, tickets[0].ID(), got.ID)
	assert.Equal(t, tickets[0].QRToken(), got.QRToken)
	assert.Equal(t, ev.ID, got.Event.ID)

	// The upper-case spelling of the same UUID is the same ticket.
	assert.Equal(t, http.StatusOK, e.do("GET", "/tickets/"+strings.ToUpper(tickets[0].ID()), nil, ownerToken).Code)

	// Someone else's ticket and a ticket that does not exist are the same answer.
	strangers := wantError(t, e.do("GET", path, nil, strangerToken), http.StatusNotFound, domain.CodeNotFound)
	unknown := wantError(t, e.do("GET", "/tickets/5e2d9c1a-3b4f-4a6d-8e7f-1a2b3c4d5e6f", nil, strangerToken), http.StatusNotFound, domain.CodeNotFound)
	assert.Equal(t, unknown, strangers)
	assert.NotContains(t, e.do("GET", path, nil, strangerToken).Body.String(), "TLT1.")

	wantError(t, e.do("GET", path, nil, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
	wantError(t, e.do("GET", path, nil, "not-a-token"), http.StatusUnauthorized, domain.CodeUnauthenticated)
	wantError(t, e.do("GET", path, nil, e.staffToken(domain.RoleAdmin)), http.StatusForbidden, domain.CodeForbidden)
	wantError(t, e.do("GET", path, nil, e.staffToken(domain.RoleGateStaff)), http.StatusForbidden, domain.CodeForbidden)
	rules := wantFields(t, e.do("GET", "/tickets/not-a-uuid", nil, ownerToken), "ticketId")
	assert.Equal(t, "id", rules["ticketId"])
	wantFields(t, e.do("GET", "/tickets/12345", nil, ownerToken), "ticketId")
}

// ---------------------------------------------------------------------------
// GET /tickets/{ticketId}/qr
// ---------------------------------------------------------------------------

func TestGetTicketQR(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	ownerID, ownerToken := e.newFan()
	_, strangerToken := e.newFan()
	_, _, tickets := e.issued(ev.ID, ownerID, north[0])
	path := "/tickets/" + tickets[0].ID() + "/qr"
	qrToken := decode[ticketJSON](t, e.do("GET", "/tickets/"+tickets[0].ID(), nil, ownerToken)).QRToken

	rec := e.do("GET", path, nil, ownerToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "image/png", rec.Header().Get("Content-Type"))
	assert.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Empty(t, rec.Header().Get("Content-Encoding"), "a PNG is not gzipped again")

	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	require.NoError(t, err)
	assert.Equal(t, 512, img.Bounds().Dx(), "the default size")
	assert.Equal(t, 512, img.Bounds().Dy())
	// The point of the endpoint: a real decoder reads back exactly qr_token.
	assert.Equal(t, qrToken, decodeQR(t, rec.Body.Bytes()))

	version, modules, err := ticketing.QRSize(qrToken)
	require.NoError(t, err)
	t.Logf("real ticket: token %d characters; QR version %d, %d x %d modules; default PNG (512 px) %d bytes",
		len(qrToken), version, modules, modules, rec.Body.Len())
	assert.Equal(t, 160, len(qrToken))
	assert.Equal(t, 9, version)

	for _, size := range []int{128, 300, 1024} {
		rec := e.do("GET", fmt.Sprintf("%s?size=%d", path, size), nil, ownerToken)
		require.Equal(t, http.StatusOK, rec.Code)
		img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
		require.NoError(t, err)
		assert.Equal(t, size, img.Bounds().Dx())
		assert.Equal(t, qrToken, decodeQR(t, rec.Body.Bytes()), "size %d", size)
		t.Logf("size %d px: PNG is %d bytes", size, rec.Body.Len())
	}

	// A fan cannot fetch another fan's QR, and gets nothing that looks like an image.
	rec = e.do("GET", path, nil, strangerToken)
	wantError(t, rec, http.StatusNotFound, domain.CodeNotFound)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.False(t, bytes.HasPrefix(rec.Body.Bytes(), []byte("\x89PNG")))
	wantError(t, e.do("GET", "/tickets/5e2d9c1a-3b4f-4a6d-8e7f-1a2b3c4d5e6f/qr", nil, ownerToken), http.StatusNotFound, domain.CodeNotFound)

	wantError(t, e.do("GET", path, nil, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
	wantError(t, e.do("GET", path, nil, e.staffToken(domain.RoleOrganiser)), http.StatusForbidden, domain.CodeForbidden)
	wantFields(t, e.do("GET", "/tickets/nope/qr", nil, ownerToken), "ticketId")
	for _, bad := range []string{"127", "1025", "0", "-5", "big", "512.5", ""} {
		rules := wantFields(t, e.do("GET", path+"?size="+bad, nil, ownerToken), "size")
		assert.Equal(t, "range", rules["size"], "size=%q", bad)
	}
	// The size is checked before the ticket is looked up, the owner after: a stranger with
	// a bad size learns nothing about whether the ticket exists.
	wantFields(t, e.do("GET", path+"?size=1", nil, strangerToken), "size")
}

// ---------------------------------------------------------------------------
// GET /t/{webToken}
// ---------------------------------------------------------------------------

// dataPNG finds the inline QR images. html/template writes "+" in an attribute as "&#43;",
// which a browser reads back as "+"; qrImages does the same.
var dataPNG = regexp.MustCompile(`src="data:image/png;base64,([^"]+)"`)

func qrImages(t testing.TB, pageHTML string) [][]byte {
	t.Helper()
	var out [][]byte
	for _, m := range dataPNG.FindAllStringSubmatch(pageHTML, -1) {
		raw, err := base64.StdEncoding.DecodeString(html.UnescapeString(m[1]))
		require.NoError(t, err)
		out = append(out, raw)
	}
	return out
}

func TestWebTicketPage(t *testing.T) {
	e := newEnv(t)
	ev, north, south := e.ticketEvent()
	fanID, _ := e.newFan()
	exec(t, `UPDATE users SET name = 'Baraka Mwangi' WHERE id = $1`, fanID)
	bookingID, ref, tickets := e.issued(ev.ID, fanID, north[0], south[3])
	token := webToken(t, bookingID)

	// The link is long, random and nothing like the reference.
	require.Regexp(t, `^[0-9a-f]{64}$`, token)
	assert.NotContains(t, token, strings.ToLower(strings.TrimPrefix(ref, "TLT-")))
	other, _, _ := e.issued(ev.ID, fanID, north[5])
	assert.NotEqual(t, token, webToken(t, other))

	rec := e.do("GET", "/t/"+token, nil, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	page := rec.Body.String()

	// Headers.
	h := rec.Header()
	assert.Equal(t, "text/html; charset=utf-8", h.Get("Content-Type"))
	assert.Equal(t, "private, no-store", h.Get("Cache-Control"))
	assert.Equal(t, "noindex, nofollow, noarchive", h.Get("X-Robots-Tag"))
	assert.Equal(t, "no-referrer", h.Get("Referrer-Policy"), "the page's address is the secret; it must not be sent on")
	assert.Equal(t, "nosniff", h.Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", h.Get("X-Frame-Options"))
	csp := h.Get("Content-Security-Policy")
	assert.Regexp(t, `^default-src 'none'; img-src data:; style-src 'sha256-[A-Za-z0-9+/=]{44}'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'$`, csp)
	assert.NotContains(t, csp, "unsafe")
	assert.NotContains(t, csp, "script-src", "no script is allowed at all: default-src 'none' covers it")

	// The one style block is exactly what the CSP hash allows, and there is nothing else to load.
	style := page[strings.Index(page, "<style>")+len("<style>") : strings.Index(page, "</style>")]
	sum := sha256Base64(style)
	assert.Contains(t, csp, "'sha256-"+sum+"'", "the style block must match its hash or the browser drops it")
	assert.Equal(t, 1, strings.Count(page, "<style>"))
	assert.NotContains(t, page, "<script")
	assert.NotContains(t, page, "http://")
	assert.NotContains(t, page, "https://")
	assert.NotContains(t, page, " style=", "no inline style attributes: the CSP would block them")
	assert.Contains(t, page, `<meta name="robots" content="noindex, nofollow, noarchive">`)
	// Brand colours, and no other palette.
	assert.Contains(t, style, "#064E3B")
	assert.Contains(t, style, "#F8E7C9")

	// Content: reference, event, each seat with its QR.
	assert.Contains(t, page, "Booking "+ref)
	assert.Contains(t, page, "Test Match")
	assert.Contains(t, page, "Home v Away")
	assert.Contains(t, page, ev.Venue.Name)
	assert.Contains(t, page, "EAT", "times are shown in Nairobi time")
	assert.Contains(t, page, "B1 A1")
	assert.Contains(t, page, "B2 A4")
	assert.Contains(t, page, "N-LOWER")
	assert.Contains(t, page, "S-LOWER")
	assert.Contains(t, page, "Baraka Mwangi")
	assert.Contains(t, page, "VIP")

	images := qrImages(t, page)
	require.Len(t, images, 2, "one QR per valid ticket")
	var decoded []string
	for _, raw := range images {
		decoded = append(decoded, decodeQR(t, raw))
	}
	assert.ElementsMatch(t, []string{tickets[0].QRToken(), tickets[1].QRToken()}, decoded, "the QR on the page is the ticket's own token")
	assert.NotContains(t, page, "TLT1.", "the token is only inside the image, not in the page text")
	assert.NotContains(t, page, token, "the page does not repeat its own secret")
	t.Logf("web ticket page with 2 tickets: %d bytes", rec.Body.Len())

	// A stale or foreign Authorization header changes nothing: the page reads no token.
	req := e.request("GET", "/t/"+token, nil, "garbage")
	assert.Equal(t, http.StatusOK, e.contract.Do(t, e.router, req).Code)

	// VOID and USED tickets are shown as such, without a QR.
	exec(t, `UPDATE tickets SET status = 'VOID' WHERE id = $1`, tickets[0].ID())
	exec(t, `UPDATE tickets SET status = 'USED', used_at = '2026-11-14T11:02:17Z' WHERE id = $1`, tickets[1].ID())
	rec = e.do("GET", "/t/"+token, nil, "")
	require.Equal(t, http.StatusOK, rec.Code)
	page = rec.Body.String()
	assert.Contains(t, page, "VOID - this ticket is no longer valid")
	assert.Contains(t, page, "USED - scanned at the gate Sat 14 Nov 2026, 2:02 PM EAT")
	assert.Empty(t, dataPNG.FindAllString(page, -1), "no scannable code for a ticket that is void or used")

	// A cancelled event says so.
	want(t, e.do("POST", fmt.Sprintf("/admin/events/%d/cancel", ev.ID), nil, ev.Token), http.StatusOK)
	page = e.do("GET", "/t/"+webToken(t, other), nil, "").Body.String()
	assert.Contains(t, page, "This event was cancelled")
	assert.Empty(t, dataPNG.FindAllString(page, -1))
}

func TestWebTicketPage_UnknownAndMalformedTokensGetTheSame404(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	fanID, _ := e.newFan()
	bookingID, ref, _ := e.issued(ev.ID, fanID, north[0])
	real := webToken(t, bookingID)

	unknown := e.do("GET", "/t/"+strings.Repeat("0", 64), nil, "")
	require.Equal(t, http.StatusNotFound, unknown.Code)
	assert.Equal(t, "text/html; charset=utf-8", unknown.Header().Get("Content-Type"))
	assert.Equal(t, "private, no-store", unknown.Header().Get("Cache-Control"))
	assert.Equal(t, "noindex, nofollow, noarchive", unknown.Header().Get("X-Robots-Tag"))
	assert.NotEmpty(t, unknown.Header().Get("Content-Security-Policy"))
	assert.Contains(t, unknown.Body.String(), "Tickets not found")
	assert.NotContains(t, unknown.Body.String(), `"error"`, "HTML, not the JSON error shape")

	// A booking that was never paid has a web token too, and no tickets: also "nothing here".
	unpaid, _ := insertBookingOf(t, ev.ID, fanID, "PENDING_PAYMENT", north[1])

	for name, tok := range map[string]string{
		"one character short":            real[:63],
		"one character long":             real + "0",
		"upper case":                     strings.ToUpper(real),
		"last character changed":         real[:63] + map[bool]string{true: "1", false: "0"}[real[63] == '0'],
		"the booking reference":          ref,
		"the booking id":                 fmt.Sprint(bookingID),
		"not hex":                        strings.Repeat("z", 64),
		"sql":                            "'%20OR%201=1--",
		"html":                           "%3Cscript%3Ealert(1)%3Cscript%3E",
		"a ticket token":                 "TLT1.AQAB.AAAA",
		"a booking with no tickets":      webToken(t, unpaid),
		"an unknown token, another one":  strings.Repeat("ab", 32),
		"real token with trailing space": real + "%20",
	} {
		t.Run(name, func(t *testing.T) {
			rec := e.do("GET", "/t/"+tok, nil, "")
			require.Equal(t, http.StatusNotFound, rec.Code)
			assert.Equal(t, unknown.Body.String(), rec.Body.String(), "the same page, byte for byte")
			assert.Equal(t, unknown.Header().Get("Content-Type"), rec.Header().Get("Content-Type"))
			assert.Equal(t, unknown.Header().Get("Cache-Control"), rec.Header().Get("Cache-Control"))
			assert.NotContains(t, rec.Body.String(), "alert(1)")
		})
	}
	// And the real one still works.
	assert.Equal(t, http.StatusOK, e.do("GET", "/t/"+real, nil, "").Code)
}

func TestWebTicketPage_EscapesEverything(t *testing.T) {
	e := newEnv(t)
	const (
		evilTitle  = `<img src=x onerror=alert(1)> "Derby" & 'Final' </h2><script>alert(2)</script>`
		evilName   = `<script>alert('name')</script><b onmouseover=alert(3)>Eve</b>`
		evilTeam   = `</p><iframe src="javascript:alert(4)">`
		evilVenue  = `Talanta <svg/onload=alert(5)>`
		evilStand  = `North" onclick="alert(6)`
		evilBlock  = `{{.Reference}} ${7*7} <style>body{display:none}</style>`
		evilTicket = `VIP<!--`
	)
	ev, north, _ := e.ticketEvent(func(b obj) {
		b["title"] = evilTitle
		b["home_team"] = evilTeam
		b["away_team"] = "Away & <Co>"
	})
	fanID, _ := e.newFan()
	exec(t, `UPDATE users SET name = $2 WHERE id = $1`, fanID, evilName)
	exec(t, `UPDATE venues SET name = $2 WHERE id = $1`, ev.Venue.ID, evilVenue)
	exec(t, `UPDATE stands SET name = $2 WHERE venue_id = $1`, ev.Venue.ID, evilStand)
	exec(t, `UPDATE blocks SET label = $2 WHERE id = $1`, ev.Venue.BlockIDs[0], evilBlock)
	exec(t, `UPDATE event_categories SET name = $2 WHERE id = $1`, ev.Cats["VIP"], evilTicket)
	bookingID, _, _ := e.issued(ev.ID, fanID, north[0])

	rec := e.do("GET", "/t/"+webToken(t, bookingID), nil, "")
	require.Equal(t, http.StatusOK, rec.Code)
	markup := rec.Body.String()

	// Nothing injected became markup.
	for _, live := range []string{"<script", "<img src=x", "<iframe", "<svg", "<b onmouseover", "onerror=alert(1)>", `onclick="alert`, "<!--", "javascript:alert(4)\">"} {
		assert.False(t, strings.Contains(markup, live), "unescaped markup on the page: %q", live)
	}
	assert.Equal(t, 1, strings.Count(markup, "<style>"), "the injected style block is text, not a second style element")
	assert.Equal(t, 1, strings.Count(markup, "<h2>"))
	assert.Equal(t, 1, strings.Count(markup, "<img "), "the only image is the QR")

	// Everything is there as text.
	assert.Contains(t, markup, "&lt;img src=x onerror=alert(1)&gt; &#34;Derby&#34; &amp; &#39;Final&#39;")
	assert.Contains(t, markup, "&lt;script&gt;alert(&#39;name&#39;)&lt;/script&gt;")
	assert.Contains(t, markup, "Away &amp; &lt;Co&gt;")
	assert.Contains(t, markup, "Talanta &lt;svg/onload=alert(5)&gt;")
	assert.Contains(t, markup, "North&#34; onclick=&#34;alert(6)")
	assert.Contains(t, markup, "{{.Reference}} ${7*7}", "template syntax in data is just text")
	assert.Contains(t, markup, "VIP&lt;!--")

	// The same data through the JSON API is plain JSON strings (and passes the contract).
	token, err := e.auth.Tokens().IssueAccess(fanID, domain.RoleFan, "test-session")
	require.NoError(t, err)
	list := decode[page[ticketJSON]](t, e.do("GET", "/me/tickets", nil, token))
	require.Len(t, list.Items, 1)
	assert.Equal(t, evilTitle, list.Items[0].Event.Title)
	assert.Equal(t, evilName, *list.Items[0].HolderName)
}

func TestWebTicketPage_RateLimitedPerIP(t *testing.T) {
	e := newEnv(t, withLimits(func(l *httpapi.RateLimits) {
		l.WebTicketPerIP = ratelimit.Rule{Limit: 3, Window: time.Minute}
	}))
	ev, north, _ := e.ticketEvent()
	fanID, _ := e.newFan()
	bookingID, _, _ := e.issued(ev.ID, fanID, north[0])
	path := "/t/" + webToken(t, bookingID)

	// Wrong guesses count too: the limit is on requests, not on hits.
	assert.Equal(t, http.StatusOK, e.do("GET", path, nil, "").Code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/t/"+strings.Repeat("0", 64), nil, "").Code)
	assert.Equal(t, http.StatusNotFound, e.do("GET", "/t/short", nil, "").Code)

	rec := e.do("GET", path, nil, "")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Regexp(t, `^[1-9][0-9]*$`, rec.Header().Get("Retry-After"))
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
	assert.Contains(t, rec.Body.String(), "Too many requests")
	assert.Empty(t, dataPNG.FindAllString(rec.Body.String(), -1))
	assert.NotContains(t, rec.Body.String(), `"error"`)

	// Another address has its own count; the window ending resets the first.
	e.remoteAddr = "203.0.113.9:5000"
	assert.Equal(t, http.StatusOK, e.do("GET", path, nil, "").Code)
	e.remoteAddr = "198.51.100.7:40000"
	e.clock.Advance(61 * time.Second)
	assert.Equal(t, http.StatusOK, e.do("GET", path, nil, "").Code)
}

// ---------------------------------------------------------------------------
// SMS
// ---------------------------------------------------------------------------

func TestSendTicketSMS(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	fanID, _ := e.newFan()
	phone := queryText(t, `SELECT phone_e164 FROM users WHERE id = $1`, fanID)
	bookingID, ref, _ := e.issued(ev.ID, fanID, north[0], north[1])
	ctx := context.Background()

	e.tickets.SendTicketSMS(ctx, bookingID)

	require.Equal(t, 1, e.sms.count(phone))
	text := e.sms.last(phone)
	link := testPublicBaseURL + "/v1/t/" + webToken(t, bookingID)
	assert.Equal(t, "Talanta: you're going! Booking "+ref+", 2 tickets: "+link, text)
	n, ok := ticketing.GSMLength(text)
	require.True(t, ok)
	assert.LessOrEqual(t, n, 160)
	t.Logf("ticket SMS: %d GSM characters", n)

	// The link in the SMS is the page.
	path := strings.TrimPrefix(text[strings.Index(text, "https://"):], testPublicBaseURL+"/v1")
	rec := e.do("GET", path, nil, "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "Booking "+ref)

	// It still works when the caller's context is already over (the end of a request).
	done, cancel := context.WithCancel(ctx)
	cancel()
	e.tickets.SendTicketSMS(done, bookingID)
	assert.Equal(t, 2, e.sms.count(phone))

	// Nothing is sent for a booking that is not paid, has no live ticket, or does not exist.
	unpaid, _ := insertBookingOf(t, ev.ID, fanID, "PENDING_PAYMENT", north[5])
	e.tickets.SendTicketSMS(ctx, unpaid)
	paidNoTickets, _ := paidBooking(t, ev.ID, fanID, north[6])
	e.tickets.SendTicketSMS(ctx, paidNoTickets)
	e.tickets.SendTicketSMS(ctx, 1<<40)
	exec(t, `UPDATE tickets SET status = 'VOID' WHERE booking_id = $1`, bookingID)
	e.tickets.SendTicketSMS(ctx, bookingID)
	assert.Equal(t, 2, e.sms.count(phone), "no further SMS")
	assert.Contains(t, e.logs.String(), "ticket sms not sent: booking is not paid or has no live ticket")
}

// failingSMS is the worst provider: it fails, and its error quotes the message it refused.
type failingSMS struct{ calls int }

func (f *failingSMS) SendSMS(_ context.Context, to, body string) error {
	f.calls++
	return fmt.Errorf("provider said 503 for message %q to %s", body, to)
}

type panickyTimeoutSMS struct{}

func (panickyTimeoutSMS) SendSMS(ctx context.Context, _, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestSendTicketSMS_FailureIsSwallowedAndTheLinkIsNotLogged(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	fanID, _ := e.newFan()
	bookingID, _, _ := e.issued(ev.ID, fanID, north[0])
	secret := webToken(t, bookingID)
	keys, _ := newTicketKeys(t, "k1")

	newService := func(sms notify.SMSSender, logs io.Writer, timeout time.Duration) *ticketing.Service {
		svc, err := ticketing.NewService(ticketing.Config{
			DB: db.Pool, Keys: keys, SMS: sms, PublicBaseURL: testPublicBaseURL, SMSTimeout: timeout,
			Logger: slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		})
		require.NoError(t, err)
		return svc
	}

	// A provider error: no panic, no error to the caller (there is none to return), one
	// error-level line, and the link is cut out of the provider's own words.
	var logs syncBuffer
	provider := &failingSMS{}
	newService(provider, &logs, 0).SendTicketSMS(context.Background(), bookingID)
	assert.Equal(t, 1, provider.calls)
	assert.Contains(t, logs.String(), "ticket sms not sent: the SMS provider failed")
	assert.Contains(t, logs.String(), `"level":"ERROR"`)
	assert.NotContains(t, logs.String(), secret, "the web token must not reach the log, even inside a provider's error")
	assert.Contains(t, logs.String(), "[web token redacted]")

	// A provider that hangs is cut off by the send's own timeout.
	var slowLogs syncBuffer
	started := time.Now()
	newService(panickyTimeoutSMS{}, &slowLogs, 150*time.Millisecond).SendTicketSMS(context.Background(), bookingID)
	assert.Less(t, time.Since(started), 3*time.Second)
	assert.Contains(t, slowLogs.String(), "ticket sms not sent")

	// The mock provider in production: the message is not sent and neither its text nor the
	// link is logged, at debug level included.
	var prodLogs syncBuffer
	prodLogger := slog.New(slog.NewJSONHandler(&prodLogs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prod, err := ticketing.NewService(ticketing.Config{
		DB: db.Pool, Keys: keys, PublicBaseURL: testPublicBaseURL, Logger: prodLogger,
		SMS: notify.LogSMS{Logger: prodLogger, Production: true},
	})
	require.NoError(t, err)
	prod.SendTicketSMS(context.Background(), bookingID)
	assert.Contains(t, prodLogs.String(), "sms not sent: no SMS provider is configured")
	assert.NotContains(t, prodLogs.String(), secret)
	assert.NotContains(t, prodLogs.String(), "/v1/t/")
	assert.NotContains(t, prodLogs.String(), "you're going")

	// In development the mock logs the body so a developer can open the link.
	var devLogs syncBuffer
	devLogger := slog.New(slog.NewJSONHandler(&devLogs, nil))
	dev, err := ticketing.NewService(ticketing.Config{
		DB: db.Pool, Keys: keys, PublicBaseURL: testPublicBaseURL, Logger: devLogger,
		SMS: notify.LogSMS{Logger: devLogger, Production: false},
	})
	require.NoError(t, err)
	dev.SendTicketSMS(context.Background(), bookingID)
	assert.Contains(t, devLogs.String(), secret)
}

// ---------------------------------------------------------------------------
// Keys
// ---------------------------------------------------------------------------

// The private key is in no response and in no log line. Every ticket endpoint is called,
// success and failure, with the logger at debug; then every byte that left the server and
// every log line is searched for the seed in each spelling it could take.
func TestTicketSigningKeyAppearsInNoResponseOrLog(t *testing.T) {
	e := newEnv(t)
	require.Len(t, e.ticketSeed, ed25519.SeedSize)
	private := ed25519.NewKeyFromSeed(e.ticketSeed)
	ev, north, _ := e.ticketEvent()
	fanID, token := e.newFan()
	_, strangerToken := e.newFan()
	bookingID, _, tickets := e.issued(ev.ID, fanID, north[0], north[1])
	id := tickets[0].ID()

	var out bytes.Buffer
	collect := func(rec *httptest.ResponseRecorder) {
		out.Write(rec.Body.Bytes())
		for k, vs := range rec.Header() {
			out.WriteString(k + ": " + strings.Join(vs, ",") + "\n")
		}
	}
	for _, req := range []struct{ path, token string }{
		{"/me/tickets", token}, {"/me/tickets?limit=1", token}, {"/me/tickets?status=BAD", token}, {"/me/tickets", ""},
		{"/tickets/" + id, token}, {"/tickets/" + id, strangerToken}, {"/tickets/zzz", token},
		{"/tickets/" + id + "/qr", token}, {"/tickets/" + id + "/qr?size=1024", token}, {"/tickets/" + id + "/qr", strangerToken},
		{"/t/" + webToken(t, bookingID), ""}, {"/t/" + strings.Repeat("0", 64), ""},
	} {
		collect(e.do("GET", req.path, nil, req.token))
	}
	// Issue errors and the SMS path log too.
	_, err := e.issue(1 << 40)
	require.Error(t, err)
	out.WriteString(err.Error())
	unpaid, _ := insertBookingOf(t, ev.ID, fanID, "PENDING_PAYMENT", north[4])
	_, err = e.issue(unpaid)
	require.Error(t, err)
	out.WriteString(fmt.Sprintf("%v %+v %#v", err, err, err))
	e.tickets.SendTicketSMS(context.Background(), bookingID)
	e.logger.Debug("service dump", "tickets", fmt.Sprintf("%+v", e.tickets), "verifier", fmt.Sprintf("%+v", e.tickets.Verifier()))
	out.WriteString(fmt.Sprintf("%v %+v %#v", e.tickets, e.tickets, e.tickets))

	haystack := out.String() + "\n" + e.logs.String() + "\n" + e.sms.last(queryText(t, `SELECT phone_e164 FROM users WHERE id = $1`, fanID))
	require.Contains(t, haystack, "TLT1.", "the search covers real ticket answers")
	require.Contains(t, haystack, `"msg":"request"`, "and real log lines")

	for _, secret := range [][]byte{e.ticketSeed, private} {
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
		} {
			assert.NotContains(t, haystack, spelling, "the private key (%s, %d bytes) is in a response or a log line", name, len(secret))
		}
	}
	// The public key is not a secret, but the ticket endpoints have no reason to send it either.
	assert.NotContains(t, haystack, hex.EncodeToString(private.Public().(ed25519.PublicKey)))
}

// Rotation, end to end: tickets issued under k1 keep their stored text and still verify
// after the server moves to k2 with only k1's public key; new tickets are signed with k2.
func TestKeyRotation_OldTicketsStillVerifyAndNewOnesUseTheNewKey(t *testing.T) {
	before := newEnv(t)
	ev, north, _ := before.ticketEvent()
	fanID, token := before.newFan()
	_, _, old := before.issued(ev.ID, fanID, north[0])
	require.Equal(t, "k1", old[0].Row.Ticket.KeyID)
	oldPublic := before.tickets.Verifier().PublicKeys()
	require.Len(t, oldPublic, 1)

	// The server restarts with a new signing key, k2, and k1 in TICKET_VERIFY_KEYS (public
	// half only: k1's private key is gone).
	rotated, _ := newTicketKeys(t, "k2", oldPublic[0])
	after := newEnv(t, withTicketKeys(rotated))
	_, _, fresh := after.issued(ev.ID, fanID, north[1])
	assert.Equal(t, "k2", fresh[0].Row.Ticket.KeyID)

	list := decode[page[ticketJSON]](t, after.do("GET", "/me/tickets", nil, token))
	require.Len(t, list.Items, 2)
	keyIDs := map[string]string{}
	for _, it := range list.Items {
		claims, err := after.tickets.Verifier().Verify(it.QRToken)
		require.NoError(t, err, "ticket %s", it.ID)
		keyIDs[it.ID] = claims.KeyID
	}
	assert.Equal(t, "k1", keyIDs[old[0].ID()])
	assert.Equal(t, "k2", keyIDs[fresh[0].ID()])
	// The old ticket's text is the one it was issued with: it was read, not signed again
	// (the new server could not have signed it).
	for _, it := range list.Items {
		if it.ID == old[0].ID() {
			assert.Equal(t, old[0].QRToken(), it.QRToken)
		}
	}
	// Its QR image still draws, too.
	rec := after.do("GET", "/tickets/"+old[0].ID()+"/qr", nil, token)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, old[0].QRToken(), decodeQR(t, rec.Body.Bytes()))

	// A server that forgot k1 refuses the old ticket by key ID, and the old server does not
	// know k2.
	forgot, _ := newTicketKeys(t, "k2")
	_, err := forgot.Verifier.Verify(old[0].QRToken())
	assert.ErrorIs(t, err, ticketing.ErrUnknownKey)
	_, err = before.tickets.Verifier().Verify(fresh[0].QRToken())
	assert.ErrorIs(t, err, ticketing.ErrUnknownKey)
}
