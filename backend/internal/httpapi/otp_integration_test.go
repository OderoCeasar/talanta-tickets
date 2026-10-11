//go:build integration

package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/config"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi/contracttest"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

func TestHealth_Contract(t *testing.T) {
	e := newEnv(t)
	rec := e.do("GET", "/healthz", nil, "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())

	// Database down: 503 with the health shape, not the error shape.
	down := httpapi.NewRouter(httpapi.Deps{DB: brokenDB{}})
	rec = e.contract.Do(t, down, e.request("GET", "/healthz", nil, ""))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.JSONEq(t, `{"status":"degraded"}`, rec.Body.String())
}

type brokenDB struct{}

func (brokenDB) Ping(context.Context) error { return context.DeadlineExceeded }

// Done-when: a good OTP login.
func TestOTPLogin_GoodCode(t *testing.T) {
	e := newEnv(t)
	phone := randPhone(t)

	rec := e.do("POST", "/auth/otp/request", map[string]string{"phone": phone}, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var requested struct {
		ExpiresInSeconds   int    `json:"expires_in_seconds"`
		ResendAfterSeconds int    `json:"resend_after_seconds"`
		DevCode            string `json:"dev_code"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &requested))
	assert.Equal(t, 300, requested.ExpiresInSeconds)
	assert.Equal(t, 60, requested.ResendAfterSeconds)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	// The code went out by SMS, and it is the one in dev_code.
	assert.Contains(t, e.sms.last(phone), requested.DevCode)
	// Only a hash is stored.
	var stored []byte
	require.NoError(t, db.Pool.QueryRow(context.Background(), `SELECT code_hash FROM otp_codes WHERE phone_e164 = $1`, phone).Scan(&stored))
	assert.Len(t, stored, 32)
	assert.NotContains(t, string(stored), requested.DevCode)

	rec = e.verifyCode(phone, requested.DevCode)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	s := decode[sessionJSON](t, rec)
	assert.Equal(t, "Bearer", s.TokenType)
	assert.Equal(t, 900, s.ExpiresInSeconds)
	assert.Equal(t, "FAN", s.User.Role, "first sign-in creates a fan")
	require.NotNil(t, s.User.Phone)
	assert.Equal(t, phone, *s.User.Phone)
	assert.Nil(t, s.User.Email)
	assert.False(t, s.User.TOTPEnabled)
	refreshExpiry, err := time.Parse(time.RFC3339, s.RefreshExpiresAt)
	require.NoError(t, err)
	assert.WithinDuration(t, time.Now().Add(30*24*time.Hour), refreshExpiry, time.Minute)

	// The access token works.
	rec = e.do("GET", "/me", nil, s.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, s.User.ID, decode[userJSON](t, rec).ID)

	// A second sign-in is the same account, not a new one.
	e.clock.Advance(61 * time.Second)
	again := e.phoneSession(phone)
	assert.Equal(t, s.User.ID, again.User.ID)
	assert.Equal(t, 1, queryInt(t, `SELECT count(*) FROM users WHERE phone_e164 = $1`, phone))
}

// The three ways Kenyans write a number are one account. The contract tells clients to send
// E.164; the server accepts local forms as a kindness, hence LenientRequest.
func TestOTPLogin_LocalPhoneFormatsAreOneAccount(t *testing.T) {
	e := newEnv(t)
	national := "7" + randDigits(t, 8)
	e164 := "+254" + national

	var ids []int64
	for _, form := range []string{"0" + national, "254" + national, e164, "0" + national[:3] + " " + national[3:6] + " " + national[6:]} {
		// The injected clock steps past the resend gap; nothing here waits on real time.
		e.clock.Advance(61 * time.Second)
		rec := e.do("POST", "/auth/otp/request", map[string]string{"phone": form}, "", contracttest.LenientRequest())
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", form, rec.Body.String())
		var body struct {
			DevCode string `json:"dev_code"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

		rec = e.do("POST", "/auth/otp/verify", map[string]string{"phone": form, "code": body.DevCode}, "", contracttest.LenientRequest())
		require.Equal(t, http.StatusOK, rec.Code, "%s: %s", form, rec.Body.String())
		s := decode[sessionJSON](t, rec)
		assert.Equal(t, e164, *s.User.Phone, "stored and returned in E.164")
		ids = append(ids, s.User.ID)
	}
	for _, id := range ids {
		assert.Equal(t, ids[0], id)
	}
}

// Done-when: a wrong code.
func TestOTPLogin_WrongCode(t *testing.T) {
	e := newEnv(t)
	phone := randPhone(t)
	code := e.requestCode(phone)

	rec := e.verifyCode(phone, wrongCode(code))
	wantError(t, rec, http.StatusUnauthorized, domain.CodeOTPInvalid)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM users WHERE phone_e164 = $1`, phone), "no account from a wrong code")

	// The right code still works afterwards.
	require.Equal(t, http.StatusOK, e.verifyCode(phone, code).Code)
}

func TestOTPLogin_NoCodeRequested(t *testing.T) {
	e := newEnv(t)
	wantError(t, e.verifyCode(randPhone(t), "123456"), http.StatusUnauthorized, domain.CodeOTPInvalid)
}

// Done-when: an expired code.
func TestOTPLogin_ExpiredCode(t *testing.T) {
	e := newEnv(t)
	phone := randPhone(t)
	code := e.requestCode(phone)

	e.clock.Advance(5*time.Minute + time.Second)

	rec := e.verifyCode(phone, code)
	wantError(t, rec, http.StatusUnauthorized, domain.CodeOTPExpired)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM users WHERE phone_e164 = $1`, phone))
}

