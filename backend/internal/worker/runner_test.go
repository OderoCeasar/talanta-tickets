package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunKeepsGoingAfterFailuresAndStopsOnCancel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())

	var failing, panicking atomic.Int32
	reached := make(chan struct{})
	jobs := []Job{
		{Name: "failing", Interval: time.Millisecond, Run: func(context.Context) error {
			if failing.Add(1) == 3 {
				close(reached)
			}
			return errors.New("boom")
		}},
		{Name: "panicking", Interval: time.Millisecond, Run: func(context.Context) error {
			panicking.Add(1)
			panic("boom")
		}},
	}

	done := make(chan struct{})
	go func() {
		Run(ctx, logger, jobs...)
		close(done)
	}()

	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("a failing job was not run again")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if panicking.Load() == 0 {
		t.Fatal("the panicking job never ran")
	}
}

func TestRunOnceAppliesTimeout(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	var sawDeadline bool
	runOnce(context.Background(), logger, Job{
		Name:    "slow",
		Timeout: 10 * time.Millisecond,
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			_, sawDeadline = ctx.Deadline()
			return ctx.Err()
		},
	})
	if !sawDeadline {
		t.Fatal("job context had no deadline")
	}
}

func TestRunWithNoJobsBlocksUntilCancel(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		Run(ctx, logger)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("Run returned before cancel")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
