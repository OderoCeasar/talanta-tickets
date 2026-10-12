package domain

import (
	"fmt"
	"time"
)

// HoldStatus is the stored state of a hold.
type HoldStatus string

const (
	HoldActive    HoldStatus = "ACTIVE"
	HoldConverted HoldStatus = "CONVERTED"
	HoldExpired   HoldStatus = "EXPIRED"
	HoldReleased  HoldStatus = "RELEASED"
)

// Limits of a hold request, as in the contract.
const (
	MaxSeatsPerHold = 50
)

// MaxBookingFeeBPS is the most BOOKING_FEE_BPS may be: 100% of the subtotal.
const MaxBookingFeeBPS = 10_000

// BookingFeeCents is the one place the booking fee is worked out: feeBPS basis points of the
// subtotal, rounded to the nearest whole shilling (a multiple of 100 cents), half a shilling
// rounding up. Integer arithmetic only.
//
// Whole shillings because M-Pesa takes nothing smaller: a fee of KES 37.50 would make a
// booking no payment could ever be started for. With prices in whole shillings the total is
// then always payable. A hold shows the fee as an estimate and the booking charges it; both
// call this function so they cannot disagree.
// A fee rate outside 0..MaxBookingFeeBPS, or a negative subtotal, gives zero.
func BookingFeeCents(subtotalCents int64, feeBPS int) int64 {
	if subtotalCents <= 0 || feeBPS <= 0 || feeBPS > MaxBookingFeeBPS {
		return 0
	}
	// The fee in shillings is subtotal*bps / 1,000,000 (10,000 for the basis points, 100 for
	// cents to shillings). Quotient and remainder separately, so the product cannot overflow
	// for any subtotal; half of the divisor is added before dividing to round half up.
	const perShilling = 1_000_000
	whole, rest := subtotalCents/perShilling, subtotalCents%perShilling
	shillings := whole*int64(feeBPS) + (rest*int64(feeBPS)+perShilling/2)/perShilling
	return shillings * 100
}

// holdMoves is every legal change of a hold's status. A hold only ever leaves ACTIVE; the
// three end states are final, so a released hold can never be paid for and a paid one can
// never be released.
var holdMoves = map[HoldStatus][]HoldStatus{
	HoldActive:    {HoldConverted, HoldExpired, HoldReleased},
	HoldConverted: {},
	HoldExpired:   {},
	HoldReleased:  {},
}

// TransitionHold is the one function every change of a hold's status goes through. It
// returns nil when the move is legal. An illegal move is a fault in the caller, not something
// a client did, so the error is not a domain error and reaches the client as INTERNAL.
func TransitionHold(from, to HoldStatus) error {
	for _, next := range holdMoves[from] {
		if next == to {
			return nil
		}
	}
	return fmt.Errorf("illegal hold transition %s -> %s", from, to)
}

// EffectiveHoldStatus is the status a hold really has at `now`. Nothing flips ACTIVE to
// EXPIRED at the moment the time runs out (the sweeper does it some seconds later, and
// correctness never waits for it), so every reader derives it here: an ACTIVE hold past its
// expiry is EXPIRED.
func EffectiveHoldStatus(stored HoldStatus, expiresAt, now time.Time) HoldStatus {
	if stored == HoldActive && !expiresAt.After(now) {
		return HoldExpired
	}
	return stored
}

// seatMoves is every legal change of a seat's inventory status.
//
//   - HELD -> HELD is a lapsed hold being taken over by a new one, or a live hold's expiry
//     being pushed out for a payment. The seat stays held; only its hold or its expiry changes.
//   - SOLD has no way out. In particular SOLD -> AVAILABLE does not exist: a sold seat is never
//     put back on sale by a release, an expiry or a sweep. (A cancelled event voids tickets and
//     refunds bookings; its sold seats stay SOLD.)
var seatMoves = map[SeatStatus][]SeatStatus{
	SeatAvailable: {SeatHeld, SeatBlocked},
	SeatHeld:      {SeatAvailable, SeatSold, SeatHeld},
	SeatSold:      {},
	SeatBlocked:   {SeatAvailable},
}

// TransitionSeat is the one function every change of a seat's status goes through. The
// locking statements in internal/store each implement exactly one of these moves and carry
// the `from` status in their WHERE clause; the booking service asks this function before it
// runs one, so a move that is not in the table cannot be issued. Like TransitionHold, an
// illegal move is a programming fault and is not a domain error.
func TransitionSeat(from, to SeatStatus) error {
	for _, next := range seatMoves[from] {
		if next == to {
			return nil
		}
	}
	return fmt.Errorf("illegal seat transition %s -> %s", from, to)
}

// SeatsUnavailableDetails is the details shape of SEATS_UNAVAILABLE.
type SeatsUnavailableDetails struct {
	UnavailableSeatIDs []int64 `json:"unavailable_seat_ids"`
}

// SeatsUnavailable builds the 409 for a hold that lost one or more seats. Nothing was held.
func SeatsUnavailable(asked int, lost []int64) *Error {
	if lost == nil {
		lost = []int64{}
	}
	msg := fmt.Sprintf("%d of the %d seats you chose are no longer available.", len(lost), asked)
	switch {
	case asked == 1:
		msg = "The seat you chose is no longer available."
	case len(lost) == 1:
		msg = fmt.Sprintf("1 of the %d seats you chose is no longer available.", asked)
	}
	return &Error{Code: CodeSeatsUnavailable, Message: msg, Details: SeatsUnavailableDetails{UnavailableSeatIDs: lost}}
}

