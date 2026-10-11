package domain

import (
	"testing"
)

func TestTransitionTicket(t *testing.T) {
	all := []TicketStatus{TicketValid, TicketUsed, TicketVoid, TicketTransferred}
	legal := map[[2]TicketStatus]bool{
		{TicketValid, TicketUsed}:        true,
		{TicketValid, TicketVoid}:        true,
		{TicketValid, TicketTransferred}: true,
		{TicketUsed, TicketVoid}:         true,
	}
	for _, from := range all {
		for _, to := range all {
			err := TransitionTicket(from, to)
			if want := legal[[2]TicketStatus{from, to}]; (err == nil) != want {
				t.Errorf("%s -> %s: legal = %v, want %v", from, to, err == nil, want)
			}
		}
	}
	// Nothing leaves VOID or TRANSFERRED, and an unknown status goes nowhere.
	for _, to := range all {
		if TransitionTicket(TicketVoid, to) == nil || TransitionTicket(TicketTransferred, to) == nil {
			t.Errorf("a final status moved to %s", to)
		}
		if TransitionTicket("LOST", to) == nil {
			t.Errorf("an unknown status moved to %s", to)
		}
	}
}

func TestTicketStatusesThatCanBecome(t *testing.T) {
	got := TicketStatusesThatCanBecome(TicketVoid)
	if len(got) != 2 || got[0] != "VALID" || got[1] != "USED" {
		t.Errorf("statuses that can become VOID = %v, want [VALID USED]", got)
	}
	if got := TicketStatusesThatCanBecome(TicketUsed); len(got) != 1 || got[0] != "VALID" {
		t.Errorf("statuses that can become USED = %v, want [VALID]", got)
	}
	if got := TicketStatusesThatCanBecome(TicketValid); len(got) != 0 {
		t.Errorf("nothing becomes VALID again, got %v", got)
	}
	// The live statuses are exactly the ones the one-live-ticket-per-seat index counts.
	for s, live := range map[TicketStatus]bool{TicketValid: true, TicketUsed: true, TicketVoid: false, TicketTransferred: false} {
		if s.IsLive() != live {
			t.Errorf("%s.IsLive() = %v", s, s.IsLive())
		}
	}
}
