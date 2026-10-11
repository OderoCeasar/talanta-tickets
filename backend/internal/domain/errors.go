// Package domain holds the types, roles and state rules of the ticketing system. It knows
// nothing about the database or HTTP.
package domain

import (
	"errors"
	"fmt"
)

// Code is the stable, machine-readable name of an error. The full list, with the HTTP status
// each one maps to, is in docs/API_NOTES.md; internal/httpapi owns that mapping.
type Code string

const (
	CodeValidationFailed           Code = "VALIDATION_FAILED"
	CodeIdempotencyKeyRequired     Code = "IDEMPOTENCY_KEY_REQUIRED"
	CodeUnauthenticated            Code = "UNAUTHENTICATED"
	CodeTokenExpired               Code = "TOKEN_EXPIRED"
	CodeOTPInvalid                 Code = "OTP_INVALID"
	CodeOTPExpired                 Code = "OTP_EXPIRED"
	CodeInvalidCredentials         Code = "INVALID_CREDENTIALS"
	CodeMFATokenInvalid            Code = "MFA_TOKEN_INVALID"
	CodeTOTPInvalid                Code = "TOTP_INVALID"
	CodeRefreshTokenInvalid        Code = "REFRESH_TOKEN_INVALID"
	CodeRefreshTokenReused         Code = "REFRESH_TOKEN_REUSED"
	CodeForbidden                  Code = "FORBIDDEN"
	CodeMockNotAllowed             Code = "MOCK_NOT_ALLOWED"
	CodeNotFound                   Code = "NOT_FOUND"
	CodeInvitationInvalid          Code = "INVITATION_INVALID"
	CodeInvitationExpired          Code = "INVITATION_EXPIRED"
	CodeSeatsUnavailable           Code = "SEATS_UNAVAILABLE"
	CodeNoAdjacentSeats            Code = "NO_ADJACENT_SEATS"
	CodeEventNotOnSale             Code = "EVENT_NOT_ON_SALE"
	CodeSalesNotOpen               Code = "SALES_NOT_OPEN"
	CodeSalesClosed                Code = "SALES_CLOSED"
	CodeHoldExpired                Code = "HOLD_EXPIRED"
	CodeHoldNotActive              Code = "HOLD_NOT_ACTIVE"
	CodeHoldAlreadyBooked          Code = "HOLD_ALREADY_BOOKED"
	CodeBookingNotPayable          Code = "BOOKING_NOT_PAYABLE"
	CodePaymentInProgress          Code = "PAYMENT_IN_PROGRESS"
	CodeEventNotReady              Code = "EVENT_NOT_READY"
	CodeInvalidStateTransition     Code = "INVALID_STATE_TRANSITION"
	CodeEventNotEditable           Code = "EVENT_NOT_EDITABLE"
	CodeBlockHasInventory          Code = "BLOCK_HAS_INVENTORY"
	CodeCategoryInUse              Code = "CATEGORY_IN_USE"
	CodeConfirmationRequired       Code = "CONFIRMATION_REQUIRED"
	CodeAlreadyExists              Code = "ALREADY_EXISTS"
	CodeEmailAlreadyRegistered     Code = "EMAIL_ALREADY_REGISTERED"
	CodeInvitationAlreadyPending   Code = "INVITATION_ALREADY_PENDING"
	CodeInvitationNotPending       Code = "INVITATION_NOT_PENDING"
	CodeTOTPAlreadyEnabled         Code = "TOTP_ALREADY_ENABLED"
	CodeTOTPNotEnabled             Code = "TOTP_NOT_ENABLED"
	CodeTOTPSetupRequired          Code = "TOTP_SETUP_REQUIRED"
	CodePayloadTooLarge            Code = "PAYLOAD_TOO_LARGE"
	CodeTicketLimitExceeded        Code = "TICKET_LIMIT_EXCEEDED"
	CodeIdempotencyKeyReused       Code = "IDEMPOTENCY_KEY_REUSED"
	CodeVenueMismatch              Code = "VENUE_MISMATCH"
	CodePasswordIncorrect          Code = "PASSWORD_INCORRECT"
	CodeTOTPCodeIncorrect          Code = "TOTP_CODE_INCORRECT"
	CodeRateLimited                Code = "RATE_LIMITED"
	CodeOTPAttemptsExceeded        Code = "OTP_ATTEMPTS_EXCEEDED"
	CodeInternal                   Code = "INTERNAL"
	CodePaymentProviderUnavailable Code = "PAYMENT_PROVIDER_UNAVAILABLE"
)

