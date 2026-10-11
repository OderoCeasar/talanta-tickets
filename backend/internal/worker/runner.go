// Package worker runs background jobs (hold sweeper, payment reconciliation) on a timer.
package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Job is one piece of repeated background work.
type Job struct {
	Name     string
	Interval time.Duration
	// Timeout bounds a single run so a stuck query cannot hold the next run back forever.
	Timeout time.Duration
	Run     func(ctx context.Context) error
}

// Run starts every job and blocks until ctx is cancelled and all in-flight runs have returned.
func Run(ctx context.Context, logger *slog.Logger, jobs ...Job) {
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loop(ctx, logger.With("job", job.Name), job)
		}()
	}
	wg.Wait()
	// With no jobs registered the process must still stay up until it is told to stop,
	// otherwise the container exits and the orchestrator restarts it in a loop.
	<-ctx.Done()
}

func loop(ctx context.Context, logger *slog.Logger, job Job) {
	ticker := time.NewTicker(job.Interval)
	defer ticker.Stop()

	for {
		runOnce(ctx, logger, job)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// runOnce never lets one failed or panicking run stop the job: the next tick tries again.
func runOnce(ctx context.Context, logger *slog.Logger, job Job) {
	runCtx := ctx
	if job.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, job.Timeout)
		defer cancel()
	}

	err := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		return job.Run(runCtx)
	}()
	if err != nil && ctx.Err() == nil {
		logger.Error("job run failed", "err", err)
	}
}
