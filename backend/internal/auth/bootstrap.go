package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// BootstrapAdmin creates the first admin from the BOOTSTRAP_ADMIN_* settings when the
// database has no admin at all. Once any admin exists it does nothing, so the variables can
// stay set, and changing them later does not change anybody's password.
//
// It reports whether it created the account. Two API instances starting together are safe:
// the check and the insert run under one advisory lock.
func (s *Service) BootstrapAdmin(ctx context.Context, rawEmail, password string) (created bool, err error) {
	email := domain.NormaliseEmail(rawEmail)
	if email == "" {
		return false, errors.New("bootstrap admin: email is empty")
	}
	if err := checkNewPassword("BOOTSTRAP_ADMIN_PASSWORD", password); err != nil {
		return false, fmt.Errorf("bootstrap admin: password must be %d to %d characters", domain.PasswordMinLength, domain.PasswordMaxLength)
	}

	// The common case, every start after the first: no lock, no hashing.
	if n, err := s.q.CountAdminUsers(ctx); err != nil {
		return false, fmt.Errorf("bootstrap admin: count admins: %w", err)
	} else if n > 0 {
		return false, nil
	}

	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}

	err = store.InTx(ctx, s.db, func(q *store.Queries) error {
		if err := q.LockAdminBootstrap(ctx); err != nil {
			return fmt.Errorf("take lock: %w", err)
		}
		// Counted again under the lock: the other instance may have just created it.
		n, err := q.CountAdminUsers(ctx)
		if err != nil {
			return fmt.Errorf("count admins: %w", err)
		}
		if n > 0 {
			return nil
		}
		name := "Admin"
		_, err = q.CreateStaffUser(ctx, store.CreateStaffUserParams{
			Email: email, Name: &name, Role: string(domain.RoleAdmin), PasswordHash: hash,
		})
		if store.IsUniqueViolation(err, store.ConstraintUsersEmail) {
			return fmt.Errorf("%s already belongs to an account that is not an admin; use another BOOTSTRAP_ADMIN_EMAIL", email)
		}
		if err != nil {
			return fmt.Errorf("create admin: %w", err)
		}
		created = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("bootstrap admin: %w", err)
	}
	if created {
		// The email is logged; the password never is.
		s.log.InfoContext(ctx, "first admin created from BOOTSTRAP_ADMIN_EMAIL", "email", email)
	}
	return created, nil
}
