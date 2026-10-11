package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

var testSecret = []byte("unit-test-secret-of-at-least-32-bytes!!")

type clock struct{ t time.Time }

func (c *clock) Now() time.Time { return c.t }

func newTestTokens() (*Tokens, *clock) {
	c := &clock{t: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)}
	return NewTokens(testSecret, c.Now), c
}

func TestAccessTokenRoundTripAndClaims(t *testing.T) {
	tokens, c := newTestTokens()
	token, err := tokens.IssueAccess(42, domain.RoleOrganiser, "5d0c8a52-9f5e-4c1b-a1a0-3a8f0f8a0001")
	require.NoError(t, err)

	p, err := tokens.VerifyAccess(token)
	require.NoError(t, err)
	assert.Equal(t, domain.Principal{UserID: 42, Role: domain.RoleOrganiser, SessionID: "5d0c8a52-9f5e-4c1b-a1a0-3a8f0f8a0001"}, p)

	// The claims the contract promises: sub, role, exp, iat, jti — and alg HS256.
	parsed, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	require.NoError(t, err)
	assert.Equal(t, "HS256", parsed.Header["alg"])
	claims := parsed.Claims.(jwt.MapClaims)
	assert.Equal(t, "42", claims["sub"])
	assert.Equal(t, "ORGANISER", claims["role"])
	assert.EqualValues(t, c.t.Unix(), claims["iat"])
	assert.EqualValues(t, c.t.Add(15*time.Minute).Unix(), claims["exp"])
	assert.NotEmpty(t, claims["jti"])

	other, err := tokens.IssueAccess(42, domain.RoleOrganiser, "")
	require.NoError(t, err)
	otherParsed, _, _ := jwt.NewParser().ParseUnverified(other, jwt.MapClaims{})
	assert.NotEqual(t, claims["jti"], otherParsed.Claims.(jwt.MapClaims)["jti"], "every token has its own jti")
}

func TestAccessTokenExpiry(t *testing.T) {
	tokens, c := newTestTokens()
	token, err := tokens.IssueAccess(1, domain.RoleFan, "")
	require.NoError(t, err)

	c.t = c.t.Add(14*time.Minute + 59*time.Second)
	_, err = tokens.VerifyAccess(token)
	require.NoError(t, err)

	c.t = c.t.Add(2 * time.Second)
	_, err = tokens.VerifyAccess(token)
	assert.ErrorIs(t, err, domain.ErrTokenExpired, "an expired but genuine token says so, so the client refreshes")
}

func sign(t *testing.T, method jwt.SigningMethod, key any, claims jwt.Claims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	require.NoError(t, err)
	return s
}

func TestAccessTokenForgeriesAreRefused(t *testing.T) {
	tokens, c := newTestTokens()
	good, err := tokens.IssueAccess(7, domain.RoleFan, "")
	require.NoError(t, err)
	parts := strings.Split(good, ".")
	require.Len(t, parts, 3)

	valid := func() accessClaims {
		return accessClaims{Role: "ADMIN", RegisteredClaims: jwt.RegisteredClaims{
			Subject: "7", IssuedAt: jwt.NewNumericDate(c.t), ExpiresAt: jwt.NewNumericDate(c.t.Add(time.Hour)), ID: "x",
		}}
	}
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	// The payload edited to ADMIN, the original signature kept.
	adminPayload := b64(`{"role":"ADMIN","sub":"7","iat":` + itoa(c.t.Unix()) + `,"exp":` + itoa(c.t.Add(time.Hour).Unix()) + `,"jti":"x"}`)

	noExp := valid()
	noExp.ExpiresAt = nil
	badRole := valid()
	badRole.Role = "SUPERUSER"
	badSub := valid()
	badSub.Subject = "seven"
	zeroSub := valid()
	zeroSub.Subject = "0"
	expiredForged := valid()
	expiredForged.ExpiresAt = jwt.NewNumericDate(c.t.Add(-time.Hour))
	future := valid()
	future.IssuedAt = jwt.NewNumericDate(c.t.Add(time.Hour))
	future.ExpiresAt = jwt.NewNumericDate(c.t.Add(2 * time.Hour))

	cases := map[string]string{
		"empty":                          "",
		"garbage":                        "not.a.jwt",
		"two parts":                      parts[0] + "." + parts[1],
		"tampered payload":               parts[0] + "." + adminPayload + "." + parts[2],
		"tampered signature":             parts[0] + "." + parts[1] + "." + flipLast(parts[2]),
		"signature stripped":             parts[0] + "." + parts[1] + ".",
		"alg none":                       b64(`{"alg":"none","typ":"JWT"}`) + "." + adminPayload + ".",
		"alg none, signed header reused": sign(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, valid()),
		"HS512 with the right secret":    sign(t, jwt.SigningMethodHS512, testSecret, valid()),
		"HS384 with the right secret":    sign(t, jwt.SigningMethodHS384, testSecret, valid()),
		"RS256":                          sign(t, jwt.SigningMethodRS256, rsaKey, valid()),
		"HS256 with another secret":      sign(t, jwt.SigningMethodHS256, []byte("some-other-secret-of-at-least-32-bytes"), valid()),
		"no exp":                         sign(t, jwt.SigningMethodHS256, testSecret, noExp),
		"unknown role":                   sign(t, jwt.SigningMethodHS256, testSecret, badRole),
		"sub not a number":               sign(t, jwt.SigningMethodHS256, testSecret, badSub),
		"sub zero":                       sign(t, jwt.SigningMethodHS256, testSecret, zeroSub),
		"issued in the future":           sign(t, jwt.SigningMethodHS256, testSecret, future),
		// Expired AND signed with the wrong key must not earn the friendlier TOKEN_EXPIRED.
		"expired and forged": sign(t, jwt.SigningMethodHS256, []byte("some-other-secret-of-at-least-32-bytes"), expiredForged),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := tokens.VerifyAccess(token)
			require.Error(t, err)
			assert.ErrorIs(t, err, domain.ErrUnauthenticated)
			assert.NotErrorIs(t, err, domain.ErrTokenExpired)
		})
	}
}

