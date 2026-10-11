//go:build integration

package httpapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

func (e *env) loginRaw(email, password string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("POST", "/auth/login", map[string]string{"email": email, "password": password}, "")
}

func TestStaffLogin_Good(t *testing.T) {
	e := newEnv(t)
	u := e.newStaff(domain.RoleOrganiser)

	rec := e.loginRaw(*u.Email, staffPassword)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	s := decode[sessionJSON](t, rec)
	require.NotNil(t, s.MFARequired)
	assert.False(t, *s.MFARequired)
	assert.Equal(t, "Bearer", s.TokenType)
	assert.Equal(t, 900, s.ExpiresInSeconds)
	assert.Equal(t, u.ID, s.User.ID)
	assert.Equal(t, "ORGANISER", s.User.Role)
	assert.Equal(t, *u.Email, *s.User.Email)
	assert.Nil(t, s.User.Phone)
	assert.NotContains(t, rec.Body.String(), "argon2", "the hash never leaves the server")
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	// The email is not case-sensitive.
	e.login(strings.ToUpper(*u.Email), staffPassword)

	// The session works and can be refreshed.
	require.Equal(t, http.StatusOK, e.do("GET", "/me", nil, s.AccessToken).Code)
	require.Equal(t, http.StatusOK, e.refresh(s.RefreshToken).Code)
}

