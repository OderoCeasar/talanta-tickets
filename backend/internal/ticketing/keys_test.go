package ticketing

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/config"
)

func clearKeyEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{EnvSigningKey, EnvSigningKeyID, EnvVerifyKeys} {
		t.Setenv(k, "")
	}
}

func loadKeys(t *testing.T, appEnv string) (*Keyring, error) {
	t.Helper()
	l := config.NewLoader()
	ring := LoadKeyring(l, config.Base{AppEnv: appEnv})
	if err := l.Err(); err != nil {
		require.Nil(t, ring, "a keyring must not be returned with a configuration problem")
		return nil, err
	}
	require.NotNil(t, ring)
	return ring, nil
}

// freshKey returns a new seed with every spelling of it a leak could take.
type secretKey struct {
	seed    []byte
	private ed25519.PrivateKey
}

func freshKey(t *testing.T) secretKey {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	return secretKey{seed: private.Seed(), private: private}
}

// spellings is every text form the secret could turn up in: the seed and the 64-byte key in
// hex (both cases), the four base64 forms, and Go's %v of the bytes.
func (k secretKey) spellings() []string {
	var out []string
	for _, b := range [][]byte{k.seed, k.private} {
		out = append(out,
			hex.EncodeToString(b), fmt.Sprintf("%X", b),
			base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b),
			base64.URLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b),
			fmt.Sprintf("%v", b), fmt.Sprint(b[:8]),
		)
	}
	return out
}

func (k secretKey) assertAbsent(t *testing.T, where, text string) {
	t.Helper()
	for _, s := range k.spellings() {
		assert.NotContains(t, text, s, "%s contains the private key", where)
	}
}

func TestProductionRefusesToStartWithoutAKey(t *testing.T) {
	clearKeyEnv(t)
	_, err := loadKeys(t, config.EnvProduction)
	require.ErrorContains(t, err, "TICKET_SIGNING_KEY is required when APP_ENV=production")

	// An unset APP_ENV is production (config.Base fails closed), and so is anything unknown.
	_, err = loadKeys(t, "")
	require.ErrorContains(t, err, "TICKET_SIGNING_KEY is required")
	_, err = loadKeys(t, "staging")
	require.ErrorContains(t, err, "TICKET_SIGNING_KEY is required")
}

func TestProductionRefusesAMalformedKey(t *testing.T) {
	key := freshKey(t)
	good := base64.StdEncoding.EncodeToString(key.seed)
	otherPublic := freshKey(t).private[32:]

	cases := map[string]struct{ key, id, verify, want string }{
		"not base64":            {key: "not base64 at all!", want: "TICKET_SIGNING_KEY is not valid base64"},
		"31 bytes":              {key: base64.StdEncoding.EncodeToString(key.seed[:31]), want: "must be 32 bytes (seed) or 64 bytes"},
		"33 bytes":              {key: base64.StdEncoding.EncodeToString(append(key.seed, 1)), want: "must be 32 bytes (seed) or 64 bytes"},
		"hex instead of base64": {key: hex.EncodeToString(key.seed), want: "must be 32 bytes (seed) or 64 bytes"},
		"64 bytes whose public half is another key's": {
			key: base64.StdEncoding.EncodeToString(append(append([]byte(nil), key.seed...), otherPublic...)), want: "not a seed followed by its own public key"},
		"key id not k<number>":    {key: good, id: "prod-2026", want: "TICKET_SIGNING_KEY_ID"},
		"key id k0 is reserved":   {key: good, id: "k0", want: "reserved for the ephemeral development key"},
		"key id too large":        {key: good, id: "k65536", want: "TICKET_SIGNING_KEY_ID"},
		"verify key not a pair":   {key: good, verify: "k2", want: "TICKET_VERIFY_KEYS"},
		"verify key wrong length": {key: good, verify: "k2:" + base64.StdEncoding.EncodeToString(otherPublic[:31]), want: "TICKET_VERIFY_KEYS"},
		"verify key bad id":       {key: good, verify: "old:" + base64.StdEncoding.EncodeToString(otherPublic), want: "TICKET_VERIFY_KEYS"},
		"verify key k0":           {key: good, verify: "k0:" + base64.StdEncoding.EncodeToString(otherPublic), want: "cannot be trusted"},
		"verify key contradicts the signing key": {
			key: good, id: "k3", verify: "k3:" + base64.StdEncoding.EncodeToString(otherPublic), want: "given twice with different public keys"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clearKeyEnv(t)
			t.Setenv(EnvSigningKey, tc.key)
			t.Setenv(EnvSigningKeyID, tc.id)
			t.Setenv(EnvVerifyKeys, tc.verify)
			_, err := loadKeys(t, config.EnvProduction)
			require.ErrorContains(t, err, tc.want)
			// Whatever was wrong with it, the message never repeats the value.
			assert.NotContains(t, err.Error(), tc.key)
			key.assertAbsent(t, "the startup error", err.Error())

			// Development is no more forgiving of a key that was set and is wrong: only an
			// absent key gets the ephemeral one.
			_, err = loadKeys(t, config.EnvDevelopment)
			require.Error(t, err)
		})
	}
}

