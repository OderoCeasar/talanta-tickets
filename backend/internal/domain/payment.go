package domain

import (
	"fmt"
	"time"
)

// BookingStatus is the stored state of a booking.
type BookingStatus string

const (
	BookingPendingPayment BookingStatus = "PENDING_PAYMENT"
	BookingPaid           BookingStatus = "PAID"
	BookingFailed         BookingStatus = "FAILED"
	BookingExpired        BookingStatus = "EXPIRED"
	BookingCancelled      BookingStatus = "CANCELLED"
	BookingRefundRequired BookingStatus = "REFUND_REQUIRED"
	BookingRefunded       BookingStatus = "REFUNDED"
)

// BookingStatuses lists every status, in the order of the contract's enum.
var BookingStatuses = []BookingStatus{
	BookingPendingPayment, BookingPaid, BookingFailed, BookingExpired, BookingCancelled, BookingRefundRequired, BookingRefunded,
}

// IsLive reports whether a booking still occupies its hold: awaiting payment or paid. It
// mirrors the bookings_live_hold_key unique index, which allows one live booking per hold.
func (s BookingStatus) IsLive() bool { return s == BookingPendingPayment || s == BookingPaid }

// bookingMoves is every legal change of a booking's status.
//
//   - PENDING_PAYMENT ends as PAID (the payment sold the seats), EXPIRED (the hold ran out),
//     CANCELLED (the fan released the hold, or the event was cancelled), FAILED (kept for a
//     payment that can never be retried; nothing writes it today) or REFUND_REQUIRED (money
//     arrived but the seats could not be given, or the amount was wrong).
//   - PAID has exactly one way out: REFUND_REQUIRED, when its event is cancelled. Nothing
//     un-sells a booking: there is no PAID -> PENDING_PAYMENT, EXPIRED or CANCELLED.
//   - EXPIRED, CANCELLED and FAILED may still become REFUND_REQUIRED: M-Pesa can report a
//     success after we gave up on the booking, and that money must be recordable.
//   - REFUND_REQUIRED becomes REFUNDED when every refund is paid. REFUNDED goes back to
//     REFUND_REQUIRED only if yet another payment for the booking turns up afterwards.
var bookingMoves = map[BookingStatus][]BookingStatus{
	BookingPendingPayment: {BookingPaid, BookingFailed, BookingExpired, BookingCancelled, BookingRefundRequired},
	BookingPaid:           {BookingRefundRequired},
	BookingFailed:         {BookingRefundRequired},
	BookingExpired:        {BookingRefundRequired},
	BookingCancelled:      {BookingRefundRequired},
	BookingRefundRequired: {BookingRefunded},
	BookingRefunded:       {BookingRefundRequired},
}

// TransitionBooking is the one function every change of a booking's status goes through.
// Callers pass the status they READ from the row under its lock, never a constant, so the
// function can actually refuse. Like TransitionHold, an illegal move is a fault in the
// caller (or a row another flow got to first) and is not a domain error.
func TransitionBooking(from, to BookingStatus) error {
	for _, next := range bookingMoves[from] {
		if next == to {
			return nil
		}
	}
	return fmt.Errorf("illegal booking transition %s -> %s", from, to)
}

// PaymentStatus is the stored state of a payment.
type PaymentStatus string

const (
	PaymentInitiated PaymentStatus = "INITIATED"
	PaymentPending   PaymentStatus = "PENDING"
	PaymentSuccess   PaymentStatus = "SUCCESS"
	PaymentFailed    PaymentStatus = "FAILED"
	PaymentTimeout   PaymentStatus = "TIMEOUT"
	PaymentReversed  PaymentStatus = "REVERSED"
)

// IsLive reports whether a payment is still in flight. It mirrors the
// payments_live_booking_idx unique index, which allows one live payment per booking.
func (s PaymentStatus) IsLive() bool { return s == PaymentInitiated || s == PaymentPending }

