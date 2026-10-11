// Package store owns the Postgres connection pool. sqlc-generated queries and the
// hand-written seat-locking queries (docs/ARCHITECTURE.md §6) will live here.
package store

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Options are session limits applied to every connection of a pool. Zero means "no limit".
type Options struct {
	// StatementTimeout cancels any single statement that runs longer. It stops one bad
	// query from holding a connection (and its locks) for ever.
	StatementTimeout time.Duration
	// IdleInTxTimeout ends a session that sits inside an open transaction doing nothing, so
	// a handler that hangs mid-transaction cannot keep row locks on seats.
	IdleInTxTimeout time.Duration
}

// Open connects to Postgres with no session limits and verifies the connection. One-shot
// tools (the seeder) use it; long-running services use OpenWithOptions.
func Open(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return OpenWithOptions(ctx, databaseURL, Options{})
}

// OpenWithOptions is Open with session limits set on every pooled connection.
//
// A statement that legitimately needs longer than StatementTimeout (publishing an event)
// raises its own limit for one transaction with SetLocalStatementTimeout.
func OpenWithOptions(ctx context.Context, databaseURL string, opts Options) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	// Sent in the startup packet, so they hold from a connection's first statement and
	// survive DISCARD ALL; Postgres reads a bare number as milliseconds.
	if opts.StatementTimeout > 0 {
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(opts.StatementTimeout.Milliseconds(), 10)
	}
	if opts.IdleInTxTimeout > 0 {
		cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] = strconv.FormatInt(opts.IdleInTxTimeout.Milliseconds(), 10)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// SetLocalStatementTimeout changes the statement timeout for the rest of the current
// transaction only; it reverts at commit or rollback. Call it on a transaction (through
// InTx) before the slow statement. Zero removes the limit for that transaction.
func SetLocalStatementTimeout(ctx context.Context, tx DBTX, d time.Duration) error {
	// set_config(…, true) is SET LOCAL with a bound value: SET itself takes no parameters.
	_, err := tx.Exec(ctx, "SELECT set_config('statement_timeout', $1, true)", strconv.FormatInt(d.Milliseconds(), 10))
	if err != nil {
		return fmt.Errorf("set local statement_timeout: %w", err)
	}
	return nil
}
