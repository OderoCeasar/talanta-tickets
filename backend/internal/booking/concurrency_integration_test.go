//go:build integration

package booking_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
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

// The seven tests of the brief, plus the races with cancel. Every one runs real parallel
// transactions against real Postgres through the real service, with the sweeper off unless
// the test is about the sweeper.

const stormDeadline = 90 * time.Second

// Test 1. 1,000 hold requests from 1,000 fans at the same 10 seats, one seat each: every
// seat has exactly one winner, the other 990 requests get SEATS_UNAVAILABLE naming their
// seat, nothing else goes wrong and nothing deadlocks.
func TestConcurrency_ThousandRequestsAtTenSeats(t *testing.T) {
	const requests, seats = 1000, 10
	f := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: seats})
	svc := newService(t)
	users := newUsers(t, requests)
	target := f.row(0)

	started := time.Now()
	outs := storm(t, requests, stormDeadline, func(i int) (booking.Hold, error) {
		return svc.Hold(context.Background(), users[i], f.EventID, []int64{target[i%seats]})
	})
	took := time.Since(started)
	noDeadlocks(t, outs)

	winners := map[int64]int{} // seat id → number of requests that were told they hold it
	for i, o := range outs {
		seat := target[i%seats]
		if o.Err == nil {
			winners[seat]++
			// Read back after commit: a winner whose seat was then taken by someone else
			// would come back with no seat.
			if assert.Len(t, o.Hold.Seats, 1, "request %d was answered 'held' but holds nothing", i) {
				assert.Equal(t, seat, o.Hold.Seats[0].EventSeatID)
			}
			continue
		}
		assert.Equal(t, []int64{seat}, unavailable(t, o.Err), "request %d", i)
	}
	heldTwice := 0
	for _, seat := range target {
		assert.Equal(t, 1, winners[seat], "seat %d must have exactly one winner", seat)
		if winners[seat] > 1 {
			heldTwice++
		}
	}
	counts := tally(outs)
	t.Logf("requests=%d winners=%d seats_unavailable=%d other_errors=%d seats_held_twice=%d took=%s",
		requests, counts[""], counts[domain.CodeSeatsUnavailable], requests-counts[""]-counts[domain.CodeSeatsUnavailable], heldTwice, took.Round(time.Millisecond))
	assert.Equal(t, map[domain.Code]int{"": seats, domain.CodeSeatsUnavailable: requests - seats}, counts)
	assert.Zero(t, heldTwice, "seats held twice")

	// The database agrees: 10 seats held by 10 live holds, and the 990 failed requests left
	// no hold row behind.
	c := f.check()
	assert.Equal(t, seats, c.Held)
	assert.Equal(t, 0, c.Available)
	assert.Equal(t, seats, queryInt(t, `SELECT count(*) FROM holds WHERE event_id = $1`, f.EventID))
	assert.Equal(t, seats, queryInt(t, `SELECT count(DISTINCT hold_id) FROM event_seats WHERE event_id = $1 AND status = 'HELD'`, f.EventID))
}

