package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"math/big"
	"net/netip"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// OTPRequested is the answer to a code request.
type OTPRequested struct {
	ExpiresIn   time.Duration
	ResendAfter time.Duration
	// DevCode is the code itself, set only when dev shortcuts are allowed. Empty otherwise.
	DevCode string
}

var errPhoneFormat = domain.InvalidField("phone", "e164",
	"Enter the phone number in international format, for example +254712345678.")

// errPhoneCountry is the same kind of answer as a malformed number, on the same field: the
// number is well formed but in a country sign-in codes are not sent to.
var errPhoneCountry = domain.InvalidField("phone", "e164",
	"We cannot send a sign-in code to numbers from this country.")

// otpPhone normalises a phone and refuses countries outside the allow-list. International SMS
// costs many times a local one, and "send a code to any number in the world" is how SMS
// pumping fraud is paid for.
func (s *Service) otpPhone(raw string) (string, error) {
	phone, ok := domain.NormalisePhone(raw)
	if !ok {
		return "", errPhoneFormat
	}
	if !domain.PhoneHasCountryCode(phone, s.otpCountryCodes) {
		return "", errPhoneCountry
	}
	return phone, nil
}

// takeGlobalOTP counts one SMS against the caps across all phones. Reaching a cap is logged at
// Error because it is the alert: either real demand has outgrown the limit or someone is
// spending our SMS budget.
func (s *Service) takeGlobalOTP(ctx context.Context) error {
	for _, c := range []struct {
		window string
		rule   ratelimit.Rule
	}{
		{"hour", s.policy.OTPGlobalHourly},
		{"day", s.policy.OTPGlobalDaily},
	} {
		if d := s.limiter.Take("otp:global:"+c.window, c.rule); !d.Allowed {
			s.log.ErrorContext(ctx, "ALERT: global sign-in SMS cap reached; code requests are being refused",
				"window", c.window, "limit", c.rule.Limit, "retry_after_seconds", ratelimit.RetryAfterSeconds(d.RetryAfter))
			return domain.RateLimited(ratelimit.RetryAfterSeconds(d.RetryAfter))
		}
	}
	return nil
}

// RequestOTP makes a new 6-digit code for the phone and sends it by SMS. The answer is the
// same whether or not the phone has an account.
func (s *Service) RequestOTP(ctx context.Context, rawPhone string, ip netip.Addr) (OTPRequested, error) {
	phone, err := s.otpPhone(rawPhone)
	if err != nil {
		return OTPRequested{}, err
	}

	// The resend gap is checked first. A refused event is not counted, so a phone tapping
	// "resend" too early does not eat into its hourly allowance.
	if d := s.limiter.Take("otp:resend:"+phone, ratelimit.Rule{Limit: 1, Window: s.policy.OTPResendAfter}); !d.Allowed {
		return OTPRequested{}, domain.RateLimited(ratelimit.RetryAfterSeconds(d.RetryAfter))
	}
	if d := s.limiter.Take("otp:phone:"+phone, s.policy.OTPPerPhone); !d.Allowed {
		return OTPRequested{}, domain.RateLimited(ratelimit.RetryAfterSeconds(d.RetryAfter))
	}
	// Last, so only requests that would really send an SMS count against the global caps;
	// checked first, one phone hammering "resend" could use them up without costing a cent.
	if err := s.takeGlobalOTP(ctx); err != nil {
		return OTPRequested{}, err
	}

	code, err := randomDigits(otpDigits)
	if err != nil {
		return OTPRequested{}, err
	}
	var requestIP *netip.Addr
	if ip.IsValid() {
		requestIP = &ip
	}

	err = store.InTx(ctx, s.db, func(q *store.Queries) error {
		// Only the newest code is ever live, so there is one six-digit target at a time.
		if _, err := q.RetireOTPCodesForPhone(ctx, phone); err != nil {
			return fmt.Errorf("retire old codes: %w", err)
		}
		_, err := q.CreateOTPCode(ctx, store.CreateOTPCodeParams{
			PhoneE164: phone,
			CodeHash:  s.hashOTP(phone, code),
			ExpiresAt: s.now().Add(s.policy.OTPLifetime),
			RequestIp: requestIP,
		})
		if err != nil {
			return fmt.Errorf("store code: %w", err)
		}
		return nil
	})
	if err != nil {
		return OTPRequested{}, fmt.Errorf("request otp: %w", err)
	}

	text := fmt.Sprintf("Your Talanta Tickets code is %s. It expires in %d minutes. Do not share it.",
		code, int(s.policy.OTPLifetime/time.Minute))
	if err := s.sms.SendSMS(ctx, phone, text); err != nil {
		return OTPRequested{}, fmt.Errorf("request otp: send sms: %w", err)
	}

	out := OTPRequested{ExpiresIn: s.policy.OTPLifetime, ResendAfter: s.policy.OTPResendAfter}
	// The dev shortcut. In production this branch must never run: the code would let anyone
	// sign in as any phone number.
	if s.devShortcuts {
		out.DevCode = code
	}
	return out, nil
}

