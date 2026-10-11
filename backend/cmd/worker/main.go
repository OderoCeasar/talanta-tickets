// Command worker runs the background jobs: the hold expiry sweeper and payment reconciliation.
// It is a separate process so a slow job never competes with request handling.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

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

	jobs := jobs(cfg)
	logger.Info("worker started", "jobs", len(jobs))
	worker.Run(ctx, logger, jobs...)
	logger.Info("worker stopped")
	return nil
}

// jobs lists the background work. Each owner adds its job here.
func jobs(_ config.Worker) []worker.Job {
	return nil
}
