// Command seed fills a migrated database with the Talanta layout (about 60,000 seats), a
// default venue template, two demo events and one user per role. It can be run any number of
// times: what is already there is left alone.
//
//	DATABASE_URL=postgres://… go run ./cmd/seed
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run() error {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	pool, err := store.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	res, err := Seed(ctx, pool, Options{
		Production: os.Getenv("APP_ENV") == "production",
		Now:        time.Now(),
	})
	if err != nil {
		return err
	}
	res.Print(os.Stdout)
	return nil
}
