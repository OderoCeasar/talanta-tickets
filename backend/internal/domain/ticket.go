package domain

import "fmt"

// TicketStatus is the stored state of a ticket.
type TicketStatus string

const (
	TicketValid       TicketStatus = "VALID"
	TicketUsed        TicketStatus = "USED"
	TicketVoid        TicketStatus = "VOID"
	TicketTransferred TicketStatus = "TRANSFERRED"
)

// ticketMoves is every legal change of a ticket's status.
//
//   - VALID -> USED is the first admitted scan at a gate.
//   - VALID -> VOID and USED -> VOID are a refund or a cancelled event. A used ticket is
//     voided too: the booking it belongs to is being paid back, and the seat's one live
//     ticket slot must be free again.
//   - VOID and TRANSFERRED are final. In particular nothing brings a void ticket back: a
//     seat that is sold again gets a new ticket with a new ID.
var ticketMoves = map[TicketStatus][]TicketStatus{
	TicketValid:       {TicketUsed, TicketVoid, TicketTransferred},
	TicketUsed:        {TicketVoid},
	TicketVoid:        {},
	TicketTransferred: {},
}

// TransitionTicket is the one function every change of a ticket's status goes through. It
// returns nil when the move is legal. Like TransitionHold, an illegal move is a fault in the
// caller, so the error is not a domain error.
func TransitionTicket(from, to TicketStatus) error {
	for _, next := range ticketMoves[from] {
		if next == to {
			return nil
		}
	}
	return fmt.Errorf("illegal ticket transition %s -> %s", from, to)
}

// TicketStatusesThatCanBecome lists the statuses a ticket may be moved to `to` from, in a
// fixed order. The statements that void tickets take their WHERE clause from it, so the SQL
// and the table above cannot disagree.
func TicketStatusesThatCanBecome(to TicketStatus) []string {
	var out []string
	for _, from := range []TicketStatus{TicketValid, TicketUsed, TicketVoid, TicketTransferred} {
		if TransitionTicket(from, to) == nil {
			out = append(out, string(from))
		}
	}
	return out
}

// IsLive reports whether a ticket still occupies its seat: VALID or USED. It mirrors the
// tickets_live_seat_idx unique index, which allows one live ticket per seat.
func (s TicketStatus) IsLive() bool { return s == TicketValid || s == TicketUsed }

// MaxGateGroupLength is the longest gate group a ticket can carry: the signed QR payload
// has a fixed 16-byte field for it (docs/TICKET_FORMAT.md).
const MaxGateGroupLength = 16
