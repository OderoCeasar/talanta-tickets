//go:build integration

package httpapi_test

import (
	"context"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/httpapi"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/notify"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ratelimit"
	"github.com/OderoCeasar/talanta-tickets/backend/internal/ticketing"
)

// Review fix: the QR image endpoint is limited per user, and a repeated request for the
// same image is served from the cache, byte for byte.
func TestGetTicketQR_RateLimitedPerUserAndCached(t *testing.T) {
	e := newEnv(t, withLimits(func(l *httpapi.RateLimits) { l.TicketQRPerUser = ratelimit.Rule{Limit: 3, Window: time.Minute} }))
	ev, north, _ := e.ticketEvent()
	fanID, token := e.newFan()
	otherID, otherToken := e.newFan()
	_, _, tickets := e.issued(ev.ID, fanID, north[0])
	_, _, others := e.issued(ev.ID, otherID, north[1])
	path := "/tickets/" + tickets[0].ID() + "/qr"

	first := e.do("GET", path, nil, token)
	require.Equal(t, http.StatusOK, first.Code)
	assert.Equal(t, tickets[0].QRToken(), decodeQR(t, first.Body.Bytes()))
	second := e.do("GET", path, nil, token)
	require.Equal(t, http.StatusOK, second.Code)
	assert.Equal(t, first.Body.Bytes(), second.Body.Bytes())
	require.Equal(t, http.StatusOK, e.do("GET", path+"?size=128", nil, token).Code)

	rec := e.do("GET", path, nil, token)
	wantError(t, rec, http.StatusTooManyRequests, domain.CodeRateLimited)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
	// Per user: another fan is not held back, and still cannot see this fan's ticket.
	require.Equal(t, http.StatusOK, e.do("GET", "/tickets/"+others[0].ID()+"/qr", nil, otherToken).Code)
	wantError(t, e.do("GET", path, nil, otherToken), http.StatusNotFound, domain.CodeNotFound)

	e.clock.Advance(61 * time.Second)
	require.Equal(t, http.StatusOK, e.do("GET", path, nil, token).Code)

	// What production runs with.
	def := httpapi.DefaultRateLimits()
	assert.Equal(t, ratelimit.Rule{Limit: 60, Window: time.Minute}, def.TicketQRPerUser)
	assert.Equal(t, ratelimit.Rule{Limit: 300, Window: time.Minute}, def.WebTicketPerIP)
}

// Review fix: with no SMS provider configured in production the mock sender delivers
// nothing, and the ticket SMS must not be logged as sent.
func TestSendTicketSMS_MockSenderInProductionIsLoggedAsNotDelivered(t *testing.T) {
	e := newEnv(t)
	ev, north, _ := e.ticketEvent()
	fanID, _ := e.newFan()
	bookingID, _, _ := e.issued(ev.ID, fanID, north[0])
	token := webToken(t, bookingID)

	for _, production := range []bool{true, false} {
		logs := &syncBuffer{}
		logger := slog.New(slog.NewJSONHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		keys, _ := newTicketKeys(t, "k1")
		svc, err := ticketing.NewService(ticketing.Config{
			DB: db.Pool, Logger: logger, Keys: keys, PublicBaseURL: testPublicBaseURL,
			SMS: notify.LogSMS{Logger: logger, Production: production},
		})
		require.NoError(t, err)
		svc.SendTicketSMS(context.Background(), bookingID)
		out := logs.String()

		assert.NotContains(t, out, "handed to the SMS sender", "production=%v: nothing was handed to anyone", production)
		if production {
			assert.Contains(t, out, `"level":"WARN"`)
			assert.Contains(t, out, "ticket sms NOT delivered")
			assert.NotContains(t, out, token, "the link is a bearer secret and is not in a production log")
			assert.NotContains(t, out, "/v1/t/")
		} else {
			assert.Contains(t, out, "not delivered")
			assert.NotContains(t, out, `"level":"WARN"`)
		}
	}

	// A real sender (here the test's box) is still logged as handed over.
	e.tickets.SendTicketSMS(context.Background(), bookingID)
	assert.Contains(t, e.logs.String(), "ticket sms handed to the SMS sender")
}
