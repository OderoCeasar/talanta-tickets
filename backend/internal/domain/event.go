package domain

import (
	"fmt"
	"time"
)

// EventStatus is the stored state of an event.
type EventStatus string

const (
	EventDraft     EventStatus = "DRAFT"
	EventPublished EventStatus = "PUBLISHED"
	EventOnSale    EventStatus = "ON_SALE"
	EventSoldOut   EventStatus = "SOLD_OUT"
	EventCancelled EventStatus = "CANCELLED"
	EventCompleted EventStatus = "COMPLETED"
)

// EventStatuses lists every status, in the order of the contract's enum.
var EventStatuses = []EventStatus{EventDraft, EventPublished, EventOnSale, EventSoldOut, EventCancelled, EventCompleted}

// PublicEventStatuses are the statuses the public event list shows.
var PublicEventStatuses = []EventStatus{EventPublished, EventOnSale, EventSoldOut}

// Valid reports whether s is one of the six statuses.
func (s EventStatus) Valid() bool {
	for _, v := range EventStatuses {
		if s == v {
			return true
		}
	}
	return false
}

// HasInventory reports whether an event in this status has been published at some point and
// so may have event_seats rows.
func (s EventStatus) HasInventory() bool { return s != EventDraft }

// IsPublished reports whether the event is live for fans: published and neither cancelled
// nor over.
func (s EventStatus) IsPublished() bool {
	return s == EventPublished || s == EventOnSale || s == EventSoldOut
}

// VisibleToPublic reports whether a public read may return the event at all. A draft is the
// organiser's business only; cancelled and completed events stay readable so a ticket
// holder's deep link keeps working.
func (s EventStatus) VisibleToPublic() bool { return s.Valid() && s != EventDraft }

// Editable reports whether the event's details and categories may still be changed.
func (s EventStatus) Editable() bool { return s != EventCancelled && s != EventCompleted }

// eventMoves is every legal change of an event's stored status. Cancelled and completed are
// final. A draft never becomes sold out or completed without being published first.
var eventMoves = map[EventStatus][]EventStatus{
	EventDraft:     {EventPublished, EventOnSale, EventCancelled},
	EventPublished: {EventOnSale, EventSoldOut, EventCancelled, EventCompleted},
	EventOnSale:    {EventPublished, EventSoldOut, EventCancelled, EventCompleted},
	EventSoldOut:   {EventOnSale, EventPublished, EventCancelled, EventCompleted},
	EventCancelled: {},
	EventCompleted: {},
}

// TransitionEvent is the one function every change of an event's status goes through. It
// returns nil when the move is legal and INVALID_STATE_TRANSITION, carrying the current
// status, when it is not. Moving to the status the event already has is not a move and is
// refused too: callers that treat a repeat as a no-op (publish, cancel) check for it first.
func TransitionEvent(from, to EventStatus) error {
	for _, next := range eventMoves[from] {
		if next == to {
			return nil
		}
	}
	return &Error{
		Code:    CodeInvalidStateTransition,
		Message: fmt.Sprintf("An event that is %s cannot become %s.", from, to),
		Details: EventStateDetails{CurrentStatus: from},
	}
}

// EventStateDetails is the details shape of INVALID_STATE_TRANSITION and EVENT_NOT_EDITABLE.
type EventStateDetails struct {
	CurrentStatus EventStatus `json:"current_status"`
}

// EventNotEditable builds the EVENT_NOT_EDITABLE error for an event in the given status.
func EventNotEditable(current EventStatus, message string) *Error {
	return &Error{Code: CodeEventNotEditable, Message: message, Details: EventStateDetails{CurrentStatus: current}}
}

// EventBusy builds the EVENT_BUSY error: the event (or, for a layout edit, an event at the
// venue) is locked by a publish, a cancel or another edit, and this request gave up waiting
// for it. Nothing was changed and the same request can simply be sent again.
func EventBusy(message string) *Error {
	if message == "" {
		message = "This event is being published or changed. Try again in a moment."
	}
	return &Error{Code: CodeEventBusy, Message: message}
}

