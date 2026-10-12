package domain

import (
	"math"
	"testing"
	"time"
)

func TestTransitionHold(t *testing.T) {
	all := []HoldStatus{HoldActive, HoldConverted, HoldExpired, HoldReleased}
	for _, from := range all {
		for _, to := range all {
			legal := from == HoldActive && to != HoldActive
			if err := TransitionHold(from, to); (err == nil) != legal {
				t.Errorf("%s -> %s: err = %v, want legal = %v", from, to, err, legal)
			}
		}
	}
}

func TestTransitionSeat(t *testing.T) {
	legal := map[[2]SeatStatus]bool{
		{SeatAvailable, SeatHeld}:    true,
		{SeatAvailable, SeatBlocked}: true,
		{SeatHeld, SeatAvailable}:    true,
		{SeatHeld, SeatSold}:         true,
		{SeatHeld, SeatHeld}:         true, // take-over of a lapsed hold, or the payment extension
		{SeatBlocked, SeatAvailable}: true,
	}
	all := []SeatStatus{SeatAvailable, SeatHeld, SeatSold, SeatBlocked}
	for _, from := range all {
		for _, to := range all {
			if err := TransitionSeat(from, to); (err == nil) != legal[[2]SeatStatus{from, to}] {
				t.Errorf("%s -> %s: err = %v, want legal = %v", from, to, err, legal[[2]SeatStatus{from, to}])
			}
		}
	}
	// The ones that matter most, by name.
	for _, to := range all {
		if TransitionSeat(SeatSold, to) == nil {
			t.Errorf("a SOLD seat must never become %s", to)
		}
	}
	if TransitionSeat(SeatAvailable, SeatSold) == nil {
		t.Error("a seat cannot be sold without being held first")
	}
}

func TestEffectiveHoldStatus(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	cases := []struct {
		stored  HoldStatus
		expires time.Time
		want    HoldStatus
	}{
		{HoldActive, now.Add(time.Second), HoldActive},
		{HoldActive, now, HoldExpired},
		{HoldActive, now.Add(-time.Minute), HoldExpired},
		{HoldReleased, now.Add(time.Hour), HoldReleased},
		{HoldConverted, now.Add(-time.Hour), HoldConverted},
		{HoldExpired, now.Add(time.Hour), HoldExpired},
	}
	for _, c := range cases {
		if got := EffectiveHoldStatus(c.stored, c.expires, now); got != c.want {
			t.Errorf("EffectiveHoldStatus(%s, %s) = %s, want %s", c.stored, c.expires.Sub(now), got, c.want)
		}
	}
}

func TestBookingFeeCents(t *testing.T) {
	cases := []struct {
		subtotal int64
		bps      int
		want     int64
	}{
		{100_000, 0, 0},
		{0, 250, 0},
		{100_000, 250, 2_500},
		// The case that made bookings unpayable: 2.5% of KES 1,500 is KES 37.50. Half a
		// shilling rounds up.
		{150_000, 250, 3_800},
		{200_000, 133, 2_700}, // 26.60 rounds up
		{100_000, 133, 1_300}, // 13.30 rounds down
		{99_999, 250, 2_500},  // 24.99975 shillings
		{299_997, 250, 7_500}, // 74.99925 shillings
		{1_960, 250, 0},       // 0.49 of a shilling is no shilling
		{2_000, 250, 100},     // exactly half a shilling rounds up
		{100_000, 1, 0},       // one basis point of KES 1,000 is 10 cents: no shilling
		{500_000, 1, 100},     // of KES 5,000 it is exactly half a shilling
		{100_000, 10_000, 100_000},
		{100_000, 10_001, 0}, // not a rate
		{100_000, -5, 0},
		{-100_000, 250, 0},
		// No overflow in the arithmetic for a subtotal no stadium will ever reach.
		{math.MaxInt64 / 200, 10_000, (math.MaxInt64/200 + 50) / 100 * 100},
	}
	for _, c := range cases {
		if got := BookingFeeCents(c.subtotal, c.bps); got != c.want {
			t.Errorf("BookingFeeCents(%d, %d) = %d, want %d", c.subtotal, c.bps, got, c.want)
		}
	}
	// Always whole shillings, never more than half a shilling from the exact fee, and the
	// same as exact integer arithmetic wherever that does not overflow.
	for subtotal := int64(0); subtotal < 3_000_000; subtotal += 7_919 {
		for _, bps := range []int{1, 99, 250, 333, 9_999} {
			got := BookingFeeCents(subtotal, bps)
			exact := subtotal * int64(bps) // in units of 1/10,000 of a cent
			want := (exact + 500_000) / 1_000_000 * 100
			if got != want || got%100 != 0 {
				t.Fatalf("BookingFeeCents(%d, %d) = %d, want %d", subtotal, bps, got, want)
			}
			if diff := got*10_000 - exact; diff > 500_000 || diff < -500_000 {
				t.Fatalf("BookingFeeCents(%d, %d) = %d is more than half a shilling from the exact fee", subtotal, bps, got)
			}
		}
	}
}

func TestCheckOnSale(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	cases := []struct {
		name        string
		stored      EventStatus
		open, close *time.Time
		want        Code // "" = on sale
		status      EventStatus
	}{
		{"on sale", EventOnSale, &past, &future, "", ""},
		{"stored PUBLISHED, window open", EventPublished, &past, &future, "", ""},
		{"no window at all", EventOnSale, nil, nil, "", ""},
		{"before the window", EventOnSale, &future, nil, CodeSalesNotOpen, EventPublished},
		{"after the window", EventOnSale, &past, &past, CodeSalesClosed, EventPublished},
		{"closes exactly now", EventOnSale, &past, &now, CodeSalesClosed, EventPublished},
		{"opens exactly now", EventPublished, &now, &future, "", ""},
		{"sold out", EventSoldOut, &past, &future, CodeEventNotOnSale, EventSoldOut},
		{"cancelled", EventCancelled, &past, &future, CodeEventNotOnSale, EventCancelled},
		{"completed", EventCompleted, &past, &past, CodeEventNotOnSale, EventCompleted},
		{"draft", EventDraft, &past, &future, CodeNotFound, ""},
		{"unknown status", EventStatus("NOPE"), &past, &future, CodeNotFound, ""},
	}
	for _, c := range cases {
		err := CheckOnSale(c.stored, c.open, c.close, now)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: %v, want on sale", c.name, err)
			}
			continue
		}
		if CodeOf(err) != c.want {
			t.Errorf("%s: %v, want %s", c.name, err, c.want)
			continue
		}
		if c.want == CodeNotFound {
			continue
		}
		d, ok := err.(*Error).Details.(SalesWindowDetails)
		if !ok || d.EventStatus != c.status {
			t.Errorf("%s: details %+v, want event_status %s", c.name, err.(*Error).Details, c.status)
		}
	}
}

func TestSeatsUnavailableMessage(t *testing.T) {
	for _, c := range []struct {
		asked int
		lost  []int64
		want  string
	}{
		{1, []int64{7}, "The seat you chose is no longer available."},
		{2, []int64{7}, "1 of the 2 seats you chose is no longer available."},
		{4, []int64{7, 8}, "2 of the 4 seats you chose are no longer available."},
	} {
		if got := SeatsUnavailable(c.asked, c.lost).Message; got != c.want {
			t.Errorf("message = %q, want %q", got, c.want)
		}
	}
	if d := SeatsUnavailable(1, nil).Details.(SeatsUnavailableDetails); d.UnavailableSeatIDs == nil {
		t.Error("unavailable_seat_ids must be [] and not null")
	}
}