// Test 1, several seats per request. 1,000 requests for 3 seats each.
func TestConcurrency_ThousandMultiSeatRequests(t *testing.T) {
	const requests = 1000

	// Four disjoint groups of three, 250 requests each, the ids in every order: exactly one
	// request per group wins and the other 996 are told all three seats are gone.
	t.Run("disjoint groups", func(t *testing.T) {
		f := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 12, MaxPerUser: 6})
		svc := newService(t)
		users := newUsers(t, requests)
		seats := f.row(0)
		orders := [][3]int{{0, 1, 2}, {2, 1, 0}, {1, 2, 0}, {2, 0, 1}, {0, 2, 1}, {1, 0, 2}}
		ask := func(i int) []int64 {
			g, o := i%4, orders[(i/4)%len(orders)]
			return []int64{seats[g*3+o[0]], seats[g*3+o[1]], seats[g*3+o[2]]}
		}
		outs := storm(t, requests, stormDeadline, func(i int) (booking.Hold, error) {
			return svc.Hold(context.Background(), users[i], f.EventID, ask(i))
		})
		noDeadlocks(t, outs)

		perGroup := map[int]int{}
		for i, o := range outs {
			want := slices.Sorted(slices.Values(ask(i)))
			if o.Err == nil {
				perGroup[i%4]++
				require.Len(t, o.Hold.Seats, 3)
				continue
			}
			// A loser may have lost one, two or all three (the winner may still have been
			// mid-transaction on some), but never a seat it did not ask for, and never none.
			lost := unavailable(t, o.Err)
			require.NotEmpty(t, lost)
			for _, id := range lost {
				assert.Contains(t, want, id)
			}
		}
		counts := tally(outs)
		t.Logf("requests=%d winners=%d seats_unavailable=%d other_errors=%d", requests, counts[""], counts[domain.CodeSeatsUnavailable], requests-counts[""]-counts[domain.CodeSeatsUnavailable])
		assert.Equal(t, map[domain.Code]int{"": 4, domain.CodeSeatsUnavailable: requests - 4}, counts)
		assert.Equal(t, map[int]int{0: 1, 1: 1, 2: 1, 3: 1}, perGroup, "one winner per group")
		c := f.check()
		assert.Equal(t, 12, c.Held)
		assert.Equal(t, 4, queryInt(t, `SELECT count(*) FROM holds WHERE event_id = $1`, f.EventID))
		assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM (SELECT hold_id FROM event_seats WHERE event_id = $1 AND status = 'HELD' GROUP BY hold_id HAVING count(*) <> 3) partial`, f.EventID),
			"every hold has all three of its seats: no partial hold")
	})

	// Ten overlapping windows of three over twelve seats ({0,1,2}, {1,2,3}, … {9,10,11}), 100
	// requests each. Which windows win depends on timing, so the number of winners is 2, 3
	// or 4 (two is possible: with {2,3,4} and {7,8,9} held, every other window overlaps one
	// of them); what is exact is that winners never overlap, every winner has all three
	// seats, and no window is left entirely free (a request only ever loses to a committed
	// hold).
	t.Run("overlapping windows", func(t *testing.T) {
		f := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 12, MaxPerUser: 6})
		svc := newService(t)
		users := newUsers(t, requests)
		seats := f.row(0)
		ask := func(i int) []int64 { w := i % 10; return []int64{seats[w+2], seats[w], seats[w+1]} }
		outs := storm(t, requests, stormDeadline, func(i int) (booking.Hold, error) {
			return svc.Hold(context.Background(), users[i], f.EventID, ask(i))
		})
		noDeadlocks(t, outs)

		owner := map[int64]int{}
		winners := 0
		for _, o := range outs {
			if o.Err != nil {
				require.NotEmpty(t, unavailable(t, o.Err))
				continue
			}
			winners++
			require.Len(t, o.Hold.Seats, 3)
			for _, s := range o.Hold.Seats {
				owner[s.EventSeatID]++
			}
		}
		heldTwice := 0
		for _, n := range owner {
			if n > 1 {
				heldTwice++
			}
		}
		counts := tally(outs)
		t.Logf("requests=%d winners=%d seats_unavailable=%d other_errors=%d seats_held_twice=%d", requests, winners, counts[domain.CodeSeatsUnavailable], requests-winners-counts[domain.CodeSeatsUnavailable], heldTwice)
		assert.Zero(t, heldTwice, "seats held twice")
		assert.Contains(t, []int{2, 3, 4}, winners)
		assert.Equal(t, requests-winners, counts[domain.CodeSeatsUnavailable])
		c := f.check()
		assert.Equal(t, winners*3, c.Held, "no partial hold")
		state := seatStates(t, seats)
		for w := 0; w < 10; w++ {
			free := state[seats[w]].Status == "AVAILABLE" && state[seats[w+1]].Status == "AVAILABLE" && state[seats[w+2]].Status == "AVAILABLE"
			assert.False(t, free, "window %d is entirely free although 100 requests asked for it", w)
		}
	})
}

// Test 2. Overlapping requests in opposite order, {5, 9} and {9, 5}, 300 times: never a
// deadlock, always exactly one winner.
func TestConcurrency_OppositeOrderNeverDeadlocks(t *testing.T) {
	const rounds = 300
	f := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 10, MaxPerUser: 50})
	seats := f.row(0)
	five, nine := seats[4], seats[8]

	// Through the service, which is what a client reaches.
	t.Run("service", func(t *testing.T) {
		svc := newService(t)
		users := newUsers(t, 2)
		for round := 0; round < rounds; round++ {
			outs := storm(t, 2, stormDeadline, func(i int) (booking.Hold, error) {
				ask := []int64{five, nine}
				if i == 1 {
					ask = []int64{nine, five}
				}
				return svc.Hold(context.Background(), users[i], f.EventID, ask)
			})
			noDeadlocks(t, outs)
			require.Equal(t, map[domain.Code]int{"": 1, domain.CodeSeatsUnavailable: 1}, tally(outs), "round %d", round)
			for i, o := range outs {
				if o.Err == nil {
					require.NoError(t, svc.Release(context.Background(), users[i], o.Hold.Row.ID))
				}
			}
		}
		t.Logf("rounds=%d deadlocks=0", rounds)
	})

	// Straight at the locking statement with the ids in opposite order, so the test does
	// not lean on the service sorting them first: it is the statement's ORDER BY id that
	// must make this safe. Both transactions lock both seats and roll back.
	t.Run("statement", func(t *testing.T) {
		var deadlocks atomic.Int64
		for round := 0; round < rounds; round++ {
			outs := storm(t, 2, stormDeadline, func(i int) (booking.Hold, error) {
				ask := []int64{five, nine}
				if i == 1 {
					ask = []int64{nine, five}
				}
				err := store.InTxRaw(context.Background(), pool, func(tx pgx.Tx) error {
					got, err := store.LockFreeSeats(context.Background(), tx, f.EventID, ask)
					if err == nil && len(got) != 2 {
						err = fmt.Errorf("locked %d of 2 free seats", len(got))
					}
					return err
				})
				if store.IsDeadlock(err) {
					deadlocks.Add(1)
				}
				return booking.Hold{}, err
			})
			for _, o := range outs {
				require.NoError(t, o.Err, "round %d", round)
			}
		}
		assert.Zero(t, deadlocks.Load())
		t.Logf("rounds=%d deadlocks=%d", rounds, deadlocks.Load())
	})
	f.check()
}

// Test 3. A request for 4 seats where 1 is taken: nothing is held and the 409 lists exactly
// that one seat.
func TestHold_NeverPartial(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 2, SeatsPerRow: 6})
	svc := newService(t)
	ctx := timeout(t)
	first, second := newUser(t), newUser(t)
	seats := f.row(0)

	taken, err := svc.Hold(ctx, first, f.EventID, []int64{seats[2]})
	require.NoError(t, err)

	_, err = svc.Hold(ctx, second, f.EventID, []int64{seats[0], seats[1], seats[2], seats[3]})
	assert.Equal(t, []int64{seats[2]}, unavailable(t, err))
	var de *domain.Error
	require.True(t, errors.As(err, &de))
	assert.Equal(t, "1 of the 4 seats you chose is no longer available.", de.Message)

	// 0 held for the second fan: the three free seats are still free, and no hold row.
	state := seatStates(t, seats)
	for _, i := range []int{0, 1, 3} {
		assert.Equal(t, "AVAILABLE", state[seats[i]].Status, "seat %d", i)
	}
	assert.Equal(t, taken.Row.ID, *state[seats[2]].HoldID)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM holds WHERE user_id = $1`, second))
	c := f.check()
	assert.Equal(t, 1, c.Held)
	assert.Equal(t, 11, c.Available)

	// Everything else that is "not free" is reported the same way, with exactly its ids:
	// a sold seat, a blocked seat, a seat of another event and an id that does not exist.
	other := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 2})
	exec(t, `UPDATE event_seats SET status = 'SOLD' WHERE id = $1`, seats[4])
	exec(t, `UPDATE event_seats SET status = 'BLOCKED' WHERE id = $1`, seats[5])
	foreign, missing := other.row(0)[0], int64(1)<<60
	_, err = svc.Hold(ctx, second, f.EventID, []int64{seats[0], seats[4], seats[5], foreign, missing})
	assert.Equal(t, slices.Sorted(slices.Values([]int64{seats[4], seats[5], foreign, missing})), unavailable(t, err))
	assert.Equal(t, "AVAILABLE", seatStates(t, []int64{foreign})[foreign].Status, "a seat of another event is untouched")
	assert.Equal(t, 1, f.check().Held)
}