// SalesOpenAt reports whether now is inside the sales window [open, close). A missing open
// time means "already open"; a missing close time means "never closes".
func SalesOpenAt(open, closeAt *time.Time, now time.Time) bool {
	if open != nil && now.Before(*open) {
		return false
	}
	if closeAt != nil && !now.Before(*closeAt) {
		return false
	}
	return true
}

// PublishStatus is the status a draft takes when it is published at `now`: ON_SALE when the
// sales window is open at that moment, PUBLISHED (visible, not yet buyable) otherwise.
func PublishStatus(open, closeAt *time.Time, now time.Time) EventStatus {
	if SalesOpenAt(open, closeAt, now) {
		return EventOnSale
	}
	return EventPublished
}

// EffectiveEventStatus is the status an event really has at `now`, worked out from the
// stored status and the sales window. Nothing flips PUBLISHED to ON_SALE when sales open, or
// back when they close, so every reader derives it here instead of trusting the stored
// value: a published event is ON_SALE exactly while its window is open. Draft, sold out,
// cancelled and completed are what they are stored as.
//
// The SQL in store/queries/events.sql (effective status filter) repeats this rule; change
// both together.
func EffectiveEventStatus(stored EventStatus, open, closeAt *time.Time, now time.Time) EventStatus {
	if stored != EventPublished && stored != EventOnSale {
		return stored
	}
	return PublishStatus(open, closeAt, now)
}

// Limits of an event, as in the contract.
const (
	EventTitleMaxLength       = 200
	EventCompetitionMaxLength = 200
	EventTeamMaxLength        = 100
	PosterURLMaxLength        = 2048
	MaxTicketsPerUserLimit    = 50
	DefaultMaxTicketsPerUser  = 6
	CategoryNameMaxLength     = 60
	CategoryDescMaxLength     = 500
	TemplateNameMaxLength     = 80
	BlockCodeMaxLength        = 12
	BlockLabelMaxLength       = 60
	MaxAssignBlocks           = 1000
	MaxAssignRows             = 5000
)

// Currency is the only currency the system sells in.
const Currency = "KES"

// ReadinessCode names one kind of thing that stops an event being published.
type ReadinessCode string

const (
	ReadinessNoSeats              ReadinessCode = "NO_SEATS"
	ReadinessNoCategories         ReadinessCode = "NO_CATEGORIES"
	ReadinessSeatsWithoutCategory ReadinessCode = "SEATS_WITHOUT_CATEGORY"
	ReadinessCategoryWithoutPrice ReadinessCode = "CATEGORY_WITHOUT_PRICE"
	ReadinessSalesWindowMissing   ReadinessCode = "SALES_WINDOW_MISSING"
	ReadinessSalesWindowInvalid   ReadinessCode = "SALES_WINDOW_INVALID"
	ReadinessKickoffInPast        ReadinessCode = "KICKOFF_IN_PAST"
)

// ReadinessProblem is one thing that stops an event being published. Fields that do not
// apply to the code are nil and sent as null.
type ReadinessProblem struct {
	Code         ReadinessCode `json:"code"`
	Message      string        `json:"message"`
	BlockID      *int64        `json:"block_id"`
	BlockCode    *string       `json:"block_code"`
	CategoryID   *int64        `json:"category_id"`
	CategoryName *string       `json:"category_name"`
	SeatCount    *int          `json:"seat_count"`
}

// NotReadyDetails is the details shape of EVENT_NOT_READY.
type NotReadyDetails struct {
	Problems []ReadinessProblem `json:"problems"`
}

// EventNotReady builds the EVENT_NOT_READY error from the readiness problems.
func EventNotReady(problems []ReadinessProblem) *Error {
	things := "things are"
	if len(problems) == 1 {
		things = "thing is"
	}
	return &Error{
		Code:    CodeEventNotReady,
		Message: fmt.Sprintf("This event cannot be published yet. %d %s missing.", len(problems), things),
		Details: NotReadyDetails{Problems: problems},
	}
}

