// Package httpapi holds the HTTP handlers, middleware and DTOs.
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/booking"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/events"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/gate"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/venue"
)

// Pinger is the part of the database pool the health check needs.
type Pinger interface {
	Ping(ctx context.Context) error
}

// RateLimits are the per-IP and per-user limits of the HTTP layer. (Per-phone and per-email
// limits belong to the auth service, which knows the normalised identity.)
//
// The per-IP numbers are deliberately loose. Kenyan mobile networks put thousands of phones
// behind one address, and on a match day they all sign in from it; a tight per-IP limit
// would lock out real fans. The per-phone and per-email limits are the tight ones.
type RateLimits struct {
	OTPRequestPerIP ratelimit.Rule
	OTPVerifyPerIP  ratelimit.Rule
	LoginPerIP      ratelimit.Rule
	TOTPVerifyPerIP ratelimit.Rule
	RefreshPerIP    ratelimit.Rule
	InvitationPerIP ratelimit.Rule
	// SensitivePerUser covers password change and the TOTP settings.
	SensitivePerUser ratelimit.Rule
	// PublicReadPerIP covers the public event and seat map reads. An open block is polled
	// every five seconds by every fan looking at it, so this one is the loosest of all: it
	// only stops a single client hammering the API. A zero limit switches it off.
	PublicReadPerIP ratelimit.Rule
	// HoldPerUser and HoldPerIP cover creating a hold (POST /holds and best available). The
	// per-user one is the real limit: a fan picks seats a handful of times, a script
	// cycling holds to keep seats off sale does not. Per IP is loose like the others. A
	// zero limit switches either off.
	HoldPerUser ratelimit.Rule
	HoldPerIP   ratelimit.Rule
	// WebTicketPerIP covers the web ticket page (GET /t/{webToken}). Its token is 256
	// random bits, so the limit is not what stops guessing; it stops one client using the
	// page to make the server work all day. A fan opens the page a few times; the page is
	// only for fans without the app, so even a stand of them behind one address stays far
	// below five a second. Zero switches it off.
	WebTicketPerIP ratelimit.Rule
	// TicketQRPerUser covers GET /tickets/{ticketId}/qr. The app draws its own QR codes; the
	// image is for sharing and printing, a few times per ticket. Zero switches it off.
	TicketQRPerUser ratelimit.Rule
	// CheckoutPerUser covers the two writes of checkout together: creating a booking and
	// starting a payment. A fan does each a handful of times; a zero limit switches it off.
	CheckoutPerUser ratelimit.Rule
	// ScanUploadPerDevice and ScanUploadPerUser cover POST /gate/scans. A device syncs every
	// few seconds at most, 500 scans a call, so the per-device limit is far above honest
	// use. The per-user one is loose because stewards at one gate often share a sign-in:
	// it counts every device of that account together. A zero limit switches either off.
	ScanUploadPerDevice ratelimit.Rule
	ScanUploadPerUser   ratelimit.Rule
}

// DefaultRateLimits is what production runs.
func DefaultRateLimits() RateLimits {
	return RateLimits{
		OTPRequestPerIP:  ratelimit.Rule{Limit: 300, Window: 10 * time.Minute},
		OTPVerifyPerIP:   ratelimit.Rule{Limit: 600, Window: 10 * time.Minute},
		LoginPerIP:       ratelimit.Rule{Limit: 30, Window: 15 * time.Minute},
		TOTPVerifyPerIP:  ratelimit.Rule{Limit: 30, Window: 15 * time.Minute},
		RefreshPerIP:     ratelimit.Rule{Limit: 1200, Window: time.Minute},
		InvitationPerIP:  ratelimit.Rule{Limit: 30, Window: 15 * time.Minute},
		SensitivePerUser: ratelimit.Rule{Limit: 10, Window: 15 * time.Minute},
		PublicReadPerIP:  ratelimit.Rule{Limit: 12000, Window: time.Minute},
		HoldPerUser:      ratelimit.Rule{Limit: 20, Window: time.Minute},
		HoldPerIP:        ratelimit.Rule{Limit: 3000, Window: time.Minute},
		WebTicketPerIP:   ratelimit.Rule{Limit: 300, Window: time.Minute},
		TicketQRPerUser:  ratelimit.Rule{Limit: 60, Window: time.Minute},
		CheckoutPerUser:  ratelimit.Rule{Limit: 30, Window: time.Minute},

		ScanUploadPerDevice: ratelimit.Rule{Limit: 120, Window: time.Minute},
		ScanUploadPerUser:   ratelimit.Rule{Limit: 3000, Window: time.Minute},
	}
}