// Wrong password and unknown email are one answer, byte for byte.
func TestStaffLogin_WrongPasswordAndUnknownEmailLookTheSame(t *testing.T) {
	e := newEnv(t)
	u := e.newStaff(domain.RoleAdmin)

	wrong := e.loginRaw(*u.Email, "not-the-password")
	wantError(t, wrong, http.StatusUnauthorized, domain.CodeInvalidCredentials)

	unknown := e.loginRaw(randEmail(t, "nobody"), staffPassword)
	wantError(t, unknown, http.StatusUnauthorized, domain.CodeInvalidCredentials)

	assert.JSONEq(t, wrong.Body.String(), unknown.Body.String())
	assert.Equal(t, wrong.Header().Get("Content-Type"), unknown.Header().Get("Content-Type"))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1`, u.ID))
}

// An unknown email costs one argon2 verification, like a wrong password, so the two cannot
// be told apart with a stopwatch. Compared against a request that does no hashing at all.
func TestStaffLogin_UnknownEmailTakesAsLongAsAWrongPassword(t *testing.T) {
	e := newEnv(t)
	u := e.newStaff(domain.RoleAdmin)

	median := func(fn func()) time.Duration {
		var ds []time.Duration
		for i := 0; i < 5; i++ {
			start := time.Now()
			fn()
			ds = append(ds, time.Since(start))
		}
		for i := range ds {
			for j := i + 1; j < len(ds); j++ {
				if ds[j] < ds[i] {
					ds[i], ds[j] = ds[j], ds[i]
				}
			}
		}
		return ds[len(ds)/2]
	}
	wrongPassword := median(func() { e.loginRaw(*u.Email, "not-the-password") })
	e2 := newEnv(t)
	unknownEmail := median(func() { e2.loginRaw(randEmail(t, "nobody"), "not-the-password") })
	e3 := newEnv(t)
	noHashing := median(func() { e3.do("POST", "/auth/login", `{"email":"nope"}`, "") })

	t.Logf("wrong password %s, unknown email %s, no hashing %s", wrongPassword, unknownEmail, noHashing)
	assert.Greater(t, unknownEmail, 5*noHashing, "an unknown email must still pay for a hash")
	ratio := float64(unknownEmail) / float64(wrongPassword)
	assert.Greater(t, ratio, 0.5, "unknown email and wrong password should take about as long")
	assert.Less(t, ratio, 2.0, "unknown email and wrong password should take about as long")
}

func TestStaffLogin_AccountsThatCannotUseAPassword(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// A fan with an email but no password.
	phone, email := randPhone(t), randEmail(t, "fan")
	_, err := e.q.CreateUser(ctx, store.CreateUserParams{PhoneE164: &phone, Email: &email, Role: "FAN"})
	require.NoError(t, err)
	wantError(t, e.loginRaw(email, staffPassword), http.StatusUnauthorized, domain.CodeInvalidCredentials)

	// A fan row that somehow has a password hash still cannot sign in this way.
	phone2, email2 := randPhone(t), randEmail(t, "fan")
	hash := staffPasswordHash
	_, err = e.q.CreateUser(ctx, store.CreateUserParams{PhoneE164: &phone2, Email: &email2, Role: "FAN", PasswordHash: &hash})
	require.NoError(t, err)
	wantError(t, e.loginRaw(email2, staffPassword), http.StatusUnauthorized, domain.CodeInvalidCredentials)
}

func TestStaffLogin_Validation(t *testing.T) {
	e := newEnv(t)
	wantFields(t, e.do("POST", "/auth/login", `{}`, ""), "email", "password")
	rules := wantFields(t, e.loginRaw("not-an-email", "x"), "email")
	assert.Equal(t, "email", rules["email"])
	wantFields(t, e.loginRaw("a@b.co", ""), "password")
	wantFields(t, e.loginRaw("a@b.co", strings.Repeat("x", 257)), "password")
	wantFields(t, e.do("POST", "/auth/login", `{"email":"a@b.co","password":"x","remember":true}`, ""), "remember")
}

func TestStaffLogin_RateLimitPerEmailAndPerIP(t *testing.T) {
	t.Run("per email, whatever the address", func(t *testing.T) {
		e := newEnv(t, withPolicy(func(p *auth.Policy) { p.LoginPerEmail = ratelimit.Rule{Limit: 3, Window: 15 * time.Minute} }))
		u := e.newStaff(domain.RoleOrganiser)

		for i := 0; i < 3; i++ {
			e.remoteAddr = "198.51.100." + string(rune('1'+i)) + ":40000"
			wantError(t, e.loginRaw(*u.Email, "wrong-password"), http.StatusUnauthorized, domain.CodeInvalidCredentials)
		}
		e.remoteAddr = "198.51.100.200:40000"
		// Now even the right password is held back, with a Retry-After.
		rec := e.loginRaw(*u.Email, staffPassword)
		wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
		assert.NotEmpty(t, rec.Header().Get("Retry-After"))

		// Another account is unaffected, and the window ends.
		other := e.newStaff(domain.RoleOrganiser)
		e.login(*other.Email, staffPassword)
		e.clock.Advance(16 * time.Minute)
		e.login(*u.Email, staffPassword)
	})

	t.Run("a good sign-in clears the count", func(t *testing.T) {
		e := newEnv(t, withPolicy(func(p *auth.Policy) { p.LoginPerEmail = ratelimit.Rule{Limit: 3, Window: 15 * time.Minute} }))
		u := e.newStaff(domain.RoleOrganiser)
		for round := 0; round < 3; round++ {
			wantError(t, e.loginRaw(*u.Email, "wrong-password"), http.StatusUnauthorized, domain.CodeInvalidCredentials)
			wantError(t, e.loginRaw(*u.Email, "wrong-password"), http.StatusUnauthorized, domain.CodeInvalidCredentials)
			e.login(*u.Email, staffPassword)
		}
	})

	t.Run("per IP, whatever the email", func(t *testing.T) {
		e := newEnv(t, withLimits(func(l *httpapi.RateLimits) { l.LoginPerIP = ratelimit.Rule{Limit: 2, Window: 15 * time.Minute} }))
		for i := 0; i < 2; i++ {
			wantError(t, e.loginRaw(randEmail(t, "x"), "whatever-password"), http.StatusUnauthorized, domain.CodeInvalidCredentials)
		}
		rec := e.loginRaw(randEmail(t, "x"), "whatever-password")
		wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
		assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	})
}

// enableTOTP runs setup and enable for a signed-in staff user and returns the secret.
func (e *env) enableTOTP(s sessionJSON) string {
	e.t.Helper()
	rec := e.do("POST", "/me/totp/setup", nil, s.AccessToken)
	require.Equal(e.t, http.StatusOK, rec.Code, rec.Body.String())
	setup := decode[struct {
		Secret     string `json:"secret"`
		OtpauthURL string `json:"otpauth_url"`
	}](e.t, rec)

	code, err := auth.TOTPCode(setup.Secret, e.clock.Now())
	require.NoError(e.t, err)
	rec = e.do("POST", "/me/totp/enable", map[string]string{"code": code}, s.AccessToken)
	require.Equal(e.t, http.StatusOK, rec.Code, rec.Body.String())
	require.True(e.t, decode[userJSON](e.t, rec).TOTPEnabled)
	return setup.Secret
}

func (e *env) totpCode(secret string) string {
	e.t.Helper()
	code, err := auth.TOTPCode(secret, e.clock.Now())
	require.NoError(e.t, err)
	return code
}

func (e *env) totpVerify(mfaToken, code string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("POST", "/auth/totp/verify", map[string]string{"mfa_token": mfaToken, "code": code}, "")
}

// passwordStep signs in a TOTP user by password and returns the mfa_token.
func (e *env) passwordStep(email string) string {
	e.t.Helper()
	rec := e.loginRaw(email, staffPassword)
	require.Equal(e.t, http.StatusOK, rec.Code, rec.Body.String())
	s := decode[sessionJSON](e.t, rec)
	require.NotNil(e.t, s.MFARequired)
	require.True(e.t, *s.MFARequired, "body: %s", rec.Body.String())
	require.NotEmpty(e.t, s.MFAToken)
	require.Empty(e.t, s.AccessToken, "no session before the second step")
	require.Empty(e.t, s.RefreshToken)
	return s.MFAToken
}

func TestStaffLogin_TOTPTwoStep(t *testing.T) {
	e := newEnv(t)
	u, s := e.staffSession(domain.RoleAdmin)
	secret := e.enableTOTP(s)

	// The code that turned TOTP on cannot be the one that signs in: move to the next step.
	e.clock.Advance(31 * time.Second)

	// Step one: the password alone no longer gives a session.
	rec := e.loginRaw(*u.Email, staffPassword)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"mfa_required":true,"mfa_token":"`+decode[sessionJSON](t, rec).MFAToken+`","expires_in_seconds":300}`, rec.Body.String())
	mfaToken := decode[sessionJSON](t, rec).MFAToken
	before := queryInt(t, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1`, u.ID)

	// The mfa_token is not a bearer token.
	wantError(t, e.do("GET", "/me", nil, mfaToken), http.StatusUnauthorized, domain.CodeUnauthenticated)

	// Step two.
	rec = e.totpVerify(mfaToken, e.totpCode(secret))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	session := decode[sessionJSON](t, rec)
	assert.Nil(t, session.MFARequired, "the second step answers a plain AuthSession")
	assert.Equal(t, u.ID, session.User.ID)
	assert.True(t, session.User.TOTPEnabled)
	assert.Equal(t, before+1, queryInt(t, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1`, u.ID))
	require.Equal(t, http.StatusOK, e.do("GET", "/me", nil, session.AccessToken).Code)

	// The mfa_token is single-use: even with the next code it is dead.
	e.clock.Advance(31 * time.Second)
	wantError(t, e.totpVerify(mfaToken, e.totpCode(secret)), http.StatusUnauthorized, domain.CodeMFATokenInvalid)
}

