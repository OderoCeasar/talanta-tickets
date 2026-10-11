package config

import (
	"encoding/json"
	"log/slog"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

// The payment providers. ProviderNone takes no payments at all: every attempt is answered
// "payments are not available", and nothing is ever marked paid.
const (
	ProviderMock  = "mock"
	ProviderMpesa = "mpesa"
	ProviderNone  = "none"
)

// Secret is a setting that must never be printed. Its text is only reachable through
// Reveal: fmt (%v, %+v, %s, %#v), slog and JSON all show "[redacted]" instead, including
// when the secret is a field of a struct that is printed whole.
type Secret string

const redacted = "[redacted]"

func (s Secret) String() string               { return redacted }
func (s Secret) GoString() string             { return redacted }
func (s Secret) LogValue() slog.Value         { return slog.StringValue(redacted) }
func (s Secret) MarshalJSON() ([]byte, error) { return json.Marshal(redacted) }
func (s Secret) MarshalText() ([]byte, error) { return []byte(redacted), nil }

// Reveal returns the secret itself, for the one place that uses it.
func (s Secret) Reveal() string { return string(s) }

// Mpesa is the Daraja configuration (the MPESA_* variables).
type Mpesa struct {
	// Env is "sandbox" or "production": which Daraja host is called.
	Env            string
	ConsumerKey    string
	ConsumerSecret Secret
	ShortCode      string
	Passkey        Secret
	// CallbackURL is our webhook as Safaricom reaches it:
	// <public origin>/v1/webhooks/mpesa/callback.
	CallbackURL string
	// TransactionType is CustomerPayBillOnline (default) or CustomerBuyGoodsOnline.
	TransactionType string
}

// AnySet reports whether any Daraja setting is present, which shows the operator means to
// use M-Pesa even if the set is incomplete.
func (m Mpesa) AnySet() bool {
	return m.ConsumerKey != "" || m.ConsumerSecret != "" || m.ShortCode != "" || m.Passkey != "" || m.CallbackURL != ""
}

// Configured reports whether every required Daraja setting is present.
func (m Mpesa) Configured() bool {
	return m.ConsumerKey != "" && m.ConsumerSecret != "" && m.ShortCode != "" && m.Passkey != "" && m.CallbackURL != ""
}

// Payments is what the API and the worker need to take payments. Both load it, so both use
// the same provider.
type Payments struct {
	// Provider is ProviderMock, ProviderMpesa or ProviderNone.
	Provider string
	Mpesa    Mpesa
	// CallbackAllowedCIDRs are the networks the M-Pesa callback is accepted from
	// (MPESA_CALLBACK_ALLOWED_CIDRS). Empty means "from anywhere", which only development
	// and test may run with.
	CallbackAllowedCIDRs []netip.Prefix
	// MockCallbackDelay and MockLateCallbackDelay are the mock provider's delays.
	MockCallbackDelay     time.Duration
	MockLateCallbackDelay time.Duration
}

// LoadPayments reads the payment settings. Problems are recorded on the loader.
//
//   - PAYMENT_PROVIDER: "mock", "mpesa" or "none". Unset means mock in development and
//     test. In production unset means mpesa as soon as any MPESA_* variable is set, and none
//     otherwise: the site runs (events, seat map, admin) but refuses to start a payment,
//     so a server that has no Daraja account yet can still be deployed. Production refuses
//     mock: the mock "pays" for anything.
//   - mpesa needs ALL of MPESA_CONSUMER_KEY, MPESA_CONSUMER_SECRET, MPESA_SHORTCODE,
//     MPESA_PASSKEY and MPESA_CALLBACK_URL. With any missing it is a configuration error; it
//     never falls back to the mock.
//   - MPESA_ENV: "sandbox" or "production". Unset means sandbox outside production; in
//     production it must be said.
//   - MPESA_CALLBACK_ALLOWED_CIDRS: read here, required by LoadAPI in production (the worker
//     serves no callback).
func LoadPayments(l *Loader, b Base) Payments {
	p := Payments{
		Provider: strings.ToLower(l.str("PAYMENT_PROVIDER", "")),
		Mpesa: Mpesa{
			Env:         strings.ToLower(l.str("MPESA_ENV", "")),
			ConsumerKey: l.str("MPESA_CONSUMER_KEY", ""),
			// Read with os.Getenv, not through str(): nothing about a secret is defaulted.
			ConsumerSecret:  Secret(strings.TrimSpace(os.Getenv("MPESA_CONSUMER_SECRET"))),
			ShortCode:       l.str("MPESA_SHORTCODE", ""),
			Passkey:         Secret(strings.TrimSpace(os.Getenv("MPESA_PASSKEY"))),
			CallbackURL:     l.str("MPESA_CALLBACK_URL", ""),
			TransactionType: l.str("MPESA_TRANSACTION_TYPE", "CustomerPayBillOnline"),
		},
		CallbackAllowedCIDRs:  l.Prefixes("MPESA_CALLBACK_ALLOWED_CIDRS", ""),
		MockCallbackDelay:     l.duration("MOCK_CALLBACK_DELAY", 2*time.Second),
		MockLateCallbackDelay: l.duration("MOCK_LATE_CALLBACK_DELAY", 7*time.Minute),
	}
	if p.Provider == "" {
		switch {
		case !b.IsProduction():
			p.Provider = ProviderMock
		case p.Mpesa.AnySet():
			p.Provider = ProviderMpesa
		default:
			p.Provider = ProviderNone
		}
	}

	switch p.Provider {
	case ProviderMock:
		if b.IsProduction() {
			l.fail("PAYMENT_PROVIDER=mock is refused when APP_ENV=production: the mock provider marks payments as paid without any money moving. Set PAYMENT_PROVIDER=mpesa and the MPESA_* variables")
		}
	case ProviderMpesa:
		if !p.Mpesa.Configured() {
			var missing []string
			for _, kv := range []struct {
				name string
				set  bool
			}{
				{"MPESA_CONSUMER_KEY", p.Mpesa.ConsumerKey != ""}, {"MPESA_CONSUMER_SECRET", p.Mpesa.ConsumerSecret != ""},
				{"MPESA_SHORTCODE", p.Mpesa.ShortCode != ""}, {"MPESA_PASSKEY", p.Mpesa.Passkey != ""},
				{"MPESA_CALLBACK_URL", p.Mpesa.CallbackURL != ""},
			} {
				if !kv.set {
					missing = append(missing, kv.name)
				}
			}
			l.fail("PAYMENT_PROVIDER=mpesa needs %s (M-Pesa is never replaced by the mock when its settings are incomplete)", strings.Join(missing, ", "))
		}
		switch p.Mpesa.Env {
		case "sandbox", "production":
		case "":
			if b.IsProduction() {
				l.fail("MPESA_ENV is required when APP_ENV=production: sandbox or production")
			}
			p.Mpesa.Env = "sandbox"
		default:
			l.fail("MPESA_ENV must be sandbox or production, got %q", p.Mpesa.Env)
		}
		switch p.Mpesa.TransactionType {
		case "CustomerPayBillOnline", "CustomerBuyGoodsOnline":
		default:
			l.fail("MPESA_TRANSACTION_TYPE must be CustomerPayBillOnline or CustomerBuyGoodsOnline, got %q", p.Mpesa.TransactionType)
		}
		if p.Mpesa.CallbackURL != "" {
			u, err := url.Parse(p.Mpesa.CallbackURL)
			switch {
			case err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https"):
				l.fail("MPESA_CALLBACK_URL must be an http(s) URL ending in /v1/webhooks/mpesa/callback")
			case b.IsProduction() && u.Scheme != "https":
				l.fail("MPESA_CALLBACK_URL must be https when APP_ENV=production")
			}
		}
	case ProviderNone:
	default:
		l.fail("PAYMENT_PROVIDER must be mock, mpesa or none, got %q", p.Provider)
	}

	return p
}
