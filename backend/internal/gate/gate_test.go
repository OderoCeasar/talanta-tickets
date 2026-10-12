package gate

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

func TestVersionRoundTripAndRefusals(t *testing.T) {
	at := time.UnixMicro(1_760_177_000_123_456)
	v := encodeVersion(42, at)
	got, ok := decodeVersion(v, 42)
	assert.True(t, ok)
	assert.True(t, got.Equal(at))

	_, ok = decodeVersion(v, 43)
	assert.False(t, ok, "another event's version")
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	for _, bad := range []string{"", "!!", v + "=", enc("g1.42"), enc("g1.42.0"), enc("g1.42.-1"), enc("g0.42.5"), enc("g1.x.5"), enc("g1.42.5.6"), stateMarkerPrefix + "abc"} {
		_, ok := decodeVersion(bad, 42)
		assert.False(t, ok, "%q", bad)
	}
}

func TestVersionUsable(t *testing.T) {
	s := &Service{policy: DefaultPolicy()}
	now := time.Now()
	assert.True(t, s.versionUsable(now.Add(-time.Minute), now))
	assert.True(t, s.versionUsable(now.Add(500*time.Millisecond), now), "a clock step")
	assert.False(t, s.versionUsable(now.Add(2*time.Second), now), "from the future")
	assert.True(t, s.versionUsable(now.Add(-6*24*time.Hour), now))
	assert.False(t, s.versionUsable(now.Add(-8*24*time.Hour), now), "too old")
	// The overlap must outlast the longest transaction that writes tickets (cancel, 55 s).
	assert.Greater(t, s.policy.VersionOverlap, 55*time.Second)
}

func TestEarlier(t *testing.T) {
	at := time.Now()
	small, large := [16]byte{1}, [16]byte{2}
	assert.True(t, earlier(at, large, nil, nil), "a USED ticket with no used_at loses to any scan")
	later := at.Add(time.Second)
	assert.True(t, earlier(at, large, &later, &small))
	assert.False(t, earlier(later, small, &at, &large))
	assert.True(t, earlier(at, small, &at, &large), "equal times: the smaller client_scan_id")
	assert.False(t, earlier(at, large, &at, &small))
	assert.False(t, earlier(at, small, &at, &small), "the holder itself")
	assert.False(t, earlier(at, small, &at, nil), "equal times against a holder with no scan on record")
}

func TestScanTime(t *testing.T) {
	s := &Service{policy: DefaultPolicy()}
	received := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	issued := received.Add(-48 * time.Hour)
	for name, c := range map[string]struct {
		at   time.Time
		kept bool
	}{
		"a minute ago":        {received.Add(-time.Minute), true},
		"exactly now":         {received, true},
		"just after issue":    {issued.Add(time.Second), true},
		"a microsecond ahead": {received.Add(time.Microsecond), false},
		"before issue":        {issued.Add(-time.Second), false},
		"eight days ago":      {received.Add(-8 * 24 * time.Hour), false},
	} {
		got, replaced := s.scanTime(c.at, received, &issued)
		assert.Equal(t, !c.kept, replaced, name)
		if c.kept {
			assert.True(t, got.Equal(c.at), name)
		} else {
			assert.True(t, got.Equal(received), name)
		}
	}
	// An unknown ticket has no issue time; only the two outer bounds apply.
	_, replaced := s.scanTime(received.Add(-6*24*time.Hour), received, nil)
	assert.False(t, replaced)
}

func TestTicketStatusRulesTheGateReliesOn(t *testing.T) {
	assert.Equal(t, []string{"VALID"}, domain.TicketStatusesThatCanBecome(domain.TicketUsed), "only a VALID ticket becomes USED")
	assert.Error(t, domain.TransitionTicket(domain.TicketUsed, domain.TicketValid))
	assert.Error(t, domain.TransitionTicket(domain.TicketVoid, domain.TicketUsed))
	assert.True(t, domain.TicketValid.AdmitsAtGate() && domain.TicketUsed.AdmitsAtGate())
	assert.False(t, domain.TicketVoid.AdmitsAtGate() || domain.TicketTransferred.AdmitsAtGate())
	codes := map[domain.TicketStatus]byte{domain.TicketValid: 'V', domain.TicketUsed: 'U', domain.TicketVoid: 'X', domain.TicketTransferred: 'T', "SOMETHING_NEW": 'X'}
	for status, code := range codes {
		assert.Equal(t, code, status.ManifestCode(), string(status))
	}
}
