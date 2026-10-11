package ticketing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/config"
)

// The environment variables of the ticket keys.
const (
	EnvSigningKey   = "TICKET_SIGNING_KEY"
	EnvSigningKeyID = "TICKET_SIGNING_KEY_ID"
	EnvVerifyKeys   = "TICKET_VERIFY_KEYS"
	// DefaultSigningKeyID is used when TICKET_SIGNING_KEY is set and its ID is not.
	DefaultSigningKeyID = "k1"
)

// The test key pair published in docs/TICKET_FORMAT.md (it is test 1 of RFC 8032, §7.1).
// Everyone has the private half, so production refuses to sign or to verify with it.
const (
	TestSeedHex      = "9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60"
	TestPublicKeyHex = "d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a"
)

const redacted = "[ticket signing key redacted]"

// Signer signs ticket payloads with the one active private key.
//
// The private key is not a field. It lives in the closure behind sign, so nothing that
// prints, logs, marshals or reflects over a Signer (or over any struct that holds one, by
// value or by pointer, in an exported or an unexported field) can reach its bytes: fmt
// prints a func as an address. The methods below only make what is printed readable.
type Signer struct {
	keyID  string
	public ed25519.PublicKey
	sign   func(message []byte) []byte
}

// NewSigner builds a signer from a 32-byte Ed25519 seed or a 64-byte private key (seed then
// public key). The error never contains any part of the key.
func NewSigner(keyID string, key []byte) (*Signer, error) {
	if _, err := ParseKeyID(keyID); err != nil {
		return nil, err
	}
	var private ed25519.PrivateKey
	switch len(key) {
	case ed25519.SeedSize:
		private = ed25519.NewKeyFromSeed(key)
	case ed25519.PrivateKeySize:
		private = ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])
		// The second half of a 64-byte key is the public key. One that does not belong to
		// the seed means the value was cut, joined or copied wrongly.
		if !bytes.Equal(private[ed25519.SeedSize:], key[ed25519.SeedSize:]) {
			return nil, errors.New("the 64-byte signing key is not a seed followed by its own public key")
		}
	default:
		return nil, fmt.Errorf("the signing key must be 32 bytes (seed) or 64 bytes (seed and public key), got %d", len(key))
	}
	public := private.Public().(ed25519.PublicKey)
	return &Signer{
		keyID:  keyID,
		public: public,
		sign:   func(message []byte) []byte { return ed25519.Sign(private, message) },
	}, nil
}

// KeyID is the ID written into every token this signer makes and stored in tickets.key_id.
func (s *Signer) KeyID() string { return s.keyID }

// PublicKey is the public half, which gate devices get.
func (s *Signer) PublicKey() ed25519.PublicKey { return append(ed25519.PublicKey(nil), s.public...) }

// Sign makes the QR text for the claims. c.KeyID is set to the signer's own.
func (s *Signer) Sign(c Claims) (string, error) {
	c.KeyID = s.keyID
	payload, err := EncodePayload(c)
	if err != nil {
		return "", err
	}
	return EncodeToken(payload, s.sign(SigningMessage(payload))), nil
}

func (s *Signer) String() string {
	if s == nil {
		return "ticketing.Signer(nil)"
	}
	return "ticketing.Signer{key_id: " + s.keyID + ", private_key: " + redacted + "}"
}

// GoString is what %#v prints.
func (s *Signer) GoString() string { return s.String() }

// Format covers every other verb (%x, %s, %d, …).
func (s *Signer) Format(f fmt.State, _ rune) { _, _ = f.Write([]byte(s.String())) }

// LogValue is what slog writes.
func (s *Signer) LogValue() slog.Value { return slog.StringValue(s.String()) }

// MarshalText keeps the signer out of JSON and every other text encoding.
func (s *Signer) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// PublicKey is one key a verifier trusts.
type PublicKey struct {
	KeyID string
	Key   ed25519.PublicKey
}

// Verifier checks ticket tokens against a set of public keys by key ID. It holds no secret:
// this is the half of the scheme that also runs on gate devices.
type Verifier struct {
	keys map[string]ed25519.PublicKey
}

