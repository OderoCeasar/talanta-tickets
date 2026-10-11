//go:build integration

package httpapi_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
)

func TestMe_GetAndUpdate(t *testing.T) {
	e := newEnv(t)
	fan := e.phoneSession(randPhone(t))
	other := e.phoneSession(randPhone(t))

	rec := e.do("GET", "/me", nil, fan.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	me := decode[userJSON](t, rec)
	assert.Equal(t, fan.User.ID, me.ID)
	assert.Nil(t, me.Name)

	rec = e.do("PATCH", "/me", map[string]string{"name": "  Achieng   Odhiambo "}, fan.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "Achieng Odhiambo", *decode[userJSON](t, rec).Name, "trimmed and single-spaced")

	// It changed the caller and nobody else. There is no way to name another user: the ID
	// comes from the token only.
	rec = e.do("GET", "/me", nil, other.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Nil(t, decode[userJSON](t, rec).Name)
	assert.Equal(t, other.User.ID, decode[userJSON](t, rec).ID)

	wantFields(t, e.do("PATCH", "/me", `{}`, fan.AccessToken), "name")
	wantFields(t, e.do("PATCH", "/me", map[string]string{"name": ""}, fan.AccessToken), "name")
	wantFields(t, e.do("PATCH", "/me", map[string]string{"name": "   "}, fan.AccessToken), "name")
	wantFields(t, e.do("PATCH", "/me", map[string]string{"name": strings.Repeat("a", 101)}, fan.AccessToken), "name")
	wantFields(t, e.do("PATCH", "/me", `{"name":"A","role":"ADMIN"}`, fan.AccessToken), "role")
	wantFields(t, e.do("PATCH", "/me", `{"name":"A","id":1}`, fan.AccessToken), "id")

	// Role is untouched by any of that.
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM users WHERE id = $1 AND role = 'FAN'`, fan.User.ID))
}

func TestMe_NeedsAGoodToken(t *testing.T) {
	e := newEnv(t)
	for _, path := range []string{"/me"} {
		rec := e.do("GET", path, nil, "")
		wantError(t, rec, http.StatusUnauthorized, domain.CodeUnauthenticated)
		assert.Contains(t, rec.Header().Get("WWW-Authenticate"), "Bearer")
	}
	wantError(t, e.do("PATCH", "/me", map[string]string{"name": "X"}, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
	wantError(t, e.do("PATCH", "/me", map[string]string{"name": "X"}, "garbage"), http.StatusUnauthorized, domain.CodeUnauthenticated)

	// A token whose user has since been deleted is not a sign-in.
	gone := e.phoneSession(randPhone(t))
	exec(t, `DELETE FROM users WHERE id = $1`, gone.User.ID)
	wantError(t, e.do("GET", "/me", nil, gone.AccessToken), http.StatusUnauthorized, domain.CodeUnauthenticated)
	wantError(t, e.do("PATCH", "/me", map[string]string{"name": "X"}, gone.AccessToken), http.StatusUnauthorized, domain.CodeUnauthenticated)
}

// Tampered, expired and wrong-algorithm tokens against a real endpoint.
func TestAccessToken_TamperedExpiredWrongAlg(t *testing.T) {
	e := newEnv(t)
	u, s := e.staffSession(domain.RoleOrganiser)
	parts := strings.Split(s.AccessToken, ".")
	require.Len(t, parts, 3)

	now := time.Now()
	claims := func(role string) jwt.MapClaims {
		return jwt.MapClaims{"sub": strconv.FormatInt(u.ID, 10), "role": role, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "jti": "forged"}
	}
	sign := func(m jwt.SigningMethod, key any, c jwt.Claims) string {
		tok, err := jwt.NewWithClaims(m, c).SignedString(key)
		require.NoError(t, err)
		return tok
	}
	b64 := func(v string) string { return base64.RawURLEncoding.EncodeToString([]byte(v)) }
	adminPayload := b64(`{"sub":"` + strconv.FormatInt(u.ID, 10) + `","role":"ADMIN","iat":` + strconv.FormatInt(now.Unix(), 10) + `,"exp":` + strconv.FormatInt(now.Add(time.Hour).Unix(), 10) + `,"jti":"x"}`)
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	flipped := parts[2][:len(parts[2])-2] + "AA"
	if flipped == parts[2] {
		flipped = parts[2][:len(parts[2])-2] + "BB"
	}

	forgeries := map[string]string{
		"payload edited to ADMIN":     parts[0] + "." + adminPayload + "." + parts[2],
		"signature altered":           parts[0] + "." + parts[1] + "." + flipped,
		"signature removed":           parts[0] + "." + parts[1] + ".",
		"alg none":                    b64(`{"alg":"none","typ":"JWT"}`) + "." + adminPayload + ".",
		"alg none via library":        sign(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, claims("ADMIN")),
		"HS512 with the real secret":  sign(jwt.SigningMethodHS512, []byte(testSecret), claims("ADMIN")),
		"RS256":                       sign(jwt.SigningMethodRS256, rsaKey, claims("ADMIN")),
		"HS256 with the wrong secret": sign(jwt.SigningMethodHS256, []byte("another-secret-of-at-least-32-bytes!!"), claims("ADMIN")),
		"unknown role":                sign(jwt.SigningMethodHS256, []byte(testSecret), claims("ROOT")),
		"not a jwt":                   "hello",
	}
	for name, token := range forgeries {
		t.Run(name, func(t *testing.T) {
			wantError(t, e.do("GET", "/me", nil, token), http.StatusUnauthorized, domain.CodeUnauthenticated)
			// And certainly not an admin.
			wantError(t, e.do("GET", "/admin/users", nil, token), http.StatusUnauthorized, domain.CodeUnauthenticated)
		})
	}

	t.Run("expired", func(t *testing.T) {
		e.clock.Advance(14 * time.Minute)
		require.Equal(t, http.StatusOK, e.do("GET", "/me", nil, s.AccessToken).Code, "still good at 14 minutes")
		e.clock.Advance(61 * time.Second)
		wantError(t, e.do("GET", "/me", nil, s.AccessToken), http.StatusUnauthorized, domain.CodeTokenExpired)
		// The refresh token is how the client gets going again.
		rec := e.refresh(s.RefreshToken)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, http.StatusOK, e.do("GET", "/me", nil, decode[sessionJSON](t, rec).AccessToken).Code)
	})

	t.Run("a token from another deployment's secret", func(t *testing.T) {
		wantError(t, e.do("GET", "/me", nil, sign(jwt.SigningMethodHS256, []byte("talanta-dev-only-jwt-secret-do-not-use-in-production"), claims("ADMIN"))),
			http.StatusUnauthorized, domain.CodeUnauthenticated)
	})
}

func TestChangePassword(t *testing.T) {
	e := newEnv(t)
	u, here := e.staffSession(domain.RoleOrganiser)
	elsewhere := e.login(*u.Email, staffPassword) // a second device
	const newPassword = "a-brand-new-pass-phrase"

	// Wrong current password: 422, not 401, and nothing changes.
	rec := e.do("POST", "/me/password", map[string]string{"current_password": "not-it-at-all", "new_password": newPassword}, here.AccessToken)
	wantError(t, rec, http.StatusUnprocessableEntity, domain.CodePasswordIncorrect)
	e.login(*u.Email, staffPassword)

	rules := wantFields(t, e.do("POST", "/me/password", map[string]string{"current_password": staffPassword, "new_password": "short"}, here.AccessToken), "new_password")
	assert.Equal(t, "min", rules["new_password"])
	wantFields(t, e.do("POST", "/me/password", `{}`, here.AccessToken), "current_password", "new_password")
	wantError(t, e.do("POST", "/me/password", map[string]string{"current_password": staffPassword, "new_password": newPassword}, ""),
		http.StatusUnauthorized, domain.CodeUnauthenticated)

	rec = e.do("POST", "/me/password", map[string]string{"current_password": staffPassword, "new_password": newPassword}, here.AccessToken)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Body.String())

	// The old password is dead, the new one works.
	wantError(t, e.loginRaw(*u.Email, staffPassword), http.StatusUnauthorized, domain.CodeInvalidCredentials)
	e.login(*u.Email, newPassword)

	// Every other session is signed out; the calling one stays.
	wantError(t, e.refresh(elsewhere.RefreshToken), http.StatusUnauthorized, domain.CodeRefreshTokenInvalid)
	require.Equal(t, http.StatusOK, e.refresh(here.RefreshToken).Code)

	// Another user's sessions are none of this.
	_, bystander := e.staffSession(domain.RoleOrganiser)
	require.Equal(t, http.StatusOK, e.refresh(bystander.RefreshToken).Code)
}

func TestPasswordAndTOTPRoutesAreStaffOnly(t *testing.T) {
	e := newEnv(t)
	fan := e.phoneSession(randPhone(t))

	calls := []struct {
		path string
		body any
	}{
		{"/me/password", map[string]string{"current_password": "x", "new_password": "long-enough-password"}},
		{"/me/totp/setup", nil},
		{"/me/totp/enable", map[string]string{"code": "123456"}},
		{"/me/totp/disable", map[string]string{"password": "x"}},
	}
	for _, c := range calls {
		wantError(t, e.do("POST", c.path, c.body, fan.AccessToken), http.StatusForbidden, domain.CodeForbidden)
		wantError(t, e.do("POST", c.path, c.body, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
	}
}

func TestTOTPSelfService(t *testing.T) {
	// This test makes more calls than one user is allowed in fifteen minutes.
	e := newEnv(t, withLimits(func(l *httpapi.RateLimits) {
		l.SensitivePerUser = ratelimit.Rule{Limit: 100, Window: 15 * time.Minute}
	}))
	u, s := e.staffSession(domain.RoleAdmin)

	// Enable before setup.
	wantError(t, e.do("POST", "/me/totp/enable", map[string]string{"code": "123456"}, s.AccessToken),
		http.StatusConflict, domain.CodeTOTPSetupRequired)
	// Disable when it is not on.
	wantError(t, e.do("POST", "/me/totp/disable", map[string]string{"password": staffPassword}, s.AccessToken),
		http.StatusConflict, domain.CodeTOTPNotEnabled)

	// Setup returns a secret and an otpauth URL that carries it.
	rec := e.do("POST", "/me/totp/setup", nil, s.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	first := decode[struct {
		Secret     string `json:"secret"`
		OtpauthURL string `json:"otpauth_url"`
	}](t, rec)
	assert.Regexp(t, `^[A-Z2-7]{32}$`, first.Secret)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	parsed, err := url.Parse(first.OtpauthURL)
	require.NoError(t, err)
	assert.Equal(t, "otpauth", parsed.Scheme)
	assert.Equal(t, "totp", parsed.Host)
	assert.Equal(t, "/Talanta Tickets:"+*u.Email, parsed.Path)
	assert.Equal(t, first.Secret, parsed.Query().Get("secret"))
	assert.Equal(t, "Talanta Tickets", parsed.Query().Get("issuer"))

	// Not on yet: sign-in is still one step.
	e.login(*u.Email, staffPassword)

	// Setup again replaces the secret; a code from the first one no longer turns it on.
	rec = e.do("POST", "/me/totp/setup", nil, s.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code)
	second := decode[struct {
		Secret string `json:"secret"`
	}](t, rec)
	require.NotEqual(t, first.Secret, second.Secret)
	wantError(t, e.do("POST", "/me/totp/enable", map[string]string{"code": e.totpCode(first.Secret)}, s.AccessToken),
		http.StatusUnprocessableEntity, domain.CodeTOTPCodeIncorrect)
	wantFields(t, e.do("POST", "/me/totp/enable", map[string]string{"code": "12345"}, s.AccessToken), "code")
	wantFields(t, e.do("POST", "/me/totp/enable", `{}`, s.AccessToken), "code")

	rec = e.do("POST", "/me/totp/enable", map[string]string{"code": e.totpCode(second.Secret)}, s.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, decode[userJSON](t, rec).TOTPEnabled)

	rec = e.do("GET", "/me", nil, s.AccessToken)
	assert.True(t, decode[userJSON](t, rec).TOTPEnabled)

	// Already on.
	wantError(t, e.do("POST", "/me/totp/setup", nil, s.AccessToken), http.StatusConflict, domain.CodeTOTPAlreadyEnabled)
	wantError(t, e.do("POST", "/me/totp/enable", map[string]string{"code": "123456"}, s.AccessToken), http.StatusConflict, domain.CodeTOTPAlreadyEnabled)

	// Turning it off needs the password.
	wantError(t, e.do("POST", "/me/totp/disable", map[string]string{"password": "not-the-password"}, s.AccessToken),
		http.StatusUnprocessableEntity, domain.CodePasswordIncorrect)
	wantFields(t, e.do("POST", "/me/totp/disable", `{}`, s.AccessToken), "password")
	rec = e.do("POST", "/me/totp/disable", map[string]string{"password": staffPassword}, s.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, decode[userJSON](t, rec).TOTPEnabled)

	// The secret is deleted, and sign-in is one step again.
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM users WHERE id = $1 AND totp_secret IS NULL AND NOT totp_enabled`, u.ID))
	e.login(*u.Email, staffPassword)
}

