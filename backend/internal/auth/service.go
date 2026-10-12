// Package auth holds the sign-in rules: phone codes for fans and gate staff, email and
// password (plus TOTP when the user has turned it on) for staff, the access and refresh
// tokens, staff invitations and the first-admin bootstrap.
package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"maps"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/notify"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// DB is what the service needs from the pool: run queries and open transactions.
type DB interface {
	store.DBTX
	store.TxBeginner
}

// Policy is every number in the sign-in rules, in one place.
type Policy struct {
	OTPLifetime    time.Duration
	OTPMaxAttempts int
	// OTPResendAfter is the gap between two codes for one phone; OTPPerPhone caps them over
	// a longer window. Both exist because every code is an SMS that costs money.
	OTPResendAfter time.Duration
	OTPPerPhone    ratelimit.Rule

	// LoginPerEmail slows password guessing against one account, whatever IP it comes from.
	LoginPerEmail ratelimit.Rule
	// MFAMaxAttempts is how many TOTP codes one MFA token may try.
	MFAMaxAttempts int
	// TOTPPerUser caps TOTP codes tried against one user at sign-in, across every MFA token.
	// The per-token cap alone is no cap: each good password mints a new token.
	TOTPPerUser ratelimit.Rule

	// OTPGlobalHourly and OTPGlobalDaily cap sign-in SMS across all phones. Per-phone limits
	// do not bound the bill: an attacker simply uses a new number for every request.
	OTPGlobalHourly ratelimit.Rule
	OTPGlobalDaily  ratelimit.Rule

	RefreshLifetime time.Duration
}

// DefaultPolicy is what production runs.
func DefaultPolicy() Policy {
	return Policy{
		OTPLifetime:     5 * time.Minute,
		OTPMaxAttempts:  5,
		OTPResendAfter:  60 * time.Second,
		OTPPerPhone:     ratelimit.Rule{Limit: 5, Window: time.Hour},
		LoginPerEmail:   ratelimit.Rule{Limit: 10, Window: 15 * time.Minute},
		MFAMaxAttempts:  5,
		TOTPPerUser:     ratelimit.Rule{Limit: 5, Window: 15 * time.Minute},
		OTPGlobalHourly: ratelimit.Rule{Limit: 2000, Window: time.Hour},
		OTPGlobalDaily:  ratelimit.Rule{Limit: 10000, Window: 24 * time.Hour},
		RefreshLifetime: 30 * 24 * time.Hour,
	}
}

