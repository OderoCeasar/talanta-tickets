package httpapi

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
)

// fakeTokens maps a token string straight to a caller, so middleware tests need no JWT.
type fakeTokens map[string]domain.Principal

func (f fakeTokens) VerifyAccess(token string) (domain.Principal, error) {
	if token == "expired" {
		return domain.Principal{}, domain.ErrTokenExpired
	}
	p, ok := f[token]
	if !ok {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	return p, nil
}

var testTokens = fakeTokens{
	"fan":       {UserID: 1, Role: domain.RoleFan},
	"organiser": {UserID: 2, Role: domain.RoleOrganiser},
	"admin":     {UserID: 3, Role: domain.RoleAdmin},
	"gate":      {UserID: 4, Role: domain.RoleGateStaff},
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testServer(limiter ratelimit.Limiter) *Server {
	if limiter == nil {
		limiter = ratelimit.NewMemory(nil)
	}
	return &Server{log: quietLogger(), tokens: testTokens, limiter: limiter, limits: DefaultRateLimits()}
}

func ok(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }

func call(h http.Handler, method, path, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAuthenticate(t *testing.T) {
	s := testServer(nil)
	var seen domain.Principal
	h := s.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = MustPrincipal(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := call(h, "GET", "/x", "organiser")
	require.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, domain.Principal{UserID: 2, Role: domain.RoleOrganiser}, seen)

	cases := []struct {
		name, header string
		code         domain.Code
	}{
		{"no header", "", domain.CodeUnauthenticated},
		{"unknown token", "Bearer nope", domain.CodeUnauthenticated},
		{"expired token", "Bearer expired", domain.CodeTokenExpired},
		{"basic auth", "Basic dXNlcjpwYXNz", domain.CodeUnauthenticated},
		{"bearer with nothing after it", "Bearer ", domain.CodeUnauthenticated},
		{"token without scheme", "organiser", domain.CodeUnauthenticated},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/x", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Equal(t, tc.code, errorIn(t, rec).Code)
			assert.Contains(t, rec.Header().Get("WWW-Authenticate"), "Bearer")
		})
	}
}

func TestOptionalAuth(t *testing.T) {
	s := testServer(nil)
	var signedIn bool
	h := s.OptionalAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, signedIn = PrincipalFrom(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	rec := call(h, "GET", "/x", "")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.False(t, signedIn, "no header: anonymous")

	rec = call(h, "GET", "/x", "fan")
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.True(t, signedIn)

	// A bad token is not quietly treated as anonymous.
	rec = call(h, "GET", "/x", "expired")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, domain.CodeTokenExpired, errorIn(t, rec).Code)
	rec = call(h, "GET", "/x", "garbage")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequireRole(t *testing.T) {
	s := testServer(nil)
	h := s.Authenticate(RequireRole(domain.RoleOrganiser, domain.RoleAdmin)(http.HandlerFunc(ok)))

	assert.Equal(t, http.StatusNoContent, call(h, "GET", "/x", "organiser").Code)
	assert.Equal(t, http.StatusNoContent, call(h, "GET", "/x", "admin").Code)
	for _, token := range []string{"fan", "gate"} {
		rec := call(h, "GET", "/x", token)
		assert.Equal(t, http.StatusForbidden, rec.Code, token)
		assert.Equal(t, domain.CodeForbidden, errorIn(t, rec).Code)
	}

	// Mounted without Authenticate by mistake: closed, not open.
	bare := RequireRole(domain.RoleAdmin)(http.HandlerFunc(ok))
	assert.Equal(t, http.StatusUnauthorized, call(bare, "GET", "/x", "admin").Code)
}

// The six route groups, exercised with stand-in handlers: this is the wiring later
// milestones mount into, so each group's door policy is pinned here.
func TestRouteGroupsEnforceRoles(t *testing.T) {
	s := testServer(nil)
	r := chi.NewRouter()
	r.NotFound(notFound)
	r.MethodNotAllowed(notFound)
	r.Route("/v1", func(v1 chi.Router) {
		v1.Group(func(r chi.Router) {
			r.Use(s.OptionalAuth)
			r.Get("/public", ok)
		})
		v1.Group(func(r chi.Router) {
			r.Use(s.Authenticate)
			r.Get("/signed-in", ok)
			r.Group(func(r chi.Router) {
				r.Use(RequireRole(domain.RoleFan))
				r.Get("/fan", ok)
			})
		})
		v1.Route("/admin", func(r chi.Router) {
			r.Use(s.Authenticate)
			r.Use(RequireRole(domain.StaffRoles...))
			r.Get("/staff", ok)
			r.Group(func(r chi.Router) {
				r.Use(RequireRole(domain.RoleAdmin))
				r.Get("/admin-only", ok)
			})
			catchAll(r)
		})
		v1.Route("/gate", func(r chi.Router) {
			r.Use(s.Authenticate)
			r.Use(RequireRole(domain.GateRoles...))
			r.Get("/scan", ok)
			catchAll(r)
		})
	})

	const (
		yes  = http.StatusNoContent
		anon = http.StatusUnauthorized
		no   = http.StatusForbidden
		gone = http.StatusNotFound
	)
	//                                       anon  fan   organiser admin gate
	want := map[string][5]int{
		"/v1/public":             {yes, yes, yes, yes, yes},
		"/v1/signed-in":          {anon, yes, yes, yes, yes},
		"/v1/fan":                {anon, yes, no, no, no},
		"/v1/admin/staff":        {anon, no, yes, yes, no},
		"/v1/admin/admin-only":   {anon, no, no, yes, no},
		"/v1/gate/scan":          {anon, no, no, yes, yes},
		"/v1/admin/no-such-path": {anon, no, gone, gone, no},
		"/v1/gate/no-such-path":  {anon, no, no, gone, gone},
		"/v1/no-such-path":       {gone, gone, gone, gone, gone},
	}
	callers := []string{"", "fan", "organiser", "admin", "gate"}
	for path, statuses := range want {
		for i, caller := range callers {
			rec := call(r, "GET", path, caller)
			assert.Equal(t, statuses[i], rec.Code, "%s as %q", path, caller)
			if rec.Code != yes {
				errorIn(t, rec) // every refusal is in the standard error shape
			}
		}
	}
}

func TestLimitByIP(t *testing.T) {
	clock := &testClock{t: time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)}
	s := testServer(ratelimit.NewMemory(clock.Now))
	h := clientIP(ClientIPResolver{})(s.LimitByIP("t", ratelimit.Rule{Limit: 2, Window: time.Minute})(http.HandlerFunc(ok)))

	from := func(remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/x", nil)
		req.RemoteAddr = remote
		// A forged header must not buy a fresh bucket.
		req.Header.Set("X-Forwarded-For", "9.9.9."+remote[len(remote)-1:])
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	assert.Equal(t, http.StatusNoContent, from("203.0.113.1:1001").Code)
	assert.Equal(t, http.StatusNoContent, from("203.0.113.1:1002").Code)
	clock.Advance(15 * time.Second)

	rec := from("203.0.113.1:1003")
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "45", rec.Header().Get("Retry-After"))
	e := errorIn(t, rec)
	assert.Equal(t, domain.CodeRateLimited, e.Code)
	assert.JSONEq(t, `{"retry_after_seconds":45}`, mustJSON(t, e.Details))

	assert.Equal(t, http.StatusNoContent, from("203.0.113.2:1001").Code, "another address has its own count")

	clock.Advance(45 * time.Second)
	assert.Equal(t, http.StatusNoContent, from("203.0.113.1:1004").Code, "the window ended")
}

func TestLimitByUser(t *testing.T) {
	s := testServer(nil)
	h := s.Authenticate(s.LimitByUser("t", ratelimit.Rule{Limit: 1, Window: time.Minute})(http.HandlerFunc(ok)))
	assert.Equal(t, http.StatusNoContent, call(h, "POST", "/x", "admin").Code)
	assert.Equal(t, http.StatusTooManyRequests, call(h, "POST", "/x", "admin").Code)
	assert.Equal(t, http.StatusNoContent, call(h, "POST", "/x", "organiser").Code)
}

func TestRecovererAnswers500InTheErrorShapeAndLogsTheStack(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := requestID(requestLogger(logger)(recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom: secret internals")
	}))))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, domain.CodeInternal, errorIn(t, rec).Code)
	assert.NotContains(t, rec.Body.String(), "boom")
	assert.Contains(t, logs.String(), "boom: secret internals")
	assert.Contains(t, logs.String(), "panic in handler")
	assert.Contains(t, logs.String(), `"status":500`)
}