func TestSensitiveRoutesAreRateLimitedPerUser(t *testing.T) {
	e := newEnv(t, withLimits(func(l *httpapi.RateLimits) {
		l.SensitivePerUser = ratelimit.Rule{Limit: 2, Window: 15 * time.Minute}
	}))
	_, s := e.staffSession(domain.RoleOrganiser)
	_, other := e.staffSession(domain.RoleOrganiser)
	body := map[string]string{"current_password": "wrong-password", "new_password": "a-brand-new-pass-phrase"}

	for i := 0; i < 2; i++ {
		wantError(t, e.do("POST", "/me/password", body, s.AccessToken), http.StatusUnprocessableEntity, domain.CodePasswordIncorrect)
	}
	rec := e.do("POST", "/me/password", body, s.AccessToken)
	wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	// The limit covers the TOTP settings too, and is per user.
	for _, c := range []struct {
		path string
		body any
	}{
		{"/me/totp/setup", nil},
		{"/me/totp/enable", map[string]string{"code": "123456"}},
		{"/me/totp/disable", map[string]string{"password": "x"}},
	} {
		wantError(t, e.do("POST", c.path, c.body, s.AccessToken), http.StatusTooManyRequests, domain.CodeRateLimited)
	}
	wantError(t, e.do("POST", "/me/password", body, other.AccessToken), http.StatusUnprocessableEntity, domain.CodePasswordIncorrect)
}

