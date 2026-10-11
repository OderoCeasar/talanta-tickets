//go:build integration

package httpapi_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/argon2"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// cheapPasswordHash is a real argon2id hash at the lowest cost, so a test can run a thousand
// password steps in seconds. The verifier reads the cost from the string.
func cheapPasswordHash(t testing.TB, password string) string {
	t.Helper()
	salt := make([]byte, 16)
	_, err := rand.Read(salt)
	require.NoError(t, err)
	key := argon2.IDKey([]byte(password), salt, 1, 64, 1, 32)
	return fmt.Sprintf("$argon2id$v=%d$m=64,t=1,p=1$%s$%s", argon2.Version,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func (e *env) newCheapStaff(role domain.Role) store.User {
	e.t.Helper()
	name := "Test " + string(role)
	u, err := e.q.CreateStaffUser(context.Background(), store.CreateStaffUserParams{
		Email: randEmail(e.t, "cheap"), Name: &name, Role: string(role), PasswordHash: cheapPasswordHash(e.t, staffPassword),
	})
	require.NoError(e.t, err)
	return u
}

// certainlyWrongTOTP is a code that matches none of the steps the server would accept now.
func (e *env) certainlyWrongTOTP(secret string) string {
	e.t.Helper()
	accepted := map[string]bool{}
	for _, d := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		code, err := auth.TOTPCode(secret, e.clock.Now().Add(d))
		require.NoError(e.t, err)
		accepted[code] = true
	}
	for i := 0; ; i++ {
		if code := fmt.Sprintf("%06d", i); !accepted[code] {
			return code
		}
	}
}

// The review's attack: someone who knows the password takes the password step over and over,
// and guesses TOTP codes with each fresh mfa_token. Every per-IP and per-email limit is
// opened wide here so the only thing standing in the way is the per-user cap on codes.
func TestStaffLogin_TOTPGuessesAreCappedPerUser(t *testing.T) {
	wide := ratelimit.Rule{Limit: 1 << 30, Window: 15 * time.Minute}
	e := newEnv(t,
		withPolicy(func(p *auth.Policy) { p.LoginPerEmail = wide }),
		withLimits(func(l *httpapi.RateLimits) { l.LoginPerIP, l.TOTPVerifyPerIP = wide, wide }),
	)
	u := e.newCheapStaff(domain.RoleAdmin)
	secret := e.enableTOTP(e.login(*u.Email, staffPassword))
	e.clock.Advance(31 * time.Second)

	const passwordSteps, guessesPerToken = 1000, 2
	wrong := e.certainlyWrongTOTP(secret)
	var looked, refused int
	for i := 0; i < passwordSteps; i++ {
		// Each good password mints a new mfa_token with its own five tries.
		mfaToken := e.passwordStep(*u.Email)
		for g := 0; g < guessesPerToken; g++ {
			rec := e.totpVerify(mfaToken, wrong)
			switch rec.Code {
			case http.StatusUnauthorized:
				// The server compared this guess against the secret.
				wantError(t, rec, http.StatusUnauthorized, domain.CodeTOTPInvalid)
				looked++
			case http.StatusTooManyRequests:
				wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
				require.NotEmpty(t, rec.Header().Get("Retry-After"))
				refused++
			default:
				t.Fatalf("guess %d/%d: unexpected answer %d: %s", i, g, rec.Code, rec.Body.String())
			}
		}
	}
	assert.Equal(t, 5, looked, "wrong codes the server was willing to check in one window")
	assert.Equal(t, passwordSteps*guessesPerToken-5, refused)

	// Even a fresh mfa_token with the right code is refused until the window ends…
	sessions := func() int { return queryInt(t, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1`, u.ID) }
	before := sessions()
	rec := e.totpVerify(e.passwordStep(*u.Email), e.totpCode(secret))
	wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	assert.Equal(t, before, sessions(), "no session was issued")

	// …while another TOTP user, and a user without TOTP, sign in as usual.
	other := e.newCheapStaff(domain.RoleOrganiser)
	otherSecret := e.enableTOTP(e.login(*other.Email, staffPassword))
	e.clock.Advance(31 * time.Second)
	require.Equal(t, http.StatusOK, e.totpVerify(e.passwordStep(*other.Email), e.totpCode(otherSecret)).Code)
	plain := e.newCheapStaff(domain.RoleOrganiser)
	e.login(*plain.Email, staffPassword)

	// After the window the real owner signs in normally.
	e.clock.Advance(15 * time.Minute)
	rec = e.totpVerify(e.passwordStep(*u.Email), e.totpCode(secret))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotEmpty(t, decode[sessionJSON](t, rec).AccessToken)

	// That success cleared the count: the next window starts with all five tries, no more.
	e.clock.Advance(31 * time.Second)
	wrong = e.certainlyWrongTOTP(secret)
	looked = 0
	for i := 0; i < 8; i++ {
		if e.totpVerify(e.passwordStep(*u.Email), wrong).Code == http.StatusUnauthorized {
			looked++
		}
	}
	assert.Equal(t, 5, looked)
}

// With TOTP on, a good password is half a sign-in and must not clear the per-email count;
// only the finished sign-in does. Without TOTP nothing changes.
func TestStaffLogin_PasswordStepAloneDoesNotClearTheCountWhenTOTPIsOn(t *testing.T) {
	e := newEnv(t, withPolicy(func(p *auth.Policy) { p.LoginPerEmail = ratelimit.Rule{Limit: 3, Window: 15 * time.Minute} }))
	u := e.newCheapStaff(domain.RoleAdmin)
	secret := e.enableTOTP(e.login(*u.Email, staffPassword))
	e.clock.Advance(16 * time.Minute)

	// Three good passwords, never followed by a code: the fourth is held back.
	for i := 0; i < 3; i++ {
		e.passwordStep(*u.Email)
	}
	rec := e.loginRaw(*u.Email, staffPassword)
	wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))

	// A finished sign-in (password, then code) does clear it.
	e.clock.Advance(16 * time.Minute)
	for round := 0; round < 3; round++ {
		wantError(t, e.loginRaw(*u.Email, "wrong-password"), http.StatusUnauthorized, domain.CodeInvalidCredentials)
		wantError(t, e.loginRaw(*u.Email, "wrong-password"), http.StatusUnauthorized, domain.CodeInvalidCredentials)
		require.Equal(t, http.StatusOK, e.totpVerify(e.passwordStep(*u.Email), e.totpCode(secret)).Code, "round %d", round)
		e.clock.Advance(31 * time.Second) // the next round needs the next code
	}

	// A user without TOTP: a good password is the whole sign-in and clears the count as before.
	plain := e.newCheapStaff(domain.RoleOrganiser)
	for round := 0; round < 3; round++ {
		wantError(t, e.loginRaw(*plain.Email, "wrong-password"), http.StatusUnauthorized, domain.CodeInvalidCredentials)
		wantError(t, e.loginRaw(*plain.Email, "wrong-password"), http.StatusUnauthorized, domain.CodeInvalidCredentials)
		e.login(*plain.Email, staffPassword)
	}
}

// Sign-in codes go only to the allowed countries (Kenya unless configured otherwise). Others
// get the ordinary validation answer on `phone`, and no SMS is paid for.
func TestOTPRequest_OnlyAllowedCountryCodes(t *testing.T) {
	foreign := []string{"+2557" + randDigits(t, 8), "+1415" + randDigits(t, 7), "+4477" + randDigits(t, 8)}

	t.Run("default allows 254 only", func(t *testing.T) {
		e := newEnv(t)
		for _, phone := range foreign {
			rec := e.do("POST", "/auth/otp/request", map[string]string{"phone": phone}, "")
			assert.Equal(t, "e164", wantFields(t, rec, "phone")["phone"], phone)
			assert.Equal(t, 0, e.sms.count(phone), "no SMS to %s", phone)
			assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM otp_codes WHERE phone_e164 = $1`, phone))
			wantFields(t, e.verifyCode(phone, "123456"), "phone")
		}
		// Kenya works, in every spelling.
		e.requestCode(randPhone(t))
	})

	t.Run("more countries can be allowed", func(t *testing.T) {
		e := newEnv(t, withOTPCountries("254", "255"))
		e.requestCode(foreign[0])
		e.requestCode(randPhone(t))
		wantFields(t, e.do("POST", "/auth/otp/request", map[string]string{"phone": foreign[1]}, ""), "phone")
	})
}

