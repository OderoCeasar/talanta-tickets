// Command worker runs the background jobs: the hold expiry sweeper and payment reconciliation.
// It is a separate process so a slow job never competes with request handling.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/booking"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/config"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/notify"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/payments/providers"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/worker"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.LoadWorker()
	if err != nil {
		return err
	}
	// Reconciliation finishes payments, and finishing a payment issues tickets: the worker
	// signs with the same key as the API (read straight into a signer, never a config field).
	keyLoader := config.NewLoader()
	ticketKeys := ticketing.LoadKeyring(keyLoader, cfg.Base)
	if err := keyLoader.Err(); err != nil {
		return err
	}
	logger := config.NewLogger(cfg.Base, "worker")
	slog.SetDefault(logger)
	if ticketKeys.Ephemeral {
		logger.Warn("TICKET_SIGNING_KEY IS NOT SET: tickets issued by the worker (when reconciliation finishes a payment) are signed " +
			"with a key made up at startup that the API does not know. Set the same TICKET_SIGNING_KEY for the API and the worker.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The same session limits as the API's pool (DB_STATEMENT_TIMEOUT and
	// DB_IDLE_IN_TX_TIMEOUT default to these): the sweeper locks hold and seat rows, and a
	// stuck sweeper transaction must not be able to sit on them.
	pool, err := store.OpenWithOptions(ctx, cfg.DatabaseURL, store.Options{
		StatementTimeout: workerStatementTimeout,
		IdleInTxTimeout:  workerIdleInTxTimeout,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	holds, err := booking.NewService(booking.Config{DB: pool, Logger: logger})
	if err != nil {
		return err
	}
	// The same provider as the API (config.LoadPayments is shared): reconciliation has to ask
	// whoever the payment was sent to. The mock is given no callback sink here: the worker
	// only queries.
	provider, mockProvider, err := providers.New(cfg.Payments, cfg.Base, logger)
	if err != nil {
		return err
	}
	if mockProvider != nil {
		logger.Warn("payment reconciliation uses the MOCK provider: no money moves", "app_env", cfg.AppEnv)
	}
	tickets, err := ticketing.NewService(ticketing.Config{
		DB: pool, Logger: logger, Keys: ticketKeys, PublicBaseURL: cfg.PublicBaseURL,
		SMS: notify.LogSMS{Logger: logger, Production: cfg.IsProduction()},
	})
	if err != nil {
		return err
	}
	pay, err := payments.NewService(payments.Config{DB: pool, Logger: logger, Provider: provider, Tickets: tickets})
	if err != nil {
		return err
	}

	jobs := jobs(cfg, pool, holds, pay, logger)
	logger.Info("worker started", "jobs", len(jobs), "payment_provider", cfg.Payments.Provider)
	worker.Run(ctx, logger, jobs...)
	// Ticket SMS started by the last reconciliation run are allowed to finish.
	pay.Wait()
	logger.Info("worker stopped")
	return nil
}

const (
	workerStatementTimeout = 15 * time.Second
	workerIdleInTxTimeout  = 15 * time.Second
)

// jobs lists the background work. Each owner adds its job here.
func jobs(cfg config.Worker, db store.DBTX, holds sweeper, pay reconciler, logger *slog.Logger) []worker.Job {
	return []worker.Job{
		authCleanupJob(db, logger),
		holdSweeperJob(cfg, holds, logger),
		reconcileJob(cfg, pay, logger),
	}
}

// reconciler is the part of the payment service the worker runs.
type reconciler interface {
	Reconcile(ctx context.Context) (payments.ReconcileResult, error)
}

// reconcileJob settles payments whose callback never came (see payments.Service.Reconcile),
// every RECONCILE_INTERVAL.
func reconcileJob(cfg config.Worker, pay reconciler, logger *slog.Logger) worker.Job {
	return worker.Job{
		Name:     "payment-reconciliation",
		Interval: cfg.ReconcileInterval,
		// A run asks the provider about up to a batch of payments, one after another.
		Timeout: 5 * time.Minute,
		Run: func(ctx context.Context) error {
			n, err := pay.Reconcile(ctx)
			if n.Claimed > 0 {
				logger.InfoContext(ctx, "payment reconciliation", "job", "payment-reconciliation",
					"claimed", n.Claimed, "sold", n.Sold, "refunds", n.Refunds, "failed", n.Failed,
					"timed_out", n.TimedOut, "still_pending", n.Pending, "errors", n.Errors)
			}
			return err
		},
	}
}

// sweeper is the part of the hold service the worker runs.
type sweeper interface {
	Sweep(ctx context.Context) (booking.SweepResult, error)
}

// holdSweeperJob closes holds whose time has run out (see booking.Service.Sweep). Nothing
// waits for it: a lapsed hold's seats can be held by anyone the moment it lapses.
func holdSweeperJob(cfg config.Worker, holds sweeper, logger *slog.Logger) worker.Job {
	return worker.Job{
		Name:     "hold-sweeper",
		Interval: cfg.SweepInterval,
		Timeout:  time.Minute,
		Run: func(ctx context.Context) error {
			n, err := holds.Sweep(ctx)
			if err != nil {
				return err
			}
			if n.Holds > 0 {
				logger.InfoContext(ctx, "hold sweep", "job", "hold-sweeper",
					"holds_expired", n.Holds, "seats_freed", n.Seats, "bookings_expired", n.Bookings)
			}
			return nil
		},
	}
}

// authCleanupJob deletes sign-in codes and refresh tokens long past any use (see
// auth.Cleanup for what "long past" means). Hourly is plenty: it bounds table growth, nothing
// reads the rows it removes.
func authCleanupJob(db store.DBTX, logger *slog.Logger) worker.Job {
	return worker.Job{
		Name:     "auth-cleanup",
		Interval: time.Hour,
		Timeout:  5 * time.Minute,
		Run: func(ctx context.Context) error {
			n, err := auth.Cleanup(ctx, db, time.Now())
			if err != nil {
				return err
			}
			if n.OTPCodes > 0 || n.RefreshTokens > 0 {
				logger.InfoContext(ctx, "auth cleanup", "job", "auth-cleanup", "otp_codes_deleted", n.OTPCodes, "refresh_tokens_deleted", n.RefreshTokens)
			}
			return nil
		},
	}
}