func TestRequestID(t *testing.T) {
	h := requestID(http.HandlerFunc(ok))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	generated := rec.Header().Get("X-Request-Id")
	assert.Len(t, generated, 24)

	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("X-Request-Id", "client-supplied-1")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, "client-supplied-1", rec.Header().Get("X-Request-Id"))

	// Anything that could forge a log line is replaced.
	for _, bad := range []string{"x", "has space in it", "new\nline-injected", strings.Repeat("a", 65), `quote"brace}`} {
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set("X-Request-Id", bad)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.NotEqual(t, bad, rec.Header().Get("X-Request-Id"))
		assert.Len(t, rec.Header().Get("X-Request-Id"), 24)
	}
}

func TestCORS(t *testing.T) {
	h := cors([]string{"http://localhost:5173", "https://talanta.example.com/"})(http.HandlerFunc(ok))

	do := func(method, origin string, preflight bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/v1/me", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if preflight {
			req.Header.Set("Access-Control-Request-Method", "PATCH")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := do("GET", "http://localhost:5173", false)
	assert.Equal(t, "http://localhost:5173", rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, rec.Header().Get("Access-Control-Expose-Headers"), "Retry-After")
	assert.Equal(t, "Origin", rec.Header().Get("Vary"))
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Credentials"))

	rec = do("GET", "https://talanta.example.com", false)
	assert.Equal(t, "https://talanta.example.com", rec.Header().Get("Access-Control-Allow-Origin"), "a trailing slash in the setting is ignored")

	rec = do("OPTIONS", "http://localhost:5173", true)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Contains(t, rec.Header().Get("Access-Control-Allow-Methods"), "PATCH")
	assert.Contains(t, rec.Header().Get("Access-Control-Allow-Headers"), "Authorization")
	assert.Contains(t, rec.Header().Get("Access-Control-Allow-Headers"), "Idempotency-Key")

	rec = do("GET", "https://evil.example", false)
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, http.StatusNoContent, rec.Code, "the request itself still runs; the browser discards the answer")

	rec = do("OPTIONS", "https://evil.example", true)
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Methods"))

	rec = do("GET", "", false)
	assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"), "the mobile app sends no Origin")
}

