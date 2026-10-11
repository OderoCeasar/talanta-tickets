//go:build integration

package httpapi_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/booking"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/events"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi/contracttest"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/notify"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/testdb"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/venue"
)

// One Postgres for the whole package. Tests never share rows: each makes its own users
// with random phones and emails, so they do not depend on order.
var (
	db *testdb.DB
	// staffPasswordHash is hashed once; argon2 under -race is slow enough to matter.
	staffPasswordHash string
)

const (
	staffPassword = "correct-horse-battery"
	testSecret    = "integration-test-secret-at-least-32-bytes"
)

func TestMain(m *testing.M) {
	flag.Parse()
	os.Exit(runMain(m))
}

func runMain(m *testing.M) int {
	var err error
	db, err = testdb.Start(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "testdb:", err)
		return 1
	}
	defer db.Close()
	if err := db.MigrateUp(); err != nil {
		fmt.Fprintln(os.Stderr, "testdb:", err)
		return 1
	}
	if staffPasswordHash, err = auth.HashPassword(staffPassword); err != nil {
		fmt.Fprintln(os.Stderr, "hash:", err)
		return 1
	}

	code := m.Run()

	// After a full, green run: every M3 operation must have had a success answer and at
	// least one error answer checked against the contract. Skipped when -run narrows the
	// set, since then most operations were never called.
	if code == 0 && flag.Lookup("test.run").Value.String() == "" {
		if missing := uncoveredOperations(); len(missing) > 0 {
			fmt.Fprintf(os.Stderr, "contract coverage: no checked answer for:\n  %s\n", strings.Join(missing, "\n  "))
			return 1
		}
	}
	return code
}

// m3Operations is every operation this milestone implements, by operationId.
var m3Operations = []string{
	"getHealth",
	"requestOtp", "verifyOtp", "passwordLogin", "verifyTotp", "refreshTokens", "logout",
	"getInvitation", "acceptInvitation",
	"getMe", "updateMe", "changePassword", "setupTotp", "enableTotp", "disableTotp",
	"listStaffUsers", "listInvitations", "createInvitation", "resendInvitation", "revokeInvitation",
}

func uncoveredOperations() []string {
	contract, err := loadContractForCoverage()
	if err != nil {
		return []string{err.Error()}
	}
	seen := contract.Seen()
	var missing []string
	for _, op := range slices.Concat(m3Operations, m4m5Operations, m6Operations) {
		var success, failure bool
		for _, s := range seen {
			name, status, _ := strings.Cut(s, " ")
			if name != op {
				continue
			}
			if strings.HasPrefix(status, "2") {
				success = true
			} else {
				failure = true
			}
		}
		if !success {
			missing = append(missing, op+": no success answer")
		}
		if !failure {
			missing = append(missing, op+": no error answer")
		}
	}
	return missing
}

// loadContractForCoverage gets the shared contract without a *testing.T.
func loadContractForCoverage() (c *contracttest.Contract, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("load contract: %v", r)
		}
	}()
	return contracttest.Load(fatalTB{}), nil
}

type fatalTB struct{ testing.TB }

func (fatalTB) Helper()                           {}
func (fatalTB) Fatalf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }

// clock is real time plus an offset the test can push forward, so database time (now())
// and service time agree until a test deliberately moves on.
type clock struct {
	mu     sync.Mutex
	offset time.Duration
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.offset)
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset += d
}

// smsBox is the SMS provider of the tests: it keeps what would have been sent.
type smsBox struct {
	mu   sync.Mutex
	sent map[string][]string
}

func (b *smsBox) SendSMS(_ context.Context, to, body string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sent == nil {
		b.sent = map[string][]string{}
	}
	b.sent[to] = append(b.sent[to], body)
	return nil
}

func (b *smsBox) last(to string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	msgs := b.sent[to]
	if len(msgs) == 0 {
		return ""
	}
	return msgs[len(msgs)-1]
}

func (b *smsBox) count(to string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.sent[to])
}

type mailBox struct {
	mu   sync.Mutex
	err  error
	sent []notify.Mail
}

func (b *mailBox) SendMail(_ context.Context, m notify.Mail) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.sent = append(b.sent, m)
	return nil
}

func (b *mailBox) all() []notify.Mail {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]notify.Mail(nil), b.sent...)
}

// syncBuffer lets the logger be written from request goroutines and read by the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type envConfig struct {
	devShortcuts bool
	policy       auth.Policy
	limits       httpapi.RateLimits
	mailErr      error
	// eventPolicy is the events service's policy; zero values mean the defaults.
	eventPolicy events.Policy
	// holdPolicy is the hold service's policy; zero values mean the defaults.
	holdPolicy booking.Policy
	// holdDB is the pool the hold service runs on; nil means the shared one.
	holdDB       booking.DB
	otpCountries []string
}

type envOption func(*envConfig)

// production builds the API the way APP_ENV=production does: no dev shortcuts.
func production() envOption { return func(c *envConfig) { c.devShortcuts = false } }

