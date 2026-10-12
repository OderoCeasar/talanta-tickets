//go:build integration

package httpapi_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
)

// OTP_TEST_CODES: a test number signs in with its configured code, on a production server,
// with no SMS sent and the code never in an answer.
func TestOTPTestCodes_FixedCodeNoSMSNoLeak(t *testing.T) {
	testPhone, realPhone := randPhone(t), randPhone(t)
	const code = "482913"
	e := newEnv(t, production(), withOTPTestCodes(map[string]string{testPhone: code}))

	rec := e.do("POST", "/auth/otp/request", map[string]string{"phone": testPhone}, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	assert.Len(t, raw, 2, "the same two fields any phone gets: %s", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), code, "the code must never be in an answer")
	assert.Zero(t, e.sms.count(testPhone), "no SMS is sent to a test number")

	// A wrong code is refused like any other, and the right one then signs in.
	wantError(t, e.verifyCode(testPhone, "000111"), http.StatusUnauthorized, domain.CodeOTPInvalid)
	ok := e.verifyCode(testPhone, code)
	require.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
	assert.NotContains(t, ok.Body.String(), code)

	// The code works once per request, like a real one.
	assert.NotEqual(t, http.StatusOK, e.verifyCode(testPhone, code).Code, "a used code must not work again")

	// A number that is not on the list is untouched: random code, sent by SMS, and the test
	// code does not open it.
	rec = e.do("POST", "/auth/otp/request", map[string]string{"phone": realPhone}, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, e.sms.count(realPhone))
	sent := regexpFind(t, `[0-9]{6}`, e.sms.last(realPhone))
	if sent != code {
		wantError(t, e.verifyCode(realPhone, code), http.StatusUnauthorized, domain.CodeOTPInvalid)
	}
	require.Equal(t, http.StatusOK, e.verifyCode(realPhone, sent).Code)
}

// A test number is no easier to guess at than a real one: the attempt limit still applies.
func TestOTPTestCodes_AttemptLimitStillApplies(t *testing.T) {
	testPhone := randPhone(t)
	const code = "482913"
	e := newEnv(t, production(), withOTPTestCodes(map[string]string{testPhone: code}))

	require.Equal(t, http.StatusOK, e.do("POST", "/auth/otp/request", map[string]string{"phone": testPhone}, "").Code)
	for i := 0; i < 5; i++ {
		rec := e.verifyCode(testPhone, "111222")
		require.NotEqual(t, http.StatusOK, rec.Code, "guess %d", i+1)
	}
	// The limit is spent: even the right code is refused until a new code is requested.
	rec := e.verifyCode(testPhone, code)
	require.NotEqual(t, http.StatusOK, rec.Code, "the right code after five wrong ones: %s", rec.Body.String())
}

// Test numbers do not spend the SMS budget: with the global cap already used up, a test
// number still gets its code while a real number is refused.
func TestOTPTestCodes_DoNotCountAgainstTheSMSCaps(t *testing.T) {
	testPhone := randPhone(t)
	e := newEnv(t, production(),
		withOTPTestCodes(map[string]string{testPhone: "482913"}),
		withPolicy(func(p *auth.Policy) { p.OTPGlobalHourly = ratelimit.Rule{Limit: 1, Window: time.Hour} }))

	require.Equal(t, http.StatusOK, e.do("POST", "/auth/otp/request", map[string]string{"phone": randPhone(t)}, "").Code)
	wantError(t, e.do("POST", "/auth/otp/request", map[string]string{"phone": randPhone(t)}, ""), http.StatusTooManyRequests, domain.CodeRateLimited)
	require.Equal(t, http.StatusOK, e.do("POST", "/auth/otp/request", map[string]string{"phone": testPhone}, "").Code)
	require.Equal(t, http.StatusOK, e.verifyCode(testPhone, "482913").Code)
}
