package ticketing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testSigner is the published test key (docs/TICKET_FORMAT.md) as key k1.
func testSigner(t testing.TB) *Signer {
	t.Helper()
	seed, err := hex.DecodeString(TestSeedHex)
	require.NoError(t, err)
	s, err := NewSigner("k1", seed)
	require.NoError(t, err)
	return s
}

func randomSigner(t testing.TB, keyID string) *Signer {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	s, err := NewSigner(keyID, private.Seed())
	require.NoError(t, err)
	return s
}

func verifierFor(t testing.TB, signers ...*Signer) *Verifier {
	t.Helper()
	var keys []PublicKey
	for _, s := range signers {
		keys = append(keys, PublicKey{KeyID: s.KeyID(), Key: s.PublicKey()})
	}
	v, err := NewVerifier(keys...)
	require.NoError(t, err)
	return v
}

func exampleClaims() Claims {
	id, _ := ParseUUID("0b0f3a52-7c1e-4d7e-9a57-0c1b4d5e6f70")
	return Claims{
		TicketID: id, EventID: 1, GateGroup: "N-LOWER",
		Nonce: [NonceSize]byte{0xa1, 0xb2, 0xc3, 0xd4, 0xe5, 0xf6, 0x07, 0x18},
	}
}

func TestGoodTokenVerifies(t *testing.T) {
	signer := testSigner(t)
	token, err := signer.Sign(exampleClaims())
	require.NoError(t, err)

	got, err := verifierFor(t, signer).Verify(token)
	require.NoError(t, err)
	want := exampleClaims()
	want.KeyID = "k1"
	assert.Equal(t, want, got)
	assert.Equal(t, "0b0f3a52-7c1e-4d7e-9a57-0c1b4d5e6f70", got.TicketUUID())

	assert.NoError(t, got.CheckEvent(1))
	assert.NoError(t, got.CheckGate("N-LOWER"))

	// The contract's pattern for qr_token, and the length the QR size was chosen for.
	assert.Regexp(t, `^TLT1\.[A-Za-z0-9_-]{68}\.[A-Za-z0-9_-]{86}$`, token)
	assert.Len(t, token, 160)
}

// A valid signature says nothing about which event or gate: those are the caller's checks,
// with their own errors.
func TestWrongEventAndWrongGate(t *testing.T) {
	signer := testSigner(t)
	token, err := signer.Sign(exampleClaims())
	require.NoError(t, err)
	claims, err := verifierFor(t, signer).Verify(token)
	require.NoError(t, err)

	assert.ErrorIs(t, claims.CheckEvent(2), ErrWrongEvent, "a ticket for event 1 at a device scanning event 2")
	assert.ErrorIs(t, claims.CheckGate("S-LOWER"), ErrWrongGate, "another stand")
	assert.ErrorIs(t, claims.CheckGate("N-UPPER"), ErrWrongGate, "same stand, other tier")
	assert.ErrorIs(t, claims.CheckGate("n-lower"), ErrWrongGate, "the comparison is exact")
	assert.NotErrorIs(t, claims.CheckEvent(2), ErrWrongGate)
	assert.NotErrorIs(t, claims.CheckGate("S-LOWER"), ErrWrongEvent)
}

// flipChar returns token with the character at i replaced by another of the base64url
// alphabet.
func flipChar(token string, i int) string {
	c := token[i]
	other := byte('A')
	if c == 'A' {
		other = 'B'
	}
	return token[:i] + string(other) + token[i+1:]
}

// Every single character of the payload and of the signature matters. That includes the last
// character of the signature, which carries only two of its bits: decoding is strict, so the
// four unused bits must be zero and no second spelling of a signature exists. (A lenient
// decoder would accept 16 different last characters as the same signature, and a test that
// flips "the last character" would then pass or fail by luck.)
func TestOneChangedCharacterAnywhereFails(t *testing.T) {
	signer := testSigner(t)
	token, err := signer.Sign(exampleClaims())
	require.NoError(t, err)
	v := verifierFor(t, signer)

	payloadStart := len(TokenPrefix) + 1
	signatureStart := payloadStart + 68 + 1
	require.Equal(t, byte('.'), token[signatureStart-1])

	for i := payloadStart; i < len(token); i++ {
		if i == signatureStart-1 {
			continue
		}
		changed := flipChar(token, i)
		_, err := v.Verify(changed)
		require.Error(t, err, "character %d changed (%q -> %q) and the token still verified", i, token[i], changed[i])
	}
}