// Deps is everything the router needs. Each milestone adds its service here.
type Deps struct {
	Logger *slog.Logger
	// DB is pinged by the health check.
	DB Pinger
	// Auth is the sign-in, session, invitation and profile service.
	Auth *auth.Service
	// Tokens checks access tokens. Nil means Auth.Tokens().
	Tokens TokenVerifier
	// Limiter backs every rate limit. Nil means a new in-memory one.
	Limiter ratelimit.Limiter
	// RateLimits nil means DefaultRateLimits.
	RateLimits *RateLimits

	// CORSOrigins are the browser origins allowed to call the API.
	CORSOrigins []string
	// ClientIP decides whose forwarding headers are believed.
	ClientIP ClientIPResolver
	// RequestTimeout bounds one request; zero means 30 seconds.
	RequestTimeout time.Duration
	// BodyLimit caps request bodies; zero means DefaultBodyLimit.
	BodyLimit int64

	// Events is the event, inventory, seat map and sales service (M4, M5).
	Events *events.Service
	// Venues is the venue layout service (M4).
	Venues *venue.Service
	// PublishTimeout bounds POST /admin/events/{id}/publish, which copies one row per seat
	// and outlasts RequestTimeout, and POST /admin/events/{id}/cancel, which on a sold-out
	// event rewrites as many tickets. Zero means 55 seconds. It must stay under the server's
	// write timeout.
	PublishTimeout time.Duration

	// Holds is the seat hold service (M6).
	Holds *booking.Service

	// Tickets is the ticket service (M7): the ticket endpoints and the web ticket page.
	Tickets *ticketing.Service

	// Payments is the payment service (M7): STK push, the M-Pesa callback, alerts, refunds.
	Payments *payments.Service
	// Pool is the database pool the booking answers read their tickets through. Nil means
	// DB, when DB is a pool.
	Pool store.DBTX

	// Gate is gate mode (M10): gate events, the manifest, scan upload.
	Gate *gate.Service
	// AllowMockOutcome is config.Base.AllowDevShortcuts(): whether the X-Mock-Outcome header
	// is read. False (the zero value, and production) refuses the header with 403.
	AllowMockOutcome bool
	// MpesaCallbackAllowed are the networks the M-Pesa callback is accepted from. Empty
	// allows every address, which production's configuration refuses.
	MpesaCallbackAllowed []netip.Prefix
}

// Server holds the dependencies handlers and middleware share. Handlers are its methods.
type Server struct {
	log     *slog.Logger
	db      Pinger
	auth    *auth.Service
	tokens  TokenVerifier
	limiter ratelimit.Limiter
	limits  RateLimits

	events         *events.Service
	venues         *venue.Service
	publishTimeout time.Duration

	holds *booking.Service

	tickets *ticketing.Service
	// qr keeps drawn QR images: the ticket image endpoint and the web ticket page share it.
	qr *ticketing.QRCache

	payments         *payments.Service
	pool             store.DBTX
	allowMockOutcome bool
	callbackAllowed  []netip.Prefix

	gate *gate.Service
}

