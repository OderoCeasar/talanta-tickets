package domain

// SeatStatus is the stored inventory status of one seat for one event.
type SeatStatus string

const (
	SeatAvailable SeatStatus = "AVAILABLE"
	SeatHeld      SeatStatus = "HELD"
	SeatSold      SeatStatus = "SOLD"
	SeatBlocked   SeatStatus = "BLOCKED"
)

// SeatCode is the one-letter seat status the seat map sends.
type SeatCode string

const (
	SeatCodeAvailable SeatCode = "A"
	SeatCodeHeld      SeatCode = "H"
	SeatCodeMine      SeatCode = "M"
	SeatCodeSold      SeatCode = "S"
	SeatCodeBlocked   SeatCode = "B"
)

// SeatCodeFor is the one place a stored seat becomes what a viewer is shown.
//
//   - A hold whose time has run out is available, whether or not the sweeper has cleared it:
//     the hold statement reclaims such a seat, so showing it as held would hide a seat that
//     can be bought.
//   - A live hold is "M" only to the viewer who owns it. To everyone else, signed in or not,
//     it is "H". Nothing about the holder leaves the server.
//   - An unknown status is shown as blocked: a seat we cannot explain is not offered.
func SeatCodeFor(status SeatStatus, holdLapsed, heldByViewer bool) SeatCode {
	switch status {
	case SeatAvailable:
		return SeatCodeAvailable
	case SeatHeld:
		if holdLapsed {
			return SeatCodeAvailable
		}
		if heldByViewer {
			return SeatCodeMine
		}
		return SeatCodeHeld
	case SeatSold:
		return SeatCodeSold
	}
	return SeatCodeBlocked
}

// SeatTypeCode is the number the seat map sends for a seat type: 0 STANDARD, 1 ACCESSIBLE,
// 2 COMPANION, 3 RESTRICTED_VIEW. An unknown type is sent as standard.
func SeatTypeCode(seatType string) int {
	switch seatType {
	case "ACCESSIBLE":
		return 1
	case "COMPANION":
		return 2
	case "RESTRICTED_VIEW":
		return 3
	}
	return 0
}

// SeatCounts is how many seats are in each state. A lapsed hold counts as available.
type SeatCounts struct {
	Sold      int
	Held      int
	Available int
	Blocked   int
}

// Total is every seat counted.
func (c SeatCounts) Total() int { return c.Sold + c.Held + c.Available + c.Blocked }

// Sellable is every seat that is or was for sale: everything but the blocked ones.
func (c SeatCounts) Sellable() int { return c.Sold + c.Held + c.Available }

// Add returns the sum of two counts.
func (c SeatCounts) Add(o SeatCounts) SeatCounts {
	return SeatCounts{Sold: c.Sold + o.Sold, Held: c.Held + o.Held, Available: c.Available + o.Available, Blocked: c.Blocked + o.Blocked}
}

// GateGroup names the gates a seat in a stand may be entered by: "<stand code>-<tier>".
func GateGroup(standCode, tier string) string { return standCode + "-" + tier }

// PaymentFailureReason turns a payment's status and the provider's result code into the
// stable value the contract calls failure_reason. Nil while the payment is live and when it
// succeeded. The codes are Daraja's STK result codes.
func PaymentFailureReason(status string, resultCode *int32) *string {
	if status != "FAILED" && status != "TIMEOUT" {
		return nil
	}
	reason := "other"
	switch {
	case status == "TIMEOUT":
		reason = "timeout"
	case resultCode == nil:
	case *resultCode == 1:
		reason = "insufficient_funds"
	case *resultCode == 2001:
		reason = "wrong_pin"
	case *resultCode == 1032:
		reason = "cancelled"
	case *resultCode == 1037:
		reason = "timeout"
	}
	return &reason
}
