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

	pool, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	jobs := jobs(cfg, pool, logger)
	logger.Info("worker started", "jobs", len(jobs))
	worker.Run(ctx, logger, jobs...)
	logger.Info("worker stopped")
	return nil
}

// jobs lists the background work. Each owner adds its job here.
func jobs(_ config.Worker, db store.DBTX, logger *slog.Logger) []worker.Job {
	return []worker.Job{
		authCleanupJob(db, logger),
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
