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

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
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
	// the TCP peer address. Production must say which it is: a range, or "none".
	TrustedProxies []netip.Prefix
	// ClientIPHeader is a single-address header to believe from a trusted proxy before
	// X-Forwarded-For. Empty by default: it is safe only when nothing but the CDN that sets
	// it can reach the origin, or the proxy overwrites it.
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
	// PublicBaseURL is the origin a fan's phone reaches the API at. The SMS ticket link is
	// PublicBaseURL + "/v1/t/<web token>".
	PublicBaseURL string
	// BootstrapAdmin is the first admin to create at startup when the database has none.
	// Empty Email means "do not bootstrap".
	BootstrapAdmin BootstrapAdmin
	// SMTP is empty (Host == "") when no mail server is configured; mail is then only logged.
	SMTP SMTP

	// BookingFeeBPS is the booking fee in basis points of the subtotal (250 = 2.5%). Zero,
	// the default, means no fee. The sum is done by domain.BookingFeeCents.
	BookingFeeBPS int

	// Argon2MaxConcurrent caps password hashing and verification running at once.
	Argon2MaxConcurrent int
	// OTPCountryCodes are the calling codes (digits, no plus) sign-in SMS may be sent to.
	OTPCountryCodes []string
	// OTPGlobalHourlyLimit and OTPGlobalDailyLimit cap sign-in SMS across all phones.
	OTPGlobalHourlyLimit int
	OTPGlobalDailyLimit  int

	// Payments is the payment provider and the M-Pesa callback allowlist. Its secrets are
	// of type Secret, which prints as "[redacted]" wherever this struct is printed.
	Payments Payments
}

// TrustNoProxies is the TRUSTED_PROXY_CIDRS value that says "the API is exposed directly:
// believe no forwarding header". Production must write it out; an empty value there is
// refused, because behind a proxy it would put every caller in one rate-limit bucket.
const TrustNoProxies = "none"

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
	// AllowPlaintext (SMTP_ALLOW_PLAINTEXT) permits a server with no STARTTLS.
	AllowPlaintext bool
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
	// Payments is the same provider configuration the API loads: reconciliation must ask
	// the provider the API sent the payment to.
	Payments Payments
	// PublicBaseURL is the origin of the ticket link in the SMS sent when reconciliation
	// finishes a payment (PUBLIC_BASE_URL, as for the API).
	PublicBaseURL string
}