// Test 4. With no sweeper anywhere, a hold whose time has run out is taken over by a new
// request, and the old hold does not stay ACTIVE pointing at nothing.
func TestHold_TakesOverALapsedHoldWithoutTheSweeper(t *testing.T) {
	f := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 6})
	svc := newService(t)
	ctx := timeout(t)
	old, second, third := newUser(t), newUser(t), newUser(t)
	seats := f.row(0)

	lapsed, err := svc.Hold(ctx, old, f.EventID, seats[:3])
	require.NoError(t, err)

	// Still live: nobody else can have the seats.
	_, err = svc.Hold(ctx, second, f.EventID, seats[:1])
	assert.Equal(t, seats[:1], unavailable(t, err))

	lapse(t, lapsed.Row.ID)
	// Nothing has swept: the rows still say ACTIVE and HELD.
	require.Equal(t, "ACTIVE", holdStatus(t, lapsed.Row.ID))
	require.Equal(t, 3, f.check().Held)
	// Its owner is already told the truth.
	seen, err := svc.Get(ctx, old, lapsed.Row.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.HoldExpired, seen.Status)
	assert.Zero(t, seen.RemainingSeconds)

	// A second fan takes one of the three seats. The old hold keeps two, so it stays as it
	// is (ACTIVE in the table, expired to everyone who asks).
	before := queryText(t, `SELECT updated_at::text FROM event_seats WHERE id = $1`, seats[0])
	one, err := svc.Hold(ctx, second, f.EventID, seats[:1])
	require.NoError(t, err)
	assert.Equal(t, "ACTIVE", holdStatus(t, lapsed.Row.ID))
	state := seatStates(t, seats)
	assert.Equal(t, one.Row.ID, *state[seats[0]].HoldID)
	assert.Equal(t, lapsed.Row.ID, *state[seats[1]].HoldID)
	assert.NotEqual(t, before, queryText(t, `SELECT updated_at::text FROM event_seats WHERE id = $1`, seats[0]), "a take-over stamps the seat")
	f.check()

	// The old owner releasing now frees nothing of the new holder's.
	// (Not called here: release would close the old hold. That path is in the release test.)

	// A third fan takes the other two, with a free seat in the same request. Now the old
	// hold has nothing left: it must be EXPIRED, with no sweeper having run.
	rest, err := svc.Hold(ctx, third, f.EventID, []int64{seats[1], seats[2], seats[3]})
	require.NoError(t, err)
	assert.Equal(t, "EXPIRED", holdStatus(t, lapsed.Row.ID), "a lapsed hold with no seat left is closed by the take-over")
	state = seatStates(t, seats)
	for _, id := range []int64{seats[1], seats[2], seats[3]} {
		assert.Equal(t, rest.Row.ID, *state[id].HoldID)
	}
	c := f.check()
	assert.Equal(t, 4, c.Held)
	assert.Equal(t, 2, c.Available)
	assert.Equal(t, 2, queryInt(t, `SELECT count(*) FROM holds WHERE event_id = $1 AND status = 'ACTIVE'`, f.EventID))

	// The old holder's unpaid booking goes with the hold; one with a live payment is left
	// for the payment flow.
	a, b := newUser(t), newUser(t)
	ha, err := svc.Hold(ctx, a, f.EventID, seats[4:5])
	require.NoError(t, err)
	hb, err := svc.Hold(ctx, b, f.EventID, seats[5:6])
	require.NoError(t, err)
	unpaid := insertBooking(t, f, a, &ha.Row.ID, "PENDING_PAYMENT", seats[4])
	paying := insertBooking(t, f, b, &hb.Row.ID, "PENDING_PAYMENT", seats[5])
	insertPayment(t, paying, "PENDING")
	lapse(t, ha.Row.ID)
	lapse(t, hb.Row.ID)
	_, err = svc.Hold(ctx, second, f.EventID, seats[4:6])
	require.NoError(t, err)
	assert.Equal(t, "EXPIRED", holdStatus(t, ha.Row.ID))
	assert.Equal(t, "EXPIRED", holdStatus(t, hb.Row.ID))
	assert.Equal(t, "EXPIRED", queryText(t, `SELECT status FROM bookings WHERE id = $1`, unpaid))
	assert.Equal(t, "PENDING_PAYMENT", queryText(t, `SELECT status FROM bookings WHERE id = $1`, paying))
	f.check()
}

