package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaymentProviderDefaults(t *testing.T) {
	t.Run("development and test default to the mock", func(t *testing.T) {
		for _, env := range []string{"development", "test"} {
			clearAPIEnv(t)
			t.Setenv("APP_ENV", env)
			cfg, err := LoadAPI()
			require.NoError(t, err)
			assert.Equal(t, ProviderMock, cfg.Payments.Provider)
			assert.Empty(t, cfg.Payments.CallbackAllowedCIDRs, "empty in dev means allow")
		}
	})

	t.Run("production defaults to mpesa and loads it", func(t *testing.T) {
		productionEnv(t)
		cfg, err := LoadAPI()
		require.NoError(t, err)
		assert.Equal(t, ProviderMpesa, cfg.Payments.Provider)
		assert.Equal(t, "production", cfg.Payments.Mpesa.Env)
		assert.True(t, cfg.Payments.Mpesa.Configured())
		assert.Equal(t, "CustomerPayBillOnline", cfg.Payments.Mpesa.TransactionType)
		require.Len(t, cfg.Payments.CallbackAllowedCIDRs, 1)
	})
}

// Rules that never bend: the mock is refused in production, and a half-configured M-Pesa is
// an error, never a silent fall back to the mock.
func TestProductionRefusesTheMockProvider(t *testing.T) {
	productionEnv(t)
	t.Setenv("PAYMENT_PROVIDER", "mock")
	_, err := LoadAPI()
	require.ErrorContains(t, err, "PAYMENT_PROVIDER=mock is refused")

	// The worker reads the same setting and refuses it the same way.
	_, err = LoadWorker()
	require.ErrorContains(t, err, "PAYMENT_PROVIDER=mock is refused")

	for _, env := range []string{"", "production"} {
		productionEnv(t)
		t.Setenv("APP_ENV", env)
		t.Setenv("PAYMENT_PROVIDER", "MOCK")
		_, err := LoadAPI()
		require.ErrorContains(t, err, "PAYMENT_PROVIDER=mock is refused", "APP_ENV=%q", env)
	}
}

func TestMpesaIsNeverReplacedByTheMock(t *testing.T) {
	for _, missing := range []string{"MPESA_CONSUMER_KEY", "MPESA_CONSUMER_SECRET", "MPESA_SHORTCODE", "MPESA_PASSKEY", "MPESA_CALLBACK_URL"} {
		t.Run("production without "+missing, func(t *testing.T) {
			productionEnv(t)
			t.Setenv(missing, "")
			cfg, err := LoadAPI()
			require.ErrorContains(t, err, "PAYMENT_PROVIDER=mpesa needs "+missing)
			assert.Equal(t, ProviderMpesa, cfg.Payments.Provider, "no fall back to the mock")
		})
	}

	t.Run("production with no M-Pesa setting at all takes no payments", func(t *testing.T) {
		productionEnv(t)
		for _, k := range []string{"MPESA_CONSUMER_KEY", "MPESA_CONSUMER_SECRET", "MPESA_SHORTCODE", "MPESA_PASSKEY", "MPESA_CALLBACK_URL", "MPESA_ENV", "MPESA_CALLBACK_ALLOWED_CIDRS"} {
			t.Setenv(k, "")
		}
		cfg, err := LoadAPI()
		require.NoError(t, err, "a server with no Daraja account yet must still start")
		assert.Equal(t, ProviderNone, cfg.Payments.Provider, "payments are off, never the mock")
	})

	t.Run("production that names mpesa with nothing set is refused", func(t *testing.T) {
		productionEnv(t)
		for _, k := range []string{"MPESA_CONSUMER_KEY", "MPESA_CONSUMER_SECRET", "MPESA_SHORTCODE", "MPESA_PASSKEY", "MPESA_CALLBACK_URL"} {
			t.Setenv(k, "")
		}
		t.Setenv("PAYMENT_PROVIDER", "mpesa")
		cfg, err := LoadAPI()
		require.ErrorContains(t, err, "PAYMENT_PROVIDER=mpesa needs")
		assert.Equal(t, ProviderMpesa, cfg.Payments.Provider)
	})

	t.Run("none can be named anywhere", func(t *testing.T) {
		clearAPIEnv(t)
		t.Setenv("APP_ENV", "development")
		t.Setenv("PAYMENT_PROVIDER", "none")
		cfg, err := LoadAPI()
		require.NoError(t, err)
		assert.Equal(t, ProviderNone, cfg.Payments.Provider)
	})

	t.Run("development asked for mpesa without its settings", func(t *testing.T) {
		clearAPIEnv(t)
		t.Setenv("APP_ENV", "development")
		t.Setenv("PAYMENT_PROVIDER", "mpesa")
		t.Setenv("MPESA_CONSUMER_KEY", "only-the-key")
		cfg, err := LoadAPI()
		require.ErrorContains(t, err, "PAYMENT_PROVIDER=mpesa needs MPESA_CONSUMER_SECRET")
		assert.Equal(t, ProviderMpesa, cfg.Payments.Provider)
	})

	t.Run("development with the sandbox fully set", func(t *testing.T) {
		clearAPIEnv(t)
		t.Setenv("APP_ENV", "development")
		t.Setenv("PAYMENT_PROVIDER", "mpesa")
		mpesaEnv(t)
		cfg, err := LoadAPI()
		require.NoError(t, err)
		assert.Equal(t, "sandbox", cfg.Payments.Mpesa.Env, "unset MPESA_ENV means sandbox outside production")
	})
}