// However many phones ask, the number of SMS per hour and per day is bounded, and reaching
// the bound is logged at Error: that line is the alert.
func TestOTPRequest_GlobalSMSCaps(t *testing.T) {
	t.Run("hourly", func(t *testing.T) {
		e := newEnv(t, withPolicy(func(p *auth.Policy) { p.OTPGlobalHourly = ratelimit.Rule{Limit: 3, Window: time.Hour} }))
		first := randPhone(t)
		e.requestCode(first)
		// Refused for the phone's own resend gap: no SMS, so it must not use up the global cap.
		wantError(t, e.do("POST", "/auth/otp/request", map[string]string{"phone": first}, ""), http.StatusTooManyRequests, domain.CodeRateLimited)
		e.requestCode(randPhone(t))
		e.requestCode(randPhone(t))
		assert.NotContains(t, e.logs.String(), "global sign-in SMS cap")

		// The fourth SMS of the hour, to a phone that has never asked before.
		blocked := randPhone(t)
		rec := e.do("POST", "/auth/otp/request", map[string]string{"phone": blocked}, "")
		wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
		assert.NotEmpty(t, rec.Header().Get("Retry-After"))
		assert.Equal(t, 0, e.sms.count(blocked), "no SMS was sent")
		assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM otp_codes WHERE phone_e164 = $1`, blocked))

		alert := lastLogLine(t, e.logs.String(), "global sign-in SMS cap")
		assert.Contains(t, alert, `"level":"ERROR"`)
		assert.Contains(t, alert, `"window":"hour"`)
		assert.Contains(t, alert, `"limit":3`)
		assert.NotContains(t, alert, blocked, "the alert does not need the phone number")

		// The next hour is a new allowance.
		e.clock.Advance(time.Hour + time.Second)
		e.requestCode(blocked)
	})

	t.Run("daily", func(t *testing.T) {
		e := newEnv(t, withPolicy(func(p *auth.Policy) { p.OTPGlobalDaily = ratelimit.Rule{Limit: 2, Window: 24 * time.Hour} }))
		e.requestCode(randPhone(t))
		e.clock.Advance(2 * time.Hour) // a new hour does not help
		e.requestCode(randPhone(t))
		e.clock.Advance(2 * time.Hour)

		blocked := randPhone(t)
		rec := e.do("POST", "/auth/otp/request", map[string]string{"phone": blocked}, "")
		wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
		assert.Equal(t, 0, e.sms.count(blocked))
		alert := lastLogLine(t, e.logs.String(), "global sign-in SMS cap")
		assert.Contains(t, alert, `"level":"ERROR"`)
		assert.Contains(t, alert, `"window":"day"`)

		e.clock.Advance(24 * time.Hour)
		e.requestCode(blocked)
	})

	t.Run("defaults", func(t *testing.T) {
		p := auth.DefaultPolicy()
		assert.Equal(t, ratelimit.Rule{Limit: 2000, Window: time.Hour}, p.OTPGlobalHourly)
		assert.Equal(t, ratelimit.Rule{Limit: 10000, Window: 24 * time.Hour}, p.OTPGlobalDaily)
		assert.Equal(t, ratelimit.Rule{Limit: 5, Window: 15 * time.Minute}, p.TOTPPerUser)
		assert.Equal(t, []string{"254"}, auth.DefaultOTPCountryCodes())
	})
}

func lastLogLine(t testing.TB, logs, containing string) string {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(logs), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], containing) {
			return lines[i]
		}
	}
	t.Fatalf("no log line contains %q", containing)
	return ""
}