func withPolicy(fn func(*auth.Policy)) envOption { return func(c *envConfig) { fn(&c.policy) } }

func withLimits(fn func(*httpapi.RateLimits)) envOption {
	return func(c *envConfig) { fn(&c.limits) }
}

func withMailError(err error) envOption { return func(c *envConfig) { c.mailErr = err } }

// withOTPCountries sets OTP_ALLOWED_COUNTRY_CODES; without it the default (254) applies.
func withOTPCountries(codes ...string) envOption {
	return func(c *envConfig) { c.otpCountries = codes }
}

// env is one API instance (its own limiter, clock, SMS and mail boxes) on the shared database.
type env struct {
	t        *testing.T
	router   http.Handler
	events   *events.Service
	venues   *venue.Service
	holds    *booking.Service
	auth     *auth.Service
	q        *store.Queries
	clock    *clock
	sms      *smsBox
	mail     *mailBox
	logs     *syncBuffer
	contract *contracttest.Contract
	// remoteAddr is the TCP peer of the next requests.
	remoteAddr string
}

func newEnv(t *testing.T, opts ...envOption) *env {
	t.Helper()
	cfg := envConfig{devShortcuts: true, policy: auth.DefaultPolicy(), limits: httpapi.DefaultRateLimits()}
	for _, opt := range opts {
		opt(&cfg)
	}

	if cfg.holdDB == nil {
		cfg.holdDB = db.Pool
	}

	e := &env{
		t:          t,
		q:          store.New(db.Pool),
		clock:      &clock{},
		sms:        &smsBox{},
		mail:       &mailBox{err: cfg.mailErr},
		logs:       &syncBuffer{},
		contract:   contracttest.Load(t),
		remoteAddr: "198.51.100.7:40000",
	}
	logger := slog.New(slog.NewJSONHandler(e.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	limiter := ratelimit.NewMemory(e.clock.Now)

	svc, err := auth.NewService(auth.Config{
		DB:           db.Pool,
		Logger:       logger,
		Secret:       []byte(testSecret),
		DevShortcuts: cfg.devShortcuts,
		AdminBaseURL: "https://admin.talanta.test",
		SMS:          e.sms,
		Mailer:       e.mail,
		Limiter:      limiter,
		Policy:       cfg.policy,
		Now:          e.clock.Now,

		OTPCountryCodes: cfg.otpCountries,
	})
	require.NoError(t, err)
	e.auth = svc
	e.events, err = events.NewService(events.Config{DB: db.Pool, Logger: logger, Policy: cfg.eventPolicy, Now: e.clock.Now})
	require.NoError(t, err)
	e.venues = venue.NewService(db.Pool)
	e.holds, err = booking.NewService(booking.Config{DB: cfg.holdDB, Logger: logger, Policy: cfg.holdPolicy})
	require.NoError(t, err)
	e.router = httpapi.NewRouter(httpapi.Deps{
		Events:      e.events,
		Venues:      e.venues,
		Holds:       e.holds,
		Logger:      logger,
		DB:          db.Pool,
		Auth:        svc,
		Limiter:     limiter,
		RateLimits:  &cfg.limits,
		CORSOrigins: []string{"http://localhost:5173"},
	})
	return e
}

// request builds a request the way a client would. body may be nil, a raw string, or
// anything JSON can encode.
func (e *env) request(method, path string, body any, token string) *http.Request {
	e.t.Helper()
	var reader io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		require.NoError(e.t, err)
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, "/v1"+path, reader)
	req.RemoteAddr = e.remoteAddr
	if reader != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

// do sends a request and checks the answer against the OpenAPI contract. Every HTTP call in
// these tests goes through here, so every answer they see is contract-checked.
func (e *env) do(method, path string, body any, token string, opts ...contracttest.Option) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.contract.Do(e.t, e.router, e.request(method, path, body, token), opts...)
}

type userJSON struct {
	ID          int64   `json:"id"`
	Phone       *string `json:"phone"`
	Email       *string `json:"email"`
	Name        *string `json:"name"`
	Role        string  `json:"role"`
	TOTPEnabled bool    `json:"totp_enabled"`
	CreatedAt   string  `json:"created_at"`
}

type sessionJSON struct {
	MFARequired      *bool    `json:"mfa_required"`
	MFAToken         string   `json:"mfa_token"`
	AccessToken      string   `json:"access_token"`
	TokenType        string   `json:"token_type"`
	ExpiresInSeconds int      `json:"expires_in_seconds"`
	RefreshToken     string   `json:"refresh_token"`
	RefreshExpiresAt string   `json:"refresh_expires_at"`
	User             userJSON `json:"user"`
}

type errorJSON struct {
	Error struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	} `json:"error"`
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &v), "body: %s", rec.Body.String())
	return v
}

