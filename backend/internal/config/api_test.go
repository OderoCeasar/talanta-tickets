package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const goodSecret = "a-private-secret-that-is-long-enough-123"

// clearAPIEnv makes each test start from an empty environment for the settings it reads,
// whatever the developer's shell has exported.
func clearAPIEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"APP_ENV", "LOG_LEVEL", "ADDR", "CORS_ORIGINS", "JWT_SECRET", "ADMIN_BASE_URL",
		"BOOTSTRAP_ADMIN_EMAIL", "BOOTSTRAP_ADMIN_PASSWORD",
		"SMTP_HOST", "SMTP_PORT", "SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_FROM",
		"TRUSTED_PROXY_CIDRS", "CLIENT_IP_HEADER",
		"HTTP_READ_TIMEOUT", "HTTP_WRITE_TIMEOUT", "HTTP_IDLE_TIMEOUT", "REQUEST_TIMEOUT",
		"DB_STATEMENT_TIMEOUT", "DB_IDLE_IN_TX_TIMEOUT",
		"SMTP_ALLOW_PLAINTEXT", "ARGON2_MAX_CONCURRENT",
		"OTP_ALLOWED_COUNTRY_CODES", "OTP_GLOBAL_HOURLY_LIMIT", "OTP_GLOBAL_DAILY_LIMIT",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("DATABASE_URL", "postgres://x")
}

// productionEnv is clearAPIEnv plus the least a production server must set, so each test
// below can then take one of them away.
func productionEnv(t *testing.T) {
	t.Helper()
	clearAPIEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("JWT_SECRET", goodSecret)
	t.Setenv("TRUSTED_PROXY_CIDRS", "172.16.0.0/12")
	t.Setenv("ADMIN_BASE_URL", "https://talanta.example.com")
}

// Production mode fails closed: a server whose env file was lost must not come up with
// development shortcuts on.
func TestAppEnvUnsetMeansProduction(t *testing.T) {
	productionEnv(t)
	t.Setenv("APP_ENV", "")

	cfg, err := LoadAPI()
	require.NoError(t, err)
	assert.Equal(t, EnvProduction, cfg.AppEnv)
	assert.True(t, cfg.IsProduction())
	assert.False(t, cfg.AllowDevShortcuts())
	assert.False(t, cfg.IsDevelopment())
	assert.False(t, cfg.JWTSecretIsDevDefault)
}

func TestAppEnvIsMatchedExactly(t *testing.T) {
	cases := []struct {
		value                    string
		wantErr                  bool
		production, devShortcuts bool
	}{
		{"production", false, true, false},
		{"development", false, false, true},
		{"test", false, false, true},
		{"", false, true, false},
		{"   ", false, true, false},
		// Near misses are errors, and while the error is being reported the config still
		// reads as production.
		{"Production", true, true, false},
		{"PRODUCTION", true, true, false},
		{"Development", true, true, false},
		{"dev", true, true, false},
		{"prod", true, true, false},
		{"staging", true, true, false},
		{"development ", false, false, true}, // surrounding space is trimmed like every other setting
	}
	for _, tc := range cases {
		t.Run("APP_ENV="+tc.value, func(t *testing.T) {
			productionEnv(t)
			t.Setenv("APP_ENV", tc.value)

			cfg, err := LoadAPI()
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "APP_ENV must be")
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.production, cfg.IsProduction())
			assert.Equal(t, tc.devShortcuts, cfg.AllowDevShortcuts())
		})
	}
}

// A Base nobody loaded (the zero value) must not unlock anything either.
func TestZeroBaseIsProduction(t *testing.T) {
	var b Base
	assert.True(t, b.IsProduction())
	assert.False(t, b.AllowDevShortcuts())
}

func TestProductionRefusesToStartWithoutJWTSecret(t *testing.T) {
	for _, env := range []string{"production", ""} {
		t.Run("APP_ENV="+env, func(t *testing.T) {
			clearAPIEnv(t)
			t.Setenv("APP_ENV", env)

			_, err := LoadAPI()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "JWT_SECRET is required")
		})
	}
}