// The key pair printed in docs/TICKET_FORMAT.md is public knowledge.
func TestProductionRefusesThePublishedTestKey(t *testing.T) {
	seed, err := hex.DecodeString(TestSeedHex)
	require.NoError(t, err)
	public, err := hex.DecodeString(TestPublicKeyHex)
	require.NoError(t, err)

	clearKeyEnv(t)
	t.Setenv(EnvSigningKey, base64.StdEncoding.EncodeToString(seed))
	_, err = loadKeys(t, config.EnvProduction)
	require.ErrorContains(t, err, "published test key")

	clearKeyEnv(t)
	t.Setenv(EnvSigningKey, base64.StdEncoding.EncodeToString(freshKey(t).seed))
	t.Setenv(EnvSigningKeyID, "k2")
	t.Setenv(EnvVerifyKeys, "k1:"+base64.RawURLEncoding.EncodeToString(public))
	_, err = loadKeys(t, config.EnvProduction)
	require.ErrorContains(t, err, "cannot be trusted in production")

	// Development and test may use it: that is what it is published for.
	clearKeyEnv(t)
	t.Setenv(EnvSigningKey, base64.StdEncoding.EncodeToString(seed))
	ring, err := loadKeys(t, config.EnvTest)
	require.NoError(t, err)
	assert.Equal(t, TestPublicKeyHex, hex.EncodeToString(ring.Signer.PublicKey()))
}

func TestKeyIsAcceptedInEverySpelling(t *testing.T) {
	key := freshKey(t)
	for name, encoded := range map[string]string{
		"seed, standard base64":         base64.StdEncoding.EncodeToString(key.seed),
		"seed, unpadded":                base64.RawStdEncoding.EncodeToString(key.seed),
		"seed, url alphabet":            base64.URLEncoding.EncodeToString(key.seed),
		"seed, url alphabet unpadded":   base64.RawURLEncoding.EncodeToString(key.seed),
		"seed with surrounding spaces":  "  " + base64.StdEncoding.EncodeToString(key.seed) + "\n",
		"64-byte key (seed and public)": base64.StdEncoding.EncodeToString(key.private),
	} {
		t.Run(name, func(t *testing.T) {
			clearKeyEnv(t)
			t.Setenv(EnvSigningKey, encoded)
			ring, err := loadKeys(t, config.EnvProduction)
			require.NoError(t, err)
			assert.False(t, ring.Ephemeral)
			assert.Equal(t, "k1", ring.Signer.KeyID(), "the default key ID")
			assert.Equal(t, []byte(key.private.Public().(ed25519.PublicKey)), []byte(ring.Signer.PublicKey()))
		})
	}
}

