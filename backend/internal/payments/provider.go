// Package payments is the money path (docs/ARCHITECTURE.md §8): starting an M-Pesa payment
// for a booking, finishing it exactly once whoever reports the result, reconciling payments
// whose callback never came, and the refund queue and organiser alerts that follow from a
// payment that could not buy its seats.
//
// All business code talks to the Provider interface. internal/payments/mock simulates M-Pesa
// for development and tests; internal/payments/mpesa is Safaricom's Daraja API.
package payments

import (
	"context"
	"fmt"
)

// Provider is the payment provider interface of §8.
type Provider interface {
	// Initiate asks the provider to prompt the payer. It returns once the provider has
	// accepted (or refused) the request; the outcome of the prompt arrives later, by
	// callback or through Query.
	Initiate(ctx context.Context, req PaymentRequest) (PaymentInit, error)
	// Query asks the provider what became of a request it accepted.
	Query(ctx context.Context, checkoutRequestID string) (PaymentStatus, error)
}

// PaymentRequest is one STK push.
type PaymentRequest struct {
	// PaymentID is our payments.id, for the provider's logs only.
	PaymentID int64
	// Reference is the booking reference (TLT-XXXXXX); the payer sees it as the account.
	Reference string
	// AmountCents is the booking total in KES cents. Providers that only take whole
	// shillings refuse an amount that is not a multiple of 100 rather than round it.
	AmountCents int64
	// Phone is the number to prompt, E.164.
	Phone string
}

// PaymentInit is the provider's acceptance of a request.
type PaymentInit struct {
	CheckoutRequestID string
	MerchantRequestID string
}

// State is where a request stands at the provider.
type State int

const (
	// StatePending: the provider has no result yet.
	StatePending State = iota
	// StateSuccess: the payer paid.
	StateSuccess
	// StateFailed: the provider reports a final failure; ResultCode says which.
	StateFailed
)

func (s State) String() string {
	switch s {
	case StatePending:
		return "pending"
	case StateSuccess:
		return "success"
	case StateFailed:
		return "failed"
	}
	return fmt.Sprintf("state(%d)", int(s))
}

// PaymentStatus is the provider's answer to Query.
type PaymentStatus struct {
	State      State
	ResultCode int
	ResultDesc string
	// AmountCents is what was paid, when the provider says. Daraja's STK query does not:
	// it is nil there, and the amount is then the one we asked Daraja to charge (an STK
	// prompt cannot be paid in part or in excess).
	AmountCents *int64
	// Receipt is the M-Pesa receipt, when the provider says.
	Receipt string
}

// Callback is a provider's result notification, already parsed. The webhook builds it from
// Daraja's envelope; the mock builds it directly.
type Callback struct {
	CheckoutRequestID string
	MerchantRequestID string
	ResultCode        int
	ResultDesc        string
	// AmountCents is nil when the body carried no readable amount.
	AmountCents *int64
	Receipt     string
	// Raw is the body as received, valid JSON, kept in payments.raw_callback.
	Raw []byte
}

// ErrAmountNotWholeShillings is returned by a provider asked to charge a fraction of a shilling.
var ErrAmountNotWholeShillings = fmt.Errorf("payments: amount is not a whole number of shillings")

type mockOutcomeKey struct{}

// WithMockOutcome marks ctx with the outcome the mock provider should play for the payment
// started under it (the X-Mock-Outcome header). The HTTP layer refuses the header outside
// development and test; a real provider never looks at it.
func WithMockOutcome(ctx context.Context, outcome string) context.Context {
	if outcome == "" {
		return ctx
	}
	return context.WithValue(ctx, mockOutcomeKey{}, outcome)
}

// MockOutcomeFrom returns the outcome WithMockOutcome set, or "".
func MockOutcomeFrom(ctx context.Context) string {
	s, _ := ctx.Value(mockOutcomeKey{}).(string)
	return s
}

// MockOutcomes is the contract's MockOutcome enum.
var MockOutcomes = []string{
	"success", "insufficient_funds", "wrong_pin", "user_cancel", "timeout",
	"no_callback", "duplicate_callback", "late_callback", "wrong_amount",
}
