// Package mock is the payment provider used in development and tests: it plays M-Pesa.
//
// It can produce every outcome of the contract's MockOutcome enum, chosen per payment by the
// X-Mock-Outcome header (payments.WithMockOutcome) or, without a header, by the last digits
// of the phone number (see MagicPhones). It delivers its "callback" by calling the same
// function the real webhook calls, after a short delay.
//
// It keeps no state. Everything Query needs is written into the checkout request ID it hands
// out (the outcome, the amount, the time), so a second process, such as the worker running
// reconciliation, can answer for a payment the API process started.
//
// It is refused in production: New returns an error unless dev shortcuts are allowed, and
// the configuration never selects it there.
package mock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
)

// The outcomes, as in the contract.
const (
	Success           = "success"
	InsufficientFunds = "insufficient_funds"
	WrongPIN          = "wrong_pin"
	UserCancel        = "user_cancel"
	Timeout           = "timeout"
	NoCallback        = "no_callback"
	DuplicateCallback = "duplicate_callback"
	LateCallback      = "late_callback"
	WrongAmount       = "wrong_amount"
)

// MagicPhones maps a phone number to an outcome, for a demo with no way to send a header.
// Any other number succeeds. (The range starts at ...101 because the seeder's demo fan and
// demo steward are +254700000001 and +254700000002, and they must be able to pay.)
var MagicPhones = map[string]string{
	"+254700000101": InsufficientFunds,
	"+254700000102": WrongPIN,
	"+254700000103": UserCancel,
	"+254700000104": Timeout,
	"+254700000105": NoCallback,
	"+254700000106": DuplicateCallback,
	"+254700000107": LateCallback,
	"+254700000108": WrongAmount,
}

// wrongAmountExtraCents is how much more than the booking total the wrong_amount outcome
// "pays": one shilling.
const wrongAmountExtraCents = 100

// ErrNotAllowed is returned by New outside development and test.
var ErrNotAllowed = errors.New("mock payment provider is not allowed in production")

// Sink receives the mock's callbacks. It is payments.Service.HandleCallback.
type Sink func(ctx context.Context, cb payments.Callback)

// Config is what the provider is built from.
type Config struct {
	// Allowed must be config.Base.AllowDevShortcuts(). New fails when it is false.
	Allowed bool
	Logger  *slog.Logger
	// CallbackDelay is how long after Initiate the callback is delivered (zero: 2 seconds).
	// Query answers "pending" for the same length of time.
	CallbackDelay time.Duration
	// LateDelay is how long after Initiate the late_callback outcome delivers its success
	// (zero: 7 minutes, past the six a hold has left when a payment starts).
	LateDelay time.Duration
	// Now is the clock (nil: time.Now). Tests move it.
	Now func() time.Time
	// After schedules fn to run after d (nil: time.AfterFunc). Tests pass a scheduler they
	// run by hand, so nothing in a test waits on a timer.
	After func(d time.Duration, fn func())
}

// Provider is the mock. It is safe for concurrent use.
type Provider struct {
	cfg  Config
	mu   sync.RWMutex
	sink Sink
}