// Test 4, under contention: 200 fans go for the 10 seats of 5 lapsed holds at once, sweeper
// off. One winner per seat, and every old hold ends EXPIRED.
func TestConcurrency_TakeOverOfLapsedHolds(t *testing.T) {
	const requests = 200
	f := newFixture(t, fixtureOpts{Rows: 1, SeatsPerRow: 10})
	svc := newService(t)
	ctx := timeout(t)
	seats := f.row(0)
	var lapsed []int64
	for i, u := range newUsers(t, 5) {
		h, err := svc.Hold(ctx, u, f.EventID, seats[i*2:i*2+2])
		require.NoError(t, err)
		lapse(t, h.Row.ID)
		lapsed = append(lapsed, h.Row.ID)
	}
	users := newUsers(t, requests)
	outs := storm(t, requests, stormDeadline, func(i int) (booking.Hold, error) {
		return svc.Hold(context.Background(), users[i], f.EventID, []int64{seats[i%10]})
	})
	noDeadlocks(t, outs)
	counts := tally(outs)
	t.Logf("requests=%d winners=%d seats_unavailable=%d", requests, counts[""], counts[domain.CodeSeatsUnavailable])
	assert.Equal(t, map[domain.Code]int{"": 10, domain.CodeSeatsUnavailable: requests - 10}, counts)
	for _, id := range lapsed {
		assert.Equal(t, "EXPIRED", holdStatus(t, id), "old hold %d", id)
	}
	c := f.check()
	assert.Equal(t, 10, c.Held)
	assert.Equal(t, 10, queryInt(t, `SELECT count(*) FROM holds WHERE event_id = $1 AND status = 'ACTIVE'`, f.EventID))
}

