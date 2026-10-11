package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalisePhone(t *testing.T) {
	good := map[string]string{
		"+254712345678":     "+254712345678",
		"0712345678":        "+254712345678",
		"0112345678":        "+254112345678",
		"254712345678":      "+254712345678",
		"254112345678":      "+254112345678",
		" 0712 345 678 ":    "+254712345678",
		"0712-345-678":      "+254712345678",
		"+254 (712) 345678": "+254712345678",
		"+255712345678":     "+255712345678", // a Tanzanian visitor, already international
		"+14155552671":      "+14155552671",
	}
	for in, want := range good {
		got, ok := NormalisePhone(in)
		assert.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}

	bad := []string{
		"", " ", "712345678", "07123", "07123456789", "0212345678", "0812345678",
		"+2540712345678", "+254212345678", "+25471234567", "+0712345678", "++254712345678",
		"254 712", "phone", "0712345a78", "+254712345678x", "25471234567", "00254712345678",
		"+1", "+1234567890123456",
	}
	for _, in := range bad {
		_, ok := NormalisePhone(in)
		assert.False(t, ok, "%q should be refused", in)
	}
}

func TestInvitationStatus(t *testing.T) {
	now := time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)

	assert.Equal(t, InvitationPending, Invitation{ExpiresAt: future}.StatusAt(now))
	assert.Equal(t, InvitationExpired, Invitation{ExpiresAt: past}.StatusAt(now))
	assert.Equal(t, InvitationExpired, Invitation{ExpiresAt: now}.StatusAt(now), "the expiry instant itself is too late")
	// Accepted and revoked are final: they win over the clock.
	assert.Equal(t, InvitationAccepted, Invitation{ExpiresAt: past, AcceptedAt: &past}.StatusAt(now))
	assert.Equal(t, InvitationRevoked, Invitation{ExpiresAt: future, RevokedAt: &past}.StatusAt(now))
	assert.Equal(t, InvitationRevoked, Invitation{ExpiresAt: past, RevokedAt: &past}.StatusAt(now))

	assert.True(t, InvitationPending.CanResend())
	assert.True(t, InvitationExpired.CanResend())
	assert.False(t, InvitationAccepted.CanResend())
	assert.False(t, InvitationRevoked.CanResend())
	assert.False(t, InvitationStatus("NOPE").Valid())
}

func TestRoles(t *testing.T) {
	assert.True(t, RoleOrganiser.IsStaff())
	assert.True(t, RoleAdmin.IsStaff())
	assert.False(t, RoleFan.IsStaff())
	assert.False(t, RoleGateStaff.IsStaff())
	assert.True(t, RoleFan.SignsInByPhone())
	assert.True(t, RoleGateStaff.SignsInByPhone())
	assert.False(t, RoleAdmin.SignsInByPhone())
	assert.False(t, Role("ROOT").Valid())
	assert.False(t, Role("").Valid())
}

func TestErrorMatchingByCode(t *testing.T) {
	wrapped := fmt.Errorf("accept: %w", &Error{Code: CodeNotFound, Message: "No such event."})
	assert.ErrorIs(t, wrapped, ErrNotFound, "same code, different message")
	assert.NotErrorIs(t, wrapped, ErrForbidden)
	assert.Equal(t, CodeNotFound, CodeOf(wrapped))
	assert.Equal(t, CodeInternal, CodeOf(errors.New("disk full")))
	assert.Equal(t, CodeInternal, CodeOf(nil))

	cause := errors.New("pq: boom")
	e := &Error{Code: CodeAlreadyExists, Message: "Taken.", Err: cause}
	assert.ErrorIs(t, e, cause)
	assert.Contains(t, e.Error(), "ALREADY_EXISTS")
}

func TestRateLimitedNeverSaysZero(t *testing.T) {
	for _, in := range []int{-5, 0, 1} {
		e := RateLimited(in)
		require.Equal(t, CodeRateLimited, e.Code)
		assert.Equal(t, RateLimitDetails{RetryAfterSeconds: 1}, e.Details)
	}
	assert.Equal(t, RateLimitDetails{RetryAfterSeconds: 90}, RateLimited(90).Details)
}

func TestNormaliseEmailAndName(t *testing.T) {
	assert.Equal(t, "ann@x.co", NormaliseEmail("  Ann@X.co "))
	assert.Equal(t, "Wanjiru Kamau", NormaliseName("  Wanjiru   Kamau\t"))
	assert.Equal(t, "", NormaliseName(" \n "))
}

func TestPhoneHasCountryCode(t *testing.T) {
	kenya := []string{"254"}
	assert.True(t, PhoneHasCountryCode("+254712345678", kenya))
	assert.False(t, PhoneHasCountryCode("+255712345678", kenya))
	assert.False(t, PhoneHasCountryCode("+14155552671", kenya))
	assert.False(t, PhoneHasCountryCode("254712345678", kenya), "not E.164: no plus")
	assert.False(t, PhoneHasCountryCode("+254712345678", nil), "an empty list allows nobody")
	assert.False(t, PhoneHasCountryCode("+254712345678", []string{""}))
	assert.True(t, PhoneHasCountryCode("+14155552671", []string{"254", "1"}))
	assert.True(t, PhoneHasCountryCode("+255712345678", []string{"254", "255"}))
}
