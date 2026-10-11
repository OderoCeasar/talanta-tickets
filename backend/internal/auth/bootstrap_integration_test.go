//go:build integration

package auth_test

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/testdb"
)

const (
	bootSecret   = "bootstrap-test-secret-at-least-32-bytes"
	bootEmail    = "owner@talanta.test"
	bootPassword = "the-owner's-first-password"
)

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newService(t *testing.T, db *testdb.DB, logs *lockedBuffer) *auth.Service {
	t.Helper()
	// Production settings: this is how the owner seeds the first admin there.
	svc, err := auth.NewService(auth.Config{
		DB:           db.Pool,
		Logger:       slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Secret:       []byte(bootSecret),
		DevShortcuts: false,
		AdminBaseURL: "https://admin.talanta.test",
	})
	require.NoError(t, err)
	return svc
}

func countAdmins(t *testing.T, db *testdb.DB) int {
	t.Helper()
	n, err := store.New(db.Pool).CountAdminUsers(context.Background())
	require.NoError(t, err)
	return int(n)
}

// Created once, and not again — not with the same settings, not with different ones.
func TestBootstrapAdmin_CreatedOnceAndNotAgain(t *testing.T) {
	db := testdb.New(t) // its own database: the test needs "no admin exists" to be true
	ctx := context.Background()
	logs := &lockedBuffer{}
	svc := newService(t, db, logs)
	require.Equal(t, 0, countAdmins(t, db))

	created, err := svc.BootstrapAdmin(ctx, " Owner@Talanta.Test ", bootPassword)
	require.NoError(t, err)
	assert.True(t, created)
	require.Equal(t, 1, countAdmins(t, db))

	admin, err := store.New(db.Pool).GetUserByEmail(ctx, bootEmail)
	require.NoError(t, err)
	assert.Equal(t, "ADMIN", admin.Role)
	assert.Equal(t, bootEmail, *admin.Email, "stored lower-cased")
	assert.False(t, admin.TotpEnabled)
	require.NotNil(t, admin.PasswordHash)
	assert.Contains(t, *admin.PasswordHash, "$argon2id$")
	ok, err := auth.VerifyPassword(bootPassword, *admin.PasswordHash)
	require.NoError(t, err)
	assert.True(t, ok)

	// It says so in the log, with the email and without the password.
	assert.Contains(t, logs.String(), "first admin created")
	assert.Contains(t, logs.String(), bootEmail)
	assert.NotContains(t, logs.String(), bootPassword)
	assert.NotContains(t, logs.String(), "argon2")

	// The created admin can sign in.
	res, err := svc.Login(ctx, bootEmail, bootPassword)
	require.NoError(t, err)
	require.NotNil(t, res.Session)
	assert.Equal(t, admin.ID, res.Session.User.ID)

	// Every later start: nothing happens.
	created, err = svc.BootstrapAdmin(ctx, bootEmail, bootPassword)
	require.NoError(t, err)
	assert.False(t, created)

	// Changed settings do not add a second admin or change the first one's password.
	created, err = svc.BootstrapAdmin(ctx, "someone.else@talanta.test", "a-different-password")
	require.NoError(t, err)
	assert.False(t, created)
	created, err = svc.BootstrapAdmin(ctx, bootEmail, "a-different-password")
	require.NoError(t, err)
	assert.False(t, created)

	require.Equal(t, 1, countAdmins(t, db))
	after, err := store.New(db.Pool).GetUserByEmail(ctx, bootEmail)
	require.NoError(t, err)
	assert.Equal(t, *admin.PasswordHash, *after.PasswordHash, "the password was not overwritten")
	var users int
	require.NoError(t, db.Pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&users))
	assert.Equal(t, 1, users)
}

// Two API instances starting at the same moment against an empty database.
func TestBootstrapAdmin_ConcurrentStartsCreateOneAdmin(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()

	const instances = 8
	services := make([]*auth.Service, instances)
	for i := range services {
		services[i] = newService(t, db, &lockedBuffer{})
	}

	results := make([]bool, instances)
	errs := make([]error, instances)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range services {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = services[i].BootstrapAdmin(ctx, bootEmail, bootPassword)
		}(i)
	}
	close(start)
	wg.Wait()

	created := 0
	for i := range services {
		require.NoError(t, errs[i], "instance %d", i)
		if results[i] {
			created++
		}
	}
	assert.Equal(t, 1, created, "exactly one instance creates the admin")
	assert.Equal(t, 1, countAdmins(t, db))
}

func TestBootstrapAdmin_Refusals(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	svc := newService(t, db, &lockedBuffer{})

	_, err := svc.BootstrapAdmin(ctx, bootEmail, "short")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "short", "the password is not echoed")
	_, err = svc.BootstrapAdmin(ctx, "  ", bootPassword)
	require.Error(t, err)
	assert.Equal(t, 0, countAdmins(t, db))

	// The email belongs to an organiser: refuse loudly rather than promote or duplicate.
	hash, err := auth.HashPassword("organiser-password")
	require.NoError(t, err)
	_, err = store.New(db.Pool).CreateStaffUser(ctx, store.CreateStaffUserParams{Email: bootEmail, Role: "ORGANISER", PasswordHash: hash})
	require.NoError(t, err)

	created, err := svc.BootstrapAdmin(ctx, bootEmail, bootPassword)
	require.ErrorContains(t, err, "already belongs to an account")
	assert.False(t, created)
	assert.Equal(t, 0, countAdmins(t, db))

	// With a free address it then works.
	created, err = svc.BootstrapAdmin(ctx, "owner2@talanta.test", bootPassword)
	require.NoError(t, err)
	assert.True(t, created)
}