func TestJWTSecretRules(t *testing.T) {
	t.Run("development falls back to the documented default and says so", func(t *testing.T) {
		clearAPIEnv(t)
		t.Setenv("APP_ENV", "development")
		cfg, err := LoadAPI()
		require.NoError(t, err)
		assert.Equal(t, DevJWTSecret, string(cfg.JWTSecret))
		assert.True(t, cfg.JWTSecretIsDevDefault)
		assert.GreaterOrEqual(t, len(DevJWTSecret), MinJWTSecretBytes)
	})

	t.Run("test env too", func(t *testing.T) {
		clearAPIEnv(t)
		t.Setenv("APP_ENV", "test")
		cfg, err := LoadAPI()
		require.NoError(t, err)
		assert.True(t, cfg.JWTSecretIsDevDefault)
	})

	t.Run("a set secret is used as is", func(t *testing.T) {
		productionEnv(t)
		cfg, err := LoadAPI()
		require.NoError(t, err)
		assert.Equal(t, goodSecret, string(cfg.JWTSecret))
		assert.False(t, cfg.JWTSecretIsDevDefault)
	})

	t.Run("too short is refused everywhere", func(t *testing.T) {
		for _, env := range []string{"production", "development"} {
			clearAPIEnv(t)
			t.Setenv("APP_ENV", env)
			t.Setenv("JWT_SECRET", strings.Repeat("x", MinJWTSecretBytes-1))
			_, err := LoadAPI()
			require.Error(t, err, env)
			assert.Contains(t, err.Error(), "at least 32 bytes")
		}
	})

	t.Run("production refuses the public development secret", func(t *testing.T) {
		clearAPIEnv(t)
		t.Setenv("APP_ENV", "production")
		t.Setenv("JWT_SECRET", DevJWTSecret)
		_, err := LoadAPI()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "public development default")
	})
}

func TestBootstrapAdminSettings(t *testing.T) {
	load := func(t *testing.T, email, password string) (API, error) {
		productionEnv(t)
		t.Setenv("BOOTSTRAP_ADMIN_EMAIL", email)
		t.Setenv("BOOTSTRAP_ADMIN_PASSWORD", password)
		return LoadAPI()
	}

	cfg, err := load(t, "", "")
	require.NoError(t, err)
	assert.False(t, cfg.BootstrapAdmin.Enabled())

	cfg, err = load(t, " Owner@Talanta.Example ", "a long enough password")
	require.NoError(t, err)
	assert.True(t, cfg.BootstrapAdmin.Enabled())
	assert.Equal(t, "owner@talanta.example", cfg.BootstrapAdmin.Email)
	assert.Equal(t, "a long enough password", cfg.BootstrapAdmin.Password)

	_, err = load(t, "owner@talanta.example", "")
	require.ErrorContains(t, err, "must be set together")
	_, err = load(t, "", "a long enough password")
	require.ErrorContains(t, err, "must be set together")
	_, err = load(t, "not-an-email", "a long enough password")
	require.ErrorContains(t, err, "BOOTSTRAP_ADMIN_EMAIL")
	_, err = load(t, "owner@talanta.example", "short")
	require.ErrorContains(t, err, "at least 10 characters")
	// The password itself never appears in the error that gets logged.
	assert.NotContains(t, err.Error(), "short\"")
}

func TestSMTPSettings(t *testing.T) {
	clearAPIEnv(t)
	t.Setenv("APP_ENV", "development")
	cfg, err := LoadAPI()
	require.NoError(t, err)
	assert.False(t, cfg.SMTP.Enabled(), "no SMTP_HOST: mail is only logged")

	t.Setenv("SMTP_HOST", "smtp.example.com")
	_, err = LoadAPI()
	require.ErrorContains(t, err, "SMTP_FROM is required")

	t.Setenv("SMTP_FROM", "Talanta Tickets <no-reply@example.com>")
	t.Setenv("SMTP_USERNAME", "user")
	t.Setenv("SMTP_PASSWORD", "pass")
	cfg, err = LoadAPI()
	require.NoError(t, err)
	assert.Equal(t, SMTP{Host: "smtp.example.com", Port: 587, Username: "user", Password: "pass", From: "Talanta Tickets <no-reply@example.com>"}, cfg.SMTP)

	t.Setenv("SMTP_PORT", "70000")
	_, err = LoadAPI()
	require.ErrorContains(t, err, "SMTP_PORT")
}

