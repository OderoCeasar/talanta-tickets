package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

// statusByCode is the one place an error code becomes an HTTP status. It mirrors the table
// in docs/API_NOTES.md; a test checks it against the ErrorCode enum of the OpenAPI file, so
// a code cannot be added to one and forgotten in the other.
var statusByCode = map[domain.Code]int{
	domain.CodeValidationFailed:       http.StatusBadRequest,
	domain.CodeIdempotencyKeyRequired: http.StatusBadRequest,

	domain.CodeUnauthenticated:     http.StatusUnauthorized,
	domain.CodeTokenExpired:        http.StatusUnauthorized,
	domain.CodeOTPInvalid:          http.StatusUnauthorized,
	domain.CodeOTPExpired:          http.StatusUnauthorized,
	domain.CodeInvalidCredentials:  http.StatusUnauthorized,
	domain.CodeMFATokenInvalid:     http.StatusUnauthorized,
	domain.CodeTOTPInvalid:         http.StatusUnauthorized,
	domain.CodeRefreshTokenInvalid: http.StatusUnauthorized,
	domain.CodeRefreshTokenReused:  http.StatusUnauthorized,

	domain.CodeForbidden:      http.StatusForbidden,
	domain.CodeMockNotAllowed: http.StatusForbidden,

	domain.CodeNotFound:          http.StatusNotFound,
	domain.CodeInvitationInvalid: http.StatusNotFound,

	domain.CodeInvitationExpired: http.StatusGone,

	domain.CodeSeatsUnavailable:         http.StatusConflict,
	domain.CodeNoAdjacentSeats:          http.StatusConflict,
	domain.CodeEventNotOnSale:           http.StatusConflict,
	domain.CodeSalesNotOpen:             http.StatusConflict,
	domain.CodeSalesClosed:              http.StatusConflict,
	domain.CodeHoldExpired:              http.StatusConflict,
	domain.CodeHoldNotActive:            http.StatusConflict,
	domain.CodeHoldAlreadyBooked:        http.StatusConflict,
	domain.CodeBookingNotPayable:        http.StatusConflict,
	domain.CodePaymentInProgress:        http.StatusConflict,
	domain.CodeEventNotReady:            http.StatusConflict,
	domain.CodeInvalidStateTransition:   http.StatusConflict,
	domain.CodeEventNotEditable:         http.StatusConflict,
	domain.CodeBlockHasInventory:        http.StatusConflict,
	domain.CodeCategoryInUse:            http.StatusConflict,
	domain.CodeConfirmationRequired:     http.StatusConflict,
	domain.CodeAlreadyExists:            http.StatusConflict,
	domain.CodeEmailAlreadyRegistered:   http.StatusConflict,
	domain.CodeInvitationAlreadyPending: http.StatusConflict,
	domain.CodeInvitationNotPending:     http.StatusConflict,
	domain.CodeTOTPAlreadyEnabled:       http.StatusConflict,
	domain.CodeTOTPNotEnabled:           http.StatusConflict,
	domain.CodeTOTPSetupRequired:        http.StatusConflict,

	domain.CodePayloadTooLarge: http.StatusRequestEntityTooLarge,

	domain.CodeTicketLimitExceeded:  http.StatusUnprocessableEntity,
	domain.CodeIdempotencyKeyReused: http.StatusUnprocessableEntity,
	domain.CodeVenueMismatch:        http.StatusUnprocessableEntity,
	domain.CodePasswordIncorrect:    http.StatusUnprocessableEntity,
	domain.CodeTOTPCodeIncorrect:    http.StatusUnprocessableEntity,

	domain.CodeRateLimited:         http.StatusTooManyRequests,
	domain.CodeOTPAttemptsExceeded: http.StatusTooManyRequests,

	domain.CodeInternal:                   http.StatusInternalServerError,
	domain.CodePaymentProviderUnavailable: http.StatusBadGateway,
}

// errorBody is the stable error shape: { "error": { "code", "message", "details" } }.
// details is always present, null when there is nothing to say.
type errorBody struct {
	Error errorPayload `json:"error"`
}

type errorPayload struct {
	Code    domain.Code `json:"code"`
	Message string      `json:"message"`
	Details any         `json:"details"`
}

const internalMessage = "Something went wrong on our side."

// WriteError turns any error into the HTTP answer. This is the only function that does so.
//
// A *domain.Error is answered with its own code, message and details. Anything else is a
// fault on our side: the real error is logged with the request ID and the client gets
// INTERNAL with a fixed message, so nothing internal (SQL, file paths, stack text) leaks.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	var de *domain.Error
	if !errors.As(err, &de) {
		logInternal(r, err)
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: domain.CodeInternal, Message: internalMessage}})
		return
	}

	status, known := statusByCode[de.Code]
	if !known || de.Code == domain.CodeInternal {
		// A domain error with a code this table does not know is a programming mistake;
		// it is treated like any other internal fault rather than guessed at.
		logInternal(r, err)
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorPayload{Code: domain.CodeInternal, Message: internalMessage}})
		return
	}

	// Contract: every 429 carries Retry-After.
	if rl, ok := de.Details.(domain.RateLimitDetails); ok && status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", strconv.Itoa(max(rl.RetryAfterSeconds, 1)))
	}
	if status == http.StatusUnauthorized && (de.Code == domain.CodeUnauthenticated || de.Code == domain.CodeTokenExpired) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="talanta"`)
	}
	if de.Err != nil {
		LoggerFrom(r.Context()).DebugContext(r.Context(), "request refused", "code", de.Code, "err", de.Err)
	}
	writeJSON(w, status, errorBody{Error: errorPayload{Code: de.Code, Message: de.Message, Details: de.Details}})
}

func logInternal(r *http.Request, err error) {
	log := LoggerFrom(r.Context())
	switch {
	case errors.Is(err, context.Canceled):
		// The client went away; nobody is reading the answer.
		log.InfoContext(r.Context(), "request cancelled by the client", "method", r.Method, "route", routePattern(r), "err", err)
	case errors.Is(err, context.DeadlineExceeded):
		log.ErrorContext(r.Context(), "request timed out", "method", r.Method, "route", routePattern(r), "err", err)
	default:
		log.ErrorContext(r.Context(), "internal error", "method", r.Method, "route", routePattern(r), "err", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