func TestStaffLogin_TOTPWrongCode(t *testing.T) {
	e := newEnv(t)
	u, s := e.staffSession(domain.RoleOrganiser)
	secret := e.enableTOTP(s)
	e.clock.Advance(31 * time.Second)
	mfaToken := e.passwordStep(*u.Email)

	// A wrong code may be retried…
	wantError(t, e.totpVerify(mfaToken, wrongCode(e.totpCode(secret))), http.StatusUnauthorized, domain.CodeTOTPInvalid)
	require.Equal(t, http.StatusOK, e.totpVerify(mfaToken, e.totpCode(secret)).Code)

	// …but only so often: five tries and the token is dead, right code or not.
	e.clock.Advance(31 * time.Second)
	mfaToken = e.passwordStep(*u.Email)
	for i := 0; i < 5; i++ {
		wantError(t, e.totpVerify(mfaToken, wrongCode(e.totpCode(secret))), http.StatusUnauthorized, domain.CodeTOTPInvalid)
	}
	wantError(t, e.totpVerify(mfaToken, e.totpCode(secret)), http.StatusUnauthorized, domain.CodeMFATokenInvalid)

	// Starting again at the password works.
	require.Equal(t, http.StatusOK, e.totpVerify(e.passwordStep(*u.Email), e.totpCode(secret)).Code)
}