// VerifyOTP exchanges a code for a session. The first good code for a phone creates its fan
// account; an existing account keeps its role, so a gate steward signs in as gate staff.
func (s *Service) VerifyOTP(ctx context.Context, rawPhone, code string) (Session, error) {
	phone, err := s.otpPhone(rawPhone)
	if err != nil {
		return Session{}, err
	}
	now := s.now()

	row, err := s.q.GetLatestOpenOTPCode(ctx, phone)
	if errors.Is(err, store.ErrNoRows) {
		return Session{}, domain.ErrOTPInvalid
	}
	if err != nil {
		return Session{}, fmt.Errorf("verify otp: find code: %w", err)
	}
	if !row.ExpiresAt.After(now) {
		return Session{}, domain.ErrOTPExpired
	}

	// The attempt is counted before the code is looked at, in one statement, so guesses made
	// in parallel cannot share an attempt. The right code on a spent row is refused too.
	if _, err := s.q.ClaimOTPAttempt(ctx, store.ClaimOTPAttemptParams{
		ID: row.ID, MaxAttempts: int32(s.policy.OTPMaxAttempts),
	}); errors.Is(err, store.ErrNoRows) {
		// A new code may be asked for once the resend gap since this one has passed.
		wait := row.CreatedAt.Add(s.policy.OTPResendAfter).Sub(now)
		secs := ratelimit.RetryAfterSeconds(wait)
		return Session{}, &domain.Error{
			Code:    domain.CodeOTPAttemptsExceeded,
			Message: "Too many wrong codes. Request a new code.",
			Details: domain.RateLimitDetails{RetryAfterSeconds: secs},
		}
	} else if err != nil {
		return Session{}, fmt.Errorf("verify otp: count attempt: %w", err)
	}

	if !hmac.Equal(s.hashOTP(phone, code), row.CodeHash) {
		return Session{}, domain.ErrOTPInvalid
	}

	var session Session
	err = store.InTx(ctx, s.db, func(q *store.Queries) error {
		n, err := q.ConsumeOTPCode(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("consume code: %w", err)
		}
		if n == 0 {
			// Used by a parallel request, or it lapsed in the last instant.
			return domain.ErrOTPInvalid
		}
		user, err := q.UpsertFanByPhone(ctx, &phone)
		if err != nil {
			return fmt.Errorf("find or create fan: %w", err)
		}
		// Staff sign in with a password (and TOTP). A phone code must never be a way round that.
		if !domain.Role(user.Role).SignsInByPhone() {
			s.log.WarnContext(ctx, "phone sign-in refused for a staff account", "user_id", user.ID, "role", user.Role)
			return domain.ErrOTPInvalid
		}
		session, err = s.startSession(ctx, q, user)
		return err
	})
	if err != nil {
		if domain.CodeOf(err) != domain.CodeInternal {
			return Session{}, err
		}
		return Session{}, fmt.Errorf("verify otp: %w", err)
	}
	return session, nil
}

const otpDigits = 6

// hashOTP is what is stored instead of the code. Six digits could be brute-forced from a
// leaked table in a blink if hashed plainly, so the hash is keyed with a server secret and
// bound to the phone.
func (s *Service) hashOTP(phone, code string) []byte {
	mac := hmac.New(sha256.New, s.otpKey)
	mac.Write([]byte(phone))
	mac.Write([]byte{0})
	mac.Write([]byte(code))
	return mac.Sum(nil)
}

// randomDigits returns n decimal digits from crypto/rand, uniformly (rand.Int rejects the
// biased tail), with leading zeros kept.
func randomDigits(n int) (string, error) {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	v, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", fmt.Errorf("auth: read random digits: %w", err)
	}
	return fmt.Sprintf("%0*d", n, v), nil
}