func LoadAPI() (API, error) {
	l := newLoader()
	cfg := API{
		Base:           l.base(),
		Addr:           l.str("ADDR", ":8080"),
		CORSOrigins:    l.list("CORS_ORIGINS", "http://localhost:5173"),
		ClientIPHeader: l.str("CLIENT_IP_HEADER", ""),
		ReadTimeout:    l.duration("HTTP_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:   l.duration("HTTP_WRITE_TIMEOUT", 60*time.Second),
		IdleTimeout:    l.duration("HTTP_IDLE_TIMEOUT", 120*time.Second),
		RequestTimeout: l.duration("REQUEST_TIMEOUT", 30*time.Second),

		DBStatementTimeout: l.duration("DB_STATEMENT_TIMEOUT", 15*time.Second),
		DBIdleInTxTimeout:  l.duration("DB_IDLE_IN_TX_TIMEOUT", 15*time.Second),

		BookingFeeBPS: l.Int("BOOKING_FEE_BPS", 0),

		Argon2MaxConcurrent:  l.Int("ARGON2_MAX_CONCURRENT", 4),
		OTPCountryCodes:      l.list("OTP_ALLOWED_COUNTRY_CODES", "254"),
		OTPGlobalHourlyLimit: l.Int("OTP_GLOBAL_HOURLY_LIMIT", 2000),
		OTPGlobalDailyLimit:  l.Int("OTP_GLOBAL_DAILY_LIMIT", 10000),
	}
	cfg.TrustedProxies = l.trustedProxies(cfg.Base)
	cfg.AdminBaseURL = l.adminBaseURL(cfg.Base)
	cfg.PublicBaseURL = l.publicBaseURL(cfg.Base)
	cfg.JWTSecret, cfg.JWTSecretIsDevDefault = l.jwtSecret(cfg.Base)
	cfg.BootstrapAdmin = l.bootstrapAdmin()
	cfg.SMTP = l.smtp()
	cfg.Payments = LoadPayments(l, cfg.Base)
	if cfg.IsProduction() && cfg.Payments.Provider == ProviderMpesa && len(cfg.Payments.CallbackAllowedCIDRs) == 0 {
		l.fail("MPESA_CALLBACK_ALLOWED_CIDRS is required when APP_ENV=production: the networks Safaricom's callbacks come from. The callback is unauthenticated; without the list anyone can post one")
	}

	if cfg.WriteTimeout <= cfg.RequestTimeout {
		l.fail("HTTP_WRITE_TIMEOUT (%s) must be longer than REQUEST_TIMEOUT (%s), or slow answers are cut off mid-write", cfg.WriteTimeout, cfg.RequestTimeout)
	}
	if cfg.BookingFeeBPS < 0 || cfg.BookingFeeBPS > domain.MaxBookingFeeBPS {
		l.fail("BOOKING_FEE_BPS must be from 0 to %d (basis points), got %d", domain.MaxBookingFeeBPS, cfg.BookingFeeBPS)
	}
	if cfg.Argon2MaxConcurrent < 1 {
		l.fail("ARGON2_MAX_CONCURRENT must be at least 1, got %d", cfg.Argon2MaxConcurrent)
	}
	for _, code := range cfg.OTPCountryCodes {
		if !isCallingCode(code) {
			l.fail("OTP_ALLOWED_COUNTRY_CODES: %q is not a calling code; write digits only, such as 254", code)
		}
	}
	if cfg.OTPGlobalHourlyLimit < 1 || cfg.OTPGlobalDailyLimit < 1 {
		l.fail("OTP_GLOBAL_HOURLY_LIMIT and OTP_GLOBAL_DAILY_LIMIT must be at least 1, got %d and %d", cfg.OTPGlobalHourlyLimit, cfg.OTPGlobalDailyLimit)
	}
	return cfg, l.err()
}

// trustedProxies reads TRUSTED_PROXY_CIDRS. Development and test may leave it empty (the TCP
// peer is the client). Production may not: behind Caddy an empty list makes Caddy's address
// "the client" for everyone, so every per-IP limit becomes one shared bucket and nobody
// notices. Production says either a range or TrustNoProxies.
func (l *Loader) trustedProxies(b Base) []netip.Prefix {
	raw := l.str("TRUSTED_PROXY_CIDRS", "")
	if strings.EqualFold(raw, TrustNoProxies) {
		return nil
	}
	if raw == "" && b.IsProduction() {
		l.fail("TRUSTED_PROXY_CIDRS is required when APP_ENV=production: set it to the network your reverse proxy connects from (for example 172.16.0.0/12), or to %q if the API is exposed directly with no proxy in front", TrustNoProxies)
		return nil
	}
	return l.Prefixes("TRUSTED_PROXY_CIDRS", "")
}

// adminBaseURL reads ADMIN_BASE_URL, where invitation links point. Production has no default
// (a link to localhost is useless and looks like it worked) and must be https: the link is a
// one-time credential.
func (l *Loader) adminBaseURL(b Base) string {
	return l.baseURL(b, "ADMIN_BASE_URL", "http://localhost:5173")
}

// publicBaseURL reads PUBLIC_BASE_URL, the origin the SMS ticket link points at. The same
// rules as ADMIN_BASE_URL for the same reason: the link is a bearer secret for the tickets,
// so in production it has no default and must be https.
func (l *Loader) publicBaseURL(b Base) string {
	return l.baseURL(b, "PUBLIC_BASE_URL", "http://localhost:8080")
}

// baseURL reads an origin that links sent to people are built on. devFallback is used only
// in development and test.
func (l *Loader) baseURL(b Base, key, devFallback string) string {
	fallback := ""
	if b.AllowDevShortcuts() {
		fallback = devFallback
	}
	raw := strings.TrimRight(l.str(key, fallback), "/")
	if raw == "" {
		l.fail("%s is required when APP_ENV=production, for example https://talanta.example.com", key)
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		l.fail("%s must be an http(s) URL such as https://talanta.example.com, got %q", key, raw)
		return raw
	}
	if b.IsProduction() && u.Scheme != "https" {
		l.fail("%s must be https when APP_ENV=production, got %q", key, raw)
	}
	return raw
}

// isCallingCode reports whether s is one to three digits not starting with zero.
func isCallingCode(s string) bool {
	if len(s) < 1 || len(s) > 3 || s[0] == '0' {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
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

		AllowPlaintext: l.Bool("SMTP_ALLOW_PLAINTEXT", false),
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
	cfg.Payments = LoadPayments(l, cfg.Base)
	cfg.PublicBaseURL = l.publicBaseURL(cfg.Base)
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