func TestStaffLogin_TOTPExpiredAndForgedMFAToken(t *testing.T) {
	e := newEnv(t)
	u, s := e.staffSession(domain.RoleOrganiser)
	secret := e.enableTOTP(s)
	e.clock.Advance(31 * time.Second)

	// Expired: five minutes and a second after the password step.
	mfaToken := e.passwordStep(*u.Email)
	e.clock.Advance(5*time.Minute + time.Second)
	wantError(t, e.totpVerify(mfaToken, e.totpCode(secret)), http.StatusUnauthorized, domain.CodeMFATokenInvalid)

	// An access token is not an mfa_token, even the user's own.
	wantError(t, e.totpVerify(s.AccessToken, e.totpCode(secret)), http.StatusUnauthorized, domain.CodeMFATokenInvalid)

	// A token of the right shape signed with the JWT secret itself (not the MFA key).
	forged, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject: "1", ID: "x", IssuedAt: jwt.NewNumericDate(time.Now()), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute)),
	}).SignedString([]byte(testSecret))
	require.NoError(t, err)
	wantError(t, e.totpVerify(forged, e.totpCode(secret)), http.StatusUnauthorized, domain.CodeMFATokenInvalid)
	wantError(t, e.totpVerify("garbage", "123456"), http.StatusUnauthorized, domain.CodeMFATokenInvalid)

	wantFields(t, e.do("POST", "/auth/totp/verify", `{}`, ""), "mfa_token", "code")
	wantFields(t, e.totpVerify("x", "12345"), "code")
}

// A code that has signed the user in once is refused a second time while it is still in
// its window, even with a fresh password step.
func TestStaffLogin_TOTPCodeReplayIsRefused(t *testing.T) {
	e := newEnv(t)
	u, s := e.staffSession(domain.RoleAdmin)
	secret := e.enableTOTP(s)
	e.clock.Advance(31 * time.Second)

	code := e.totpCode(secret)
	require.Equal(t, http.StatusOK, e.totpVerify(e.passwordStep(*u.Email), code).Code)

	second := e.passwordStep(*u.Email)
	wantError(t, e.totpVerify(second, code), http.StatusUnauthorized, domain.CodeTOTPInvalid)

	// The refused replay did not burn the token: the next code signs in.
	e.clock.Advance(31 * time.Second)
	require.Equal(t, http.StatusOK, e.totpVerify(second, e.totpCode(secret)).Code)
}

func TestStaffLogin_TOTPTurnedOffAfterThePasswordStep(t *testing.T) {
	e := newEnv(t)
	u, s := e.staffSession(domain.RoleOrganiser)
	secret := e.enableTOTP(s)
	e.clock.Advance(31 * time.Second)
	mfaToken := e.passwordStep(*u.Email)

	rec := e.do("POST", "/me/totp/disable", map[string]string{"password": staffPassword}, s.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	wantError(t, e.totpVerify(mfaToken, e.totpCode(secret)), http.StatusUnauthorized, domain.CodeMFATokenInvalid)
	e.login(*u.Email, staffPassword) // back to one step
}