// wantError asserts the status and the error code, and returns the body for more checks.
func wantError(t *testing.T, rec *httptest.ResponseRecorder, status int, code domain.Code) errorJSON {
	t.Helper()
	require.Equal(t, status, rec.Code, "body: %s", rec.Body.String())
	e := decode[errorJSON](t, rec)
	require.Equal(t, string(code), e.Error.Code, "body: %s", rec.Body.String())
	require.NotEmpty(t, e.Error.Message)
	return e
}

// wantFields asserts a 400 VALIDATION_FAILED naming exactly these fields, and returns
// field → rule.
func wantFields(t *testing.T, rec *httptest.ResponseRecorder, fields ...string) map[string]string {
	t.Helper()
	e := wantError(t, rec, http.StatusBadRequest, domain.CodeValidationFailed)
	var details struct {
		Fields []struct {
			Field, Rule, Message string
		} `json:"fields"`
	}
	require.NoError(t, json.Unmarshal(e.Error.Details, &details))
	got := map[string]string{}
	var names []string
	for _, f := range details.Fields {
		got[f.Field] = f.Rule
		names = append(names, f.Field)
		require.NotEmpty(t, f.Message)
	}
	require.ElementsMatch(t, fields, names, "body: %s", rec.Body.String())
	return got
}

func randDigits(t testing.TB, n int) string {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
	v, err := rand.Int(rand.Reader, max)
	require.NoError(t, err)
	return fmt.Sprintf("%0*d", n, v)
}

func randPhone(t testing.TB) string { return "+2547" + randDigits(t, 8) }

func randEmail(t testing.TB, prefix string) string {
	return fmt.Sprintf("%s.%s@talanta.test", prefix, randDigits(t, 12))
}

// newStaff inserts an organiser or admin whose password is staffPassword.
func (e *env) newStaff(role domain.Role) store.User {
	e.t.Helper()
	name := "Test " + string(role)
	u, err := e.q.CreateStaffUser(context.Background(), store.CreateStaffUserParams{
		Email: randEmail(e.t, strings.ToLower(string(role))), Name: &name, Role: string(role), PasswordHash: staffPasswordHash,
	})
	require.NoError(e.t, err)
	return u
}

// login signs a staff user in by password and requires a full session (no TOTP).
func (e *env) login(email, password string) sessionJSON {
	e.t.Helper()
	rec := e.do("POST", "/auth/login", map[string]string{"email": email, "password": password}, "")
	require.Equal(e.t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	s := decode[sessionJSON](e.t, rec)
	require.NotNil(e.t, s.MFARequired)
	require.False(e.t, *s.MFARequired)
	require.NotEmpty(e.t, s.AccessToken)
	return s
}

// staffSession makes a staff user and signs them in.
func (e *env) staffSession(role domain.Role) (store.User, sessionJSON) {
	e.t.Helper()
	u := e.newStaff(role)
	return u, e.login(*u.Email, staffPassword)
}

// requestCode asks for a phone code and returns it from the dev_code field.
func (e *env) requestCode(phone string) string {
	e.t.Helper()
	rec := e.do("POST", "/auth/otp/request", map[string]string{"phone": phone}, "")
	require.Equal(e.t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var body struct {
		DevCode string `json:"dev_code"`
	}
	require.NoError(e.t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Regexp(e.t, `^[0-9]{6}$`, body.DevCode)
	return body.DevCode
}

func (e *env) verifyCode(phone, code string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do("POST", "/auth/otp/verify", map[string]string{"phone": phone, "code": code}, "")
}

// phoneSession signs a phone in with a fresh code.
func (e *env) phoneSession(phone string) sessionJSON {
	e.t.Helper()
	rec := e.verifyCode(phone, e.requestCode(phone))
	require.Equal(e.t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return decode[sessionJSON](e.t, rec)
}

// wrongCode is a six-digit code that is certainly not the given one.
func wrongCode(code string) string {
	if code == "000000" {
		return "000001"
	}
	return "000000"
}

func exec(t testing.TB, sql string, args ...any) {
	t.Helper()
	_, err := db.Pool.Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}

func queryInt(t testing.TB, sql string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, db.Pool.QueryRow(context.Background(), sql, args...).Scan(&n))
	return n
}

func regexpFind(t testing.TB, pattern, s string) string {
	t.Helper()
	m := regexp.MustCompile(pattern).FindString(s)
	require.NotEmpty(t, m, "%q not found in %q", pattern, s)
	return m
}

// httptestDo sends a request without the contract check, for paths that are not in the
// contract (unknown routes).
func httptestDo(e *env, method, path, token string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, e.request(method, path, nil, token))
	return rec
}

type invitationJSON struct {
	ID        int64  `json:"id"`
	Email     string `json:"email"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	InvitedBy struct {
		ID    int64   `json:"id"`
		Name  *string `json:"name"`
		Email *string `json:"email"`
	} `json:"invited_by"`
	ExpiresAt  string  `json:"expires_at"`
	CreatedAt  string  `json:"created_at"`
	AcceptedAt *string `json:"accepted_at"`
	InviteURL  *string `json:"invite_url"`
}

type page[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}
