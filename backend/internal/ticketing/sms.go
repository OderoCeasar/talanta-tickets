package ticketing

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/notify"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/store"
)

// SMSSingleLimit is how many characters of the GSM 7-bit alphabet fit in one SMS.
const SMSSingleLimit = 160

// gsmBasic is the GSM 03.38 basic character set as far as plain text needs it; gsmExtended
// are the characters that cost two (an escape and the character).
const (
	gsmBasic    = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"
	gsmExtended = "^{}\\[~]|€"
)

// GSMLength counts the 7-bit characters a text takes in an SMS. ok is false when the text
// has a character outside the GSM alphabet: the whole message is then sent as UCS-2, where
// one SMS holds only 70.
func GSMLength(text string) (septets int, ok bool) {
	for _, r := range text {
		switch {
		case strings.ContainsRune(gsmBasic, r):
			septets++
		case strings.ContainsRune(gsmExtended, r):
			septets += 2
		default:
			return 0, false
		}
	}
	return septets, true
}

// TicketSMSText builds the "you're going" message (§9.4): the booking reference and the
// link to the web ticket page. It uses only characters of the GSM alphabet, and picks the
// friendliest wording that fits one SMS; the link alone is about 100 characters, so with a
// long PUBLIC_BASE_URL it falls back to shorter wordings, and to the shortest whatever its
// length. The event title is left out on purpose: titles are long and may hold characters
// that turn the whole SMS into UCS-2.
func TicketSMSText(reference string, tickets int, link string) string {
	count := fmt.Sprintf("%d tickets", tickets)
	if tickets == 1 {
		count = "1 ticket"
	}
	wordings := []string{
		fmt.Sprintf("Talanta: you're going! Booking %s, %s. Show at the gate: %s", reference, count, link),
		fmt.Sprintf("Talanta: you're going! Booking %s, %s: %s", reference, count, link),
		fmt.Sprintf("Talanta booking %s. Tickets: %s", reference, link),
		fmt.Sprintf("%s %s", reference, link),
	}
	for _, text := range wordings {
		if n, ok := GSMLength(text); ok && n <= SMSSingleLimit {
			return text
		}
	}
	return wordings[len(wordings)-1]
}

// mockSMS reports whether a sender is the log-only mock, and whether it runs in production
// (where it does not even log the text).
func mockSMS(sender notify.SMSSender) (mock, production bool) {
	switch m := sender.(type) {
	case notify.LogSMS:
		return true, m.Production
	case *notify.LogSMS:
		return m != nil, m != nil && m.Production
	}
	return false, false
}

// SendTicketSMS sends the "you're going" message for a paid booking to the phone of the fan
// who made it. Call it AFTER the payment transaction has committed, never inside it: it
// reads the booking through the pool and calls the SMS provider.
//
// It returns nothing because nothing may depend on it: a booking that is not PAID, has no
// live ticket or no phone, a database error and a failed send are all logged and swallowed.
// The fan has paid; a missing SMS must not undo or delay that. The send has its own
// timeout (Config.SMSTimeout) and is not cut short by the caller's context ending, so it is
// safe to call at the end of a request.
//
// The message text and the link are never logged here. (The mock SMS provider logs the body
// outside production only.)
func (s *Service) SendTicketSMS(ctx context.Context, bookingID int64) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.smsTimeout)
	defer cancel()

	b, err := s.q.GetBookingForTicketSMS(ctx, bookingID)
	switch {
	case errors.Is(err, store.ErrNoRows):
		s.log.WarnContext(ctx, "ticket sms not sent: no such booking", "booking_id", bookingID)
		return
	case err != nil:
		s.log.ErrorContext(ctx, "ticket sms not sent: could not read the booking", "booking_id", bookingID, "err", err)
		return
	case b.Status != bookingPaid || b.LiveTickets == 0:
		s.log.WarnContext(ctx, "ticket sms not sent: booking is not paid or has no live ticket",
			"booking_id", bookingID, "booking_status", b.Status, "live_tickets", b.LiveTickets)
		return
	case b.PhoneE164 == nil || s.sms == nil:
		s.log.WarnContext(ctx, "ticket sms not sent: no phone number or no SMS sender", "booking_id", bookingID)
		return
	}

	text := TicketSMSText(b.Reference, int(b.LiveTickets), s.WebTicketURL(b.WebToken))
	if err := s.sms.SendSMS(ctx, *b.PhoneE164, text); err != nil {
		// A provider's error can quote the request it refused. The link is a bearer
		// secret, so it is cut out of whatever comes back before it is logged.
		reason := strings.ReplaceAll(err.Error(), b.WebToken, "[web token redacted]")
		s.log.ErrorContext(ctx, "ticket sms not sent: the SMS provider failed; the fan can still open the tickets in the app",
			"booking_id", bookingID, "err", reason)
		return
	}
	// The mock sender "succeeds" without delivering anything. Saying "sent" then would be a
	// lie in the one log line an operator reads to find out why a fan got no message.
	if mock, production := mockSMS(s.sms); mock {
		if production {
			s.log.WarnContext(ctx, "ticket sms NOT delivered: no SMS provider is configured; the fan can still open the tickets in the app",
				"booking_id", bookingID, "tickets", b.LiveTickets)
			return
		}
		s.log.InfoContext(ctx, "ticket sms written to the log by the mock SMS sender, not delivered", "booking_id", bookingID, "tickets", b.LiveTickets)
		return
	}
	s.log.InfoContext(ctx, "ticket sms handed to the SMS sender", "booking_id", bookingID, "tickets", b.LiveTickets)
}
