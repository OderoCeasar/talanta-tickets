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
	"net/url"
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
		Production: !isLocalDevelopment(os.Getenv("APP_ENV"), databaseURL),
		Now:        time.Now(),
	})
	if err != nil {
		return err
	}
	res.Print(os.Stdout)
	return nil
}

// isLocalDevelopment decides whether demo events and demo logins may be created. Both
// conditions must hold, so neither a forgotten APP_ENV nor a production DATABASE_URL pasted
// into a developer's shell can put public passwords or a made-up match on a real database.
func isLocalDevelopment(appEnv, databaseURL string) bool {
	if appEnv != "development" && appEnv != "test" {
		return false
	}
	u, err := url.Parse(databaseURL)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	// "db" is the Postgres service name in the local docker-compose.yml.
	case "localhost", "127.0.0.1", "::1", "db":
		return true
	}
	return false
}
