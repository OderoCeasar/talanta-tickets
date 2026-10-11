//go:build integration

package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/testdb"
)

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func TestOpenWithOptions_SessionLimits(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()

	pool, err := store.OpenWithOptions(ctx, db.URL, store.Options{
		StatementTimeout: 300 * time.Millisecond,
		IdleInTxTimeout:  400 * time.Millisecond,
	})
	require.NoError(t, err)
	defer pool.Close()

	t.Run("the limits are set on the session", func(t *testing.T) {
		var statement, idle string
		require.NoError(t, pool.QueryRow(ctx, `SELECT current_setting('statement_timeout'), current_setting('idle_in_transaction_session_timeout')`).Scan(&statement, &idle))
		assert.Equal(t, "300ms", statement)
		assert.Equal(t, "400ms", idle)
	})

	t.Run("a statement over the limit is cancelled", func(t *testing.T) {
		start := time.Now()
		_, err := pool.Exec(ctx, `SELECT pg_sleep(5)`)
		require.Error(t, err)
		assert.Equal(t, "57014", pgCode(err), "query_canceled: %v", err)
		assert.Less(t, time.Since(start), 3*time.Second)

		// The pool is still usable afterwards.
		_, err = pool.Exec(ctx, `SELECT 1`)
		require.NoError(t, err)
	})

	t.Run("one transaction can raise its own limit, and only its own", func(t *testing.T) {
		err := store.InTxRaw(ctx, pool, func(tx pgx.Tx) error {
			if err := store.SetLocalStatementTimeout(ctx, tx, 5*time.Second); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `SELECT pg_sleep(0.8)`)
			return err
		})
		require.NoError(t, err, "the long statement ran under the raised limit")

		// After the transaction the pool's limit is back, on every connection.
		for i := 0; i < 6; i++ {
			var statement string
			require.NoError(t, pool.QueryRow(ctx, `SELECT current_setting('statement_timeout')`).Scan(&statement))
			require.Equal(t, "300ms", statement)
		}
		_, err = pool.Exec(ctx, `SELECT pg_sleep(1)`)
		assert.Equal(t, "57014", pgCode(err))
	})

	t.Run("a transaction left idle is ended by the server", func(t *testing.T) {
		tx, err := pool.Begin(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback(ctx) }()
		_, err = tx.Exec(ctx, `SELECT 1`)
		require.NoError(t, err)

		time.Sleep(900 * time.Millisecond)
		_, err = tx.Exec(ctx, `SELECT 1`)
		require.Error(t, err, "the session should have been terminated while idle in a transaction")
	})

	t.Run("plain Open sets no limits", func(t *testing.T) {
		plain, err := store.Open(ctx, db.URL)
		require.NoError(t, err)
		defer plain.Close()
		var statement, idle string
		require.NoError(t, plain.QueryRow(ctx, `SELECT current_setting('statement_timeout'), current_setting('idle_in_transaction_session_timeout')`).Scan(&statement, &idle))
		assert.Equal(t, "0", statement)
		assert.Equal(t, "0", idle)
	})
}

func TestInTx_CommitsRollsBackAndPassesErrorsThrough(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	q := store.New(db.Pool)
	phone := func(s string) *string { return &s }

	require.NoError(t, store.InTx(ctx, db.Pool, func(q *store.Queries) error {
		_, err := q.UpsertFanByPhone(ctx, phone("+254700000101"))
		return err
	}))
	_, err := q.GetUserByPhone(ctx, phone("+254700000101"))
	require.NoError(t, err, "committed")

	sentinel := errors.New("stop")
	err = store.InTx(ctx, db.Pool, func(q *store.Queries) error {
		if _, err := q.UpsertFanByPhone(ctx, phone("+254700000102")); err != nil {
			return err
		}
		return sentinel
	})
	require.ErrorIs(t, err, sentinel, "the caller's error comes back unchanged")
	_, err = q.GetUserByPhone(ctx, phone("+254700000102"))
	require.ErrorIs(t, err, store.ErrNoRows, "rolled back")

	// A panic rolls back too.
	assert.Panics(t, func() {
		_ = store.InTx(ctx, db.Pool, func(q *store.Queries) error {
			_, _ = q.UpsertFanByPhone(ctx, phone("+254700000103"))
			panic("boom")
		})
	})
	_, err = q.GetUserByPhone(ctx, phone("+254700000103"))
	require.ErrorIs(t, err, store.ErrNoRows)

	// Unique violations are recognised by constraint.
	email := "dup@talanta.test"
	_, err = q.CreateStaffUser(ctx, store.CreateStaffUserParams{Email: email, Role: "ADMIN", PasswordHash: "x"})
	require.NoError(t, err)
	_, err = q.CreateStaffUser(ctx, store.CreateStaffUserParams{Email: "DUP@talanta.test", Role: "ADMIN", PasswordHash: "x"})
	require.Error(t, err)
	assert.True(t, store.IsUniqueViolation(err, store.ConstraintUsersEmail))
	assert.True(t, store.IsUniqueViolation(err, ""))
	assert.False(t, store.IsUniqueViolation(err, store.ConstraintInvitationLiveEmail))
	assert.False(t, store.IsUniqueViolation(sentinel, ""))
}
