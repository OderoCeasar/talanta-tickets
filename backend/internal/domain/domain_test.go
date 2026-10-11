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

func TestTransitionEvent(t *testing.T) {
	legal := map[EventStatus][]EventStatus{
		EventDraft:     {EventPublished, EventOnSale, EventCancelled},
		EventPublished: {EventOnSale, EventSoldOut, EventCancelled, EventCompleted},
		EventOnSale:    {EventPublished, EventSoldOut, EventCancelled, EventCompleted},
		EventSoldOut:   {EventOnSale, EventPublished, EventCancelled, EventCompleted},
		EventCancelled: {},
		EventCompleted: {},
	}
	for _, from := range EventStatuses {
		for _, to := range EventStatuses {
			want := false
			for _, ok := range legal[from] {
				want = want || ok == to
			}
			err := TransitionEvent(from, to)
			if want {
				assert.NoError(t, err, "%s -> %s", from, to)
				continue
			}
			require.Error(t, err, "%s -> %s must be refused", from, to)
			assert.Equal(t, CodeInvalidStateTransition, CodeOf(err))
			var de *Error
			require.True(t, errors.As(err, &de))
			assert.Equal(t, EventStateDetails{CurrentStatus: from}, de.Details, "the answer says where the event is")
		}
	}
	// The moves that must never happen, spelt out.
	assert.Error(t, TransitionEvent(EventCancelled, EventOnSale), "a cancelled event never goes back on sale")
	assert.Error(t, TransitionEvent(EventCompleted, EventCancelled))
	assert.Error(t, TransitionEvent(EventDraft, EventSoldOut), "a draft has no inventory to sell out")
	assert.Error(t, TransitionEvent(EventOnSale, EventDraft), "published is for good")
	assert.Error(t, TransitionEvent("NOPE", EventPublished))
}

func TestEffectiveEventStatusAndPublishStatus(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }

	// Publish: ON_SALE inside [open, close), PUBLISHED otherwise; a missing open is "open".
	assert.Equal(t, EventOnSale, PublishStatus(at(-time.Hour), at(time.Hour), now))
	assert.Equal(t, EventOnSale, PublishStatus(at(0), at(time.Hour), now), "open is inclusive")
	assert.Equal(t, EventOnSale, PublishStatus(nil, at(time.Hour), now), "no open time: open now")
	assert.Equal(t, EventPublished, PublishStatus(at(time.Second), at(time.Hour), now))
	assert.Equal(t, EventPublished, PublishStatus(at(-time.Hour), at(0), now), "close is exclusive")

	// Reads derive the same thing from either stored value, so nothing depends on a flip.
	for _, stored := range []EventStatus{EventPublished, EventOnSale} {
		assert.Equal(t, EventOnSale, EffectiveEventStatus(stored, at(-time.Hour), at(time.Hour), now), stored)
		assert.Equal(t, EventPublished, EffectiveEventStatus(stored, at(time.Hour), at(2*time.Hour), now), stored)
		assert.Equal(t, EventPublished, EffectiveEventStatus(stored, at(-2*time.Hour), at(-time.Hour), now), stored)
	}
	// Everything else is what it is stored as, whatever the window says.
	for _, stored := range []EventStatus{EventDraft, EventSoldOut, EventCancelled, EventCompleted} {
		assert.Equal(t, stored, EffectiveEventStatus(stored, at(-time.Hour), at(time.Hour), now))
	}

	assert.False(t, EventDraft.VisibleToPublic())
	assert.True(t, EventCancelled.VisibleToPublic())
	assert.True(t, EventCompleted.VisibleToPublic())
	assert.False(t, EventStatus("NOPE").VisibleToPublic())
	assert.False(t, EventCancelled.Editable())
	assert.False(t, EventDraft.HasInventory())
}

func TestSeatCodeFor(t *testing.T) {
	// status, hold lapsed, held by the viewer -> code
	assert.Equal(t, SeatCodeAvailable, SeatCodeFor(SeatAvailable, false, false))
	assert.Equal(t, SeatCodeSold, SeatCodeFor(SeatSold, false, false))
	assert.Equal(t, SeatCodeBlocked, SeatCodeFor(SeatBlocked, false, false))
	assert.Equal(t, SeatCodeHeld, SeatCodeFor(SeatHeld, false, false), "someone else's hold")
	assert.Equal(t, SeatCodeMine, SeatCodeFor(SeatHeld, false, true), "the viewer's own hold")
	assert.Equal(t, SeatCodeAvailable, SeatCodeFor(SeatHeld, true, false), "a lapsed hold is available")
	assert.Equal(t, SeatCodeAvailable, SeatCodeFor(SeatHeld, true, true), "even to the fan who held it")
	// "Mine" can only ever come out of a live hold.
	for _, s := range []SeatStatus{SeatAvailable, SeatSold, SeatBlocked} {
		assert.NotEqual(t, SeatCodeMine, SeatCodeFor(s, false, true), s)
	}
	assert.Equal(t, SeatCodeBlocked, SeatCodeFor("NOPE", false, false), "a seat we cannot explain is not offered")

	assert.Equal(t, []int{0, 1, 2, 3, 0}, []int{
		SeatTypeCode("STANDARD"), SeatTypeCode("ACCESSIBLE"), SeatTypeCode("COMPANION"), SeatTypeCode("RESTRICTED_VIEW"), SeatTypeCode("?"),
	})
}