func TestMpesaProductionRules(t *testing.T) {
	cases := []struct{ key, value, want string }{
		{"MPESA_ENV", "", "MPESA_ENV is required"},
		{"MPESA_ENV", "live", "MPESA_ENV must be sandbox or production"},
		{"MPESA_CALLBACK_URL", "http://talanta.example.com/v1/webhooks/mpesa/callback", "MPESA_CALLBACK_URL must be https"},
		{"MPESA_CALLBACK_URL", "talanta.example.com", "MPESA_CALLBACK_URL must be an http(s) URL"},
		{"MPESA_CALLBACK_ALLOWED_CIDRS", "", "MPESA_CALLBACK_ALLOWED_CIDRS is required"},
		{"MPESA_CALLBACK_ALLOWED_CIDRS", "safaricom", "is not a CIDR"},
		{"MPESA_TRANSACTION_TYPE", "Till", "MPESA_TRANSACTION_TYPE must be"},
		{"PAYMENT_PROVIDER", "stripe", "PAYMENT_PROVIDER must be mock, mpesa or none"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			productionEnv(t)
			t.Setenv(tc.key, tc.value)
			_, err := LoadAPI()
			require.ErrorContains(t, err, tc.want)
		})
	}
}

// The Daraja secrets are in the config struct, which gets printed and logged. However it is
// printed, they must not appear.
func TestPaymentSecretsAreNeverPrinted(t *testing.T) {
	productionEnv(t)
	cfg, err := LoadAPI()
	require.NoError(t, err)
	require.Equal(t, testConsumerSecret, cfg.Payments.Mpesa.ConsumerSecret.Reveal())
	require.Equal(t, testPasskey, cfg.Payments.Mpesa.Passkey.Reveal())

	var logged bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug}))
	logger.Info("config", "cfg", cfg, "payments", cfg.Payments, "mpesa", cfg.Payments.Mpesa, "secret", cfg.Payments.Mpesa.ConsumerSecret)
	text := slog.New(slog.NewTextHandler(&logged, nil))
	text.Info("config", "cfg", cfg, "mpesa", cfg.Payments.Mpesa, "passkey", cfg.Payments.Mpesa.Passkey)

	asJSON, err := json.Marshal(cfg.Payments)
	require.NoError(t, err)

	printed := []string{
		logged.String(), string(asJSON),
		fmt.Sprintf("%v", cfg), fmt.Sprintf("%+v", cfg), fmt.Sprintf("%#v", cfg),
		fmt.Sprintf("%v", cfg.Payments.Mpesa), fmt.Sprintf("%+v", cfg.Payments.Mpesa), fmt.Sprintf("%#v", cfg.Payments.Mpesa),
		fmt.Sprintf("%s %v %q %#v", cfg.Payments.Mpesa.Passkey, cfg.Payments.Mpesa.Passkey, cfg.Payments.Mpesa.Passkey, cfg.Payments.Mpesa.Passkey),
		fmt.Sprint(&cfg.Payments.Mpesa), fmt.Sprintf("%+v", &cfg),
	}
	for i, out := range printed {
		assert.NotContains(t, out, testConsumerSecret, "output %d", i)
		assert.NotContains(t, out, testPasskey, "output %d", i)
	}
	assert.Contains(t, printed[2], "[redacted]")
}