// Every byte of the signed bytes matters too, tested below the text encoding: flip one bit
// of each payload byte and of each signature byte, re-encode canonically, and verify.
func TestOneFlippedBitInAnyByteFails(t *testing.T) {
	signer := testSigner(t)
	token, err := signer.Sign(exampleClaims())
	require.NoError(t, err)
	v := verifierFor(t, signer)
	payload, signature, err := splitToken(token)
	require.NoError(t, err)

	for i := range payload {
		p := append([]byte(nil), payload...)
		p[i] ^= 0x01
		_, err := v.Verify(EncodeToken(p, signature))
		require.Error(t, err, "payload byte %d", i)
	}
	for i := range signature {
		s := append([]byte(nil), signature...)
		s[i] ^= 0x01
		_, err := v.Verify(EncodeToken(payload, s))
		require.ErrorIs(t, err, ErrBadSignature, "signature byte %d", i)
	}
}

// Changing what a ticket says without the key is the forgery that matters: each field
// changed on its own, with the original signature kept.
func TestChangedFieldWithOldSignatureIsBadSignature(t *testing.T) {
	signer := testSigner(t)
	token, err := signer.Sign(exampleClaims())
	require.NoError(t, err)
	_, signature, err := splitToken(token)
	require.NoError(t, err)
	v := verifierFor(t, signer)

	cases := map[string]func(*Claims){
		"another event":  func(c *Claims) { c.EventID = 2 },
		"another gate":   func(c *Claims) { c.GateGroup = "VIP-LOWER" },
		"another ticket": func(c *Claims) { c.TicketID[15] ^= 0xff },
		"another nonce":  func(c *Claims) { c.Nonce[0] ^= 0xff },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			c := exampleClaims()
			c.KeyID = "k1"
			change(&c)
			payload, err := EncodePayload(c)
			require.NoError(t, err)
			_, err = v.Verify(EncodeToken(payload, signature))
			assert.ErrorIs(t, err, ErrBadSignature)
		})
	}
}

func TestTokenSignedWithAnotherKeyFails(t *testing.T) {
	real, forger := testSigner(t), randomSigner(t, "k1")
	token, err := forger.Sign(exampleClaims())
	require.NoError(t, err)

	_, err = verifierFor(t, real).Verify(token)
	assert.ErrorIs(t, err, ErrBadSignature, "same key ID, another private key")
}

func TestUnknownKeyIDFails(t *testing.T) {
	k1, k7 := testSigner(t), randomSigner(t, "k7")
	token, err := k7.Sign(exampleClaims())
	require.NoError(t, err)

	_, err = verifierFor(t, k1).Verify(token)
	assert.ErrorIs(t, err, ErrUnknownKey)
	assert.NotErrorIs(t, err, ErrBadSignature)
}

// A signature made with the same key over the bare payload (no context label), or over
// another label, is not a ticket signature.
func TestSignatureIsDomainSeparated(t *testing.T) {
	seed, err := hex.DecodeString(TestSeedHex)
	require.NoError(t, err)
	private := ed25519.NewKeyFromSeed(seed)
	c := exampleClaims()
	c.KeyID = "k1"
	payload, err := EncodePayload(c)
	require.NoError(t, err)
	v := verifierFor(t, testSigner(t))

	bare := ed25519.Sign(private, payload)
	_, err = v.Verify(EncodeToken(payload, bare))
	assert.ErrorIs(t, err, ErrBadSignature, "signature over the payload alone")

	other := ed25519.Sign(private, append([]byte("talanta.refund.v1\x00"), payload...))
	_, err = v.Verify(EncodeToken(payload, other))
	assert.ErrorIs(t, err, ErrBadSignature, "signature under another context label")

	good := ed25519.Sign(private, SigningMessage(payload))
	_, err = v.Verify(EncodeToken(payload, good))
	assert.NoError(t, err)
	assert.Equal(t, "talanta.ticket.v1\x00", SignatureContext)
}