// Test 5. One fan sends two hold requests at the same moment that together break the
// ticket limit (4 + 4 against a limit of 6). Exactly one succeeds, every time.
func TestConcurrency_TicketLimitCannotBeRaced(t *testing.T) {
	const rounds = 100
	f := newFixture(t, fixtureOpts{Rows: rounds, SeatsPerRow: 8, Blocks: 1, MaxPerUser: 6})
	// rows are labelled chr(64+i): fine past 26 for a test, the labels are not read.
	svc := newService(t)
	users := newUsers(t, rounds)

	bothWon := 0
	for round := 0; round < rounds; round++ {
		row := f.row(round)
		outs := storm(t, 2, stormDeadline, func(i int) (booking.Hold, error) {
			return svc.Hold(context.Background(), users[round], f.EventID, row[i*4:i*4+4])
		})
		noDeadlocks(t, outs)
		counts := tally(outs)
		if counts[""] == 2 {
			bothWon++
		}
		assert.Equal(t, map[domain.Code]int{"": 1, domain.CodeTicketLimitExceeded: 1}, counts, "round %d", round)
		for _, o := range outs {
			var de *domain.Error
			if errors.As(o.Err, &de) && de.Code == domain.CodeTicketLimitExceeded {
				assert.Equal(t, domain.TicketLimitDetails{Allowed: 6, HeldOrBought: 4, Requested: 4, Remaining: 2}, de.Details)
			}
		}
		held := queryInt(t, `SELECT count(*) FROM event_seats es JOIN holds h ON h.id = es.hold_id WHERE h.user_id = $1 AND es.status = 'HELD'`, users[round])
		assert.Equal(t, 4, held, "round %d: the fan holds %d seats against a limit of 6", round, held)
	}
	t.Logf("rounds=%d both_requests_won=%d", rounds, bothWon)
	assert.Zero(t, bothWon, "rounds in which both requests passed the limit")
	assert.Equal(t, rounds*4, f.check().Held)
}