// TicketLimitDetails is the details shape of TICKET_LIMIT_EXCEEDED.
type TicketLimitDetails struct {
	Allowed      int `json:"allowed"`
	HeldOrBought int `json:"held_or_bought"`
	Requested    int `json:"requested"`
	Remaining    int `json:"remaining"`
}

// TicketLimitExceeded builds the 422 for a hold that would pass max_tickets_per_user.
func TicketLimitExceeded(allowed, heldOrBought, requested int) *Error {
	remaining := max(allowed-heldOrBought, 0)
	left := fmt.Sprintf("You have %d left.", remaining)
	if remaining == 0 {
		left = "You have none left."
	}
	return &Error{
		Code:    CodeTicketLimitExceeded,
		Message: fmt.Sprintf("You can buy up to %d tickets for this event. %s", allowed, left),
		Details: TicketLimitDetails{Allowed: allowed, HeldOrBought: heldOrBought, Requested: requested, Remaining: remaining},
	}
}

// SalesWindowDetails is the details shape of EVENT_NOT_ON_SALE, SALES_NOT_OPEN and
// SALES_CLOSED.
type SalesWindowDetails struct {
	EventStatus  EventStatus `json:"event_status"`
	SalesOpenAt  *time.Time  `json:"sales_open_at"`
	SalesCloseAt *time.Time  `json:"sales_close_at"`
}

// CheckOnSale is the rule for "may seats of this event be held at `now`". It returns nil
// when they may, NOT_FOUND for a draft (a draft does not exist to a fan), and otherwise the
// most specific of the three sales errors:
//
//   - a published event before its window: SALES_NOT_OPEN;
//   - a published event after its window: SALES_CLOSED;
//   - anything else that is not on sale (sold out, cancelled, completed): EVENT_NOT_ON_SALE.
func CheckOnSale(stored EventStatus, open, closeAt *time.Time, now time.Time) error {
	if !stored.VisibleToPublic() {
		return ErrNotFound
	}
	effective := EffectiveEventStatus(stored, open, closeAt, now)
	if effective == EventOnSale {
		return nil
	}
	details := SalesWindowDetails{EventStatus: effective, SalesOpenAt: utcPtr(open), SalesCloseAt: utcPtr(closeAt)}
	if effective == EventPublished {
		if open != nil && now.Before(*open) {
			return &Error{Code: CodeSalesNotOpen, Message: "Ticket sales for this event have not opened yet.", Details: details}
		}
		return &Error{Code: CodeSalesClosed, Message: "Ticket sales for this event have closed.", Details: details}
	}
	return &Error{Code: CodeEventNotOnSale, Message: "This event is not on sale.", Details: details}
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// NoAdjacentSeatsDetails is the details shape of NO_ADJACENT_SEATS.
type NoAdjacentSeatsDetails struct {
	CategoryID int64 `json:"category_id"`
	Quantity   int   `json:"quantity"`
	Available  int   `json:"available"`
}

// NoAdjacentSeats builds the 409 for a best-available request that found no run of
// neighbouring seats, or lost every run it tried.
func NoAdjacentSeats(categoryID int64, categoryName string, quantity, available int) *Error {
	msg := fmt.Sprintf("We could not find %d seats together in %s.", quantity, categoryName)
	if quantity == 1 {
		msg = fmt.Sprintf("We could not find a seat in %s.", categoryName)
	}
	return &Error{
		Code:    CodeNoAdjacentSeats,
		Message: msg,
		Details: NoAdjacentSeatsDetails{CategoryID: categoryID, Quantity: quantity, Available: available},
	}
}

// HoldStateDetails is the details shape of HOLD_EXPIRED and HOLD_NOT_ACTIVE.
type HoldStateDetails struct {
	HoldID     int64      `json:"hold_id"`
	HoldStatus HoldStatus `json:"hold_status"`
	ExpiresAt  time.Time  `json:"expires_at"`
}

// HoldExpiredError builds HOLD_EXPIRED: the hold ran out, whether or not the sweeper has
// closed it yet.
func HoldExpiredError(holdID int64, expiresAt time.Time) *Error {
	return &Error{
		Code:    CodeHoldExpired,
		Message: "Your hold on these seats has run out.",
		Details: HoldStateDetails{HoldID: holdID, HoldStatus: HoldExpired, ExpiresAt: expiresAt.UTC()},
	}
}

// HoldNotActive builds HOLD_NOT_ACTIVE for a hold that was released or already paid for.
func HoldNotActive(holdID int64, status HoldStatus, expiresAt time.Time) *Error {
	msg := "This hold is no longer active."
	if status == HoldConverted {
		msg = "These seats have already been bought."
	}
	return &Error{
		Code:    CodeHoldNotActive,
		Message: msg,
		Details: HoldStateDetails{HoldID: holdID, HoldStatus: status, ExpiresAt: expiresAt.UTC()},
	}
}

// PaymentInProgressDetails is the details shape of PAYMENT_IN_PROGRESS.
type PaymentInProgressDetails struct {
	PaymentID     int64  `json:"payment_id"`
	PaymentStatus string `json:"payment_status"`
}

// PaymentInProgress builds the 409 for an action refused because a payment is still live.
func PaymentInProgress(paymentID int64, paymentStatus string) *Error {
	return &Error{
		Code:    CodePaymentInProgress,
		Message: "A payment for these seats is in progress. Wait for it to finish.",
		Details: PaymentInProgressDetails{PaymentID: paymentID, PaymentStatus: paymentStatus},
	}
}