func TestMFATokenIsNotAnAccessTokenAndViceVersa(t *testing.T) {
	tokens, c := newTestTokens()
	mfa, id, err := tokens.IssueMFA(9)
	require.NoError(t, err)
	access, err := tokens.IssueAccess(9, domain.RoleAdmin, "")
	require.NoError(t, err)

	userID, gotID, expires, err := tokens.VerifyMFA(mfa)
	require.NoError(t, err)
	assert.Equal(t, int64(9), userID)
	assert.Equal(t, id, gotID)
	assert.True(t, c.t.Add(MFATokenTTL).Equal(expires), "expires %s", expires)

	_, err = tokens.VerifyAccess(mfa)
	assert.ErrorIs(t, err, domain.ErrUnauthenticated, "an MFA token must never work as a bearer token")
	_, _, _, err = tokens.VerifyMFA(access)
	assert.ErrorIs(t, err, domain.ErrMFATokenInvalid, "an access token must never pass the MFA step")

	c.t = c.t.Add(MFATokenTTL + time.Second)
	_, _, _, err = tokens.VerifyMFA(mfa)
	assert.ErrorIs(t, err, domain.ErrMFATokenInvalid, "expired")

	_, _, _, err = tokens.VerifyMFA("garbage")
	assert.ErrorIs(t, err, domain.ErrMFATokenInvalid)
}

func TestTokensFromDifferentSecretsDoNotVerify(t *testing.T) {
	a := NewTokens([]byte("secret-a-secret-a-secret-a-secret-a"), nil)
	b := NewTokens([]byte("secret-b-secret-b-secret-b-secret-b"), nil)
	token, err := a.IssueAccess(1, domain.RoleFan, "")
	require.NoError(t, err)
	_, err = b.VerifyAccess(token)
	assert.ErrorIs(t, err, domain.ErrUnauthenticated)
}

func TestRandomDigits(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		code, err := randomDigits(6)
		require.NoError(t, err)
		require.Regexp(t, `^[0-9]{6}$`, code)
		seen[code] = true
	}
	assert.Greater(t, len(seen), 190, "codes must not repeat like a stuck generator")
}

func TestHashTokenAndUUID(t *testing.T) {
	assert.Len(t, hashToken("x"), 32)
	assert.NotEqual(t, hashToken("a"), hashToken("b"))

	u, err := newUUID()
	require.NoError(t, err)
	s := uuidString(u)
	assert.Regexp(t, `^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`, s)
	back, ok := parseUUID(s)
	require.True(t, ok)
	assert.Equal(t, u, back)
	_, ok = parseUUID("not-a-uuid")
	assert.False(t, ok)
	_, ok = parseUUID("")
	assert.False(t, ok)
}

func TestMFAGuard(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	exp := now.Add(5 * time.Minute)
	g := newMFAGuard()

	// Five tries, then the token is dead.
	for i := 0; i < 5; i++ {
		require.True(t, g.attempt("tok", exp, now, 5), "try %d", i+1)
	}
	assert.False(t, g.attempt("tok", exp, now, 5))

	// Success burns the token and claims the step.
	require.True(t, g.attempt("tok2", exp, now, 5))
	require.True(t, g.finish("tok2", 1, 100))
	assert.False(t, g.attempt("tok2", exp, now, 5), "single use")

	// The same step, or an older one, cannot sign the same user in again; another user can.
	require.True(t, g.attempt("tok3", exp, now, 5))
	assert.False(t, g.finish("tok3", 1, 100), "replay")
	assert.False(t, g.finish("tok3", 1, 99), "older step")
	assert.True(t, g.attempt("tok3", exp, now, 5), "a refused replay does not burn the token")
	assert.True(t, g.finish("tok3", 1, 101))
	assert.True(t, g.finish("tok4", 2, 100), "steps are per user")

	g.forget(1)
	assert.True(t, g.claimStep(1, 50), "a new secret starts clean")

	// Expired tokens are swept.
	g.attempt("old", now.Add(time.Second), now, 5)
	g.attempt("trigger", exp.Add(time.Hour), now.Add(2*time.Minute), 5)
	g.mu.Lock()
	_, kept := g.tokens["old"]
	g.mu.Unlock()
	assert.False(t, kept)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func flipLast(s string) string {
	last := s[len(s)-1]
	repl := byte('A')
	if last == 'A' {
		repl = 'B'
	}
	return s[:len(s)-1] + string(repl)
}
