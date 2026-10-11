package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
)

type (
	loggerKey    struct{}
	principalKey struct{}
	requestInfo  struct{ userID int64 }
	infoKey      struct{}
)

// requestIDHeader is echoed on every answer so a user can quote it when reporting a fault.
const requestIDHeader = "X-Request-Id"

// requestID gives every request an ID, takes a sane one from the caller if offered (so a
// client or proxy can correlate), and returns it in the answer.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(requestIDHeader)
		if !validRequestID(id) {
			var b [12]byte
			_, _ = rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set(requestIDHeader, id)
		// chi's key, so middleware.GetReqID keeps working for anything that uses it.
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), middleware.RequestIDKey, id)))
	})
}

// validRequestID keeps a caller-supplied ID from injecting anything into the logs.
func validRequestID(id string) bool {
	if len(id) < 8 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// clientIP resolves the caller's address once and puts it on the context.
func clientIP(resolver ClientIPResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), clientIPKey{}, resolver.ClientIP(r))
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// requestLogger puts a logger carrying the request ID on the context, so every line a
// handler or service writes can be tied back to one request, then logs the outcome.
func requestLogger(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			logger := base.With("request_id", middleware.GetReqID(r.Context()))
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			info := &requestInfo{}

			ctx := context.WithValue(r.Context(), loggerKey{}, logger)
			ctx = context.WithValue(ctx, infoKey{}, info)
			next.ServeHTTP(ww, r.WithContext(ctx))

			// Health checks arrive every few seconds from Docker and would drown the log.
			if r.URL.Path == "/healthz" || r.URL.Path == "/v1/healthz" {
				return
			}
			attrs := []any{
				"method", r.Method,
				// The route pattern, not the URL: paths can carry secrets (invitation tokens).
				"route", routePattern(r),
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"remote_ip", ClientIP(ctx).String(),
			}
			if info.userID != 0 {
				attrs = append(attrs, "user_id", info.userID)
			}
			logger.Info("request", attrs...)
		})
	}
}

// LoggerFrom returns the request's logger, or the default one outside a request.
func LoggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// recoverer turns a panic into a logged 500 in the standard error shape, instead of a
// dropped connection.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			// The one panic net/http wants back: it means "abort this response quietly".
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			LoggerFrom(r.Context()).ErrorContext(r.Context(), "panic in handler",
				"method", r.Method, "route", routePattern(r), "panic", fmt.Sprint(rec), "stack", string(debug.Stack()))
			WriteError(w, r, fmt.Errorf("panic: %v", rec))
		}()
		next.ServeHTTP(w, r)
	})
}

// timeout bounds how long a request may work. Database calls and outside calls take the
// request context, so they are cancelled when it runs out and the handler answers through
// WriteError like any other failure.
//
// The context from before the deadline is kept on the request so one route can swap the
// deadline for its own (routeTimeout); a deadline cannot otherwise be pushed out once set.
func timeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			untimed := r.Context()
			ctx, cancel := context.WithTimeout(context.WithValue(untimed, untimedKey{}, untimed), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

type untimedKey struct{}

// rebasedContext carries the values of one context and the lifetime of another.
type rebasedContext struct {
	context.Context // values: logger, principal, request ID, …
	life            context.Context
}

func (c rebasedContext) Deadline() (time.Time, bool) { return c.life.Deadline() }
func (c rebasedContext) Done() <-chan struct{}       { return c.life.Done() }
func (c rebasedContext) Err() error                  { return c.life.Err() }

// routeTimeout gives the routes it wraps their own time limit in place of the router-wide
// one. It still ends when the client goes away or the server shuts down, because the new
// deadline hangs off the context the router-wide timeout started from.
func routeTimeout(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			base, ok := r.Context().Value(untimedKey{}).(context.Context)
			if !ok {
				// Not behind timeout(): there is no router-wide deadline to replace.
				base = r.Context()
			}
			life, cancel := context.WithTimeout(base, d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(rebasedContext{Context: r.Context(), life: life}))
		})
	}
}