// The same, with the expiry moved in the database rather than on the service's clock, so
// the SQL guard (expires_at > now()) is what refuses the code.
func TestOTPLogin_ExpiredCodeByDatabaseTime(t *testing.T) {
	e := newEnv(t)
	phone := randPhone(t)
	code := e.requestCode(phone)
	exec(t, `UPDATE otp_codes SET expires_at = now() - interval '1 second' WHERE phone_e164 = $1`, phone)

	wantError(t, e.verifyCode(phone, code), http.StatusUnauthorized, domain.CodeOTPExpired)
}

func TestOTPLogin_CodeWorksOnce(t *testing.T) {
	e := newEnv(t)
	phone := randPhone(t)
	code := e.requestCode(phone)

	require.Equal(t, http.StatusOK, e.verifyCode(phone, code).Code)
	wantError(t, e.verifyCode(phone, code), http.StatusUnauthorized, domain.CodeOTPInvalid)
}

func TestOTPLogin_NewCodeRetiresTheOldOne(t *testing.T) {
	e := newEnv(t)
	phone := randPhone(t)
	first := e.requestCode(phone)
	e.clock.Advance(61 * time.Second)
	second := e.requestCode(phone)
	if first == second {
		t.Skip("the two random codes happened to be equal (one in a million)")
	}

	wantError(t, e.verifyCode(phone, first), http.StatusUnauthorized, domain.CodeOTPInvalid)
	require.Equal(t, http.StatusOK, e.verifyCode(phone, second).Code)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM otp_codes WHERE phone_e164 = $1 AND consumed_at IS NULL`, phone))
}

// Five wrong codes kill the code: the sixth try is refused even with the right code.
func TestOTPLogin_AttemptLimit(t *testing.T) {
	e := newEnv(t)
	phone := randPhone(t)
	code := e.requestCode(phone)

	for i := 1; i <= 5; i++ {
		wantError(t, e.verifyCode(phone, wrongCode(code)), http.StatusUnauthorized, domain.CodeOTPInvalid)
	}

	rec := e.verifyCode(phone, code)
	body := wantError(t, rec, http.StatusTooManyRequests, domain.CodeOTPAttemptsExceeded)
	retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, retry, 1)
	assert.LessOrEqual(t, retry, 60)
	assert.JSONEq(t, `{"retry_after_seconds":`+strconv.Itoa(retry)+`}`, string(body.Error.Details))
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM users WHERE phone_e164 = $1`, phone))

	// A new code gives a fresh start.
	e.clock.Advance(61 * time.Second)
	require.Equal(t, http.StatusOK, e.verifyCode(phone, e.requestCode(phone)).Code)
}

// Guesses made at the same moment share the five attempts; they do not get five each.
func TestOTPLogin_ParallelGuessesShareTheAttemptLimit(t *testing.T) {
	e := newEnv(t)
	phone := randPhone(t)
	code := e.requestCode(phone)

	const guesses = 30
	statuses := make([]int, guesses)
	var wg sync.WaitGroup
	for i := 0; i < guesses; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = e.verifyCode(phone, wrongCode(code)).Code
		}(i)
	}
	wg.Wait()

	counted := 0
	for _, s := range statuses {
		if s == http.StatusUnauthorized {
			counted++
		} else {
			assert.Equal(t, http.StatusTooManyRequests, s)
		}
	}
	assert.Equal(t, 5, counted, "exactly five guesses were compared with the code")
	assert.Equal(t, 5, queryInt(t, `SELECT attempts FROM otp_codes WHERE phone_e164 = $1`, phone))
}

