package main

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/config"
)

// The cleanup must actually be scheduled: the queries existed for a milestone with no caller.
func TestJobsIncludeHourlyAuthCleanup(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, job := range jobs(config.Worker{}, nil, nil, nil, logger) {
		if job.Name != "auth-cleanup" {
			continue
		}
		if job.Interval != time.Hour {
			t.Errorf("auth-cleanup interval = %s, want 1h", job.Interval)
		}
		if job.Timeout <= 0 || job.Run == nil {
			t.Errorf("auth-cleanup needs a timeout and a Run, got %+v", job)
		}
		return
	}
	t.Fatal("no auth-cleanup job is registered")
}

// Correction 7: the hold sweeper runs in the worker, at SWEEP_INTERVAL.
func TestJobsIncludeHoldSweeperAtTheConfiguredInterval(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, job := range jobs(config.Worker{SweepInterval: 30 * time.Second}, nil, nil, nil, logger) {
		if job.Name != "hold-sweeper" {
			continue
		}
		if job.Interval != 30*time.Second {
			t.Errorf("hold-sweeper interval = %s, want the configured 30s", job.Interval)
		}
		if job.Timeout <= 0 || job.Run == nil {
			t.Errorf("hold-sweeper needs a timeout and a Run, got %+v", job)
		}
		return
	}
	t.Fatal("no hold-sweeper job is registered")
}

// §8.2: the reconciliation job runs in the worker, at RECONCILE_INTERVAL.
func TestJobsIncludePaymentReconciliationAtTheConfiguredInterval(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, job := range jobs(config.Worker{ReconcileInterval: 30 * time.Second}, nil, nil, nil, logger) {
		if job.Name != "payment-reconciliation" {
			continue
		}
		if job.Interval != 30*time.Second {
			t.Errorf("payment-reconciliation interval = %s, want the configured 30s", job.Interval)
		}
		if job.Timeout <= 0 || job.Run == nil {
			t.Errorf("payment-reconciliation needs a timeout and a Run, got %+v", job)
		}
		return
	}
	t.Fatal("no payment-reconciliation job is registered")
}
