// Package config reads settings from the environment and refuses to start on a bad value.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	EnvDevelopment = "development"
	EnvTest        = "test"
	EnvProduction  = "production"
)

// Base is what every binary (api, worker, seed) needs.
type Base struct {
	AppEnv      string
	DatabaseURL string
	LogLevel    slog.Level
}

// IsProduction reports whether production rules apply. It is true for anything that is not
// explicitly development or test, so a Base that was never loaded (or a future fourth value)
// fails closed.
func (b Base) IsProduction() bool { return !b.AllowDevShortcuts() }

// IsDevelopment reports whether APP_ENV is exactly "development".
func (b Base) IsDevelopment() bool { return b.AppEnv == EnvDevelopment }

// IsTest reports whether APP_ENV is exactly "test".
func (b Base) IsTest() bool { return b.AppEnv == EnvTest }

// AllowDevShortcuts gates every dev-only shortcut (OTP in the response, mock payment
// outcomes, the public signing secret). Shortcuts are on only when the environment is named
// as development or test; "not production" is never the question asked.
func (b Base) AllowDevShortcuts() bool { return b.IsDevelopment() || b.IsTest() }

type API struct {
	Base
	Addr string
	// CORSOrigins lists the admin site origins allowed to call the API from a browser.
	CORSOrigins []string
	// TrustedProxies are the networks our own reverse proxies connect from. Forwarding
	// headers are believed only on connections from these; empty means trust nobody and use
	// the TCP peer address.
	TrustedProxies []netip.Prefix
	// ClientIPHeader is the header a trusted proxy puts the caller's address in.
	ClientIPHeader string

	// Server timeouts. WriteTimeout must outlast the slowest handler (event publish).
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	IdleTimeout    time.Duration
	RequestTimeout time.Duration

	// Database session limits, applied to every pooled connection.
	DBStatementTimeout time.Duration
	DBIdleInTxTimeout  time.Duration

	// JWTSecret signs access tokens and keys the other auth hashes.
	JWTSecret []byte
	// JWTSecretIsDevDefault is true when JWT_SECRET was not set and DevJWTSecret is in use.
	// Never true in production.
	JWTSecretIsDevDefault bool

	// AdminBaseURL is where the admin site is served; invitation links point into it.
	AdminBaseURL string
	// BootstrapAdmin is the first admin to create at startup when the database has none.
	// Empty Email means "do not bootstrap".
	BootstrapAdmin BootstrapAdmin
	// SMTP is empty (Host == "") when no mail server is configured; mail is then only logged.
	SMTP SMTP
}

// BootstrapAdmin comes from BOOTSTRAP_ADMIN_EMAIL and BOOTSTRAP_ADMIN_PASSWORD.
type BootstrapAdmin struct {
	Email    string
	Password string
}

func (b BootstrapAdmin) Enabled() bool { return b.Email != "" }

// SMTP comes from the SMTP_* variables.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

func (s SMTP) Enabled() bool { return s.Host != "" }

// DevJWTSecret is the documented signing secret used in development and test when JWT_SECRET
// is not set, so `make dev` works with no setup. It is public: production refuses to start
// without its own JWT_SECRET, and refuses this value.
const DevJWTSecret = "talanta-dev-only-jwt-secret-do-not-use-in-production"

// MinJWTSecretBytes is the least a signing secret may be: HS256 keys shorter than the hash
// output weaken the signature.
const MinJWTSecretBytes = 32

// MinBootstrapPasswordLength matches the password rule of the API contract.
const MinBootstrapPasswordLength = 10

type Worker struct {
	Base
	SweepInterval     time.Duration
	ReconcileInterval time.Duration
}