// Error is an expected failure: something a caller did, or a state the system is in, that
// has a code a client can switch on. Anything that is not an *Error is a fault on our side
// and reaches the client as INTERNAL with nothing of its text.
type Error struct {
	Code Code
	// Message is plain English and safe to show to the user.
	Message string
	// Details is nil or one of the fixed shapes in the contract (ValidationDetails, …).
	Details any
	// Err is the cause, kept for logs only.
	Err error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// Is makes errors.Is(err, domain.ErrNotFound) match any error with the same code, whatever
// its message or details.
func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == e.Code
}

// E builds an Error with no details.
func E(code Code, message string) *Error { return &Error{Code: code, Message: message} }

// CodeOf returns err's code, or CodeInternal when err is not a domain error.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// Sentinels for errors.Is and for returning as they are. Each call site may also build its
// own *Error with the same code and a more specific message.
var (
	ErrUnauthenticated          = E(CodeUnauthenticated, "Sign in to continue.")
	ErrTokenExpired             = E(CodeTokenExpired, "Your session has expired.")
	ErrForbidden                = E(CodeForbidden, "You do not have access to this.")
	ErrNotFound                 = E(CodeNotFound, "We could not find that.")
	ErrPayloadTooLarge          = E(CodePayloadTooLarge, "The request is too large.")
	ErrOTPInvalid               = E(CodeOTPInvalid, "That code is not right.")
	ErrOTPExpired               = E(CodeOTPExpired, "That code has expired. Request a new one.")
	ErrInvalidCredentials       = E(CodeInvalidCredentials, "The email or password is not right.")
	ErrMFATokenInvalid          = E(CodeMFATokenInvalid, "This sign-in has expired. Enter your email and password again.")
	ErrTOTPInvalid              = E(CodeTOTPInvalid, "That authenticator code is not right.")
	ErrRefreshTokenInvalid      = E(CodeRefreshTokenInvalid, "Your session has ended. Sign in again.")
	ErrRefreshTokenReused       = E(CodeRefreshTokenReused, "Your session was ended for safety. Sign in again.")
	ErrInvitationInvalid        = E(CodeInvitationInvalid, "This invitation link is not valid.")
	ErrInvitationExpired        = E(CodeInvitationExpired, "This invitation has expired. Ask an admin to send it again.")
	ErrEmailAlreadyRegistered   = E(CodeEmailAlreadyRegistered, "An account with this email already exists.")
	ErrInvitationAlreadyPending = E(CodeInvitationAlreadyPending, "This email already has a pending invitation. Resend or revoke it.")
	ErrInvitationNotPending     = E(CodeInvitationNotPending, "This invitation is no longer pending.")
	ErrTOTPAlreadyEnabled       = E(CodeTOTPAlreadyEnabled, "Two-step sign-in is already on.")
	ErrTOTPNotEnabled           = E(CodeTOTPNotEnabled, "Two-step sign-in is not on.")
	ErrTOTPSetupRequired        = E(CodeTOTPSetupRequired, "Start the setup before entering a code.")
	ErrPasswordIncorrect        = E(CodePasswordIncorrect, "The password is not right.")
	ErrTOTPCodeIncorrect        = E(CodeTOTPCodeIncorrect, "That authenticator code is not right.")
)

// FieldError is one bad field of a request.
type FieldError struct {
	// Field is the JSON path in the body (seat_ids[2]) or the query, path or header name.
	Field string `json:"field"`
	// Rule is the rule that failed: required, e164, min, max, …
	Rule string `json:"rule"`
	// Message is plain English, to show next to the field.
	Message string `json:"message"`
}

// ValidationDetails is the details shape of VALIDATION_FAILED and its relatives.
type ValidationDetails struct {
	Fields []FieldError `json:"fields"`
}

// Invalid builds a VALIDATION_FAILED error for the given fields.
func Invalid(fields ...FieldError) *Error {
	return &Error{
		Code:    CodeValidationFailed,
		Message: "Some fields are not valid.",
		Details: ValidationDetails{Fields: fields},
	}
}

// InvalidField is Invalid for a single field.
func InvalidField(field, rule, message string) *Error {
	return Invalid(FieldError{Field: field, Rule: rule, Message: message})
}

// RateLimitDetails is the details shape of RATE_LIMITED and OTP_ATTEMPTS_EXCEEDED. The HTTP
// layer also sends the value as the Retry-After header.
type RateLimitDetails struct {
	RetryAfterSeconds int `json:"retry_after_seconds"`
}

// RateLimited builds a RATE_LIMITED error. Seconds below one are raised to one: Retry-After
// must be at least a second.
func RateLimited(retryAfterSeconds int) *Error {
	if retryAfterSeconds < 1 {
		retryAfterSeconds = 1
	}
	return &Error{
		Code:    CodeRateLimited,
		Message: fmt.Sprintf("Too many requests. Try again in %d seconds.", retryAfterSeconds),
		Details: RateLimitDetails{RetryAfterSeconds: retryAfterSeconds},
	}
}