// New builds the mock provider, or refuses outside development and test.
func New(cfg Config) (*Provider, error) {
	if !cfg.Allowed {
		return nil, ErrNotAllowed
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.CallbackDelay <= 0 {
		cfg.CallbackDelay = 2 * time.Second
	}
	if cfg.LateDelay <= 0 {
		cfg.LateDelay = 7 * time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.After == nil {
		cfg.After = func(d time.Duration, fn func()) { time.AfterFunc(d, fn) }
	}
	return &Provider{cfg: cfg}, nil
}

// SetSink says where callbacks go. Without one the mock delivers nothing, which is what the
// worker wants: it only queries.
func (p *Provider) SetSink(sink Sink) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sink = sink
}

// ValidOutcome reports whether s is one of the nine outcomes.
func ValidOutcome(s string) bool {
	for _, o := range payments.MockOutcomes {
		if s == o {
			return true
		}
	}
	return false
}

const idPrefix = "ws_MOCK"

// request is what a checkout request ID says about the payment it was made for.
type request struct {
	Outcome     string
	AmountCents int64
	CreatedAt   time.Time
	Nonce       string
}

func (r request) id() string {
	return fmt.Sprintf("%s.%s.%d.%d.%s", idPrefix, r.Outcome, r.AmountCents, r.CreatedAt.UnixMilli(), r.Nonce)
}

func parseID(id string) (request, bool) {
	parts := strings.Split(id, ".")
	if len(parts) != 5 || parts[0] != idPrefix || !ValidOutcome(parts[1]) {
		return request{}, false
	}
	amount, err1 := strconv.ParseInt(parts[2], 10, 64)
	ms, err2 := strconv.ParseInt(parts[3], 10, 64)
	if err1 != nil || err2 != nil || amount < 0 {
		return request{}, false
	}
	return request{Outcome: parts[1], AmountCents: amount, CreatedAt: time.UnixMilli(ms), Nonce: parts[4]}, true
}

// Initiate accepts the request and schedules the callback its outcome calls for.
func (p *Provider) Initiate(ctx context.Context, req payments.PaymentRequest) (payments.PaymentInit, error) {
	if req.AmountCents <= 0 || req.AmountCents%100 != 0 {
		return payments.PaymentInit{}, payments.ErrAmountNotWholeShillings
	}
	outcome := payments.MockOutcomeFrom(ctx)
	if outcome == "" {
		outcome = MagicPhones[req.Phone]
	}
	if outcome == "" {
		outcome = Success
	}
	if !ValidOutcome(outcome) {
		return payments.PaymentInit{}, fmt.Errorf("mock payment provider: unknown outcome %q", outcome)
	}
	nonce := make([]byte, 6)
	if _, err := rand.Read(nonce); err != nil {
		return payments.PaymentInit{}, fmt.Errorf("mock payment provider: %w", err)
	}
	r := request{Outcome: outcome, AmountCents: req.AmountCents, CreatedAt: p.cfg.Now(), Nonce: hex.EncodeToString(nonce)}
	init := payments.PaymentInit{CheckoutRequestID: r.id(), MerchantRequestID: "mock-" + r.Nonce}
	p.cfg.Logger.InfoContext(ctx, "mock payment provider: STK push accepted",
		"payment_id", req.PaymentID, "outcome", outcome, "checkout_request_id", init.CheckoutRequestID)

	switch outcome {
	case NoCallback:
	case LateCallback:
		p.deliverAfter(p.cfg.LateDelay, r, init)
	case DuplicateCallback:
		p.deliverAfter(p.cfg.CallbackDelay, r, init)
		p.deliverAfter(p.cfg.CallbackDelay, r, init)
	default:
		p.deliverAfter(p.cfg.CallbackDelay, r, init)
	}
	return init, nil
}

func (p *Provider) deliverAfter(d time.Duration, r request, init payments.PaymentInit) {
	p.cfg.After(d, func() {
		p.mu.RLock()
		sink := p.sink
		p.mu.RUnlock()
		if sink == nil {
			return
		}
		// The delivery outlives the request that started the payment.
		sink(context.Background(), p.callback(r, init))
	})
}

// final is what the payment's outcome comes to once the "phone" has answered.
func (r request) final() payments.PaymentStatus {
	receipt := "MOCK" + strings.ToUpper(r.Nonce)
	amount := r.AmountCents
	switch r.Outcome {
	case InsufficientFunds:
		return payments.PaymentStatus{State: payments.StateFailed, ResultCode: domain.ResultInsufficientFunds, ResultDesc: "The balance is insufficient for the transaction."}
	case WrongPIN:
		return payments.PaymentStatus{State: payments.StateFailed, ResultCode: domain.ResultWrongPIN, ResultDesc: "The initiator information is invalid."}
	case UserCancel:
		return payments.PaymentStatus{State: payments.StateFailed, ResultCode: domain.ResultCancelledByUser, ResultDesc: "Request cancelled by user."}
	case Timeout:
		return payments.PaymentStatus{State: payments.StateFailed, ResultCode: domain.ResultTimeout, ResultDesc: "DS timeout user cannot be reached."}
	case WrongAmount:
		amount += wrongAmountExtraCents
	}
	return payments.PaymentStatus{
		State: payments.StateSuccess, ResultCode: domain.ResultSuccess,
		ResultDesc: "The service request is processed successfully.", AmountCents: &amount, Receipt: receipt,
	}
}

// settlesAfter is how long after Initiate the outcome is known to Query.
func (p *Provider) settlesAfter(r request) time.Duration {
	if r.Outcome == LateCallback {
		return p.cfg.LateDelay
	}
	return p.cfg.CallbackDelay
}

// Query answers from the checkout request ID alone: "pending" until the outcome's delay has
// passed, then the outcome. An ID this provider did not issue is an error.
func (p *Provider) Query(_ context.Context, checkoutRequestID string) (payments.PaymentStatus, error) {
	r, ok := parseID(checkoutRequestID)
	if !ok {
		return payments.PaymentStatus{}, fmt.Errorf("mock payment provider: %q is not one of my checkout request IDs", checkoutRequestID)
	}
	if p.cfg.Now().Sub(r.CreatedAt) < p.settlesAfter(r) {
		return payments.PaymentStatus{State: payments.StatePending, ResultDesc: "The transaction is being processed"}, nil
	}
	return r.final(), nil
}

// callback builds the callback for a request, with a raw body in Daraja's envelope so that
// payments.raw_callback looks in development as it will in production.
func (p *Provider) callback(r request, init payments.PaymentInit) payments.Callback {
	st := r.final()
	cb := payments.Callback{
		CheckoutRequestID: init.CheckoutRequestID, MerchantRequestID: init.MerchantRequestID,
		ResultCode: st.ResultCode, ResultDesc: st.ResultDesc, AmountCents: st.AmountCents, Receipt: st.Receipt,
	}
	stk := map[string]any{
		"MerchantRequestID": init.MerchantRequestID, "CheckoutRequestID": init.CheckoutRequestID,
		"ResultCode": st.ResultCode, "ResultDesc": st.ResultDesc,
	}
	if st.State == payments.StateSuccess {
		stk["CallbackMetadata"] = map[string]any{"Item": []map[string]any{
			{"Name": "Amount", "Value": json.Number(fmt.Sprintf("%d.%02d", *st.AmountCents/100, *st.AmountCents%100))},
			{"Name": "MpesaReceiptNumber", "Value": st.Receipt},
			{"Name": "TransactionDate", "Value": json.Number(p.cfg.Now().UTC().Add(3 * time.Hour).Format("20060102150405"))},
		}}
	}
	raw, err := json.Marshal(map[string]any{"Body": map[string]any{"stkCallback": stk}, "mock": true})
	if err == nil {
		cb.Raw = raw
	}
	return cb
}