// Test 6. Best available under contention, with demand equal to supply: 50 groups of 4 ask
// at the same moment for a category that has exactly 50 runs of four. Every group gets 4
// neighbouring seats in one row, and no seat is given to two groups. Several rounds: before
// the later tries walked several runs each, one to three groups a round were told
// NO_ADJACENT_SEATS while a whole run stood free.
func TestConcurrency_BestAvailableFiftyGroupsOfFour(t *testing.T) {
	const groups, size, rounds = 50, 4, 5
	svc := newService(t)
	for round := 1; round <= rounds; round++ {
		// 1 block x 10 rows x 20 seats = 200 seats: exactly 50 runs of four, no spare.
		f := newFixture(t, fixtureOpts{Blocks: 1, Rows: 10, SeatsPerRow: 20})
		users := newUsers(t, groups)

		started := time.Now()
		outs := storm(t, groups, stormDeadline, func(i int) (booking.Hold, error) {
			return svc.HoldBestAvailable(context.Background(), users[i], f.EventID, f.CategoryID, size)
		})
		took := time.Since(started)
		noDeadlocks(t, outs)

		owner := map[int64]int{}
		shared := 0
		for i, o := range outs {
			require.NoError(t, o.Err, "round %d, group %d", round, i)
			require.Len(t, o.Hold.Seats, size, "round %d, group %d", round, i)
			first := o.Hold.Seats[0]
			for k, s := range o.Hold.Seats {
				assert.Equal(t, first.BlockID, s.BlockID, "group %d: one block", i)
				assert.Equal(t, first.RowLabel, s.RowLabel, "group %d: one row", i)
				assert.Equal(t, first.SeatNumber+int32(k), s.SeatNumber, "group %d: neighbouring seat numbers", i)
				owner[s.EventSeatID]++
				if owner[s.EventSeatID] > 1 {
					shared++
				}
			}
		}
		t.Logf("round=%d groups=%d runs=%d got_seats=%d seats_shared=%d took=%s", round, groups, groups, tally(outs)[""], shared, took.Round(time.Millisecond))
		assert.Zero(t, shared, "seats given to two groups")
		assert.Len(t, owner, groups*size)
		c := f.check()
		assert.Equal(t, groups*size, c.Held)
		assert.Zero(t, c.Available, "every seat of the category is held")
	}
}

// Test 7. The sweeper and live hold requests at the same time: nothing blocks, nothing
// deadlocks, nothing is lost. 100 lapsed holds (2 seats each) are being swept by two
// sweepers in a loop while 600 requests go for those same seats and for free ones, and the
// winners of every other request release at once.
func TestConcurrency_SweeperAndLiveHoldsTogether(t *testing.T) {
	const lapsedHolds, requests = 100, 600
	f := newFixture(t, fixtureOpts{Blocks: 1, Rows: 20, SeatsPerRow: 20, MaxPerUser: 50}) // 400 seats
	// A small batch so one run is many transactions, interleaved with the requests.
	svc := newService(t, func(p *booking.Policy) { p.SweepBatch = 7 })
	ctx := timeout(t)
	seats := f.all()

	var lapsed []int64
	for i, u := range newUsers(t, lapsedHolds) {
		h, err := svc.Hold(ctx, u, f.EventID, seats[i*2:i*2+2]) // seats 0..199
		require.NoError(t, err)
		lapse(t, h.Row.ID)
		lapsed = append(lapsed, h.Row.ID)
	}

	stop := make(chan struct{})
	var sweeps, skipped, sweptHolds, sweptSeats atomic.Int64
	var sweepErr atomic.Value
	var wg sync.WaitGroup
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				res, err := svc.Sweep(context.Background())
				if err != nil {
					sweepErr.Store(err)
					return
				}
				sweeps.Add(1)
				if res.Skipped {
					skipped.Add(1)
				}
				sweptHolds.Add(int64(res.Holds))
				sweptSeats.Add(int64(res.Seats))
			}
		}()
	}

	users := newUsers(t, requests)
	started := time.Now()
	outs := storm(t, requests, stormDeadline, func(i int) (booking.Hold, error) {
		// Seats 0..299: the 200 lapsed ones and 100 free ones, 2 requests per seat.
		h, err := svc.Hold(context.Background(), users[i], f.EventID, []int64{seats[i%300]})
		if err == nil && i%2 == 0 {
			if rerr := svc.Release(context.Background(), users[i], h.Row.ID); rerr != nil {
				return h, fmt.Errorf("release: %w", rerr)
			}
			h.Status = domain.HoldReleased
		}
		return h, err
	})
	took := time.Since(started)
	close(stop)
	wg.Wait()
	if err, _ := sweepErr.Load().(error); err != nil {
		t.Fatalf("sweeper failed: %v", err)
	}
	noDeadlocks(t, outs)

	// One last sweep closes whatever lapsed hold nobody took over.
	last, err := svc.Sweep(ctx)
	require.NoError(t, err)
	sweptHolds.Add(int64(last.Holds))
	sweptSeats.Add(int64(last.Seats))

	counts := tally(outs)
	kept := 0
	for _, o := range outs {
		if o.Err == nil && o.Hold.Status == domain.HoldActive {
			kept++
		}
	}
	t.Logf("requests=%d winners=%d (kept=%d released=%d) seats_unavailable=%d other_errors=%d | sweeps=%d skipped_for_the_other_sweeper=%d holds_swept=%d seats_swept=%d | took=%s",
		requests, counts[""], kept, counts[""]-kept, counts[domain.CodeSeatsUnavailable], requests-counts[""]-counts[domain.CodeSeatsUnavailable],
		sweeps.Load(), skipped.Load(), sweptHolds.Load(), sweptSeats.Load(), took.Round(time.Millisecond))
	assert.Equal(t, requests, counts[""]+counts[domain.CodeSeatsUnavailable], "only success and SEATS_UNAVAILABLE: %v", counts)
	// Every seat got at least one winner: 2 requests each, and a request only loses to
	// another request's committed hold (possibly one released a moment later).
	assert.GreaterOrEqual(t, counts[""], 300)

	// Nothing lost: every old hold is closed, every seat is either free or held by exactly
	// the live hold of a request that kept it.
	for _, id := range lapsed {
		require.Equal(t, "EXPIRED", holdStatus(t, id), "lapsed hold %d", id)
	}
	c := f.check()
	assert.Equal(t, 400, c.Total)
	assert.Equal(t, kept, c.Held, "held seats = requests that won and kept their seat")
	assert.Equal(t, 400-kept, c.Available)
	assert.Equal(t, kept, queryInt(t, `SELECT count(*) FROM holds WHERE event_id = $1 AND status = 'ACTIVE'`, f.EventID))
	for _, o := range outs {
		if o.Err == nil && o.Hold.Status == domain.HoldActive {
			st := seatStates(t, []int64{o.Hold.Seats[0].EventSeatID})[o.Hold.Seats[0].EventSeatID]
			require.Equal(t, "HELD", st.Status)
			require.Equal(t, o.Hold.Row.ID, *st.HoldID, "a kept seat still belongs to the request that won it")
		}
	}
}

