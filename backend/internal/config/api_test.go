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
	} {
		t.Setenv(k, "")
	}
	t.Setenv("DATABASE_URL", "postgres://x")
}

// Production mode fails closed: a server whose env file was lost must not come up with
// development shortcuts on.
func TestAppEnvUnsetMeansProduction(t *testing.T) {
	clearAPIEnv(t)
	t.Setenv("JWT_SECRET", goodSecret)

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
			clearAPIEnv(t)
			t.Setenv("JWT_SECRET", goodSecret)
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
		clearAPIEnv(t)
		t.Setenv("APP_ENV", "production")
		t.Setenv("JWT_SECRET", goodSecret)
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
		clearAPIEnv(t)
		t.Setenv("APP_ENV", "production")
		t.Setenv("JWT_SECRET", goodSecret)
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
	assert.Equal(t, "CF-Connecting-IP", cfg.ClientIPHeader)
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
