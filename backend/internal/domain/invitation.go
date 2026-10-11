package domain

import "time"

// InvitationStatus is derived from the invitation's timestamps; it is not stored.
type InvitationStatus string

const (
	InvitationPending  InvitationStatus = "PENDING"
	InvitationAccepted InvitationStatus = "ACCEPTED"
	InvitationExpired  InvitationStatus = "EXPIRED"
	InvitationRevoked  InvitationStatus = "REVOKED"
)

// Valid reports whether s is one of the four statuses.
func (s InvitationStatus) Valid() bool {
	switch s {
	case InvitationPending, InvitationAccepted, InvitationExpired, InvitationRevoked:
		return true
	}
	return false
}

// InvitationLifetime is how long an invitation link works.
const InvitationLifetime = 72 * time.Hour

// Inviter is the admin who sent an invitation.
type Inviter struct {
	ID    int64
	Name  *string
	Email *string
}

// Invitation is an offer of a staff account to an email address.
type Invitation struct {
	ID         int64
	Email      string
	Role       Role
	InvitedBy  Inviter
	ExpiresAt  time.Time
	AcceptedAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
	// InviteURL carries the one-time token. It is set only on the answer of create and
	// resend; the server keeps just a hash, so it cannot be produced again.
	InviteURL *string
}

// StatusAt is the one place an invitation's status is worked out. Accepted and revoked are
// final and win over expiry.
func (i Invitation) StatusAt(now time.Time) InvitationStatus {
	return InvitationStatusAt(i.AcceptedAt, i.RevokedAt, i.ExpiresAt, now)
}

// InvitationStatusAt is StatusAt for callers that hold the raw timestamps.
func InvitationStatusAt(acceptedAt, revokedAt *time.Time, expiresAt, now time.Time) InvitationStatus {
	switch {
	case acceptedAt != nil:
		return InvitationAccepted
	case revokedAt != nil:
		return InvitationRevoked
	case !expiresAt.After(now):
		return InvitationExpired
	default:
		return InvitationPending
	}
}

// CanResend reports whether a new link may be issued: only while nobody has accepted and no
// admin has revoked. An expired invitation becomes pending again.
func (s InvitationStatus) CanResend() bool {
	return s == InvitationPending || s == InvitationExpired
}

// Page is one page of a list, with the cursor for the next (nil on the last page).
type Page[T any] struct {
	Items      []T
	NextCursor *string
}

// Paging bounds shared by every list endpoint.
const (
	DefaultPageSize = 20
	MaxPageSize     = 100
)