// bodyLimit caps the request body. Reading past the cap fails, which DecodeJSON reports as
// 413 PAYLOAD_TOO_LARGE.
func bodyLimit(n int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > n {
				WriteError(w, r, domain.ErrPayloadTooLarge)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// noStore keeps answers that carry tokens or secrets out of every cache.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// cors lets the admin site, served from another origin in development, call the API from a
// browser. The mobile app sends no Origin and is not affected. Credentials (cookies) are not
// allowed: the API authenticates by bearer token only.
func cors(origins []string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(origins))
	for _, o := range origins {
		allowed[strings.TrimRight(o, "/")] = true
	}
	const (
		methods = "GET, POST, PATCH, PUT, DELETE, OPTIONS"
		headers = "Authorization, Content-Type, Idempotency-Key, X-Mock-Outcome, X-Request-Id"
		exposed = "Retry-After, X-Request-Id"
	)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			// The answer differs by Origin, so caches must key on it.
			w.Header().Add("Vary", "Origin")
			if origin == "" || !allowed[origin] {
				if isPreflight(r) {
					// An origin we do not know gets no CORS headers; the browser blocks it.
					w.WriteHeader(http.StatusNoContent)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Expose-Headers", exposed)
			if isPreflight(r) {
				w.Header().Set("Access-Control-Allow-Methods", methods)
				w.Header().Set("Access-Control-Allow-Headers", headers)
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
}

// TokenVerifier checks an access token. *auth.Tokens implements it.
type TokenVerifier interface {
	VerifyAccess(token string) (domain.Principal, error)
}

// PrincipalFrom returns the signed-in caller. ok is false on a public route with no token.
func PrincipalFrom(ctx context.Context) (domain.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(domain.Principal)
	return p, ok
}

// MustPrincipal is PrincipalFrom for handlers behind Authenticate, where a caller is always
// present. Without one it panics: the route was mounted in the wrong group.
func MustPrincipal(ctx context.Context) domain.Principal {
	p, ok := PrincipalFrom(ctx)
	if !ok {
		panic("httpapi: handler needs a signed-in caller but was mounted without Authenticate")
	}
	return p
}

// bearerToken reads "Authorization: Bearer <token>". present is false when there is no
// Authorization header at all.
func bearerToken(r *http.Request) (token string, present bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	scheme, rest, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return "", true
	}
	return strings.TrimSpace(rest), true
}

func (s *Server) withPrincipal(r *http.Request, token string) (*http.Request, error) {
	p, err := s.tokens.VerifyAccess(token)
	if err != nil {
		return nil, err
	}
	if info, ok := r.Context().Value(infoKey{}).(*requestInfo); ok {
		info.userID = p.UserID
	}
	return r.WithContext(context.WithValue(r.Context(), principalKey{}, p)), nil
}

// Authenticate requires a good access token: 401 UNAUTHENTICATED without one, 401
// TOKEN_EXPIRED when it has run out.
func (s *Server) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, _ := bearerToken(r)
		if token == "" {
			WriteError(w, r, domain.ErrUnauthenticated)
			return
		}
		authed, err := s.withPrincipal(r, token)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		next.ServeHTTP(w, authed)
	})
}

// OptionalAuth is for public routes that answer more to a signed-in caller (the seat map
// marks the caller's own holds). No Authorization header: the request goes through
// anonymous. A header that is present but bad is still a 401, so an app with an expired
// token refreshes it instead of silently being shown the anonymous view.
func (s *Server) OptionalAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, present := bearerToken(r)
		if !present {
			next.ServeHTTP(w, r)
			return
		}
		if token == "" {
			WriteError(w, r, domain.ErrUnauthenticated)
			return
		}
		authed, err := s.withPrincipal(r, token)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		next.ServeHTTP(w, authed)
	})
}

// RequireRole lets through only callers whose role is listed; anyone else signed in gets
// 403 FORBIDDEN. It must sit behind Authenticate.
func RequireRole(roles ...domain.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := PrincipalFrom(r.Context())
			if !ok {
				WriteError(w, r, domain.ErrUnauthenticated)
				return
			}
			for _, role := range roles {
				if p.Role == role {
					next.ServeHTTP(w, r)
					return
				}
			}
			WriteError(w, r, domain.ErrForbidden)
		})
	}
}

// LimitByIP allows rule events per client address for the routes it wraps. name separates
// one limit's counts from another's.
func (s *Server) LimitByIP(name string, rule ratelimit.Rule) func(http.Handler) http.Handler {
	return s.limit(func(r *http.Request) string {
		return "ip:" + name + ":" + rateLimitKey(ClientIP(r.Context()))
	}, rule)
}

// LimitByUser allows rule events per signed-in user. It must sit behind Authenticate.
func (s *Server) LimitByUser(name string, rule ratelimit.Rule) func(http.Handler) http.Handler {
	return s.limit(func(r *http.Request) string {
		return "user:" + name + ":" + strconv.FormatInt(MustPrincipal(r.Context()).UserID, 10)
	}, rule)
}

func (s *Server) limit(key func(*http.Request) string, rule ratelimit.Rule) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if d := s.limiter.Take(key(r), rule); !d.Allowed {
				WriteError(w, r, domain.RateLimited(ratelimit.RetryAfterSeconds(d.RetryAfter)))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