func TestMalformedTokens(t *testing.T) {
	signer := testSigner(t)
	token, err := signer.Sign(exampleClaims())
	require.NoError(t, err)
	v := verifierFor(t, signer)
	payload, signature, err := splitToken(token)
	require.NoError(t, err)
	parts := strings.Split(token, ".")

	// resign makes a correctly signed token over arbitrary payload bytes, to reach the checks
	// that sit behind the signature.
	seed, _ := hex.DecodeString(TestSeedHex)
	private := ed25519.NewKeyFromSeed(seed)
	resign := func(p []byte) string { return EncodeToken(p, ed25519.Sign(private, SigningMessage(p))) }
	withByte := func(i int, b byte) []byte {
		p := append([]byte(nil), payload...)
		p[i] = b
		return p
	}

	cases := []struct {
		name  string
		token string
		want  error
	}{
		{"empty", "", ErrBadPrefix},
		{"no dots", "TLT1", ErrBadPrefix},
		{"other prefix", "TLT2." + parts[1] + "." + parts[2], ErrBadPrefix},
		{"lower-case prefix", "tlt1." + parts[1] + "." + parts[2], ErrBadPrefix},
		{"prefix with a space", " TLT1." + parts[1] + "." + parts[2], ErrBadPrefix},
		{"two parts", "TLT1." + parts[1], ErrBadPrefix},
		{"four parts", token + ".x", ErrBadPrefix},
		{"a JWT", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.c2ln", ErrBadPrefix},

		{"payload not base64", "TLT1." + "!" + parts[1][1:] + "." + parts[2], ErrBadEncoding},
		{"payload in the standard alphabet", "TLT1." + strings.Repeat("+", 68) + "." + parts[2], ErrBadEncoding},
		{"payload padded", "TLT1." + parts[1] + "=." + parts[2], ErrBadEncoding},
		{"signature not base64", "TLT1." + parts[1] + "." + parts[2][:85] + "*", ErrBadEncoding},
		{"signature padded", "TLT1." + parts[1] + "." + parts[2] + "==", ErrBadEncoding},
		{"signature with spare bits set", "TLT1." + parts[1] + "." + parts[2][:85] + "_", ErrBadEncoding},
		{"trailing newline", token + "\n", ErrBadEncoding},
		{"impossible base64 length", "TLT1." + parts[1] + "." + parts[2] + "AAA", ErrBadEncoding},
		{"carriage return inside the payload", "TLT1." + parts[1][:10] + "\r\n" + parts[1][10:] + "." + parts[2], ErrBadEncoding},
		{"space inside the signature", "TLT1." + parts[1] + "." + parts[2][:40] + " " + parts[2][40:], ErrBadEncoding},

		{"empty payload", "TLT1.." + parts[2], ErrBadLength},
		{"empty signature", "TLT1." + parts[1] + ".", ErrBadLength},
		{"signature one byte short", EncodeToken(payload, signature[:63]), ErrBadLength},
		{"signature one byte long", EncodeToken(payload, append(append([]byte(nil), signature...), 0)), ErrBadLength},
		{"payload one byte short", EncodeToken(payload[:PayloadSize-1], signature), ErrBadLength},
		{"payload one byte long", EncodeToken(append(append([]byte(nil), payload...), 0), signature), ErrBadLength},

		{"version 0", EncodeToken(withByte(0, 0), signature), ErrUnknownVersion},
		{"version 2", EncodeToken(withByte(0, 2), signature), ErrUnknownVersion},
		{"version 2, correctly signed", resign(withByte(0, 2)), ErrUnknownVersion},
		{"version 2 with another length", EncodeToken(append([]byte{2}, make([]byte, 20)...), signature), ErrUnknownVersion},

		{"event 0, correctly signed", resign(func() []byte {
			p := append([]byte(nil), payload...)
			clear(p[offEventID : offEventID+sizeEventID])
			return p
		}()), ErrMalformedPayload},
		{"event above int64, correctly signed", resign(withByte(offEventID, 0x80)), ErrMalformedPayload},
		{"gate group in lower case, correctly signed", resign(withByte(offGateGroup, 'n')), ErrMalformedPayload},
		{"zero byte inside the gate group, correctly signed", resign(withByte(offGateGroup+1, 0)), ErrMalformedPayload},
		{"empty gate group, correctly signed", resign(func() []byte {
			p := append([]byte(nil), payload...)
			clear(p[offGateGroup : offGateGroup+sizeGate])
			return p
		}()), ErrMalformedPayload},
		// Not signed by us: the answer is "bad signature", never a comment on its fields.
		{"event 0, not signed", EncodeToken(func() []byte {
			p := append([]byte(nil), payload...)
			clear(p[offEventID : offEventID+sizeEventID])
			return p
		}(), signature), ErrBadSignature},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := v.Verify(tc.token)
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.want, "got: %v", err)
		})
	}

	// The eight reasons are eight different errors.
	all := []error{ErrBadPrefix, ErrBadEncoding, ErrBadLength, ErrUnknownVersion, ErrMalformedPayload, ErrUnknownKey, ErrBadSignature, ErrWrongEvent, ErrWrongGate}
	for i, a := range all {
		for j, b := range all {
			if i != j {
				assert.NotErrorIs(t, a, b)
			}
		}
	}
}