func TestProxyTimeoutAndURLSettings(t *testing.T) {
	clearAPIEnv(t)
	t.Setenv("APP_ENV", "development")

	cfg, err := LoadAPI()
	require.NoError(t, err)
	assert.Empty(t, cfg.TrustedProxies, "by default nobody's forwarding headers are believed")
	assert.Equal(t, "", cfg.ClientIPHeader, "no single-address header is believed unless the operator opts in")
	assert.Equal(t, 15*time.Second, cfg.ReadTimeout)
	assert.Equal(t, 60*time.Second, cfg.WriteTimeout)
	assert.Equal(t, 120*time.Second, cfg.IdleTimeout)
	assert.Equal(t, 30*time.Second, cfg.RequestTimeout)
	assert.Equal(t, 15*time.Second, cfg.DBStatementTimeout)
	assert.Equal(t, 15*time.Second, cfg.DBIdleInTxTimeout)
	assert.Equal(t, "http://localhost:5173", cfg.AdminBaseURL)

	t.Setenv("TRUSTED_PROXY_CIDRS", "172.16.0.0/12, 10.1.2.3 ,fd00::/8")
	t.Setenv("ADMIN_BASE_URL", "https://talanta.example.com/")
	cfg, err = LoadAPI()
	require.NoError(t, err)
	require.Len(t, cfg.TrustedProxies, 3)
	assert.Equal(t, "172.16.0.0/12", cfg.TrustedProxies[0].String())
	assert.Equal(t, "10.1.2.3/32", cfg.TrustedProxies[1].String())
	assert.Equal(t, "https://talanta.example.com", cfg.AdminBaseURL, "trailing slash dropped so links do not get a double one")

	t.Setenv("CLIENT_IP_HEADER", "CF-Connecting-IP")
	cfg, err = LoadAPI()
	require.NoError(t, err)
	assert.Equal(t, "CF-Connecting-IP", cfg.ClientIPHeader, "the header is still there for those who ask for it")
	t.Setenv("CLIENT_IP_HEADER", "")

	t.Setenv("TRUSTED_PROXY_CIDRS", "none")
	cfg, err = LoadAPI()
	require.NoError(t, err)
	assert.Empty(t, cfg.TrustedProxies, `"none" trusts nobody`)

	t.Setenv("TRUSTED_PROXY_CIDRS", "172.16.0.0/12,everyone")
	_, err = LoadAPI()
	require.ErrorContains(t, err, "TRUSTED_PROXY_CIDRS")

	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	t.Setenv("ADMIN_BASE_URL", "talanta.example.com")
	_, err = LoadAPI()
	require.ErrorContains(t, err, "ADMIN_BASE_URL")

	t.Setenv("ADMIN_BASE_URL", "")
	t.Setenv("HTTP_WRITE_TIMEOUT", "20s")
	_, err = LoadAPI()
	require.ErrorContains(t, err, "HTTP_WRITE_TIMEOUT")
}

// Production must say where its proxy is. An empty list behind Caddy would make Caddy "the
// client" for every request and put all callers in one rate-limit bucket, silently.
func TestProductionRequiresTrustedProxiesOrNone(t *testing.T) {
	for _, env := range []string{"production", ""} {
		t.Run("empty is refused, APP_ENV="+env, func(t *testing.T) {
			productionEnv(t)
			t.Setenv("APP_ENV", env)
			t.Setenv("TRUSTED_PROXY_CIDRS", "")
			_, err := LoadAPI()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "TRUSTED_PROXY_CIDRS is required when APP_ENV=production")
			assert.Contains(t, err.Error(), `"none"`, "the message names the way out")
		})
	}

	t.Run("a range is accepted", func(t *testing.T) {
		productionEnv(t)
		t.Setenv("TRUSTED_PROXY_CIDRS", "172.16.0.0/12")
		cfg, err := LoadAPI()
		require.NoError(t, err)
		require.Len(t, cfg.TrustedProxies, 1)
	})

	for _, v := range []string{"none", "NONE", " none "} {
		t.Run("explicit "+v+" means exposed directly, trust nobody", func(t *testing.T) {
			productionEnv(t)
			t.Setenv("TRUSTED_PROXY_CIDRS", v)
			cfg, err := LoadAPI()
			require.NoError(t, err)
			assert.Empty(t, cfg.TrustedProxies)
		})
	}

	for _, env := range []string{"development", "test"} {
		t.Run("empty stays the default in "+env, func(t *testing.T) {
			clearAPIEnv(t)
			t.Setenv("APP_ENV", env)
			cfg, err := LoadAPI()
			require.NoError(t, err)
			assert.Empty(t, cfg.TrustedProxies)
		})
	}
}