// NewRouter builds the API. Every route sits under /v1 because the admin web app is served
// from the same origin, and its client-side routes (/events, /login, …) would otherwise
// collide with API paths. /healthz is also exposed at the root for container health checks.
func NewRouter(d Deps) http.Handler {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.Limiter == nil {
		d.Limiter = ratelimit.NewMemory(nil)
	}
	if d.RateLimits == nil {
		limits := DefaultRateLimits()
		d.RateLimits = &limits
	}
	if d.Tokens == nil && d.Auth != nil {
		d.Tokens = d.Auth.Tokens()
	}
	if d.RequestTimeout <= 0 {
		d.RequestTimeout = 30 * time.Second
	}
	if d.BodyLimit <= 0 {
		d.BodyLimit = DefaultBodyLimit
	}
	if d.PublishTimeout <= 0 {
		d.PublishTimeout = 55 * time.Second
	}
	s := &Server{log: d.Logger, db: d.DB, auth: d.Auth, tokens: d.Tokens, limiter: d.Limiter, limits: *d.RateLimits}
	s.events, s.venues, s.publishTimeout = d.Events, d.Venues, d.PublishTimeout
	s.holds = d.Holds
	s.tickets = d.Tickets
	s.qr = ticketing.NewQRCache(0)
	if d.Pool == nil {
		d.Pool, _ = d.DB.(store.DBTX)
	}
	s.payments, s.pool = d.Payments, d.Pool
	s.allowMockOutcome, s.callbackAllowed = d.AllowMockOutcome, d.MpesaCallbackAllowed
	s.gate = d.Gate

	r := chi.NewRouter()
	// Order matters: the ID and the caller's address first so every later line can log
	// them; recovery inside the logger so a panic is still logged as a 500 request.
	r.Use(requestID)
	r.Use(clientIP(d.ClientIP))
	r.Use(requestLogger(d.Logger))
	r.Use(recoverer)
	r.Use(cors(d.CORSOrigins))
	r.Use(middleware.Compress(5))
	r.Use(timeout(d.RequestTimeout))
	r.Use(bodyLimit(d.BodyLimit))

	// Unknown paths and wrong methods answer in the standard error shape too.
	r.NotFound(notFound)
	r.MethodNotAllowed(notFound)

	r.Get("/healthz", s.health)
	r.Route("/v1", func(v1 chi.Router) {
		v1.Get("/healthz", s.health)
		s.routes(v1)
	})
	return r
}

// routes lays out the groups every endpoint belongs to. To add an endpoint, mount its
// handler in the group whose callers it serves; the group's middleware does the rest.
//
//	sign-in    /auth/*: no token is looked at
//	public     no token needed (OptionalAuth still identifies a signed-in caller)
//	web page   the HTML ticket page from the SMS link: no token is looked at
//	webhook    the M-Pesa callback: no token is looked at, always 200
//	signed-in  any role
//	fan        FAN only
//	staff      ORGANISER and ADMIN, everything under /admin
//	admin      ADMIN only, inside /admin
//	gate       GATE_STAFF and ADMIN, everything under /gate
func (s *Server) routes(v1 chi.Router) {
	// Sign-in. No access token is read here at all: these are the routes a client calls
	// precisely when its access token is missing or dead, so a stale Authorization header
	// must not get in the way of a refresh or a logout.
	v1.Group(func(r chi.Router) {
		s.signInRoutes(r)
	})

	// Public.
	v1.Group(func(r chi.Router) {
		r.Use(s.OptionalAuth)
		s.publicRoutes(r)
	})

	// The web ticket page. Like sign-in it reads no access token: it is opened in a phone's
	// browser, and its answers are HTML, never the JSON error shape.
	v1.Group(func(r chi.Router) {
		s.webTicketRoutes(r)
	})

	// The M-Pesa callback. Called by Safaricom, with no token; like the web ticket page it
	// must not be turned into a 401 by a stray Authorization header: it always answers 200.
	v1.Group(func(r chi.Router) {
		s.webhookRoutes(r)
	})

	// Signed in, any role.
	v1.Group(func(r chi.Router) {
		r.Use(s.Authenticate)
		s.signedInRoutes(r)

		// Fans only.
		r.Group(func(r chi.Router) {
			r.Use(RequireRole(domain.RoleFan))
			s.fanRoutes(r)
		})
	})

	// /admin and /gate are sub-routers, not groups, on purpose: a sub-router's middleware
	// runs for every path under the prefix, including ones that do not exist. So a fan
	// probing /v1/admin/anything gets 403 whether or not the route is there, and cannot map
	// the admin API from the difference between 403 and 404.
	v1.Route("/admin", func(r chi.Router) {
		r.Use(s.Authenticate)
		r.Use(RequireRole(domain.StaffRoles...))
		s.staffRoutes(r)

		r.Group(func(r chi.Router) {
			r.Use(RequireRole(domain.RoleAdmin))
			s.adminRoutes(r)
		})
		catchAll(r)
	})

	v1.Route("/gate", func(r chi.Router) {
		r.Use(s.Authenticate)
		r.Use(RequireRole(domain.GateRoles...))
		s.gateRoutes(r)
		catchAll(r)
	})
}

