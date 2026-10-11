package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNoRows is what a :one query returns when nothing matched. Re-exported so services do
// not import pgx just to compare an error.
var ErrNoRows = pgx.ErrNoRows

// TxBeginner is the part of the pool InTx needs.
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// InTx runs fn in one transaction: commit when fn returns nil, roll back otherwise (and on
// panic). fn's error is returned unchanged so callers can still match domain errors.
func InTx(ctx context.Context, db TxBeginner, fn func(q *Queries) error) error {
	return InTxRaw(ctx, db, func(tx pgx.Tx) error { return fn(New(tx)) })
}

// InTxRaw is InTx for callers that need the transaction itself, for example to call
// SetLocalStatementTimeout or a hand-written locking query before using the queries.
func InTxRaw(ctx context.Context, db TxBeginner, fn func(tx pgx.Tx) error) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	// After a successful Commit this is a no-op that returns ErrTxClosed.
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

// Names of the unique constraints services need to tell apart.
const (
	ConstraintUsersEmail          = "users_email_lower_key"
	ConstraintInvitationLiveEmail = "invitations_live_email_key"
)

// IsUniqueViolation reports whether err is a unique-constraint failure. With a constraint
// name it must be that one; with none, any.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}