// Config is what the service is built from.
type Config struct {
	DB     DB
	Logger *slog.Logger
	// Secret is the JWT secret. Access tokens are signed with it; the MFA-token key and the
	// OTP hashing key are derived from it.
	Secret []byte
	// DevShortcuts allows the sign-in code to be returned in the OTP answer. It must come
	// from config.Base.AllowDevShortcuts and nowhere else.
	DevShortcuts bool
	// AdminBaseURL is the admin site's address; invitation links are AdminBaseURL/invite/<token>.
	AdminBaseURL string

	SMS     notify.SMSSender
	Mailer  notify.Mailer
	Limiter ratelimit.Limiter
	Policy  Policy
	// OTPCountryCodes are the calling codes (digits, no plus) a sign-in code may be sent to.
	// Empty means DefaultOTPCountryCodes.
	OTPCountryCodes []string
	// OTPTestCodes maps a test phone (E.164) to the fixed code it always gets. No SMS is sent
	// to these numbers and they do not count against the SMS caps. See RequestOTP.
	OTPTestCodes map[string]string
	// Argon2MaxConcurrent caps password hashing and verification running at once. Zero means
	// DefaultArgon2MaxConcurrent.
	Argon2MaxConcurrent int
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Service implements the auth rules. It is safe for concurrent use.
type Service struct {
	db           DB
	q            *store.Queries
	log          *slog.Logger
	tokens       *Tokens
	otpKey       []byte
	devShortcuts bool
	adminBaseURL string
	sms          notify.SMSSender
	mailer       notify.Mailer
	limiter      ratelimit.Limiter
	policy       Policy
	now          func() time.Time
	mfa          *mfaGuard
	hasher       *passwordHasher
	// otpCountryCodes is the allow-list for sign-in SMS, as calling codes without the plus.
	otpCountryCodes []string
	// otpTestCodes: test phone → its fixed code. Read-only after NewService.
	otpTestCodes map[string]string
	// dummyHash is verified when a login names an email with no account, so that answer
	// takes as long as a wrong password does.
	dummyHash string
}

// NewService checks its dependencies and builds the service.
func NewService(cfg Config) (*Service, error) {
	if cfg.DB == nil {
		return nil, fmt.Errorf("auth: DB is required")
	}
	if len(cfg.Secret) < 32 {
		return nil, fmt.Errorf("auth: secret must be at least 32 bytes, got %d", len(cfg.Secret))
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Limiter == nil {
		cfg.Limiter = ratelimit.NewMemory(cfg.Now)
	}
	if cfg.SMS == nil {
		cfg.SMS = notify.LogSMS{Logger: cfg.Logger, Production: !cfg.DevShortcuts}
	}
	if cfg.Mailer == nil {
		cfg.Mailer = notify.LogMailer{Logger: cfg.Logger, Production: !cfg.DevShortcuts}
	}
	if cfg.Policy == (Policy{}) {
		cfg.Policy = DefaultPolicy()
	}
	if len(cfg.OTPCountryCodes) == 0 {
		cfg.OTPCountryCodes = DefaultOTPCountryCodes()
	}

	filler, err := randomToken(24)
	if err != nil {
		return nil, err
	}
	dummy, err := HashPassword(filler)
	if err != nil {
		return nil, err
	}

	return &Service{
		db:           cfg.DB,
		q:            store.New(cfg.DB),
		log:          cfg.Logger,
		tokens:       NewTokens(cfg.Secret, cfg.Now),
		otpKey:       DeriveKey(cfg.Secret, "talanta:otp-code:v1"),
		devShortcuts: cfg.DevShortcuts,
		adminBaseURL: cfg.AdminBaseURL,
		sms:          cfg.SMS,
		mailer:       cfg.Mailer,
		limiter:      cfg.Limiter,
		policy:       cfg.Policy,
		now:          cfg.Now,
		mfa:          newMFAGuard(),
		hasher:       newPasswordHasher(cfg.Argon2MaxConcurrent),

		otpCountryCodes: append([]string(nil), cfg.OTPCountryCodes...),
		otpTestCodes:    maps.Clone(cfg.OTPTestCodes),
		dummyHash:       dummy,
	}, nil
}

// PasswordVerifications is how many argon2 verifications the service has completed, real or
// dummy. Tests use it to prove a login for an unknown email still pays for one.
func (s *Service) PasswordVerifications() uint64 { return s.hasher.verifies.Load() }

// DefaultOTPCountryCodes is where sign-in SMS may go when OTP_ALLOWED_COUNTRY_CODES is not
// set: Kenya only.
func DefaultOTPCountryCodes() []string { return []string{"254"} }

// Tokens is the access-token checker the HTTP layer authenticates requests with.
func (s *Service) Tokens() *Tokens { return s.tokens }

// Session is what a successful sign-in returns.
type Session struct {
	AccessToken      string
	ExpiresIn        time.Duration
	RefreshToken     string
	RefreshExpiresAt time.Time
	User             domain.User
}

func toUser(u store.User) domain.User {
	return domain.User{
		ID:          u.ID,
		Phone:       u.PhoneE164,
		Email:       u.Email,
		Name:        u.Name,
		Role:        domain.Role(u.Role),
		TOTPEnabled: u.TotpEnabled,
		CreatedAt:   u.CreatedAt,
	}
}

// issueSession adds a refresh token to family and signs an access token for it. Called
// inside the transaction that decided the sign-in is good, so a session never exists
// without the facts it rests on.
func (s *Service) issueSession(ctx context.Context, q *store.Queries, u store.User, family pgtype.UUID) (Session, error) {
	refresh, err := randomToken(32)
	if err != nil {
		return Session{}, err
	}
	refresh = refreshTokenPrefix + refresh
	expires := s.now().Add(s.policy.RefreshLifetime)

	if _, err := q.CreateRefreshToken(ctx, store.CreateRefreshTokenParams{
		UserID:    u.ID,
		FamilyID:  family,
		TokenHash: hashToken(refresh),
		ExpiresAt: expires,
	}); err != nil {
		return Session{}, fmt.Errorf("store refresh token: %w", err)
	}

	access, err := s.tokens.IssueAccess(u.ID, domain.Role(u.Role), uuidString(family))
	if err != nil {
		return Session{}, err
	}
	return Session{
		AccessToken:      access,
		ExpiresIn:        AccessTokenTTL,
		RefreshToken:     refresh,
		RefreshExpiresAt: expires.UTC(),
		User:             toUser(u),
	}, nil
}

// startSession begins a new token family: one per sign-in.
func (s *Service) startSession(ctx context.Context, q *store.Queries, u store.User) (Session, error) {
	family, err := newUUID()
	if err != nil {
		return Session{}, err
	}
	return s.issueSession(ctx, q, u, family)
}

// refreshTokenPrefix marks the value as ours in logs and secret scanners. It carries no meaning.
const refreshTokenPrefix = "rt_"

// newUUID makes a random (version 4) UUID.
func newUUID() (pgtype.UUID, error) {
	var u pgtype.UUID
	if _, err := rand.Read(u.Bytes[:]); err != nil {
		return u, fmt.Errorf("auth: read random bytes: %w", err)
	}
	u.Bytes[6] = (u.Bytes[6] & 0x0f) | 0x40
	u.Bytes[8] = (u.Bytes[8] & 0x3f) | 0x80
	u.Valid = true
	return u, nil
}

func uuidString(u pgtype.UUID) string {
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func parseUUID(s string) (pgtype.UUID, bool) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil || !u.Valid {
		return pgtype.UUID{}, false
	}
	return u, true
}