// Invitation links are one-time credentials: production must name a real https address.
func TestAdminBaseURLInProduction(t *testing.T) {
	t.Run("required", func(t *testing.T) {
		productionEnv(t)
		t.Setenv("ADMIN_BASE_URL", "")
		_, err := LoadAPI()
		require.ErrorContains(t, err, "ADMIN_BASE_URL is required when APP_ENV=production")
	})
	for _, v := range []string{"http://talanta.example.com", "http://localhost:5173"} {
		t.Run("must be https: "+v, func(t *testing.T) {
			productionEnv(t)
			t.Setenv("ADMIN_BASE_URL", v)
			_, err := LoadAPI()
			require.ErrorContains(t, err, "ADMIN_BASE_URL must be https when APP_ENV=production")
		})
	}
	t.Run("https is accepted", func(t *testing.T) {
		productionEnv(t)
		t.Setenv("ADMIN_BASE_URL", "https://talanta.example.com/")
		cfg, err := LoadAPI()
		require.NoError(t, err)
		assert.Equal(t, "https://talanta.example.com", cfg.AdminBaseURL)
	})
	t.Run("development keeps the localhost default and may use http", func(t *testing.T) {
		clearAPIEnv(t)
		t.Setenv("APP_ENV", "development")
		cfg, err := LoadAPI()
		require.NoError(t, err)
		assert.Equal(t, "http://localhost:5173", cfg.AdminBaseURL)
	})
}

func TestArgon2OTPAndSMTPPlaintextSettings(t *testing.T) {
	clearAPIEnv(t)
	t.Setenv("APP_ENV", "development")

	cfg, err := LoadAPI()
	require.NoError(t, err)
	assert.Equal(t, 4, cfg.Argon2MaxConcurrent)
	assert.Equal(t, []string{"254"}, cfg.OTPCountryCodes)
	assert.Equal(t, 2000, cfg.OTPGlobalHourlyLimit)
	assert.Equal(t, 10000, cfg.OTPGlobalDailyLimit)

	t.Setenv("ARGON2_MAX_CONCURRENT", "2")
	t.Setenv("OTP_ALLOWED_COUNTRY_CODES", "254, 255,256")
	t.Setenv("OTP_GLOBAL_HOURLY_LIMIT", "50")
	t.Setenv("OTP_GLOBAL_DAILY_LIMIT", "400")
	t.Setenv("SMTP_HOST", "smtp.example.com")
	t.Setenv("SMTP_FROM", "a@b.co")
	cfg, err = LoadAPI()
	require.NoError(t, err)
	assert.Equal(t, 2, cfg.Argon2MaxConcurrent)
	assert.Equal(t, []string{"254", "255", "256"}, cfg.OTPCountryCodes)
	assert.Equal(t, 50, cfg.OTPGlobalHourlyLimit)
	assert.Equal(t, 400, cfg.OTPGlobalDailyLimit)
	assert.False(t, cfg.SMTP.AllowPlaintext, "plaintext mail is off unless asked for")

	t.Setenv("SMTP_ALLOW_PLAINTEXT", "true")
	cfg, err = LoadAPI()
	require.NoError(t, err)
	assert.True(t, cfg.SMTP.AllowPlaintext)

	for key, bad := range map[string]string{
		"ARGON2_MAX_CONCURRENT":     "0",
		"OTP_ALLOWED_COUNTRY_CODES": "+254",
		"OTP_GLOBAL_HOURLY_LIMIT":   "0",
		"OTP_GLOBAL_DAILY_LIMIT":    "-1",
		"SMTP_ALLOW_PLAINTEXT":      "maybe",
	} {
		t.Run(key+"="+bad, func(t *testing.T) {
			t.Setenv(key, bad)
			_, err := LoadAPI()
			require.Error(t, err)
			assert.Contains(t, err.Error(), key)
		})
	}
}
