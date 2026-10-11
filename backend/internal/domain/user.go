package domain

import (
	"strings"
	"time"
)

// Role is what a user may do. It is stored on the user and copied into the access token.
type Role string

const (
	RoleFan       Role = "FAN"
	RoleOrganiser Role = "ORGANISER"
	RoleAdmin     Role = "ADMIN"
	RoleGateStaff Role = "GATE_STAFF"
)

// Valid reports whether r is one of the four roles.
func (r Role) Valid() bool {
	switch r {
	case RoleFan, RoleOrganiser, RoleAdmin, RoleGateStaff:
		return true
	}
	return false
}

// IsStaff reports whether r signs in with email and password. Only these roles can be
// invited, have a password and may turn TOTP on.
func (r Role) IsStaff() bool { return r == RoleOrganiser || r == RoleAdmin }

// SignsInByPhone reports whether r signs in with a phone code.
func (r Role) SignsInByPhone() bool { return r == RoleFan || r == RoleGateStaff }

// Role sets used by the route groups.
var (
	StaffRoles = []Role{RoleOrganiser, RoleAdmin}
	GateRoles  = []Role{RoleGateStaff, RoleAdmin}
)

// User is an account. It never carries the password hash or the TOTP secret: those stay
// inside the auth service.
type User struct {
	ID          int64
	Phone       *string
	Email       *string
	Name        *string
	Role        Role
	TOTPEnabled bool
	CreatedAt   time.Time
}

// Principal is the caller of a request, as proven by the access token.
type Principal struct {
	UserID int64
	Role   Role
	// SessionID is the refresh-token family the access token was issued with, so "sign out
	// my other sessions" can keep the calling one.
	SessionID string
}

// Password length bounds, as in the contract's Password schema.
const (
	PasswordMinLength = 10
	PasswordMaxLength = 256
	NameMaxLength     = 100
	EmailMaxLength    = 254
)

// NormaliseEmail lower-cases and trims an address so "Ann@x.co " and "ann@x.co" are one login.
func NormaliseEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// NormaliseName trims a display name and collapses it to nil-safe form. Empty means "no name".
func NormaliseName(name string) string {
	return strings.Join(strings.Fields(name), " ")
}
