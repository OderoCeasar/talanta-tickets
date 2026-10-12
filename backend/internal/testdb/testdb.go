// Package testdb starts a real Postgres 16 in a container for integration tests and applies
// the project's migrations to it. Tests that use it carry the build tag `integration`.
//
//	func TestSomething(t *testing.T) {
//	    db := testdb.New(t)          // migrated, empty, removed when the test ends
//	    q := store.New(db.Pool)
//	    …
//	}
//
// Each call starts its own container, so tests never share data. A package with many tests
// can start one in TestMain with Start and give each test its own rows instead.
package testdb

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5:// driver
	_ "github.com/golang-migrate/migrate/v4/source/file"     // registers the file:// source
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Image is the same Postgres the compose file and production run.
const Image = "postgres:16-alpine"

// DB is one throwaway database.
type DB struct {
	Pool *pgxpool.Pool
	// URL is a postgres:// connection string, as the API and the seeder read from DATABASE_URL.
	URL string

	container *postgres.PostgresContainer
}

// New starts a database, migrates it to the latest version and removes it when the test ends.
func New(t testing.TB) *DB {
	t.Helper()
	db, err := Start(context.Background())
	if err != nil {
		t.Fatalf("testdb: %v", err)
	}
	t.Cleanup(db.Close)
	if err := db.MigrateUp(); err != nil {
		t.Fatalf("testdb: %v", err)
	}
	return db
}

// Start starts a database with an empty schema. The caller must Close it.
func Start(ctx context.Context) (*DB, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	container, err := postgres.Run(ctx, Image,
		postgres.WithDatabase("talanta"),
		postgres.WithUsername("talanta"),
		postgres.WithPassword("talanta"),
		// The same two checks as postgres.BasicWaitStrategies, with a longer deadline. The
		// default is 60 s, and when every test package starts its own Postgres at once on a
		// busy machine the first-run initdb can take longer than that: the whole suite then
		// fails on container start-up without a single assertion having run.
		testcontainers.WithWaitStrategy(wait.ForAll(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			wait.ForListeningPort("5432/tcp"),
		).WithDeadline(150*time.Second)),
	)
	if err != nil {
		if container != nil {
			_ = testcontainers.TerminateContainer(container)
		}
		return nil, fmt.Errorf("start %s: %w", Image, err)
	}
	db := &DB{container: container}

	db.URL, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("connection string: %w", err)
	}
	db.Pool, err = pgxpool.New(ctx, db.URL)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open pool: %w", err)
	}
	if err := db.Pool.Ping(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return db, nil
}

// Close closes the pool and removes the container.
func (db *DB) Close() {
	if db.Pool != nil {
		db.Pool.Close()
	}
	if db.container != nil {
		_ = testcontainers.TerminateContainer(db.container)
	}
}

// MigrateUp applies every migration that has not been applied.
func (db *DB) MigrateUp() error {
	return db.migrate(func(m *migrate.Migrate) error { return m.Up() })
}

// MigrateDown rolls every migration back, leaving an empty schema.
func (db *DB) MigrateDown() error {
	return db.migrate(func(m *migrate.Migrate) error { return m.Down() })
}

// MigrationVersion is the version the database is at; 0 when nothing is applied.
func (db *DB) MigrationVersion() (uint, error) {
	var version uint
	err := db.migrate(func(m *migrate.Migrate) error {
		v, dirty, err := m.Version()
		if errors.Is(err, migrate.ErrNilVersion) {
			return nil
		}
		if err != nil {
			return err
		}
		if dirty {
			return fmt.Errorf("version %d is dirty", v)
		}
		version = v
		return nil
	})
	return version, err
}

func (db *DB) migrate(fn func(*migrate.Migrate) error) error {
	// golang-migrate picks its driver by URL scheme; pgx v5 is registered as pgx5.
	url := "pgx5://" + strings.TrimPrefix(strings.TrimPrefix(db.URL, "postgres://"), "postgresql://")
	m, err := migrate.New("file://"+MigrationsDir(), url)
	if err != nil {
		return fmt.Errorf("open migrations: %w", err)
	}
	defer m.Close()
	if err := fn(m); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// MigrationsDir is the absolute path of backend/migrations, found from this file's own
// location so it works whatever directory `go test` runs a package in.
func MigrationsDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("testdb: cannot find its own source file")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "migrations")
}
