//go:build integration

package testdb

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tables(t *testing.T, db *DB) []string {
	t.Helper()
	rows, err := db.Pool.Query(context.Background(), `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name <> 'schema_migrations'
		ORDER BY table_name`)
	require.NoError(t, err)
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		names = append(names, n)
	}
	require.NoError(t, rows.Err())
	return names
}

// Every migration must have a down file that really undoes it: up, all the way down, and up
// again has to work on an empty Postgres 16.
func TestMigrationsUpDownUp(t *testing.T) {
	db, err := Start(context.Background())
	require.NoError(t, err)
	t.Cleanup(db.Close)

	ups, err := filepath.Glob(filepath.Join(MigrationsDir(), "*.up.sql"))
	require.NoError(t, err)
	downs, err := filepath.Glob(filepath.Join(MigrationsDir(), "*.down.sql"))
	require.NoError(t, err)
	require.NotEmpty(t, ups)
	require.Len(t, downs, len(ups), "every migration needs a down file")
	for _, f := range downs {
		info, err := os.Stat(f)
		require.NoError(t, err)
		assert.NotZero(t, info.Size(), "%s is empty", filepath.Base(f))
	}

	require.NoError(t, db.MigrateUp())
	v, err := db.MigrationVersion()
	require.NoError(t, err)
	assert.EqualValues(t, len(ups), v)
	first := tables(t, db)
	assert.Contains(t, first, "event_seats")
	assert.Contains(t, first, "invitations")

	require.NoError(t, db.MigrateDown())
	assert.Empty(t, tables(t, db), "down must remove every table")
	var leftovers int
	require.NoError(t, db.Pool.QueryRow(context.Background(),
		`SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		 WHERE n.nspname = 'public' AND p.proname = 'set_updated_at'`).Scan(&leftovers))
	assert.Zero(t, leftovers, "down must remove the trigger function")

	require.NoError(t, db.MigrateUp())
	assert.Equal(t, first, tables(t, db), "the second up must build the same tables")
}