// NewVerifier builds a verifier. Every key must be 32 bytes and every ID a valid key ID.
func NewVerifier(keys ...PublicKey) (*Verifier, error) {
	v := &Verifier{keys: make(map[string]ed25519.PublicKey, len(keys))}
	for _, k := range keys {
		if _, err := ParseKeyID(k.KeyID); err != nil {
			return nil, err
		}
		if len(k.Key) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("public key %s must be %d bytes, got %d", k.KeyID, ed25519.PublicKeySize, len(k.Key))
		}
		if old, dup := v.keys[k.KeyID]; dup && !bytes.Equal(old, k.Key) {
			return nil, fmt.Errorf("key ID %s is given twice with different public keys", k.KeyID)
		}
		v.keys[k.KeyID] = append(ed25519.PublicKey(nil), k.Key...)
	}
	return v, nil
}

// PublicKeys lists the trusted keys, ordered by key number. The gate manifest sends them.
func (v *Verifier) PublicKeys() []PublicKey {
	out := make([]PublicKey, 0, len(v.keys))
	for id, key := range v.keys {
		out = append(out, PublicKey{KeyID: id, Key: append(ed25519.PublicKey(nil), key...)})
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := ParseKeyID(out[i].KeyID)
		b, _ := ParseKeyID(out[j].KeyID)
		return a < b
	})
	return out
}

// Verify checks a QR text and returns what it says. The checks run in the order written in
// docs/TICKET_FORMAT.md; each failure is one of the Err values in token.go.
//
// A nil error means only "this server (or one holding a trusted key) signed exactly this".
// Whether the ticket is for this event and this gate, and whether it is still valid, are
// separate questions: Claims.CheckEvent, Claims.CheckGate and the ticket's status.
func (v *Verifier) Verify(token string) (Claims, error) {
	payload, signature, err := splitToken(token)
	if err != nil {
		return Claims{}, err
	}
	claims, err := DecodePayload(payload)
	// A payload that is well-formed but for one impossible field is still checked against
	// its signature first: "malformed" must only ever be said about something we signed.
	if err != nil && !errors.Is(err, ErrMalformedPayload) {
		return Claims{}, err
	}
	malformed := err != nil

	keyID := KeyID(uint16(payload[offKeyNumber])<<8 | uint16(payload[offKeyNumber+1]))
	key, known := v.keys[keyID]
	if !known {
		return Claims{}, ErrUnknownKey
	}
	if !ed25519.Verify(key, SigningMessage(payload), signature) {
		return Claims{}, ErrBadSignature
	}
	if malformed {
		return Claims{}, ErrMalformedPayload
	}
	return claims, nil
}

// Keyring is the server's ticket keys: one signer and the verifier for every key that may
// have signed a ticket still in circulation.
type Keyring struct {
	Signer   *Signer
	Verifier *Verifier
	// Ephemeral is true when no key was configured and one was made up at startup
	// (development and test only). Tickets signed with it stop verifying when the process
	// ends.
	Ephemeral bool
}

// NewKeyring builds a keyring from a signer and the public keys of retired signers.
func NewKeyring(signer *Signer, retired ...PublicKey) (*Keyring, error) {
	keys := append([]PublicKey{{KeyID: signer.keyID, Key: signer.public}}, retired...)
	verifier, err := NewVerifier(keys...)
	if err != nil {
		return nil, err
	}
	return &Keyring{Signer: signer, Verifier: verifier}, nil
}

// NewEphemeralKeyring makes a keyring around a fresh random key with the reserved ID k0.
func NewEphemeralKeyring() (*Keyring, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate ephemeral ticket key: %w", err)
	}
	signer, err := NewSigner(EphemeralKeyID, private.Seed())
	if err != nil {
		return nil, err
	}
	ring, err := NewKeyring(signer)
	if err != nil {
		return nil, err
	}
	ring.Ephemeral = true
	return ring, nil
}