func TestDevelopmentGetsAnEphemeralKey(t *testing.T) {
	for _, env := range []string{config.EnvDevelopment, config.EnvTest} {
		clearKeyEnv(t)
		a, err := loadKeys(t, env)
		require.NoError(t, err)
		assert.True(t, a.Ephemeral)
		assert.Equal(t, EphemeralKeyID, a.Signer.KeyID())

		token, err := a.Signer.Sign(exampleClaims())
		require.NoError(t, err)
		_, err = a.Verifier.Verify(token)
		require.NoError(t, err)

		// "Dies with the process": the next start makes another key, and the old ticket no
		// longer verifies.
		b, err := loadKeys(t, env)
		require.NoError(t, err)
		_, err = b.Verifier.Verify(token)
		assert.ErrorIs(t, err, ErrBadSignature)
	}

	// A key ID or a retired key without a signing key is a half-finished setup, not a
	// request for an ephemeral key.
	clearKeyEnv(t)
	t.Setenv(EnvSigningKeyID, "k2")
	_, err := loadKeys(t, config.EnvDevelopment)
	require.ErrorContains(t, err, "TICKET_SIGNING_KEY is not")
}

// Rotation: k1 is retired and k2 takes over. Tickets signed with k1 before the change still
// verify through TICKET_VERIFY_KEYS, which holds only k1's public half; new tickets carry k2.
func TestKeyRotation(t *testing.T) {
	oldKey, newKey := freshKey(t), freshKey(t)

	clearKeyEnv(t)
	t.Setenv(EnvSigningKey, base64.StdEncoding.EncodeToString(oldKey.seed))
	t.Setenv(EnvSigningKeyID, "k1")
	before, err := loadKeys(t, config.EnvProduction)
	require.NoError(t, err)
	oldTicket, err := before.Signer.Sign(exampleClaims())
	require.NoError(t, err)

	clearKeyEnv(t)
	t.Setenv(EnvSigningKey, base64.StdEncoding.EncodeToString(newKey.seed))
	t.Setenv(EnvSigningKeyID, "k2")
	t.Setenv(EnvVerifyKeys, " k1:"+base64.RawURLEncoding.EncodeToString(before.Signer.PublicKey())+" ")
	after, err := loadKeys(t, config.EnvProduction)
	require.NoError(t, err)

	claims, err := after.Verifier.Verify(oldTicket)
	require.NoError(t, err, "a ticket signed with the retired key still verifies")
	assert.Equal(t, "k1", claims.KeyID)

	newTicket, err := after.Signer.Sign(exampleClaims())
	require.NoError(t, err)
	claims, err = after.Verifier.Verify(newTicket)
	require.NoError(t, err)
	assert.Equal(t, "k2", claims.KeyID, "new tickets use the new key ID")
	assert.NotEqual(t, oldTicket, newTicket)

	keys := after.Verifier.PublicKeys()
	require.Len(t, keys, 2)
	assert.Equal(t, "k1", keys[0].KeyID)
	assert.Equal(t, "k2", keys[1].KeyID)

	// Without the retired key in TICKET_VERIFY_KEYS the old ticket is refused by key ID.
	clearKeyEnv(t)
	t.Setenv(EnvSigningKey, base64.StdEncoding.EncodeToString(newKey.seed))
	t.Setenv(EnvSigningKeyID, "k2")
	forgot, err := loadKeys(t, config.EnvProduction)
	require.NoError(t, err)
	_, err = forgot.Verifier.Verify(oldTicket)
	assert.ErrorIs(t, err, ErrUnknownKey)

	// The old verifier does not know the new key either.
	_, err = before.Verifier.Verify(newTicket)
	assert.ErrorIs(t, err, ErrUnknownKey)

	// A retired ID re-used for a different key is refused as a forgery, not accepted.
	oldIDNewKey, err := NewSigner("k1", newKey.seed)
	require.NoError(t, err)
	forged, err := oldIDNewKey.Sign(exampleClaims())
	require.NoError(t, err)
	_, err = after.Verifier.Verify(forged)
	assert.ErrorIs(t, err, ErrBadSignature)
}