// SalesWindowProblems checks the parts of readiness that need no database: the sales window
// and the kickoff time.
func SalesWindowProblems(kickoff time.Time, open, closeAt *time.Time, now time.Time) []ReadinessProblem {
	var out []ReadinessProblem
	switch {
	case open == nil || closeAt == nil:
		out = append(out, ReadinessProblem{Code: ReadinessSalesWindowMissing, Message: "Set when ticket sales open and close."})
	case !closeAt.After(*open):
		out = append(out, ReadinessProblem{Code: ReadinessSalesWindowInvalid, Message: "Ticket sales must close after they open."})
	case closeAt.After(kickoff):
		out = append(out, ReadinessProblem{Code: ReadinessSalesWindowInvalid, Message: "Ticket sales must close at or before kickoff."})
	}
	if !kickoff.After(now) {
		out = append(out, ReadinessProblem{Code: ReadinessKickoffInPast, Message: "The kickoff time has already passed."})
	}
	return out
}

// PublishedEventProblems checks an edit of an event that has already been published, on the
// merged row, against the same rules publish enforced (SalesWindowProblems), and names the
// field to blame for each. Without it an edit could undo readiness: clear sales_close_at so
// sales never close, or move the kickoff into the past.
//
// kickoffChanged says whether the edit moves the kickoff. An event whose kickoff has simply
// come and gone must stay editable (a poster, a typo in the title), so "kickoff in the past"
// is only a problem when this edit is what puts it there.
func PublishedEventProblems(kickoff time.Time, open, closeAt *time.Time, kickoffChanged bool, now time.Time) []FieldError {
	var out []FieldError
	if open == nil {
		out = append(out, FieldError{Field: "sales_open_at", Rule: "required", Message: "A published event must say when ticket sales open."})
	}
	if closeAt == nil {
		out = append(out, FieldError{Field: "sales_close_at", Rule: "required", Message: "A published event must say when ticket sales close."})
	}
	if open != nil && closeAt != nil {
		switch {
		case !closeAt.After(*open):
			out = append(out, FieldError{Field: "sales_close_at", Rule: "after_open", Message: "Sales must close after they open."})
		case closeAt.After(kickoff):
			out = append(out, FieldError{Field: "sales_close_at", Rule: "before_kickoff", Message: "Sales must close at or before kickoff."})
		}
	}
	if kickoffChanged && !kickoff.After(now) {
		out = append(out, FieldError{Field: "kickoff_at", Rule: "future", Message: "The kickoff of a published event cannot be moved into the past."})
	}
	return out
}

// SalesStartedDetails is the details shape of CONFIRMATION_REQUIRED.
type SalesStartedDetails struct {
	Sold int `json:"sold"`
	Held int `json:"held"`
}

// CategoryInUseDetails is the details shape of CATEGORY_IN_USE. Seats is every inventory row
// of the category, so "the event is published with it" (Seats > 0, Sold and Held zero) can
// be told from "tickets are sold or held in it".
type CategoryInUseDetails struct {
	Seats int `json:"seats"`
	Sold  int `json:"sold"`
	Held  int `json:"held"`
}

// InventoryDetails is the details shape of BLOCK_HAS_INVENTORY.
type InventoryDetails struct {
	EventIDs []int64 `json:"event_ids"`
}

// BlockHasInventory builds the BLOCK_HAS_INVENTORY error.
func BlockHasInventory(eventIDs []int64, message string) *Error {
	if eventIDs == nil {
		eventIDs = []int64{}
	}
	return &Error{Code: CodeBlockHasInventory, Message: message, Details: InventoryDetails{EventIDs: eventIDs}}
}

// AlreadyExists builds the ALREADY_EXISTS error naming the field whose value is taken.
func AlreadyExists(field, message string) *Error {
	return &Error{
		Code:    CodeAlreadyExists,
		Message: message,
		Details: ValidationDetails{Fields: []FieldError{{Field: field, Rule: "unique", Message: message}}},
	}
}

// VenueMismatch builds the VENUE_MISMATCH error. fields may be empty, giving null details.
func VenueMismatch(message string, fields ...FieldError) *Error {
	e := &Error{Code: CodeVenueMismatch, Message: message}
	if len(fields) > 0 {
		e.Details = ValidationDetails{Fields: fields}
	}
	return e
}