// catchAll gives a sub-router a route for every path under its prefix. It is what makes the
// promise above true: chi only runs a router's middleware once that router has a route, so
// a sub-router with none would answer 404 to everyone, signed in
// or not. Real routes are more specific and always win over this one.
func catchAll(r chi.Router) {
	r.HandleFunc("/", notFound)
	r.HandleFunc("/*", notFound)
}

// signInRoutes: paths are relative to /v1.
func (s *Server) signInRoutes(r chi.Router) {
	l := s.limits
	// Answers here carry tokens.
	r.Use(noStore)
	r.With(s.LimitByIP("otp-request", l.OTPRequestPerIP)).Post("/auth/otp/request", s.requestOTP)
	r.With(s.LimitByIP("otp-verify", l.OTPVerifyPerIP)).Post("/auth/otp/verify", s.verifyOTP)
	r.With(s.LimitByIP("login", l.LoginPerIP)).Post("/auth/login", s.passwordLogin)
	r.With(s.LimitByIP("totp-verify", l.TOTPVerifyPerIP)).Post("/auth/totp/verify", s.verifyTOTP)
	r.With(s.LimitByIP("refresh", l.RefreshPerIP)).Post("/auth/refresh", s.refreshTokens)
	r.Post("/auth/logout", s.logout)

	invite := s.LimitByIP("invitation", l.InvitationPerIP)
	r.With(invite).Get("/auth/invitations/{token}", s.getInvitation)
	r.With(invite).Post("/auth/invitations/{token}/accept", s.acceptInvitation)
}

// publicRoutes: paths are relative to /v1.
func (s *Server) publicRoutes(r chi.Router) {
	// M4/M5: events, categories and the three seat map levels.
	s.publicEventRoutes(r)
	// (The web ticket page and the M-Pesa callback have their own groups.)
}

// signedInRoutes: any role. Paths are relative to /v1.
func (s *Server) signedInRoutes(r chi.Router) {
	r.Get("/me", s.getMe)
	r.Patch("/me", s.updateMe)

	// Password and TOTP exist only for accounts that sign in by email.
	r.Group(func(r chi.Router) {
		r.Use(RequireRole(domain.StaffRoles...))
		r.Use(s.LimitByUser("sensitive", s.limits.SensitivePerUser))
		r.Post("/me/password", s.changePassword)
		r.Post("/me/totp/setup", s.setupTOTP)
		r.Post("/me/totp/enable", s.enableTOTP)
		r.Post("/me/totp/disable", s.disableTOTP)
	})
}

// fanRoutes: FAN only. Paths are relative to /v1.
func (s *Server) fanRoutes(r chi.Router) {
	// M6: holds and /me/holds.
	s.fanHoldRoutes(r)
	// M7: /me/tickets, /tickets/{ticketId} and its QR image.
	s.fanTicketRoutes(r)
	// M7: bookings, payments, /me/bookings.
	s.fanBookingRoutes(r)
}

// staffRoutes: ORGANISER and ADMIN. Paths are relative to /v1/admin.
func (s *Server) staffRoutes(r chi.Router) {
	// M4: venues, blocks, templates, events, categories, assignments, sales.
	s.staffEventRoutes(r)
	// M7: alerts and refunds.
	s.staffPaymentRoutes(r)
}

// adminRoutes: ADMIN only. Paths are relative to /v1/admin.
func (s *Server) adminRoutes(r chi.Router) {
	r.Get("/users", s.listStaffUsers)
	r.Get("/invitations", s.listInvitations)
	r.Post("/invitations", s.createInvitation)
	r.Post("/invitations/{invitationId}/resend", s.resendInvitation)
	r.Delete("/invitations/{invitationId}", s.revokeInvitation)
}

func notFound(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, domain.ErrNotFound)
}

// health is the one endpoint whose failure body is not the standard error shape: container
// health checks only look at the status.
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := s.db.Ping(ctx); err != nil {
		s.log.Error("health check: database unreachable", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "degraded"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// routePattern is the matched route ("/v1/auth/invitations/{token}"), for logs.
func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	return "unmatched"
}
