package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6238 names HMAC-SHA1; it is what authenticator apps implement.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP per RFC 6238 with the parameters every authenticator app defaults to: HMAC-SHA1,
// 30-second steps, 6 digits. One step either side of now is accepted to allow for clock
// drift and for a code typed just as it rolls over.
const (
	totpPeriod      = 30 * time.Second
	totpDigits      = 6
	totpSkewSteps   = 1
	totpSecretBytes = 20 // the SHA-1 block the RFC recommends
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateTOTPSecret returns a new random shared secret in base32, as authenticator apps
// expect it.
func GenerateTOTPSecret() (string, error) {
	b := make([]byte, totpSecretBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: read totp secret: %w", err)
	}
	return totpEncoding.EncodeToString(b), nil
}

func decodeTOTPSecret(secret string) ([]byte, error) {
	// People type secrets by hand: lower case, spaces and padding are all tolerated.
	clean := strings.ToUpper(strings.TrimRight(strings.ReplaceAll(secret, " ", ""), "="))
	key, err := totpEncoding.DecodeString(clean)
	if err != nil || len(key) == 0 {
		return nil, fmt.Errorf("auth: totp secret is not base32")
	}
	return key, nil
}

// hotp is RFC 4226: HMAC the counter, pick four bytes by the last nibble, keep the low
// decimal digits.
func hotp(key []byte, counter uint64, digits int) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)

	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff

	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, value%mod)
}

func totpStep(t time.Time) int64 { return t.Unix() / int64(totpPeriod/time.Second) }

// TOTPCode is the code an authenticator shows for secret at time t.
func TOTPCode(secret string, t time.Time) (string, error) {
	key, err := decodeTOTPSecret(secret)
	if err != nil {
		return "", err
	}
	return hotp(key, uint64(totpStep(t)), totpDigits), nil
}

// VerifyTOTP reports whether code is right for secret at now, give or take one step. It
// returns the step that matched so the caller can refuse a second use of the same code.
func VerifyTOTP(secret, code string, now time.Time) (step int64, ok bool) {
	key, err := decodeTOTPSecret(secret)
	if err != nil || len(code) != totpDigits {
		return 0, false
	}
	current := totpStep(now)
	// Every candidate is compared, in constant time each, so the time taken does not say
	// which step (if any) matched.
	for s := current - totpSkewSteps; s <= current+totpSkewSteps; s++ {
		if s < 0 {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(hotp(key, uint64(s), totpDigits)), []byte(code)) == 1 {
			step, ok = s, true
		}
	}
	return step, ok
}

// TOTPURL is the otpauth:// URL an authenticator app reads from a QR code.
func TOTPURL(issuer, account, secret string) string {
	q := url.Values{}
	q.Set("secret", secret)
	q.Set("issuer", issuer)
	// The label is "issuer:account"; PathEscape leaves the colon and encodes spaces as %20,
	// which is what the key-URI format asks for (QueryEscape's + is not understood by all apps).
	return "otpauth://totp/" + url.PathEscape(issuer) + ":" + url.PathEscape(account) +
		"?" + strings.ReplaceAll(q.Encode(), "+", "%20")
}
