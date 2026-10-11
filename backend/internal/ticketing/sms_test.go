package ticketing

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testWebToken = "3f9a1c0b7e2d4a6f8091b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2d3e4f5a6b"

func TestTicketSMSText(t *testing.T) {
	link := "https://talanta.vendorsoko.com/v1/t/" + testWebToken

	text := TicketSMSText("TLT-8H3K2M", 2, link)
	assert.Equal(t, "Talanta: you're going! Booking TLT-8H3K2M, 2 tickets: "+link, text)
	n, ok := GSMLength(text)
	require.True(t, ok, "only GSM 7-bit characters, or the SMS is sent as UCS-2 with a 70-character limit")
	assert.LessOrEqual(t, n, SMSSingleLimit)
	assert.Equal(t, 154, n)
	assert.True(t, strings.HasSuffix(text, link), "the link is last, so a phone that cuts the text still links it whole")

	one := TicketSMSText("TLT-8H3K2M", 1, link)
	assert.Contains(t, one, "1 ticket:")
	assert.NotContains(t, one, "1 tickets")

	// Fifty tickets (the most one booking holds) still fits.
	n, ok = GSMLength(TicketSMSText("TLT-8H3K2M", 50, link))
	assert.True(t, ok)
	assert.LessOrEqual(t, n, SMSSingleLimit)

	// A short origin leaves room for the friendlier wording.
	short := TicketSMSText("TLT-8H3K2M", 2, "https://tlt.ke/v1/t/"+testWebToken)
	assert.Contains(t, short, "Show at the gate:")
	n, _ = GSMLength(short)
	assert.LessOrEqual(t, n, SMSSingleLimit)

	// A long origin drops words, never the reference or the link.
	longLink := "https://tickets.talanta-stadium.example.co.ke/v1/t/" + testWebToken
	long := TicketSMSText("TLT-8H3K2M", 2, longLink)
	assert.Equal(t, "Talanta booking TLT-8H3K2M. Tickets: "+longLink, long)
	n, _ = GSMLength(long)
	assert.LessOrEqual(t, n, SMSSingleLimit)

	// When nothing fits in one SMS the shortest wording is sent anyway.
	huge := "https://" + strings.Repeat("a", 80) + ".example/v1/t/" + testWebToken
	assert.Equal(t, "TLT-8H3K2M "+huge, TicketSMSText("TLT-8H3K2M", 2, huge))
}

func TestGSMLength(t *testing.T) {
	n, ok := GSMLength("you're going!")
	assert.True(t, ok)
	assert.Equal(t, 13, n)

	n, ok = GSMLength("a[b]")
	assert.True(t, ok)
	assert.Equal(t, 6, n, "extension characters cost two")

	_, ok = GSMLength("you’re going")
	assert.False(t, ok, "a typographic apostrophe is not in the GSM alphabet")
	_, ok = GSMLength("Harambee ⚽")
	assert.False(t, ok)
}