func TestTimeoutPutsADeadlineOnTheContext(t *testing.T) {
	var had bool
	h := timeout(50 * time.Millisecond)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, had = r.Context().Deadline()
		<-r.Context().Done()
	}))
	start := time.Now()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	assert.True(t, had)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestRouterAnswersUnknownPathsAndGzipsInTheStandardWay(t *testing.T) {
	router := NewRouter(Deps{Logger: quietLogger(), DB: fakeDB{}, Tokens: testTokens})

	rec := call(router, "GET", "/v1/no-such-thing", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, domain.CodeNotFound, errorIn(t, rec).Code)
	assert.NotEmpty(t, rec.Header().Get("X-Request-Id"))

	rec = call(router, "DELETE", "/v1/healthz", "")
	assert.Equal(t, http.StatusNotFound, rec.Code, "wrong method is answered in the error shape too")
	errorIn(t, rec)

	req := httptest.NewRequest("GET", "/v1/healthz", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, "gzip", rec.Header().Get("Content-Encoding"))
}

// The real router, before any gate route exists: the door policy must already hold. (chi
// skips a sub-router's middleware until it has a route; catchAll is what prevents that.)
func TestRealRouterGuardsAdminAndGateEvenWithNoRoutesMounted(t *testing.T) {
	router := NewRouter(Deps{Logger: quietLogger(), DB: fakeDB{}, Tokens: testTokens})

	for _, path := range []string{"/v1/gate", "/v1/gate/", "/v1/gate/events", "/v1/gate/a/b/c", "/v1/admin", "/v1/admin/", "/v1/admin/events", "/v1/admin/a/b/c"} {
		assert.Equal(t, http.StatusUnauthorized, call(router, "GET", path, "").Code, "%s with no token", path)
		assert.Equal(t, http.StatusForbidden, call(router, "GET", path, "fan").Code, "%s as fan", path)
		assert.Equal(t, http.StatusForbidden, call(router, "POST", path, "fan").Code, "POST %s as fan", path)
	}
	assert.Equal(t, http.StatusForbidden, call(router, "GET", "/v1/gate/events", "organiser").Code)
	assert.Equal(t, http.StatusForbidden, call(router, "GET", "/v1/admin/events", "gate").Code)
	assert.Equal(t, http.StatusNotFound, call(router, "GET", "/v1/gate/events", "gate").Code)
	assert.Equal(t, http.StatusNotFound, call(router, "GET", "/v1/gate/events", "admin").Code)
	assert.Equal(t, http.StatusNotFound, call(router, "GET", "/v1/admin/events", "organiser").Code)
	// ADMIN-only routes that do exist refuse an organiser.
	assert.Equal(t, http.StatusForbidden, call(router, "GET", "/v1/admin/users", "organiser").Code)
	assert.Equal(t, http.StatusForbidden, call(router, "GET", "/v1/admin/invitations", "organiser").Code)
}