func LoadAPI() (API, error) {
	l := newLoader()
	cfg := API{
		Base:           l.base(),
		Addr:           l.str("ADDR", ":8080"),
		CORSOrigins:    l.list("CORS_ORIGINS", "http://localhost:5173"),
		TrustedProxies: l.Prefixes("TRUSTED_PROXY_CIDRS", ""),
		ClientIPHeader: l.str("CLIENT_IP_HEADER", "CF-Connecting-IP"),
		ReadTimeout:    l.duration("HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:   l.duration("HTTP_WRITE_TIMEOUT", 60*time.Second),
		IdleTimeout:    l.duration("HTTP_IDLE_TIMEOUT", 120*time.Second),
		RequestTimeout: l.duration("REQUEST_TIMEOUT", 30*time.Second),

		DBStatementTimeout: l.duration("DB_STATEMENT_TIMEOUT", 15*time.Second),
		DBIdleInTxTimeout:  l.duration("DB_IDLE_IN_TX_TIMEOUT", 15*time.Second),
		AdminBaseURL:       strings.TrimRight(l.str("ADMIN_BASE_URL", "http://localhost:5173"), "/"),
	}
	cfg.JWTSecret, cfg.JWTSecretIsDevDefault = l.jwtSecret(cfg.Base)
	cfg.BootstrapAdmin = l.bootstrapAdmin()
	cfg.SMTP = l.smtp()

	if cfg.WriteTimeout <= cfg.RequestTimeout {
		l.fail("HTTP_WRITE_TIMEOUT (%s) must be longer than REQUEST_TIMEOUT (%s), or slow answers are cut off mid-write", cfg.WriteTimeout, cfg.RequestTimeout)
	}
	if u, err := url.Parse(cfg.AdminBaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		l.fail("ADMIN_BASE_URL must be an http(s) URL such as https://talanta.example.com, got %q", cfg.AdminBaseURL)
	}
	return cfg, l.err()
}

// jwtSecret reads JWT_SECRET. Production must set its own; elsewhere a fixed, public default
// keeps local setup to zero steps.
func (l *Loader) jwtSecret(b Base) (secret []byte, isDevDefault bool) {
	// Not trimmed through str(): a secret is used byte for byte.
	raw := os.Getenv("JWT_SECRET")
	if raw == "" {
		if !b.AllowDevShortcuts() {
			l.fail("JWT_SECRET is required when APP_ENV=production (at least %d bytes)", MinJWTSecretBytes)
			return nil, false
		}
		return []byte(DevJWTSecret), true
	}
	if len(raw) < MinJWTSecretBytes {
		l.fail("JWT_SECRET must be at least %d bytes, got %d", MinJWTSecretBytes, len(raw))
	}
	if b.IsProduction() && raw == DevJWTSecret {
		l.fail("JWT_SECRET is the public development default; set a private one in production")
	}
	return []byte(raw), false
}

func (l *Loader) bootstrapAdmin() BootstrapAdmin {
	b := BootstrapAdmin{
		Email: strings.ToLower(l.str("BOOTSTRAP_ADMIN_EMAIL", "")),
		// Not trimmed: a password is used exactly as given.
		Password: os.Getenv("BOOTSTRAP_ADMIN_PASSWORD"),
	}
	switch {
	case b.Email == "" && b.Password == "":
		return BootstrapAdmin{}
	case b.Email == "" || b.Password == "":
		l.fail("BOOTSTRAP_ADMIN_EMAIL and BOOTSTRAP_ADMIN_PASSWORD must be set together")
		return BootstrapAdmin{}
	}
	if addr, err := mail.ParseAddress(b.Email); err != nil || addr.Address != b.Email {
		l.fail("BOOTSTRAP_ADMIN_EMAIL is not an email address")
	}
	if n := len([]rune(b.Password)); n < MinBootstrapPasswordLength {
		l.fail("BOOTSTRAP_ADMIN_PASSWORD must be at least %d characters, got %d", MinBootstrapPasswordLength, n)
	}
	return b
}

func (l *Loader) smtp() SMTP {
	s := SMTP{
		Host:     l.str("SMTP_HOST", ""),
		Port:     l.Int("SMTP_PORT", 587),
		Username: l.str("SMTP_USERNAME", ""),
		Password: os.Getenv("SMTP_PASSWORD"),
		From:     l.str("SMTP_FROM", ""),
	}
	if !s.Enabled() {
		return SMTP{}
	}
	if s.Port < 1 || s.Port > 65535 {
		l.fail("SMTP_PORT must be between 1 and 65535, got %d", s.Port)
	}
	if _, err := mail.ParseAddress(s.From); err != nil {
		l.fail("SMTP_FROM is required when SMTP_HOST is set, for example \"Talanta Tickets <no-reply@example.com>\"")
	}
	return s
}

func LoadWorker() (Worker, error) {
	l := newLoader()
	cfg := Worker{
		Base:              l.base(),
		SweepInterval:     l.duration("SWEEP_INTERVAL", 30*time.Second),
		ReconcileInterval: l.duration("RECONCILE_INTERVAL", 30*time.Second),
	}
	return cfg, l.err()
}

// LoadBase is for one-shot tools such as the seeder.
func LoadBase() (Base, error) {
	l := newLoader()
	b := l.base()
	return b, l.err()
}

// Loader collects every problem so a misconfigured deploy reports all of them at once
// instead of one per restart. Other packages use it to read their own settings.
type Loader struct {
	problems []string
}

func NewLoader() *Loader { return newLoader() }

func newLoader() *Loader { return &Loader{} }

func (l *Loader) base() Base {
	b := Base{
		// Unset means production: a server that lost its env file must not come up with
		// development shortcuts switched on.
		AppEnv:      l.str("APP_ENV", EnvProduction),
		DatabaseURL: l.Required("DATABASE_URL"),
	}
	switch b.AppEnv {
	case EnvDevelopment, EnvTest, EnvProduction:
	default:
		// Exact, case-sensitive match: "Production" or "prod" is a typo to fix, not a mode.
		l.fail("APP_ENV must be development, test or production, got %q", b.AppEnv)
		b.AppEnv = EnvProduction
	}
	if err := b.LogLevel.UnmarshalText([]byte(l.str("LOG_LEVEL", "info"))); err != nil {
		l.fail("LOG_LEVEL: %v", err)
	}
	return b
}

func (l *Loader) Err() error { return l.err() }

func (l *Loader) err() error {
	if len(l.problems) == 0 {
		return nil
	}
	return errors.New("config: " + strings.Join(l.problems, "; "))
}

func (l *Loader) fail(format string, args ...any) {
	l.problems = append(l.problems, fmt.Sprintf(format, args...))
}

// Fail records a problem found by a caller's own validation.
func (l *Loader) Fail(format string, args ...any) { l.fail(format, args...) }

func (l *Loader) Str(key, fallback string) string { return l.str(key, fallback) }

func (l *Loader) str(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func (l *Loader) Required(key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		l.fail("%s is required", key)
	}
	return v
}

func (l *Loader) List(key, fallback string) []string { return l.list(key, fallback) }

func (l *Loader) list(key, fallback string) []string {
	var out []string
	for _, part := range strings.Split(l.str(key, fallback), ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// Prefixes reads a comma-separated list of CIDRs. A bare address is taken as a single host.
func (l *Loader) Prefixes(key, fallback string) []netip.Prefix {
	var out []netip.Prefix
	for _, part := range l.list(key, fallback) {
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(part); err == nil {
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		l.fail("%s: %q is not a CIDR such as 172.16.0.0/12", key, part)
	}
	return out
}

func (l *Loader) Duration(key string, fallback time.Duration) time.Duration {
	return l.duration(key, fallback)
}

func (l *Loader) duration(key string, fallback time.Duration) time.Duration {
	raw := l.str(key, "")
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		l.fail("%s must be a positive duration such as 30s, got %q", key, raw)
		return fallback
	}
	return d
}

func (l *Loader) Int(key string, fallback int) int {
	raw := l.str(key, "")
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		l.fail("%s must be a whole number, got %q", key, raw)
		return fallback
	}
	return n
}

func (l *Loader) Bool(key string, fallback bool) bool {
	raw := l.str(key, "")
	if raw == "" {
		return fallback
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		l.fail("%s must be true or false, got %q", key, raw)
		return fallback
	}
	return b
}

// NewLogger builds the JSON logger every binary uses.
func NewLogger(b Base, service string) *slog.Logger {
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: b.LogLevel})
	return slog.New(h).With("service", service, "env", b.AppEnv)
}