func TestOTPRequest_RateLimitPerPhone(t *testing.T) {
	e := newEnv(t)
	phone := randPhone(t)
	e.requestCode(phone)

	// Straight away: the resend gap.
	rec := e.do("POST", "/auth/otp/request", map[string]string{"phone": phone}, "")
	body := wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
	retry, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	require.NoError(t, err)
	assert.InDelta(t, 60, retry, 2)
	assert.JSONEq(t, `{"retry_after_seconds":`+strconv.Itoa(retry)+`}`, string(body.Error.Details))
	assert.Equal(t, 1, e.sms.count(phone), "a refused request sends no SMS")

	// Another phone is not affected.
	e.requestCode(randPhone(t))

	// The hourly cap: five codes, then nothing until the hour is up.
	for i := 2; i <= 5; i++ {
		e.clock.Advance(61 * time.Second)
		e.requestCode(phone)
	}
	e.clock.Advance(61 * time.Second)
	rec = e.do("POST", "/auth/otp/request", map[string]string{"phone": phone}, "")
	wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
	retry, err = strconv.Atoi(rec.Header().Get("Retry-After"))
	require.NoError(t, err)
	assert.Greater(t, retry, 50*60, "the wait is the rest of the hour, not the resend gap")
	assert.Equal(t, 5, e.sms.count(phone))

	e.clock.Advance(time.Hour)
	e.requestCode(phone)
}

func TestOTPRequest_RateLimitPerIP(t *testing.T) {
	e := newEnv(t, withLimits(func(l *httpapi.RateLimits) {
		l.OTPRequestPerIP = ratelimit.Rule{Limit: 3, Window: 10 * time.Minute}
	}))
	for i := 0; i < 3; i++ {
		e.requestCode(randPhone(t))
	}
	// A fourth phone from the same address is refused…
	rec := e.do("POST", "/auth/otp/request", map[string]string{"phone": randPhone(t)}, "")
	wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))

	// …and a forged forwarding header does not buy a new allowance.
	req := e.request("POST", "/auth/otp/request", map[string]string{"phone": randPhone(t)}, "")
	req.Header.Set("X-Forwarded-For", "203.0.113.50")
	req.Header.Set("CF-Connecting-IP", "203.0.113.50")
	req.Header.Set("X-Real-IP", "203.0.113.50")
	rec = e.contract.Do(t, e.router, req)
	wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)

	// Another address has its own allowance.
	e.remoteAddr = "198.51.100.99:40000"
	e.requestCode(randPhone(t))
}

// The dev shortcut must not exist in production. Built without dev shortcuts, the answer
// has no dev_code key at all, and the code never appears anywhere in the body.
func TestOTPRequest_DevCodeAbsentInProduction(t *testing.T) {
	e := newEnv(t, production())
	phone := randPhone(t)

	rec := e.do("POST", "/auth/otp/request", map[string]string{"phone": phone}, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
	_, present := raw["dev_code"]
	assert.False(t, present, "dev_code must not be in a production answer: %s", rec.Body.String())
	assert.Len(t, raw, 2, "only expires_in_seconds and resend_after_seconds")

	// The code was still made and sent by SMS, and still works — only the leak is gone.
	sms := e.sms.last(phone)
	require.NotEmpty(t, sms)
	code := regexpFind(t, `[0-9]{6}`, sms)
	assert.NotContains(t, rec.Body.String(), code)
	require.Equal(t, http.StatusOK, e.verifyCode(phone, code).Code)
}

// The same guarantee from the top: with APP_ENV unset the loaded config is production, and
// an API built from that config returns no dev_code.
func TestOTPRequest_UnsetAppEnvIsProductionAndReturnsNoDevCode(t *testing.T) {
	t.Setenv("APP_ENV", "")
	t.Setenv("DATABASE_URL", db.URL)
	t.Setenv("JWT_SECRET", testSecret)
	// What production must set, or the config does not load at all.
	t.Setenv("ADMIN_BASE_URL", "https://talanta.example.com")
	t.Setenv("TRUSTED_PROXY_CIDRS", "none")
	for _, k := range []string{"BOOTSTRAP_ADMIN_EMAIL", "BOOTSTRAP_ADMIN_PASSWORD", "SMTP_HOST", "HTTP_WRITE_TIMEOUT", "REQUEST_TIMEOUT",
		"CLIENT_IP_HEADER", "ARGON2_MAX_CONCURRENT", "OTP_ALLOWED_COUNTRY_CODES", "OTP_GLOBAL_HOURLY_LIMIT", "OTP_GLOBAL_DAILY_LIMIT", "SMTP_ALLOW_PLAINTEXT"} {
		t.Setenv(k, "")
	}
	cfg, err := config.LoadAPI()
	require.NoError(t, err)
	require.True(t, cfg.IsProduction())
	require.False(t, cfg.AllowDevShortcuts())

	sms := &smsBox{}
	svc, err := auth.NewService(auth.Config{
		DB: db.Pool, Secret: cfg.JWTSecret, DevShortcuts: cfg.AllowDevShortcuts(),
		AdminBaseURL: cfg.AdminBaseURL, SMS: sms,
	})
	require.NoError(t, err)
	router := httpapi.NewRouter(httpapi.Deps{DB: db.Pool, Auth: svc})

	e := newEnv(t) // only for building and checking the request
	phone := randPhone(t)
	rec := e.contract.Do(t, router, e.request("POST", "/auth/otp/request", map[string]string{"phone": phone}, ""))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "dev_code")
	assert.NotContains(t, rec.Body.String(), regexpFind(t, `[0-9]{6}`, sms.last(phone)))
}

