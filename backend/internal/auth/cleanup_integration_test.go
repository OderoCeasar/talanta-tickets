//go:build integration

package auth_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/testdb"
)

// Cleanup removes sign-in codes older than a day and refresh tokens that expired more than a
// week ago, and nothing else.
func TestCleanup_DeletesOnlyRowsPastRetention(t *testing.T) {
	db := testdb.New(t)
	ctx := context.Background()
	now := time.Now()

	var userID int64
	require.NoError(t, db.Pool.QueryRow(ctx,
		`INSERT INTO users (phone_e164, role) VALUES ('+254712000001', 'FAN') RETURNING id`).Scan(&userID))

	otp := func(phone string, age time.Duration) {
		t.Helper()
		created := now.Add(-age)
		_, err := db.Pool.Exec(ctx,
			`INSERT INTO otp_codes (phone_e164, code_hash, expires_at, created_at) VALUES ($1, '\x00', $2, $3)`,
			phone, created.Add(5*time.Minute), created)
		require.NoError(t, err)
	}
	otp("+254712000010", 25*time.Hour)             // gone
	otp("+254712000011", 24*time.Hour+time.Minute) // gone
	otp("+254712000012", 23*time.Hour+59*time.Minute)
	otp("+254712000013", time.Minute)

	token := func(hash string, expiredFor time.Duration, used bool) {
		t.Helper()
		expires := now.Add(-expiredFor)
		_, err := db.Pool.Exec(ctx,
			`INSERT INTO refresh_tokens (user_id, family_id, token_hash, expires_at, used_at)
			 VALUES ($1, gen_random_uuid(), $2, $3, CASE WHEN $4 THEN $3::timestamptz - interval '1 day' END)`,
			userID, []byte(hash), expires, used)
		require.NoError(t, err)
	}
	token("expired-8-days", 8*24*time.Hour, false)     // gone
	token("expired-8-days-used", 8*24*time.Hour, true) // gone
	token("expired-6-days", 6*24*time.Hour, true)      // kept: still inside the week
	token("live", -29*24*time.Hour, false)             // kept: expires in the future
	token("expired-just-now", time.Minute, false)      // kept

	got, err := auth.Cleanup(ctx, db.Pool, now)
	require.NoError(t, err)
	assert.Equal(t, auth.Cleaned{OTPCodes: 2, RefreshTokens: 2}, got)

	var phones []string
	rows, err := db.Pool.Query(ctx, `SELECT phone_e164 FROM otp_codes ORDER BY phone_e164`)
	require.NoError(t, err)
	for rows.Next() {
		var p string
		require.NoError(t, rows.Scan(&p))
		phones = append(phones, p)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"+254712000012", "+254712000013"}, phones)

	var hashes []string
	rows, err = db.Pool.Query(ctx, `SELECT convert_from(token_hash, 'UTF8') FROM refresh_tokens ORDER BY 1`)
	require.NoError(t, err)
	for rows.Next() {
		var h string
		require.NoError(t, rows.Scan(&h))
		hashes = append(hashes, h)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"expired-6-days", "expired-just-now", "live"}, hashes)

	// A second run finds nothing to do.
	got, err = auth.Cleanup(ctx, db.Pool, now)
	require.NoError(t, err)
	assert.Equal(t, auth.Cleaned{}, got)

	assert.Equal(t, 24*time.Hour, auth.OTPCodeRetention)
	assert.Equal(t, 7*24*time.Hour, auth.RefreshTokenRetention)
}