// Done-when: a fan refused on an admin route. And the rest of the role matrix for the
// routes this milestone adds.
func TestAuthorisation_RolesOnAdminRoutes(t *testing.T) {
	e := newEnv(t)
	fan := e.phoneSession(randPhone(t)).AccessToken
	gatePhone := randPhone(t)
	exec(t, `INSERT INTO users (phone_e164, role) VALUES ($1, 'GATE_STAFF')`, gatePhone)
	gate := e.phoneSession(gatePhone).AccessToken
	_, organiserSession := e.staffSession(domain.RoleOrganiser)
	organiser := organiserSession.AccessToken
	_, adminSession := e.staffSession(domain.RoleAdmin)
	admin := adminSession.AccessToken

	// Something real for resend and revoke to be refused on.
	rec := e.do("POST", "/admin/invitations", map[string]string{"email": randEmail(t, "invitee"), "role": "ORGANISER"}, admin)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	id := strconv.FormatInt(decode[invitationJSON](t, rec).ID, 10)

	adminOnly := []struct {
		method, path string
		body         any
	}{
		{"GET", "/admin/users", nil},
		{"GET", "/admin/invitations", nil},
		{"POST", "/admin/invitations", map[string]string{"email": randEmail(t, "x"), "role": "ADMIN"}},
		{"POST", "/admin/invitations/" + id + "/resend", nil},
		{"DELETE", "/admin/invitations/" + id, nil},
	}
	before := queryInt(t, `SELECT count(*) FROM invitations`)
	for _, c := range adminOnly {
		name := c.method + " " + c.path
		wantError(t, e.do(c.method, c.path, c.body, ""), http.StatusUnauthorized, domain.CodeUnauthenticated)
		wantError(t, e.do(c.method, c.path, c.body, fan), http.StatusForbidden, domain.CodeForbidden)
		wantError(t, e.do(c.method, c.path, c.body, gate), http.StatusForbidden, domain.CodeForbidden)
		// An organiser is staff, but inviting staff is for admins.
		rec := e.do(c.method, c.path, c.body, organiser)
		wantError(t, rec, http.StatusForbidden, domain.CodeForbidden)
		assert.NotContains(t, rec.Body.String(), "invite_url", name)
	}
	assert.Equal(t, before, queryInt(t, `SELECT count(*) FROM invitations`), "a refused caller created nothing")
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM invitations WHERE id = $1 AND revoked_at IS NULL`, id), "and revoked nothing")
	assert.Empty(t, e.mail.all()[1:], "and sent no mail beyond the admin's own invitation")

	// The admin is let through on all of them.
	require.Equal(t, http.StatusOK, e.do("GET", "/admin/users", nil, admin).Code)
	require.Equal(t, http.StatusOK, e.do("GET", "/admin/invitations", nil, admin).Code)
	require.Equal(t, http.StatusOK, e.do("POST", "/admin/invitations/"+id+"/resend", nil, admin).Code)
	require.Equal(t, http.StatusNoContent, e.do("DELETE", "/admin/invitations/"+id, nil, admin).Code)
}

// A fan gets 403 on any /admin or /gate path, existing or not, so the admin API cannot be
// mapped from the difference between 403 and 404. (These paths are not in the contract for
// this milestone or at all, so they bypass the contract helper.)
func TestAuthorisation_FanCannotProbeAdminOrGatePaths(t *testing.T) {
	e := newEnv(t)
	fan := e.phoneSession(randPhone(t)).AccessToken
	_, organiser := e.staffSession(domain.RoleOrganiser)

	for _, path := range []string{"/admin", "/admin/", "/admin/events", "/admin/no-such-thing", "/admin/invitations/1/anything", "/gate/events", "/gate/nothing"} {
		rec := httptestDo(e, "GET", path, fan)
		assert.Equal(t, http.StatusForbidden, rec.Code, path)
		rec = httptestDo(e, "GET", path, "")
		assert.Equal(t, http.StatusUnauthorized, rec.Code, path)
	}
	// Staff see an honest 404 for what is not there yet, and 403 for the gate.
	assert.Equal(t, http.StatusNotFound, httptestDo(e, "GET", "/admin/no-such-thing", organiser.AccessToken).Code)
	assert.Equal(t, http.StatusForbidden, httptestDo(e, "GET", "/gate/events", organiser.AccessToken).Code)
}

// The admin site puts each validation message next to the form field of the same name, so
// details.fields[].field must be the JSON body field name exactly, for every body this
// milestone accepts.
func TestValidation_FieldNamesAreTheRequestBodyFieldNames(t *testing.T) {
	e := newEnv(t, withLimits(func(l *httpapi.RateLimits) {
		l.SensitivePerUser = ratelimit.Rule{Limit: 100, Window: 15 * time.Minute}
	}))
	_, admin := e.staffSession(domain.RoleAdmin)
	_, inviteToken := e.invite(admin.AccessToken, randEmail(t, "fields"), "ORGANISER")

	cases := []struct {
		method, path, body, token string
		fields                    []string
	}{
		{"POST", "/auth/otp/request", `{"phone":"x"}`, "", []string{"phone"}},
		{"POST", "/auth/otp/verify", `{"phone":"x","code":"1"}`, "", []string{"code"}},
		{"POST", "/auth/otp/verify", `{"phone":"x","code":"123456"}`, "", []string{"phone"}},
		{"POST", "/auth/otp/verify", `{}`, "", []string{"phone", "code"}},
		{"POST", "/auth/login", `{"email":"x","password":""}`, "", []string{"email", "password"}},
		{"POST", "/auth/totp/verify", `{"mfa_token":"","code":"abc"}`, "", []string{"mfa_token", "code"}},
		{"POST", "/auth/refresh", `{"refresh_token":""}`, "", []string{"refresh_token"}},
		{"POST", "/auth/logout", `{}`, "", []string{"refresh_token"}},
		{"POST", "/auth/invitations/" + inviteToken + "/accept", `{"name":"","password":"short"}`, "", []string{"name", "password"}},
		{"PATCH", "/me", `{"name":""}`, admin.AccessToken, []string{"name"}},
		{"POST", "/me/password", `{"current_password":"","new_password":"short"}`, admin.AccessToken, []string{"current_password", "new_password"}},
		{"POST", "/me/totp/enable", `{"code":"12"}`, admin.AccessToken, []string{"code"}},
		{"POST", "/me/totp/disable", `{"password":""}`, admin.AccessToken, []string{"password"}},
		{"POST", "/admin/invitations", `{"email":"x","role":"FAN"}`, admin.AccessToken, []string{"email", "role"}},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			wantFields(t, e.do(c.method, c.path, c.body, c.token), c.fields...)
		})
	}
}