// LoadKeyring reads the ticket keys from the environment. Problems are recorded on the
// loader, so they are reported with every other configuration problem; a nil keyring means
// at least one was recorded.
//
//   - TICKET_SIGNING_KEY: base64 of the 32-byte seed or the 64-byte private key. Required in
//     production. In development and test, when it is unset, an ephemeral key is generated.
//   - TICKET_SIGNING_KEY_ID: "k1" to "k65535"; default k1.
//   - TICKET_VERIFY_KEYS: optional "kid:base64 public key" pairs, comma-separated, for
//     retired keys whose tickets must still verify.
//
// No message recorded here contains any part of a key.
func LoadKeyring(l *config.Loader, b config.Base) *Keyring {
	// Read with os.Getenv, not the loader: a secret is never passed around as a setting.
	rawKey := strings.TrimSpace(os.Getenv(EnvSigningKey))
	keyID := l.Str(EnvSigningKeyID, "")
	retiredRaw := l.List(EnvVerifyKeys, "")

	if rawKey == "" {
		if !b.AllowDevShortcuts() {
			l.Fail("%s is required when APP_ENV=production: base64 of a 32-byte Ed25519 seed (`openssl rand -base64 32`)", EnvSigningKey)
			return nil
		}
		if keyID != "" || len(retiredRaw) > 0 {
			l.Fail("%s and %s are set but %s is not: set the key, or unset all three to run with an ephemeral development key", EnvSigningKeyID, EnvVerifyKeys, EnvSigningKey)
			return nil
		}
		ring, err := NewEphemeralKeyring()
		if err != nil {
			l.Fail("%v", err)
			return nil
		}
		return ring
	}

	ok := true
	fail := func(format string, args ...any) {
		l.Fail(format, args...)
		ok = false
	}

	if keyID == "" {
		keyID = DefaultSigningKeyID
	}
	if keyID == EphemeralKeyID {
		fail("%s: %s is reserved for the ephemeral development key; use k1 or higher", EnvSigningKeyID, EphemeralKeyID)
	}

	var signer *Signer
	key, err := decodeKey(rawKey)
	if err != nil {
		// err is ours and says only "not base64": never the value.
		fail("%s is not valid base64", EnvSigningKey)
	} else if signer, err = NewSigner(keyID, key); err != nil {
		fail("%s / %s: %v", EnvSigningKey, EnvSigningKeyID, err)
	}

	var retired []PublicKey
	for _, pair := range retiredRaw {
		id, encoded, found := strings.Cut(pair, ":")
		pub, err := decodeKey(encoded)
		switch {
		case !found || err != nil || len(pub) != ed25519.PublicKeySize:
			fail("%s: each entry must be <key id>:<base64 of a 32-byte public key>, for example k1:11qYAYKx…; entry for %q is not", EnvVerifyKeys, id)
			continue
		case id == EphemeralKeyID:
			fail("%s: %s is reserved for the ephemeral development key and cannot be trusted", EnvVerifyKeys, EphemeralKeyID)
			continue
		}
		if _, err := ParseKeyID(id); err != nil {
			fail("%s: %v", EnvVerifyKeys, err)
			continue
		}
		retired = append(retired, PublicKey{KeyID: id, Key: pub})
	}

	if b.IsProduction() {
		// The key pair printed in docs/TICKET_FORMAT.md is public. Signing with it lets
		// anyone make tickets; trusting it lets anyone's tickets through the gate.
		if signer != nil && fmt.Sprintf("%x", []byte(signer.public)) == TestPublicKeyHex {
			fail("%s is the published test key from docs/TICKET_FORMAT.md; generate a private one for production", EnvSigningKey)
		}
		for _, k := range retired {
			if fmt.Sprintf("%x", []byte(k.Key)) == TestPublicKeyHex {
				fail("%s: %s is the published test key from docs/TICKET_FORMAT.md and cannot be trusted in production", EnvVerifyKeys, k.KeyID)
			}
		}
	}
	if !ok || signer == nil {
		return nil
	}
	ring, err := NewKeyring(signer, retired...)
	if err != nil {
		l.Fail("%s: %v", EnvVerifyKeys, err)
		return nil
	}
	return ring
}

// decodeKey reads base64 in any of its four spellings (standard or URL alphabet, padded or
// not), because keys are pasted from many tools.
func decodeKey(s string) ([]byte, error) {
	s = strings.TrimRight(strings.TrimSpace(s), "=")
	s = strings.NewReplacer("+", "-", "/", "_").Replace(s)
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, errors.New("not base64")
	}
	return b, nil
}
