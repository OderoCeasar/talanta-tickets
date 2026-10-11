//go:build integration

package httpapi_test

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
)

func (e *env) refresh(token string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("POST", "/auth/refresh", map[string]string{"refresh_token": token}, "")
}

func TestRefresh_Rotates(t *testing.T) {
	e := newEnv(t)
	s := e.phoneSession(randPhone(t))

	rec := e.refresh(s.RefreshToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	next := decode[sessionJSON](t, rec)
	assert.NotEqual(t, s.RefreshToken, next.RefreshToken)
	assert.NotEqual(t, s.AccessToken, next.AccessToken)
	assert.Equal(t, s.User.ID, next.User.ID)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	require.Equal(t, http.StatusOK, e.do("GET", "/me", nil, next.AccessToken).Code)

	// The chain keeps going: each new token works once.
	third := e.refresh(next.RefreshToken)
	require.Equal(t, http.StatusOK, third.Code)

	// Only hashes are stored.
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refresh_tokens WHERE encode(token_hash, 'escape') LIKE 'rt_%'`))
	assert.Equal(t, 3, queryInt(t, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1`, s.User.ID))
	assert.Equal(t, 1, queryInt(t, `SELECT count(DISTINCT family_id) FROM refresh_tokens WHERE user_id = $1`, s.User.ID))
}

// Done-when: a reused refresh token revokes the family — the newer token stops working too.
func TestRefresh_ReuseRevokesTheWholeFamily(t *testing.T) {
	e := newEnv(t)
	phone := randPhone(t)
	s := e.phoneSession(phone)

	// The owner refreshes: token 1 is now used, token 2 is live.
	rec := e.refresh(s.RefreshToken)
	require.Equal(t, http.StatusOK, rec.Code)
	second := decode[sessionJSON](t, rec)

	// Someone presents token 1 again. One of the two holders is a thief.
	rec = e.refresh(s.RefreshToken)
	wantError(t, rec, http.StatusUnauthorized, domain.CodeRefreshTokenReused)

	// The newer token — the one that was perfectly good a moment ago — is dead too.
	rec = e.refresh(second.RefreshToken)
	wantError(t, rec, http.StatusUnauthorized, domain.CodeRefreshTokenInvalid)

	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1 AND revoked_at IS NULL`, s.User.ID),
		"every token of the sign-in is revoked")

	// Another sign-in of the same user is a different family and is untouched.
	e.clock.Advance(61 * time.Second)
	other := e.phoneSession(phone)
	e.clock.Advance(61 * time.Second)
	victim := e.phoneSession(phone)
	used := e.refresh(victim.RefreshToken)
	require.Equal(t, http.StatusOK, used.Code)
	wantError(t, e.refresh(victim.RefreshToken), http.StatusUnauthorized, domain.CodeRefreshTokenReused)
	require.Equal(t, http.StatusOK, e.refresh(other.RefreshToken).Code, "an unrelated session survives")
}

func TestRefresh_UnknownExpiredRevoked(t *testing.T) {
	e := newEnv(t)

	wantError(t, e.refresh("rt_this-token-was-never-issued"), http.StatusUnauthorized, domain.CodeRefreshTokenInvalid)

	expired := e.phoneSession(randPhone(t))
	exec(t, `UPDATE refresh_tokens SET expires_at = now() - interval '1 minute' WHERE user_id = $1`, expired.User.ID)
	wantError(t, e.refresh(expired.RefreshToken), http.StatusUnauthorized, domain.CodeRefreshTokenInvalid)

	revoked := e.phoneSession(randPhone(t))
	exec(t, `UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1`, revoked.User.ID)
	wantError(t, e.refresh(revoked.RefreshToken), http.StatusUnauthorized, domain.CodeRefreshTokenInvalid)

	// An access token is not a refresh token.
	s := e.phoneSession(randPhone(t))
	wantError(t, e.refresh(s.AccessToken), http.StatusUnauthorized, domain.CodeRefreshTokenInvalid)
}

// What the web client relies on: 400 and 401 mean "the session is over", so neither may be
// answered for a token that is still good. The only 400 is a malformed body, and after it
// the token still works.
func TestRefresh_Only400IsAMalformedBodyAndItLeavesTheTokenGood(t *testing.T) {
	e := newEnv(t)
	s := e.phoneSession(randPhone(t))

	wantFields(t, e.do("POST", "/auth/refresh", `{}`, ""), "refresh_token")
	wantFields(t, e.do("POST", "/auth/refresh", `{"refresh_token":""}`, ""), "refresh_token")
	wantFields(t, e.do("POST", "/auth/refresh", `{"refresh_token":123}`, ""), "refresh_token")
	wantFields(t, e.do("POST", "/auth/refresh", `{"token":"`+s.RefreshToken+`"}`, ""), "token")
	wantFields(t, e.do("POST", "/auth/refresh", `not json`, ""), "body")

	// Extra headers a browser adds, an expired access token alongside: none of it matters.
	req := e.request("POST", "/auth/refresh", map[string]string{"refresh_token": s.RefreshToken}, "an-expired-or-garbage-access-token")
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("Origin", "http://localhost:5173")
	rec := e.contract.Do(t, e.router, req)
	require.Equal(t, http.StatusOK, rec.Code, "a good refresh token must refresh: %s", rec.Body.String())
}

// Normal use must never meet the refresh rate limit: a burst far beyond what one user (or a
// whole office behind one address) does in a minute all succeeds under the default limits.
func TestRefresh_DefaultRateLimitIsNotHitByNormalUse(t *testing.T) {
	limits := httpapi.DefaultRateLimits()
	assert.GreaterOrEqual(t, limits.RefreshPerIP.Limit, 600, "per-IP refresh allowance per window")
	assert.LessOrEqual(t, limits.RefreshPerIP.Window, time.Minute)

	e := newEnv(t)
	token := e.phoneSession(randPhone(t)).RefreshToken
	for i := 0; i < 40; i++ {
		rec := e.refresh(token)
		require.Equal(t, http.StatusOK, rec.Code, "refresh %d: %s", i+1, rec.Body.String())
		token = decode[sessionJSON](t, rec).RefreshToken
	}
}

// When the limit is hit, the answer is 429 (transient for the client), never 401, and the
// token is not consumed.
func TestRefresh_RateLimitedAnswerIs429AndKeepsTheToken(t *testing.T) {
	e := newEnv(t, withLimits(func(l *httpapi.RateLimits) {
		l.RefreshPerIP = ratelimit.Rule{Limit: 1, Window: time.Minute}
	}))
	s := e.phoneSession(randPhone(t))
	rec := e.refresh(s.RefreshToken)
	require.Equal(t, http.StatusOK, rec.Code)
	next := decode[sessionJSON](t, rec).RefreshToken

	rec = e.refresh(next)
	wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))

	e.clock.Advance(61 * time.Second)
	require.Equal(t, http.StatusOK, e.refresh(next).Code, "the token survived the 429")
}

// Two requests racing with one token: at most one may win, and nobody ends up holding a live
// token in a revoked family.
func TestRefresh_ConcurrentUseOfOneToken(t *testing.T) {
	e := newEnv(t)
	s := e.phoneSession(randPhone(t))

	const n = 12
	recs := make([]*httptest.ResponseRecorder, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			recs[i] = e.refresh(s.RefreshToken)
		}(i)
	}
	wg.Wait()

	var winners []sessionJSON
	for _, rec := range recs {
		switch rec.Code {
		case http.StatusOK:
			winners = append(winners, decode[sessionJSON](t, rec))
		case http.StatusUnauthorized:
		default:
			t.Fatalf("unexpected status %d: %s", rec.Code, rec.Body.String())
		}
	}
	require.Len(t, winners, 1, "exactly one request rotates the token")

	// The losers presented a used token, so the family is revoked — including the winner's
	// new token. Nothing live remains.
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1 AND revoked_at IS NULL`, s.User.ID))
	wantError(t, e.refresh(winners[0].RefreshToken), http.StatusUnauthorized, domain.CodeRefreshTokenInvalid)
}

