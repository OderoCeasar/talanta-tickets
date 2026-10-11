package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// totpIssuer is the name an authenticator app shows next to the code.
const totpIssuer = "Talanta Tickets"

// loadUser reads the caller's own row. A token for a user that no longer exists is not a
// valid sign-in.
func (s *Service) loadUser(ctx context.Context, userID int64) (store.User, error) {
	u, err := s.q.GetUserByID(ctx, userID)
	if errors.Is(err, store.ErrNoRows) {
		return store.User{}, domain.ErrUnauthenticated
	}
	if err != nil {
		return store.User{}, fmt.Errorf("find user %d: %w", userID, err)
	}
	return u, nil
}

// loadStaff is loadUser for the routes only an account with a password may use.
func (s *Service) loadStaff(ctx context.Context, userID int64) (store.User, error) {
	u, err := s.loadUser(ctx, userID)
	if err != nil {
		return store.User{}, err
	}
	if !domain.Role(u.Role).IsStaff() || u.PasswordHash == nil || u.Email == nil {
		return store.User{}, domain.ErrForbidden
	}
	return u, nil
}

// Me returns the caller.
func (s *Service) Me(ctx context.Context, userID int64) (domain.User, error) {
	u, err := s.loadUser(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	return toUser(u), nil
}

// UpdateName changes the caller's display name.
func (s *Service) UpdateName(ctx context.Context, userID int64, rawName string) (domain.User, error) {
	name, err := cleanName(rawName)
	if err != nil {
		return domain.User{}, err
	}
	u, err := s.q.UpdateUserName(ctx, store.UpdateUserNameParams{ID: userID, Name: &name})
	if errors.Is(err, store.ErrNoRows) {
		return domain.User{}, domain.ErrUnauthenticated
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("update name of user %d: %w", userID, err)
	}
	return toUser(u), nil
}

func cleanName(raw string) (string, error) {
	name := domain.NormaliseName(raw)
	switch n := len([]rune(name)); {
	case n == 0:
		return "", domain.InvalidField("name", "required", "Enter a name.")
	case n > domain.NameMaxLength:
		return "", domain.InvalidField("name", "max", fmt.Sprintf("Use at most %d characters.", domain.NameMaxLength))
	}
	return name, nil
}

func checkNewPassword(field, password string) error {
	switch n := len([]rune(password)); {
	case n < domain.PasswordMinLength:
		return domain.InvalidField(field, "min", fmt.Sprintf("Use at least %d characters.", domain.PasswordMinLength))
	case n > domain.PasswordMaxLength:
		return domain.InvalidField(field, "max", fmt.Sprintf("Use at most %d characters.", domain.PasswordMaxLength))
	}
	return nil
}

// checkPassword verifies the caller's current password on a signed-in route.
func (s *Service) checkPassword(u store.User, password string) error {
	ok, err := VerifyPassword(password, *u.PasswordHash)
	if err != nil {
		return fmt.Errorf("user %d: %w", u.ID, err)
	}
	if !ok {
		return domain.ErrPasswordIncorrect
	}
	return nil
}

// ChangePassword sets a new password and signs out every other session of the user. The
// calling session (who.SessionID) stays signed in.
func (s *Service) ChangePassword(ctx context.Context, who domain.Principal, current, next string) error {
	u, err := s.loadStaff(ctx, who.UserID)
	if err != nil {
		return err
	}
	if err := checkNewPassword("new_password", next); err != nil {
		return err
	}
	if err := s.checkPassword(u, current); err != nil {
		return err
	}
	// Hashed before the transaction: argon2 is slow on purpose and must not hold a connection.
	hash, err := HashPassword(next)
	if err != nil {
		return err
	}

	err = store.InTx(ctx, s.db, func(q *store.Queries) error {
		if _, err := q.SetUserPasswordHash(ctx, store.SetUserPasswordHashParams{ID: u.ID, PasswordHash: &hash}); err != nil {
			return err
		}
		// If the token names no session there is nothing to keep: sign out everywhere.
		if keep, ok := parseUUID(who.SessionID); ok {
			_, err = q.RevokeOtherRefreshTokenFamilies(ctx, store.RevokeOtherRefreshTokenFamiliesParams{UserID: u.ID, KeepFamilyID: keep})
		} else {
			_, err = q.RevokeRefreshTokensForUser(ctx, u.ID)
		}
		return err
	})
	if err != nil {
		return fmt.Errorf("change password of user %d: %w", u.ID, err)
	}
	s.log.InfoContext(ctx, "password changed", "user_id", u.ID)
	return nil
}

// TOTPSetup is a new secret, shown to the user once.
type TOTPSetup struct {
	Secret string
	URL    string
}

// SetupTOTP stores a fresh secret, not yet in force. Calling it again replaces the secret.
func (s *Service) SetupTOTP(ctx context.Context, userID int64) (TOTPSetup, error) {
	u, err := s.loadStaff(ctx, userID)
	if err != nil {
		return TOTPSetup{}, err
	}
	if u.TotpEnabled {
		return TOTPSetup{}, domain.ErrTOTPAlreadyEnabled
	}
	secret, err := GenerateTOTPSecret()
	if err != nil {
		return TOTPSetup{}, err
	}
	n, err := s.q.SetPendingTOTPSecret(ctx, store.SetPendingTOTPSecretParams{ID: u.ID, TotpSecret: secret})
	if err != nil {
		return TOTPSetup{}, fmt.Errorf("store totp secret of user %d: %w", u.ID, err)
	}
	if n == 0 {
		// Turned on by a parallel request between the read and the write.
		return TOTPSetup{}, domain.ErrTOTPAlreadyEnabled
	}
	s.mfa.forget(u.ID)
	return TOTPSetup{Secret: secret, URL: TOTPURL(totpIssuer, *u.Email, secret)}, nil
}

// EnableTOTP turns TOTP on once the user proves their authenticator has the secret.
func (s *Service) EnableTOTP(ctx context.Context, userID int64, code string) (domain.User, error) {
	u, err := s.loadStaff(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	if u.TotpEnabled {
		return domain.User{}, domain.ErrTOTPAlreadyEnabled
	}
	if u.TotpSecret == nil {
		return domain.User{}, domain.ErrTOTPSetupRequired
	}
	step, ok := VerifyTOTP(*u.TotpSecret, code, s.now())
	if !ok {
		return domain.User{}, domain.ErrTOTPCodeIncorrect
	}

	// Conditional on the secret that was checked: if setup ran again in between, the code
	// proved the old secret and must not switch on the new one.
	n, err := s.q.EnableTOTP(ctx, store.EnableTOTPParams{ID: u.ID, TotpSecret: *u.TotpSecret})
	if err != nil {
		return domain.User{}, fmt.Errorf("enable totp of user %d: %w", u.ID, err)
	}
	if n == 0 {
		return domain.User{}, domain.ErrTOTPCodeIncorrect
	}
	// The code that turned TOTP on cannot also be the one used at the next sign-in.
	s.mfa.claimStep(u.ID, step)
	s.log.InfoContext(ctx, "totp enabled", "user_id", u.ID)

	u.TotpEnabled = true
	return toUser(u), nil
}

// DisableTOTP turns TOTP off and deletes the secret. It asks for the password again so a
// borrowed, signed-in laptop is not enough to remove the second factor.
func (s *Service) DisableTOTP(ctx context.Context, userID int64, password string) (domain.User, error) {
	u, err := s.loadStaff(ctx, userID)
	if err != nil {
		return domain.User{}, err
	}
	if !u.TotpEnabled {
		return domain.User{}, domain.ErrTOTPNotEnabled
	}
	if err := s.checkPassword(u, password); err != nil {
		return domain.User{}, err
	}
	if _, err := s.q.SetUserTOTP(ctx, store.SetUserTOTPParams{ID: u.ID, TotpSecret: nil, TotpEnabled: false}); err != nil {
		return domain.User{}, fmt.Errorf("disable totp of user %d: %w", u.ID, err)
	}
	s.mfa.forget(u.ID)
	s.log.InfoContext(ctx, "totp disabled", "user_id", u.ID)

	u.TotpEnabled = false
	return toUser(u), nil
}
