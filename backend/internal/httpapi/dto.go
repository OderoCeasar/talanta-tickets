package httpapi

import (
	"time"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/auth"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

// The types in this file are the JSON shapes of backend/docs/openapi.yaml, field for field.
// Nullable fields are pointers without omitempty: the contract wants the key present with
// null, not missing.

// ts formats a time as the contract does: RFC 3339, UTC, whole seconds.
func ts(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func tsPtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := ts(*t)
	return &s
}

type userDTO struct {
	ID          int64       `json:"id"`
	Phone       *string     `json:"phone"`
	Email       *string     `json:"email"`
	Name        *string     `json:"name"`
	Role        domain.Role `json:"role"`
	TOTPEnabled bool        `json:"totp_enabled"`
	CreatedAt   string      `json:"created_at"`
}

func newUserDTO(u domain.User) userDTO {
	return userDTO{
		ID: u.ID, Phone: u.Phone, Email: u.Email, Name: u.Name, Role: u.Role,
		TOTPEnabled: u.TOTPEnabled, CreatedAt: ts(u.CreatedAt),
	}
}

type authSessionDTO struct {
	AccessToken      string  `json:"access_token"`
	TokenType        string  `json:"token_type"`
	ExpiresInSeconds int     `json:"expires_in_seconds"`
	RefreshToken     string  `json:"refresh_token"`
	RefreshExpiresAt string  `json:"refresh_expires_at"`
	User             userDTO `json:"user"`
}

func newAuthSessionDTO(s auth.Session) authSessionDTO {
	return authSessionDTO{
		AccessToken:      s.AccessToken,
		TokenType:        "Bearer",
		ExpiresInSeconds: int(s.ExpiresIn / time.Second),
		RefreshToken:     s.RefreshToken,
		RefreshExpiresAt: ts(s.RefreshExpiresAt),
		User:             newUserDTO(s.User),
	}
}

// loginSessionDTO is AuthSession plus mfa_required: false.
type loginSessionDTO struct {
	MFARequired bool `json:"mfa_required"`
	authSessionDTO
}

type mfaChallengeDTO struct {
	MFARequired      bool   `json:"mfa_required"`
	MFAToken         string `json:"mfa_token"`
	ExpiresInSeconds int    `json:"expires_in_seconds"`
}

type otpRequestedDTO struct {
	ExpiresInSeconds   int `json:"expires_in_seconds"`
	ResendAfterSeconds int `json:"resend_after_seconds"`
	// DevCode is left out of the JSON entirely when empty, which it always is in production.
	DevCode string `json:"dev_code,omitempty"`
}

type totpSetupDTO struct {
	Secret     string `json:"secret"`
	OtpauthURL string `json:"otpauth_url"`
}

type inviterDTO struct {
	ID    int64   `json:"id"`
	Name  *string `json:"name"`
	Email *string `json:"email"`
}

type invitationDTO struct {
	ID         int64                   `json:"id"`
	Email      string                  `json:"email"`
	Role       domain.Role             `json:"role"`
	Status     domain.InvitationStatus `json:"status"`
	InvitedBy  inviterDTO              `json:"invited_by"`
	ExpiresAt  string                  `json:"expires_at"`
	CreatedAt  string                  `json:"created_at"`
	AcceptedAt *string                 `json:"accepted_at"`
	InviteURL  *string                 `json:"invite_url"`
}

func newInvitationDTO(i domain.Invitation, now time.Time) invitationDTO {
	return invitationDTO{
		ID:         i.ID,
		Email:      i.Email,
		Role:       i.Role,
		Status:     i.StatusAt(now),
		InvitedBy:  inviterDTO{ID: i.InvitedBy.ID, Name: i.InvitedBy.Name, Email: i.InvitedBy.Email},
		ExpiresAt:  ts(i.ExpiresAt),
		CreatedAt:  ts(i.CreatedAt),
		AcceptedAt: tsPtr(i.AcceptedAt),
		InviteURL:  i.InviteURL,
	}
}

type invitationPreviewDTO struct {
	Email     string      `json:"email"`
	Role      domain.Role `json:"role"`
	ExpiresAt string      `json:"expires_at"`
}

type staffUserDTO struct {
	ID          int64       `json:"id"`
	Name        *string     `json:"name"`
	Email       string      `json:"email"`
	Role        domain.Role `json:"role"`
	TOTPEnabled bool        `json:"totp_enabled"`
	CreatedAt   string      `json:"created_at"`
}

// pageDTO is the shape of every paged list: { items, next_cursor }.
type pageDTO[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"next_cursor"`
}
