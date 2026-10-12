// Package providers builds the payment provider the configuration names. The API and the
// worker both call it, so both always talk to the same provider.
package providers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/config"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments/mock"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments/mpesa"
)

// New returns the provider cfg selects. When that is the mock it is returned a second time,
// typed, so the caller can tell it where to deliver callbacks; otherwise the second value is
// nil.
//
// The mock can be built only where dev shortcuts are allowed: config.LoadPayments already
// refuses PAYMENT_PROVIDER=mock in production, and mock.New refuses again here, so neither a
// config struct built by hand nor a future change to the loader can put it in production.
// An M-Pesa configuration that is incomplete is an error; nothing falls back to the mock.
func New(cfg config.Payments, base config.Base, logger *slog.Logger) (payments.Provider, *mock.Provider, error) {
	switch cfg.Provider {
	case config.ProviderMock:
		m, err := mock.New(mock.Config{
			Allowed: base.AllowDevShortcuts(), Logger: logger,
			CallbackDelay: cfg.MockCallbackDelay, LateDelay: cfg.MockLateCallbackDelay,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("payment provider: %w", err)
		}
		return m, m, nil
	case config.ProviderMpesa:
		baseURL := mpesa.SandboxBaseURL
		if cfg.Mpesa.Env == "production" {
			baseURL = mpesa.ProductionBaseURL
		}
		p, err := mpesa.New(mpesa.Config{
			BaseURL: baseURL, ConsumerKey: cfg.Mpesa.ConsumerKey, ConsumerSecret: cfg.Mpesa.ConsumerSecret.Reveal(),
			ShortCode: cfg.Mpesa.ShortCode, Passkey: cfg.Mpesa.Passkey.Reveal(), CallbackURL: cfg.Mpesa.CallbackURL,
			TransactionType: cfg.Mpesa.TransactionType, Logger: logger,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("payment provider: %w", err)
		}
		return p, nil, nil
	case config.ProviderNone:
		return disabled{}, nil, nil
	}
	return nil, nil, fmt.Errorf("payment provider: unknown provider %q", cfg.Provider)
}

// ErrPaymentsDisabled is what the "none" provider answers to everything.
var ErrPaymentsDisabled = errors.New("payments are switched off: no payment provider is configured")

// disabled is the provider of a deployment that takes no payments yet. It refuses every
// request, so a payment can be neither started nor marked paid; the caller reports the
// refusal as "provider unavailable".
type disabled struct{}

func (disabled) Initiate(context.Context, payments.PaymentRequest) (payments.PaymentInit, error) {
	// Nothing was sent anywhere: the attempt is closed as FAILED at once.
	return payments.PaymentInit{}, payments.NotSent(ErrPaymentsDisabled)
}

// TakesPayments is false: the payment service ignores every callback while payments are off.
func (disabled) TakesPayments() bool { return false }

func (disabled) Query(context.Context, string) (payments.PaymentStatus, error) {
	return payments.PaymentStatus{}, ErrPaymentsDisabled
}
