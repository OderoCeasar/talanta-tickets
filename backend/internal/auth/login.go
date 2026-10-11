package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// LoginResult is one of two things: a session (the user has no TOTP), or a challenge for
// the second step. Exactly one field is set.
type LoginResult struct {
	Session *Session
	MFA     *MFAChallenge
}

// MFAChallenge is the answer to a good password when TOTP is on.
type MFAChallenge struct {
	Token     string
	ExpiresIn time.Duration
}

// Login is the password step of a staff sign-in.
func (s *Service) Login(ctx context.Context, rawEmail, password string) (LoginResult, error) {
	email := domain.NormaliseEmail(rawEmail)

	limitKey := "login:email:" + email
	if d := s.limiter.Take(limitKey, s.policy.LoginPerEmail); !d.Allowed {
		return LoginResult{}, domain.RateLimited(ratelimit.RetryAfterSeconds(d.RetryAfter))
	}

	user, err := s.q.GetUserByEmail(ctx, email)
	found := err == nil
	if err != nil && !errors.Is(err, store.ErrNoRows) {
		return LoginResult{}, fmt.Errorf("login: find user: %w", err)
	}

	// Always verify one argon2 hash, the user's or a dummy, so an unknown email takes as
	// long as a wrong password and the two cannot be told apart by timing.
	hash := s.dummyHash
	canSignIn := found && user.PasswordHash != nil && domain.Role(user.Role).IsStaff()
	if canSignIn {
		hash = *user.PasswordHash
	}
	ok, verr := VerifyPassword(password, hash)
	if verr != nil && canSignIn {
		// The stored hash is unreadable. That is our fault, not a wrong password.
		return LoginResult{}, fmt.Errorf("login: user %d: %w", user.ID, verr)
	}
	if !canSignIn || !ok {
		return LoginResult{}, domain.ErrInvalidCredentials
	}

	// A good password clears the count, so a user who mistyped a few times is not locked out
	// of their next sign-in.
	s.limiter.Reset(limitKey)

	if user.TotpEnabled {
		token, _, err := s.tokens.IssueMFA(user.ID)
		if err != nil {
			return LoginResult{}, err
		}
		return LoginResult{MFA: &MFAChallenge{Token: token, ExpiresIn: MFATokenTTL}}, nil
	}

	session, err := s.startSession(ctx, s.q, user)
	if err != nil {
		return LoginResult{}, fmt.Errorf("login: %w", err)
	}
	return LoginResult{Session: &session}, nil
}

// VerifyTOTPLogin is the second step of a staff sign-in.
func (s *Service) VerifyTOTPLogin(ctx context.Context, mfaToken, code string) (Session, error) {
	userID, tokenID, expires, err := s.tokens.VerifyMFA(mfaToken)
	if err != nil {
		return Session{}, err
	}
	now := s.now()
	if !s.mfa.attempt(tokenID, expires, now, s.policy.MFAMaxAttempts) {
		return Session{}, domain.ErrMFATokenInvalid
	}

	user, err := s.q.GetUserByID(ctx, userID)
	if errors.Is(err, store.ErrNoRows) {
		return Session{}, domain.ErrMFATokenInvalid
	}
	if err != nil {
		return Session{}, fmt.Errorf("verify totp: find user: %w", err)
	}
	// TOTP was turned off, or the account changed, after the password step.
	if !user.TotpEnabled || user.TotpSecret == nil || !domain.Role(user.Role).IsStaff() {
		return Session{}, domain.ErrMFATokenInvalid
	}

	step, ok := VerifyTOTP(*user.TotpSecret, code, now)
	if !ok {
		return Session{}, domain.ErrTOTPInvalid
	}
	// A code that already signed this user in is refused, and the token burnt, together.
	if !s.mfa.finish(tokenID, user.ID, step) {
		s.log.WarnContext(ctx, "totp code replay refused", "user_id", user.ID)
		return Session{}, domain.ErrTOTPInvalid
	}

	session, err := s.startSession(ctx, s.q, user)
	if err != nil {
		return Session{}, fmt.Errorf("verify totp: %w", err)
	}
	return session, nil
}

// Refresh swaps a refresh token for a new pair. Each token works once. A token presented a
// second time means two parties hold it, so the whole family is revoked: the thief and the
// owner are both signed out, and the owner signs in again.
func (s *Service) Refresh(ctx context.Context, presented string) (Session, error) {
	hash := hashToken(presented)

	// Read once without a lock to learn the family, then again under the family lock.
	first, err := s.q.GetRefreshTokenByHash(ctx, hash)
	if errors.Is(err, store.ErrNoRows) {
		return Session{}, domain.ErrRefreshTokenInvalid
	}
	if err != nil {
		return Session{}, fmt.Errorf("refresh: find token: %w", err)
	}

	var session Session
	reused := false
	err = store.InTx(ctx, s.db, func(q *store.Queries) error {
		if err := q.LockRefreshTokenFamily(ctx, first.FamilyID); err != nil {
			return fmt.Errorf("lock family: %w", err)
		}
		row, err := q.GetRefreshTokenByHash(ctx, hash)
		if errors.Is(err, store.ErrNoRows) {
			return domain.ErrRefreshTokenInvalid
		}
		if err != nil {
			return fmt.Errorf("re-read token: %w", err)
		}

		if row.UsedAt != nil {
			// Reuse. The revocation must be committed, so this returns nil and the error
			// is raised after the transaction.
			if _, err := q.RevokeRefreshTokenFamily(ctx, row.FamilyID); err != nil {
				return fmt.Errorf("revoke family: %w", err)
			}
			reused = true
			return nil
		}
		if row.RevokedAt != nil || !row.ExpiresAt.After(s.now()) {
			return domain.ErrRefreshTokenInvalid
		}

		n, err := q.MarkRefreshTokenUsed(ctx, row.ID)
		if err != nil {
			return fmt.Errorf("mark token used: %w", err)
		}
		if n == 0 {
			return domain.ErrRefreshTokenInvalid
		}
		user, err := q.GetUserByID(ctx, row.UserID)
		if errors.Is(err, store.ErrNoRows) {
			return domain.ErrRefreshTokenInvalid
		}
		if err != nil {
			return fmt.Errorf("find user: %w", err)
		}
		session, err = s.issueSession(ctx, q, user, row.FamilyID)
		return err
	})
	if err != nil {
		if domain.CodeOf(err) != domain.CodeInternal {
			return Session{}, err
		}
		return Session{}, fmt.Errorf("refresh: %w", err)
	}
	if reused {
		s.log.WarnContext(ctx, "refresh token reuse: session revoked", "user_id", first.UserID, "family_id", uuidString(first.FamilyID))
		return Session{}, domain.ErrRefreshTokenReused
	}
	return session, nil
}

// Logout revokes the token's whole family. An unknown token is not an error: the caller
// wanted to be signed out and is.
func (s *Service) Logout(ctx context.Context, presented string) error {
	row, err := s.q.GetRefreshTokenByHash(ctx, hashToken(presented))
	if errors.Is(err, store.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("logout: find token: %w", err)
	}
	err = store.InTx(ctx, s.db, func(q *store.Queries) error {
		// The same lock as rotation, so a refresh in flight cannot add a token after this.
		if err := q.LockRefreshTokenFamily(ctx, row.FamilyID); err != nil {
			return err
		}
		_, err := q.RevokeRefreshTokenFamily(ctx, row.FamilyID)
		return err
	})
	if err != nil {
		return fmt.Errorf("logout: %w", err)
	}
	return nil
}