func newEvents(t testing.TB) *events.Service {
	t.Helper()
	svc, err := events.NewService(events.Config{DB: adminPool, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	return svc
}

// A hold racing an event cancel. 1,000 hold requests at 500 seats, and the organiser cancels
// in the middle of them. No deadlock; every request either held its seat before the cancel
// (and the cancel then freed it) or was refused; and when it is over the cancelled event has
// not one HELD seat. The cancel must also get its turn: the event row is share-locked by
// every hold, and the test reports how long the cancel waited for it.
func TestConcurrency_HoldsRacingACancel(t *testing.T) {
	const requests = 1000
	f := newFixture(t, fixtureOpts{Blocks: 1, Rows: 25, SeatsPerRow: 20, MaxPerUser: 6}) // 500 seats
	svc, evs := newService(t), newEvents(t)
	users := newUsers(t, requests)
	seats := f.all()

	var cancelTook time.Duration
	var cancelErr error
	var released int
	cancelled := make(chan struct{})
	var inFlight atomic.Int64
	go func() {
		defer close(cancelled)
		// Let the storm get going, then cancel while requests are in flight.
		for inFlight.Load() < 100 {
			time.Sleep(200 * time.Microsecond)
		}
		started := time.Now()
		ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		res, err := evs.Cancel(ctx, f.EventID)
		cancelTook, cancelErr, released = time.Since(started), err, res.HoldsReleased
	}()

	started := time.Now()
	outs := storm(t, requests, stormDeadline, func(i int) (booking.Hold, error) {
		inFlight.Add(1)
		return svc.Hold(context.Background(), users[i], f.EventID, []int64{seats[i%500]})
	})
	stormTook := time.Since(started)
	<-cancelled
	require.NoError(t, cancelErr, "cancel")
	noDeadlocks(t, outs)

	counts := tally(outs)
	t.Logf("requests=%d held_before_cancel=%d seats_unavailable=%d event_not_on_sale=%d other_errors=%d | cancel waited+ran %s and released %d holds | storm took %s",
		requests, counts[""], counts[domain.CodeSeatsUnavailable], counts[domain.CodeEventNotOnSale],
		requests-counts[""]-counts[domain.CodeSeatsUnavailable]-counts[domain.CodeEventNotOnSale],
		cancelTook.Round(time.Millisecond), released, stormTook.Round(time.Millisecond))
	assert.Equal(t, requests, counts[""]+counts[domain.CodeSeatsUnavailable]+counts[domain.CodeEventNotOnSale], "%v", counts)
	// Every hold that succeeded did so before the cancel, and the cancel released it.
	assert.Equal(t, counts[""], released, "holds that won = holds the cancel released")
	assert.Less(t, cancelTook, 10*time.Second, "the cancel was starved by the holds' share locks")

	c := f.check() // includes: a cancelled event has no HELD seat and no ACTIVE hold
	assert.Equal(t, 0, c.Held)
	assert.Equal(t, 500, c.Available)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM holds WHERE event_id = $1 AND status = 'ACTIVE'`, f.EventID))

	// And afterwards the free seats of the cancelled event cannot be held.
	_, err := svc.Hold(context.Background(), users[0], f.EventID, seats[:1])
	assert.Equal(t, domain.CodeEventNotOnSale, domain.CodeOf(err))
	assert.Equal(t, 0, f.check().Held)
}

// The sweeper, a cancel and live holds, all at once. The sweeper spans events and cancel
// touches seats in no particular order, so this is the combination that would deadlock if
// the sweeper did not follow the lock order (event first).
func TestConcurrency_SweeperCancelAndLiveHoldsTogether(t *testing.T) {
	const eventsN, perEvent = 6, 150
	svc, evs := newService(t, func(p *booking.Policy) { p.SweepBatch = 5 }), newEvents(t)
	ctx := timeout(t)

	var fx []*fixture
	var lapsed []int64
	for e := 0; e < eventsN; e++ {
		f := newFixture(t, fixtureOpts{Rows: 10, SeatsPerRow: 10, MaxPerUser: 50}) // 100 seats
		fx = append(fx, f)
		seats := f.all()
		for i, u := range newUsers(t, 20) {
			h, err := svc.Hold(ctx, u, f.EventID, seats[i*2:i*2+2]) // seats 0..39
			require.NoError(t, err)
			lapse(t, h.Row.ID)
			lapsed = append(lapsed, h.Row.ID)
		}
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var sweepErr, cancelErr atomic.Value
	var sweeps atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := svc.Sweep(context.Background()); err != nil {
				sweepErr.Store(err)
				return
			}
			sweeps.Add(1)
		}
	}()

	users := newUsers(t, eventsN*perEvent)
	var inFlight atomic.Int64
	// Half the events are cancelled while the storm and the sweeper run.
	for e := 0; e < eventsN; e += 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for inFlight.Load() < int64(50*(e+1)) {
				time.Sleep(200 * time.Microsecond)
			}
			if _, err := evs.Cancel(context.Background(), fx[e].EventID); err != nil {
				cancelErr.Store(err)
			}
		}()
	}
	outs := storm(t, eventsN*perEvent, stormDeadline, func(i int) (booking.Hold, error) {
		inFlight.Add(1)
		f := fx[i%eventsN]
		return svc.Hold(context.Background(), users[i], f.EventID, []int64{f.all()[(i/eventsN)%60]})
	})
	close(stop)
	wg.Wait()
	if err, _ := sweepErr.Load().(error); err != nil {
		t.Fatalf("sweeper failed: %v", err)
	}
	if err, _ := cancelErr.Load().(error); err != nil {
		t.Fatalf("cancel failed: %v", err)
	}
	noDeadlocks(t, outs)
	_, err := svc.Sweep(ctx)
	require.NoError(t, err)

	counts := tally(outs)
	t.Logf("requests=%d winners=%d seats_unavailable=%d event_not_on_sale=%d other_errors=%d sweeps=%d",
		len(outs), counts[""], counts[domain.CodeSeatsUnavailable], counts[domain.CodeEventNotOnSale],
		len(outs)-counts[""]-counts[domain.CodeSeatsUnavailable]-counts[domain.CodeEventNotOnSale], sweeps.Load())
	assert.Equal(t, len(outs), counts[""]+counts[domain.CodeSeatsUnavailable]+counts[domain.CodeEventNotOnSale], "%v", counts)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM holds WHERE id = ANY($1) AND status = 'ACTIVE'`, lapsed), "every lapsed hold is closed")
	for e, f := range fx {
		c := f.check()
		if e%2 == 0 {
			assert.Equal(t, 0, c.Held, "cancelled event %d", e)
			assert.Equal(t, "CANCELLED", queryText(t, `SELECT status FROM events WHERE id = $1`, f.EventID))
		}
	}
}