// paymentMoves is every legal change of a payment's status.
//
//   - INITIATED is "row written, M-Pesa not asked yet". It becomes PENDING when M-Pesa takes
//     the request, FAILED when it refuses, TIMEOUT when the request was never sent (a crash).
//   - PENDING ends as SUCCESS, FAILED or TIMEOUT.
//   - TIMEOUT is our verdict ("we stopped waiting"), not M-Pesa's, so M-Pesa may still
//     overrule it: a late result moves it to SUCCESS or FAILED. That is how money that
//     arrived after we gave up is still recorded.
//   - FAILED has exactly one way out: SUCCESS, "money received after all". A failure can be
//     recorded on the word of a callback, or of a query answer that later turns out not to
//     have been final; if M-Pesa then confirms the payment was paid, that money must be
//     recorded and end in tickets or a refund. Nothing else leaves FAILED: a second failure,
//     or a timeout, for a FAILED payment is refused.
//   - SUCCESS is final (SUCCESS -> REVERSED is the only move left): a failure reported after
//     a success is ignored. This is what makes a repeated callback harmless: the second one
//     asks for SUCCESS -> SUCCESS and is refused.
var paymentMoves = map[PaymentStatus][]PaymentStatus{
	PaymentInitiated: {PaymentPending, PaymentFailed, PaymentTimeout},
	PaymentPending:   {PaymentSuccess, PaymentFailed, PaymentTimeout},
	PaymentTimeout:   {PaymentSuccess, PaymentFailed},
	PaymentSuccess:   {PaymentReversed},
	PaymentFailed:    {PaymentSuccess},
	PaymentReversed:  {},
}

// TransitionPayment is the one function every change of a payment's status goes through,
// asked with the status read from the row under its lock.
func TransitionPayment(from, to PaymentStatus) error {
	for _, next := range paymentMoves[from] {
		if next == to {
			return nil
		}
	}
	return fmt.Errorf("illegal payment transition %s -> %s", from, to)
}

// Daraja's STK result codes, as far as the app tells them apart.
const (
	ResultSuccess           = 0
	ResultInsufficientFunds = 1
	ResultCancelledByUser   = 1032
	ResultTimeout           = 1037
	ResultExpired           = 1019
	ResultWrongPIN          = 2001
)

// finalSTKFailures are the STK push result codes that mean "this prompt is over and no money
// moved". The list is the result-code table of Safaricom's Daraja documentation for M-Pesa
// Express (developer.safaricom.co.ke, APIs > M-Pesa Express > "Result codes" / "STK push
// error codes"), written down from that table; it could not be checked against a live
// Daraja from here (docs/STATUS.md lists it under what needs real credentials).
//
//	1     the balance is insufficient for the transaction
//	1001  unable to lock subscriber: another transaction is in progress for this customer
//	1019  the transaction expired before the customer answered
//	1025  an error occurred while sending the push request
//	1032  the request was cancelled by the user
//	1037  DS timeout: the customer's phone could not be reached
//	2001  the initiator information is invalid: the customer entered a wrong PIN
//	9999  an error occurred while sending the push request
//
// Deliberately not on it: 4999 ("the transaction is still under processing"), which is not
// a result at all, and any code this list does not know. Those are "not final": the payment
// stays open and M-Pesa is asked again. A code wrongly left off this list costs a slower
// answer (the payment waits for reconciliation to time it out, with an alert); a code
// wrongly put on it would close a payment the fan is about to pay for.
var finalSTKFailures = map[int]bool{
	ResultInsufficientFunds: true,
	1001:                    true,
	ResultExpired:           true,
	1025:                    true,
	ResultCancelledByUser:   true,
	ResultTimeout:           true,
	ResultWrongPIN:          true,
	9999:                    true,
}

// IsFinalSTKFailure reports whether an STK result code is a known final failure. Zero
// (success) and every code not on the documented list are not.
func IsFinalSTKFailure(code int) bool { return finalSTKFailures[code] }

