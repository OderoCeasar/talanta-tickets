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
	"github.com/OderoCeasar/talanta-tickets/backend/internal/config"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/notify"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
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
	logger := config.NewLogger(cfg.Base, "api")
	slog.SetDefault(logger)

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

	authService, err := auth.NewService(auth.Config{
		DB:           pool,
		Logger:       logger,
		Secret:       cfg.JWTSecret,
		DevShortcuts: cfg.AllowDevShortcuts(),
		AdminBaseURL: cfg.AdminBaseURL,
		// No SMS provider yet (M7): codes are written to the log.
		SMS:     notify.LogSMS{Logger: logger, Production: cfg.IsProduction()},
		Mailer:  mailer,
		Limiter: limiter,
		Policy:  authPolicy,

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
	return srv.Shutdown(shutdownCtx)
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
