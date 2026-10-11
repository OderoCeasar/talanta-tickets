package auth

import (
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rfcSecret is the ASCII key "12345678901234567890" used by RFC 4226 and RFC 6238.
var rfcKey = []byte("12345678901234567890")

func TestHOTPMatchesRFC4226AppendixD(t *testing.T) {
	want := []string{"755224", "287082", "359152", "969429", "338314", "254676", "287922", "162583", "399871", "520489"}
	for counter, code := range want {
		assert.Equal(t, code, hotp(rfcKey, uint64(counter), 6), "counter %d", counter)
	}
}

func TestTOTPMatchesRFC6238AppendixB(t *testing.T) {
	// The SHA-1 rows of the RFC's table: 8 digits, 30-second steps.
	vectors := []struct {
		unix int64
		code string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}
	for _, v := range vectors {
		got := hotp(rfcKey, uint64(totpStep(time.Unix(v.unix, 0))), 8)
		assert.Equal(t, v.code, got, "t=%d", v.unix)
	}
}

func TestTOTPCodeIsTheLastSixDigitsOfTheRFCVector(t *testing.T) {
	secret := totpEncoding.EncodeToString(rfcKey)
	code, err := TOTPCode(secret, time.Unix(59, 0))
	require.NoError(t, err)
	assert.Equal(t, "287082", code)
}

func TestVerifyTOTPWindow(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	require.NoError(t, err)
	now := time.Date(2026, 10, 11, 9, 0, 15, 0, time.UTC)
	code, err := TOTPCode(secret, now)
	require.NoError(t, err)

	step, ok := VerifyTOTP(secret, code, now)
	require.True(t, ok)
	assert.Equal(t, totpStep(now), step)

	_, ok = VerifyTOTP(secret, code, now.Add(30*time.Second))
	assert.True(t, ok, "one step late is accepted")
	_, ok = VerifyTOTP(secret, code, now.Add(-30*time.Second))
	assert.True(t, ok, "one step early is accepted")
	_, ok = VerifyTOTP(secret, code, now.Add(60*time.Second))
	assert.False(t, ok, "two steps late is refused")
	_, ok = VerifyTOTP(secret, code, now.Add(-60*time.Second))
	assert.False(t, ok, "two steps early is refused")
}

func TestVerifyTOTPRefusesBadInput(t *testing.T) {
	secret, err := GenerateTOTPSecret()
	require.NoError(t, err)
	now := time.Now()
	code, err := TOTPCode(secret, now)
	require.NoError(t, err)

	wrong := "000000"
	if code == wrong {
		wrong = "000001"
	}
	for name, c := range map[string]string{"wrong": wrong, "short": code[:5], "long": code + "0", "empty": ""} {
		_, ok := VerifyTOTP(secret, c, now)
		assert.False(t, ok, name)
	}
	_, ok := VerifyTOTP("not base32 !!", code, now)
	assert.False(t, ok)
}

func TestGenerateTOTPSecretIsRandomBase32(t *testing.T) {
	a, err := GenerateTOTPSecret()
	require.NoError(t, err)
	b, err := GenerateTOTPSecret()
	require.NoError(t, err)
	assert.NotEqual(t, a, b)
	assert.Len(t, a, 32)
	key, err := decodeTOTPSecret(a)
	require.NoError(t, err)
	assert.Len(t, key, totpSecretBytes)
}

func TestTOTPURL(t *testing.T) {
	raw := TOTPURL("Talanta Tickets", "organiser@talanta.test", "JBSWY3DPEHPK3PXP")
	assert.Equal(t, "otpauth://totp/Talanta%20Tickets:organiser@talanta.test?issuer=Talanta%20Tickets&secret=JBSWY3DPEHPK3PXP", raw)

	u, err := url.Parse(raw)
	require.NoError(t, err)
	assert.Equal(t, "otpauth", u.Scheme)
	assert.Equal(t, "totp", u.Host)
	assert.Equal(t, "/Talanta Tickets:organiser@talanta.test", u.Path)
	assert.Equal(t, "JBSWY3DPEHPK3PXP", u.Query().Get("secret"))
	assert.Equal(t, "Talanta Tickets", u.Query().Get("issuer"))
}