// The contract's PaymentFailureReason values.
const (
	FailureInsufficientFunds = "insufficient_funds"
	FailureWrongPIN          = "wrong_pin"
	FailureCancelled         = "cancelled"
	FailureTimeout           = "timeout"
	FailureWrongAmount       = "wrong_amount"
	FailureOther             = "other"
)

// PaymentFailureReason turns a payment's status and the provider's result code into the
// stable value the contract calls failure_reason. Nil while the payment is live and when it
// succeeded.
//
// A FAILED payment whose result code is 0 is the wrong-amount case: M-Pesa itself said "paid"
// and named an amount that was not the booking's total, so we refused it (and queued a
// refund). No other path stores FAILED with a success code.
func PaymentFailureReason(status string, resultCode *int32) *string {
	if status != string(PaymentFailed) && status != string(PaymentTimeout) {
		return nil
	}
	reason := FailureOther
	switch {
	case status == string(PaymentTimeout):
		reason = FailureTimeout
	case resultCode == nil:
	case *resultCode == ResultSuccess:
		reason = FailureWrongAmount
	case *resultCode == ResultInsufficientFunds:
		reason = FailureInsufficientFunds
	case *resultCode == ResultWrongPIN:
		reason = FailureWrongPIN
	case *resultCode == ResultCancelledByUser:
		reason = FailureCancelled
	case *resultCode == ResultTimeout, *resultCode == ResultExpired:
		reason = FailureTimeout
	}
	return &reason
}

// BookingStateDetails is the details shape of BOOKING_NOT_PAYABLE and HOLD_ALREADY_BOOKED.
type BookingStateDetails struct {
	BookingReference string        `json:"booking_reference"`
	BookingStatus    BookingStatus `json:"booking_status"`
}

// BookingNotPayable builds the 409 for a payment asked for a booking that is not awaiting one.
func BookingNotPayable(reference string, status BookingStatus) *Error {
	msg := "This booking can no longer be paid for."
	if status == BookingPaid {
		msg = "This booking is already paid."
	}
	return &Error{Code: CodeBookingNotPayable, Message: msg, Details: BookingStateDetails{BookingReference: reference, BookingStatus: status}}
}

// HoldAlreadyBooked builds the 409 for a second booking asked of a hold that has a live one.
func HoldAlreadyBooked(reference string, status BookingStatus) *Error {
	return &Error{
		Code:    CodeHoldAlreadyBooked,
		Message: "These seats already have a booking.",
		Details: BookingStateDetails{BookingReference: reference, BookingStatus: status},
	}
}

// HoldTooShort builds the HOLD_EXPIRED answer for a payment that cannot start because the
// hold has too little time left, or no longer has every seat of the booking. The hold may
// still read ACTIVE for a little longer; the fan has to start again from the map either way.
func HoldTooShort(holdID int64, status HoldStatus, expiresAt time.Time) *Error {
	return &Error{
		Code:    CodeHoldExpired,
		Message: "There is not enough time left on your hold to pay. Choose your seats again.",
		Details: HoldStateDetails{HoldID: holdID, HoldStatus: status, ExpiresAt: expiresAt.UTC()},
	}
}

// IdempotencyKeyReused builds the 422 for a key that already made a booking from another hold.
func IdempotencyKeyReused() *Error {
	return E(CodeIdempotencyKeyReused, "This checkout key was already used for different seats.")
}

// PaymentUnknownIDMessage is what a payment says (result_desc) while it is live with no
// provider ID: the request may have reached M-Pesa, and we are finding out.
const PaymentUnknownIDMessage = "We are checking with M-Pesa whether the request went through. Do not pay again yet."

// PaymentProviderUnavailable builds the 502 for an STK request M-Pesa did not take.
func PaymentProviderUnavailable(cause error) *Error {
	return &Error{Code: CodePaymentProviderUnavailable, Message: "M-Pesa is not responding. Try again in a moment.", Err: cause}
}
