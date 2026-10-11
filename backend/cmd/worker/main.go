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
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
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
	logger := config.NewLogger(cfg.Base, "worker")
	slog.SetDefault(logger)

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
	jobs := jobs(cfg, pool, holds, logger)
	logger.Info("worker started", "jobs", len(jobs))
	worker.Run(ctx, logger, jobs...)
	logger.Info("worker stopped")
	return nil
}

const (
	workerStatementTimeout = 15 * time.Second
	workerIdleInTxTimeout  = 15 * time.Second
)

// jobs lists the background work. Each owner adds its job here.
func jobs(cfg config.Worker, db store.DBTX, holds sweeper, logger *slog.Logger) []worker.Job {
	return []worker.Job{
		authCleanupJob(db, logger),
		holdSweeperJob(cfg, holds, logger),
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
