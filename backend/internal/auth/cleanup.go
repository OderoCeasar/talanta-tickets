package auth

import (
	"context"
	"fmt"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// How long dead sign-in rows are kept before the worker deletes them. Both tables only ever
// grow otherwise: one row per code requested and one per token rotation.
const (
	// OTPCodeRetention is far past a code's five-minute life and the one-hour per-phone
	// window; a day leaves room to look into "I never got my code".
	OTPCodeRetention = 24 * time.Hour
	// RefreshTokenRetention is counted from expiry, not creation. An expired token cannot
	// sign anyone in, so reuse detection no longer needs its row; the week is for tracing a
	// reported session.
	RefreshTokenRetention = 7 * 24 * time.Hour
)

// Cleaned is what one cleanup run removed.
type Cleaned struct {
	OTPCodes      int64
	RefreshTokens int64
}

// Cleanup deletes sign-in codes created more than OTPCodeRetention before now and refresh
// tokens that expired more than RefreshTokenRetention before now. It needs no Service (the
// worker has no JWT secret) and is safe to run from several processes at once: each delete is
// one statement and deleting nothing is not an error.
func Cleanup(ctx context.Context, db store.DBTX, now time.Time) (Cleaned, error) {
	q := store.New(db)
	var out Cleaned
	var err error
	if out.OTPCodes, err = q.DeleteOTPCodesCreatedBefore(ctx, now.Add(-OTPCodeRetention)); err != nil {
		return out, fmt.Errorf("auth cleanup: delete old sign-in codes: %w", err)
	}
	if out.RefreshTokens, err = q.DeleteRefreshTokensExpiredBefore(ctx, now.Add(-RefreshTokenRetention)); err != nil {
		return out, fmt.Errorf("auth cleanup: delete expired refresh tokens: %w", err)
	}
	return out, nil
}