func TestSalesWindowProblems(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	kickoff := now.Add(48 * time.Hour)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	codes := func(k time.Time, open, closeAt *time.Time) []ReadinessCode {
		var out []ReadinessCode
		for _, p := range SalesWindowProblems(k, open, closeAt, now) {
			out = append(out, p.Code)
		}
		return out
	}
	assert.Empty(t, codes(kickoff, at(time.Hour), at(48*time.Hour)))
	assert.Equal(t, []ReadinessCode{ReadinessSalesWindowMissing}, codes(kickoff, nil, at(time.Hour)))
	assert.Equal(t, []ReadinessCode{ReadinessSalesWindowMissing}, codes(kickoff, at(time.Hour), nil))
	assert.Equal(t, []ReadinessCode{ReadinessSalesWindowInvalid}, codes(kickoff, at(2*time.Hour), at(2*time.Hour)))
	assert.Equal(t, []ReadinessCode{ReadinessSalesWindowInvalid}, codes(kickoff, at(time.Hour), at(49*time.Hour)))
	assert.Equal(t, []ReadinessCode{ReadinessKickoffInPast}, codes(now, at(-2*time.Hour), at(-time.Hour)))

	assert.Equal(t, "This event cannot be published yet. 1 thing is missing.", EventNotReady(make([]ReadinessProblem, 1)).Message)
	assert.Equal(t, "This event cannot be published yet. 3 things are missing.", EventNotReady(make([]ReadinessProblem, 3)).Message)
}

func TestPaymentFailureReason(t *testing.T) {
	code := func(n int32) *int32 { return &n }
	assert.Nil(t, PaymentFailureReason("SUCCESS", code(0)))
	assert.Nil(t, PaymentFailureReason("PENDING", nil))
	assert.Equal(t, "timeout", *PaymentFailureReason("TIMEOUT", nil))
	assert.Equal(t, "insufficient_funds", *PaymentFailureReason("FAILED", code(1)))
	assert.Equal(t, "wrong_pin", *PaymentFailureReason("FAILED", code(2001)))
	assert.Equal(t, "cancelled", *PaymentFailureReason("FAILED", code(1032)))
	assert.Equal(t, "timeout", *PaymentFailureReason("FAILED", code(1037)))
	assert.Equal(t, "other", *PaymentFailureReason("FAILED", code(17)))
	assert.Equal(t, "other", *PaymentFailureReason("FAILED", nil))
}

func TestPublishedEventProblems(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	at := func(h int) *time.Time { v := now.Add(time.Duration(h) * time.Hour); return &v }
	fields := func(kickoff time.Time, open, closeAt *time.Time, changed bool) []string {
		var out []string
		for _, p := range PublishedEventProblems(kickoff, open, closeAt, changed, now) {
			out = append(out, p.Field+":"+p.Rule)
		}
		return out
	}

	assert.Empty(t, fields(*at(48), at(-1), at(47), true), "a sound window and a future kickoff")
	assert.Equal(t, []string{"sales_close_at:required"}, fields(*at(48), at(-1), nil, false), "sales would never close")
	assert.Equal(t, []string{"sales_open_at:required"}, fields(*at(48), nil, at(47), false))
	assert.Equal(t, []string{"sales_open_at:required", "sales_close_at:required"}, fields(*at(48), nil, nil, false))
	assert.Equal(t, []string{"sales_close_at:after_open"}, fields(*at(48), at(10), at(10), false))
	assert.Equal(t, []string{"sales_close_at:before_kickoff"}, fields(*at(48), at(-1), at(49), false))
	assert.Equal(t, []string{"kickoff_at:future"}, fields(*at(-1), at(-48), at(-2), true), "this edit moves the kickoff into the past")
	assert.Equal(t, []string{"kickoff_at:future"}, fields(now, at(-48), at(-2), true), "exactly now is not the future")
	assert.Empty(t, fields(*at(-1), at(-48), at(-2), false), "a kickoff that has simply passed does not block other edits")

	// The same inputs publish judges: whatever SalesWindowProblems refuses, this refuses.
	for _, c := range []struct{ open, closeAt *time.Time }{{nil, at(1)}, {at(1), nil}, {at(5), at(4)}, {at(1), at(72)}} {
		assert.NotEmpty(t, SalesWindowProblems(*at(48), c.open, c.closeAt, now))
		assert.NotEmpty(t, PublishedEventProblems(*at(48), c.open, c.closeAt, false, now))
	}
}
