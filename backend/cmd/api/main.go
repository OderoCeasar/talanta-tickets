package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/booking"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/config"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/events"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/notify"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments/providers"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/venue"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadAPI()
	if err != nil {
		return err
	}
	// The ticket signing key is read by the package that uses it, straight into a signer:
	// it is never a field of the config struct, which gets printed and logged.
	keyLoader := config.NewLoader()
	ticketKeys := ticketing.LoadKeyring(keyLoader, cfg.Base)
	if err := keyLoader.Err(); err != nil {
		return err
	}
	logger := config.NewLogger(cfg.Base, "api")
	slog.SetDefault(logger)

	if ticketKeys.Ephemeral {
		logger.Warn("TICKET_SIGNING_KEY IS NOT SET: TICKETS ARE SIGNED WITH A KEY MADE UP AT STARTUP. "+
			"Every ticket issued now stops verifying when this process ends. Set TICKET_SIGNING_KEY to keep tickets across restarts.",
			"key_id", ticketKeys.Signer.KeyID())
	} else {
		logger.Info("ticket signing key loaded", "key_id", ticketKeys.Signer.KeyID(), "verify_keys", len(ticketKeys.Verifier.PublicKeys()))
	}

	if cfg.JWTSecretIsDevDefault {
		logger.Warn("JWT_SECRET IS NOT SET: USING THE PUBLIC DEVELOPMENT SECRET. " +
			"Anyone can forge a sign-in for this server. Never run it reachable from the internet like this.")
	}
	if cfg.AllowDevShortcuts() {
		logger.Warn("development shortcuts are ON: sign-in codes are returned in API answers", "app_env", cfg.AppEnv)
	}
	if len(cfg.TrustedProxies) == 0 {
		logger.Info("no TRUSTED_PROXY_CIDRS: forwarding headers are ignored and the TCP peer is taken as the client address")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := store.OpenWithOptions(ctx, cfg.DatabaseURL, store.Options{
		StatementTimeout: cfg.DBStatementTimeout,
		IdleInTxTimeout:  cfg.DBIdleInTxTimeout,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	mailer, err := newMailer(cfg, logger)
	if err != nil {
		return err
	}
	limiter := ratelimit.NewMemory(nil)

	authPolicy := auth.DefaultPolicy()
	authPolicy.OTPGlobalHourly.Limit = cfg.OTPGlobalHourlyLimit
	authPolicy.OTPGlobalDaily.Limit = cfg.OTPGlobalDailyLimit

	// No SMS provider yet: messages are written to the log (never their text in production).
	sms := notify.LogSMS{Logger: logger, Production: cfg.IsProduction()}

	authService, err := auth.NewService(auth.Config{
		DB:           pool,
		Logger:       logger,
		Secret:       cfg.JWTSecret,
		DevShortcuts: cfg.AllowDevShortcuts(),
		AdminBaseURL: cfg.AdminBaseURL,
		SMS:          sms,
		Mailer:       mailer,
		Limiter:      limiter,
		Policy:       authPolicy,

		OTPCountryCodes:     cfg.OTPCountryCodes,
		Argon2MaxConcurrent: cfg.Argon2MaxConcurrent,
	})
	if err != nil {
		return err
	}

	if cfg.BootstrapAdmin.Enabled() {
		bootCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, err := authService.BootstrapAdmin(bootCtx, cfg.BootstrapAdmin.Email, cfg.BootstrapAdmin.Password)
		cancel()
		if err != nil {
			return err
		}
	}

	// Publish copies one inventory row per seat and needs longer than REQUEST_TIMEOUT. Its
	// limit is tied to the write timeout so the answer is never cut off mid-write; the
	// publish transaction raises its own statement timeout to match.
	publishTimeout := max(cfg.WriteTimeout-5*time.Second, cfg.RequestTimeout)
	eventPolicy := events.DefaultPolicy()
	eventPolicy.PublishTimeout = publishTimeout
	eventPolicy.CancelTimeout = publishTimeout
	eventService, err := events.NewService(events.Config{DB: pool, Logger: logger, Policy: eventPolicy})
	if err != nil {
		return err
	}

	holdPolicy := booking.DefaultPolicy()
	holdPolicy.BookingFeeBPS = cfg.BookingFeeBPS
	holdService, err := booking.NewService(booking.Config{DB: pool, Logger: logger, Policy: holdPolicy})
	if err != nil {
		return err
	}

	// The payment flow calls ticketService.IssueForBooking inside its transaction and
	// ticketService.SendTicketSMS after the commit.
	ticketService, err := ticketing.NewService(ticketing.Config{
		DB: pool, Logger: logger, Keys: ticketKeys, SMS: sms, PublicBaseURL: cfg.PublicBaseURL,
	})
	if err != nil {
		return err
	}

	// The provider is the one the configuration names; production cannot name the mock and
	// an incomplete M-Pesa configuration has already stopped the start.
	provider, mockProvider, err := providers.New(cfg.Payments, cfg.Base, logger)
	if err != nil {
		return err
	}
	paymentPolicy := payments.DefaultPolicy()
	paymentPolicy.HoldWindow, paymentPolicy.EventLockWait = holdPolicy.PaymentWindow, holdPolicy.EventLockWait
	paymentService, err := payments.NewService(payments.Config{
		DB: pool, Logger: logger, Provider: provider, Tickets: ticketService, Policy: paymentPolicy,
	})
	if err != nil {
		return err
	}
	if mockProvider != nil {
		logger.Warn("payments use the MOCK provider: no money moves. X-Mock-Outcome chooses what a payment does", "app_env", cfg.AppEnv)
		// The mock's "callback" goes where the real one goes after the webhook has parsed
		// it, on the same background runner.
		mockProvider.SetSink(func(ctx context.Context, cb payments.Callback) { paymentService.HandleCallbackAsync(ctx, cb) })
	} else if cfg.Payments.Provider == config.ProviderNone {
		logger.Warn("payments are OFF: no provider is configured, so every attempt to pay is answered 'not available'. Set the MPESA_* variables to take payments")
	} else {
		logger.Info("payments go through M-Pesa (Daraja)", "mpesa_env", cfg.Payments.Mpesa.Env, "short_code", cfg.Payments.Mpesa.ShortCode)
	}
	if len(cfg.Payments.CallbackAllowedCIDRs) == 0 {
		logger.Warn("MPESA_CALLBACK_ALLOWED_CIDRS is not set: the M-Pesa callback is accepted from any address")
	}

	srv := &http.Server{
		Addr: cfg.Addr,
		Handler: httpapi.NewRouter(httpapi.Deps{
			Logger:         logger,
			DB:             pool,
			Auth:           authService,
			Limiter:        limiter,
			CORSOrigins:    cfg.CORSOrigins,
			ClientIP:       httpapi.ClientIPResolver{TrustedProxies: cfg.TrustedProxies, Header: cfg.ClientIPHeader},
			RequestTimeout: cfg.RequestTimeout,
			Events:         eventService,
			Venues:         venue.NewService(pool),
			PublishTimeout: publishTimeout,
			Holds:          holdService,
			Tickets:        ticketService,
			Payments:       paymentService,
			Pool:           pool,

			AllowMockOutcome:     cfg.AllowDevShortcuts(),
			MpesaCallbackAllowed: cfg.Payments.CallbackAllowedCIDRs,
		}),
		// Each bound closes one way a slow or silent client can hold a connection open.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       cfg.ReadTimeout,
		// Longer than the slowest handler, or its answer is cut off mid-write.
		WriteTimeout:   cfg.WriteTimeout,
		IdleTimeout:    cfg.IdleTimeout,
		MaxHeaderBytes: 64 << 10,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("api listening", "addr", cfg.Addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	// Give in-flight requests a moment to finish before the container is killed.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = srv.Shutdown(shutdownCtx)
	// Callback work and ticket SMS already started are allowed to finish (each has its own
	// deadline) before the pool is closed under them.
	paymentService.Wait()
	return err
}

// newMailer picks SMTP when SMTP_HOST is set and the logging mock otherwise.
func newMailer(cfg config.API, logger *slog.Logger) (notify.Mailer, error) {
	if !cfg.SMTP.Enabled() {
		return notify.LogMailer{Logger: logger, Production: cfg.IsProduction()}, nil
	}
	logger.Info("mail goes through SMTP", "host", cfg.SMTP.Host, "port", cfg.SMTP.Port)
	return notify.NewSMTPMailer(notify.SMTPConfig{
		Host:     cfg.SMTP.Host,
		Port:     cfg.SMTP.Port,
		Username: cfg.SMTP.Username,
		Password: cfg.SMTP.Password,
		From:     cfg.SMTP.From,

		AllowPlaintext: cfg.SMTP.AllowPlaintext,
	})
}