func TestLogout_RevokesTheFamily(t *testing.T) {
	e := newEnv(t)
	s := e.phoneSession(randPhone(t))
	rec := e.refresh(s.RefreshToken)
	require.Equal(t, http.StatusOK, rec.Code)
	current := decode[sessionJSON](t, rec)

	rec = e.do("POST", "/auth/logout", map[string]string{"refresh_token": current.RefreshToken}, "")
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Empty(t, rec.Body.String())

	wantError(t, e.refresh(current.RefreshToken), http.StatusUnauthorized, domain.CodeRefreshTokenInvalid)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refresh_tokens WHERE user_id = $1 AND revoked_at IS NULL`, s.User.ID))

	// Signing out twice, or with a token we never issued, is still 204.
	require.Equal(t, http.StatusNoContent, e.do("POST", "/auth/logout", map[string]string{"refresh_token": current.RefreshToken}, "").Code)
	require.Equal(t, http.StatusNoContent, e.do("POST", "/auth/logout", map[string]string{"refresh_token": "rt_unknown"}, "").Code)

	// An older token of the chain also signs the session out (no access token needed).
	s2 := e.phoneSession(randPhone(t))
	rec = e.refresh(s2.RefreshToken)
	require.Equal(t, http.StatusOK, rec.Code)
	newer := decode[sessionJSON](t, rec)
	require.Equal(t, http.StatusNoContent, e.do("POST", "/auth/logout", map[string]string{"refresh_token": s2.RefreshToken}, "").Code)
	wantError(t, e.refresh(newer.RefreshToken), http.StatusUnauthorized, domain.CodeRefreshTokenInvalid)

	wantFields(t, e.do("POST", "/auth/logout", `{}`, ""), "refresh_token")
}