func TestPayloadLayout(t *testing.T) {
	assert.Equal(t, 51, PayloadSize)
	assert.Equal(t, 64, SignatureSize)
	// The layout table is contiguous and covers the payload exactly.
	next := 0
	for _, f := range PayloadLayout {
		assert.Equal(t, next, f.Offset, f.Name)
		next += f.Size
	}
	assert.Equal(t, PayloadSize, next)

	c := exampleClaims()
	c.KeyID = "k258"
	c.EventID = 0x0102030405060708
	p, err := EncodePayload(c)
	require.NoError(t, err)
	assert.Equal(t, byte(1), p[0])
	assert.Equal(t, []byte{0x01, 0x02}, p[1:3], "key number 258, big-endian")
	assert.Equal(t, "0b0f3a527c1e4d7e9a570c1b4d5e6f70", hex.EncodeToString(p[3:19]))
	assert.Equal(t, []byte{1, 2, 3, 4, 5, 6, 7, 8}, p[19:27], "event ID, big-endian")
	assert.Equal(t, "N-LOWER\x00\x00\x00\x00\x00\x00\x00\x00\x00", string(p[27:43]))
	assert.Equal(t, "a1b2c3d4e5f60718", hex.EncodeToString(p[43:51]))

	back, err := DecodePayload(p)
	require.NoError(t, err)
	assert.Equal(t, c, back)
}

func TestEncodePayloadRefusesWhatCannotBeIssued(t *testing.T) {
	for name, change := range map[string]func(*Claims){
		"event 0":                  func(c *Claims) { c.EventID = 0 },
		"negative event":           func(c *Claims) { c.EventID = -1 },
		"gate group too long":      func(c *Claims) { c.GateGroup = "ABCDEFGHIJK-LOWER" },
		"gate group without tier":  func(c *Claims) { c.GateGroup = "N" },
		"gate group, unknown tier": func(c *Claims) { c.GateGroup = "N-MIDDLE" },
		"gate group, lower case":   func(c *Claims) { c.GateGroup = "n-LOWER" },
		"gate group, no stand":     func(c *Claims) { c.GateGroup = "-LOWER" },
		"bad key id":               func(c *Claims) { c.KeyID = "key-1" },
	} {
		t.Run(name, func(t *testing.T) {
			c := exampleClaims()
			c.KeyID = "k1"
			change(&c)
			_, err := EncodePayload(c)
			assert.Error(t, err)
		})
	}
	// The longest gate group that fits: a ten-character stand code.
	c := exampleClaims()
	c.KeyID = "k1"
	c.GateGroup = "ABCDEFGHIJ-UPPER"
	p, err := EncodePayload(c)
	require.NoError(t, err)
	back, err := DecodePayload(p)
	require.NoError(t, err)
	assert.Equal(t, "ABCDEFGHIJ-UPPER", back.GateGroup)
}

func TestKeyIDs(t *testing.T) {
	for id, want := range map[string]uint16{"k0": 0, "k1": 1, "k42": 42, "k65535": 65535} {
		n, err := ParseKeyID(id)
		require.NoError(t, err, id)
		assert.Equal(t, want, n)
		assert.Equal(t, id, KeyID(n))
	}
	for _, id := range []string{"", "k", "1", "K1", "k01", "k65536", "k-1", "k1 ", "k1a", "key1", "k999999"} {
		_, err := ParseKeyID(id)
		assert.Error(t, err, id)
	}
}

func TestUUIDText(t *testing.T) {
	id, err := ParseUUID("0B0F3A52-7C1E-4D7E-9A57-0C1B4D5E6F70")
	require.NoError(t, err)
	assert.Equal(t, "0b0f3a52-7c1e-4d7e-9a57-0c1b4d5e6f70", FormatUUID(id))
	for _, bad := range []string{"", "0b0f3a52", "0b0f3a527c1e4d7e9a570c1b4d5e6f70", "0b0f3a52-7c1e-4d7e-9a57-0c1b4d5e6f7g", "0b0f3a52-7c1e-4d7e-9a57-0c1b4d5e6f700", "0b0f3a52-7c1e-4d7e-9a57-0c1b4d5e-f70"} {
		_, err := ParseUUID(bad)
		assert.Error(t, err, bad)
	}
}