// holder stands for any struct of ours that ends up holding a signer: by pointer, by value,
// exported and unexported.
type holder struct {
	Name    string
	Signer  *Signer
	signer  *Signer
	ByValue Signer
	byValue Signer
	Ring    *Keyring
	ring    Keyring
}

// The private key appears in no printed, logged or marshalled form of a signer or of
// anything that holds one.
func TestPrivateKeyIsNeverPrintedLoggedOrMarshalled(t *testing.T) {
	key := freshKey(t)
	signer, err := NewSigner("k9", key.seed)
	require.NoError(t, err)
	ring, err := NewKeyring(signer)
	require.NoError(t, err)
	h := holder{Name: "h", Signer: signer, signer: signer, ByValue: *signer, byValue: *signer, Ring: ring, ring: *ring}

	values := map[string]any{
		"*Signer": signer, "Signer": *signer, "*Keyring": ring, "Keyring": *ring,
		"holder": h, "*holder": &h, "[]any": []any{signer, *signer, ring}, "map": map[string]any{"s": signer, "v": *signer},
	}
	for name, v := range values {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x", "%X", "%q", "%d"} {
			key.assertAbsent(t, fmt.Sprintf("%s of %s", verb, name), fmt.Sprintf(verb, v))
		}
		if b, err := json.Marshal(v); err == nil {
			key.assertAbsent(t, "JSON of "+name, string(b))
		}
	}
	assert.Equal(t, "ticketing.Signer{key_id: k9, private_key: [ticket signing key redacted]}", fmt.Sprintf("%v", signer))
	assert.Equal(t, fmt.Sprintf("%v", signer), fmt.Sprintf("%+v", signer))
	assert.Equal(t, fmt.Sprintf("%v", signer), fmt.Sprintf("%#v", signer))

	// slog, at debug, in both handlers, as a value and inside groups and structs.
	for _, format := range []string{"json", "text"} {
		var buf bytes.Buffer
		var handler slog.Handler = slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
		if format == "text" {
			handler = slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
		}
		log := slog.New(handler)
		log.Debug("signer", "signer", signer, "by_value", *signer, "ring", ring, "ring_value", *ring, "holder", h, "holder_ptr", &h)
		log.Info("group", slog.Group("keys", slog.Any("signer", signer), slog.Any("ring", ring)))
		log.Error("as error text", "err", fmt.Errorf("could not sign with %v / %+v", signer, h))
		key.assertAbsent(t, "the "+format+" log", buf.String())
		assert.Contains(t, buf.String(), "redacted")
	}

	// Errors from every way of misusing the signer.
	_, err = signer.Sign(Claims{EventID: 0, GateGroup: "N-LOWER"})
	require.Error(t, err)
	key.assertAbsent(t, "a signing error", err.Error())
	_, err = NewSigner("nope", key.seed)
	require.Error(t, err)
	key.assertAbsent(t, "a constructor error", err.Error())
	_, err = NewSigner("k1", key.private[:63])
	require.Error(t, err)
	key.assertAbsent(t, "a length error", err.Error())

	// And what is meant to be public is public: the token and the public key.
	token, err := signer.Sign(exampleClaims())
	require.NoError(t, err)
	key.assertAbsent(t, "a token", token)
	assert.Equal(t, []byte(key.private.Public().(ed25519.PublicKey)), []byte(signer.PublicKey()))
}

// The check above is only worth something if it can see a leak. A struct that keeps the key
// in a field is printed by fmt whatever methods it has, when it sits in an unexported field
// of something else; this is the mistake Signer's closure avoids.
func TestLeakDetectorSeesANaiveSigner(t *testing.T) {
	type naive struct{ key ed25519.PrivateKey }
	type outer struct{ n naive }
	key := freshKey(t)
	leaked := fmt.Sprintf("%v", outer{n: naive{key: key.private}})
	found := false
	for _, s := range key.spellings() {
		if bytes.Contains([]byte(leaked), []byte(s)) {
			found = true
		}
	}
	assert.True(t, found, "the detector must recognise a printed key")
}