// Nothing in the answer says whether a phone already has an account.
func TestOTPRequest_SameAnswerForKnownAndUnknownPhone(t *testing.T) {
	e := newEnv(t, production())
	known := randPhone(t)
	_, err := e.q.UpsertFanByPhone(context.Background(), &known)
	require.NoError(t, err)

	a := e.do("POST", "/auth/otp/request", map[string]string{"phone": known}, "")
	b := e.do("POST", "/auth/otp/request", map[string]string{"phone": randPhone(t)}, "")
	require.Equal(t, http.StatusOK, a.Code)
	assert.Equal(t, a.Code, b.Code)
	assert.JSONEq(t, a.Body.String(), b.Body.String())

	// And a wrong code is the same refusal for both.
	wa := e.verifyCode(known, "000000")
	wb := e.verifyCode(randPhone(t), "000000")
	assert.Equal(t, wa.Code, wb.Code)
	assert.JSONEq(t, wa.Body.String(), wb.Body.String())
}

// An existing account keeps its role: the seeded gate steward signs in as gate staff.
func TestOTPLogin_GateStaffKeepsRole(t *testing.T) {
	e := newEnv(t)
	phone := randPhone(t)
	name := "Gate Steward"
	steward, err := e.q.CreateUser(context.Background(), store.CreateUserParams{PhoneE164: &phone, Name: &name, Role: "GATE_STAFF"})
	require.NoError(t, err)

	s := e.phoneSession(phone)
	assert.Equal(t, steward.ID, s.User.ID)
	assert.Equal(t, "GATE_STAFF", s.User.Role)
	assert.Equal(t, "Gate Steward", *s.User.Name)

	rec := e.do("GET", "/me", nil, s.AccessToken)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "GATE_STAFF", decode[userJSON](t, rec).Role)
}

// A phone code must never be a way round a staff password.
func TestOTPLogin_StaffAccountWithAPhoneIsRefused(t *testing.T) {
	e := newEnv(t)
	phone := randPhone(t)
	email := randEmail(t, "admin")
	hash := staffPasswordHash
	_, err := e.q.CreateUser(context.Background(), store.CreateUserParams{
		PhoneE164: &phone, Email: &email, Role: "ADMIN", PasswordHash: &hash,
	})
	require.NoError(t, err)

	code := e.requestCode(phone)
	wantError(t, e.verifyCode(phone, code), http.StatusUnauthorized, domain.CodeOTPInvalid)
	assert.Equal(t, 0, queryInt(t, `SELECT count(*) FROM refresh_tokens r JOIN users u ON u.id = r.user_id WHERE u.phone_e164 = $1`, phone))
}

func TestOTP_Validation(t *testing.T) {
	e := newEnv(t)

	for _, bad := range []string{"", "12345", "+2540712345678", "0812345678", "not a phone", "+254712345678901234567890"} {
		rec := e.do("POST", "/auth/otp/request", map[string]string{"phone": bad}, "")
		wantFields(t, rec, "phone")
	}
	wantFields(t, e.do("POST", "/auth/otp/request", `{}`, ""), "phone")
	wantFields(t, e.do("POST", "/auth/otp/request", `{"phone":"+254712345678","admin":true}`, ""), "admin")
	wantFields(t, e.do("POST", "/auth/otp/request", `{"phone":`, ""), "body")
	wantFields(t, e.do("POST", "/auth/otp/request", `{"phone":254712345678}`, ""), "phone")
	wantFields(t, e.do("POST", "/auth/otp/request", nil, ""), "body")

	phone := randPhone(t)
	for _, code := range []string{"", "12345", "1234567", "12345a", "12 456", "-12345", "+12345", "1.2345"} {
		rules := wantFields(t, e.verifyCode(phone, code), "code")
		assert.NotEmpty(t, rules["code"])
	}
	wantFields(t, e.do("POST", "/auth/otp/verify", `{}`, ""), "phone", "code")
	wantFields(t, e.verifyCode("nope", "123456"), "phone")
}
