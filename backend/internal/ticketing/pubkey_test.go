package ticketing

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	require.NoError(t, err)
	require.Len(t, b, 32)
	return b
}

// The eight points whose order divides 8, in their canonical encodings.
var smallOrderKeys = map[string]string{
	"the identity (order 1)":  "0100000000000000000000000000000000000000000000000000000000000000",
	"order 2":                 "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
	"order 4":                 "0000000000000000000000000000000000000000000000000000000000000000",
	"order 4, the other sign": "0000000000000000000000000000000000000000000000000000000000000080",
	"order 8":                 "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc05",
	"order 8, other sign":     "26e8958fc2b227b045c3f489f2ef98f0d5dfac05d3c63339b13802886d53fc85",
	"order 8, second pair":    "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac037a",
	"order 8, second pair -":  "c7176a703d4dd84fba3c0b760d10670f2a2053fa2c39ccc64ec7fd7792ac03fa",
}

// Other spellings of the same points, and bytes that are no point at all.
var nonCanonicalKeys = map[string]string{
	"the identity with the sign bit": "0100000000000000000000000000000000000000000000000000000000000080",
	"order 2 with the sign bit":      "ecffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	"y = p (order 4 again)":          "edffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
	"y = p + 1 (the identity again)": "eeffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
	"y = 2^255 - 1":                  "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff7f",
	"y = 2: not on the curve":        "0200000000000000000000000000000000000000000000000000000000000000",
}

func TestCheckPublicKey(t *testing.T) {
	for name, h := range smallOrderKeys {
		assert.ErrorIs(t, CheckPublicKey(mustHex(t, h)), ErrPublicKeySmallOrder, name)
	}
	for name, h := range nonCanonicalKeys {
		assert.ErrorIs(t, CheckPublicKey(mustHex(t, h)), ErrPublicKeyNotCanonical, name)
	}
	assert.ErrorIs(t, CheckPublicKey(make([]byte, 31)), ErrPublicKeyNotCanonical)

	// Real keys pass: the test key of the spec and a few hundred fresh ones.
	assert.NoError(t, CheckPublicKey(mustHex(t, TestPublicKeyHex)))
	for i := 0; i < 300; i++ {
		public, _, err := ed25519.GenerateKey(rand.Reader)
		require.NoError(t, err)
		require.NoError(t, CheckPublicKey(public), "%x", []byte(public))
	}
}

// Why it matters, shown with the standard library itself: under the identity as public key
// one fixed "signature" verifies any message whatever.
func TestASmallOrderKeyWouldVerifyAnything(t *testing.T) {
	identity := mustHex(t, smallOrderKeys["the identity (order 1)"])
	forged := make([]byte, ed25519.SignatureSize)
	forged[0] = 1 // R = the identity, S = 0
	for _, payload := range []string{"", "any ticket", "any other ticket"} {
		require.True(t, ed25519.Verify(identity, []byte(payload), forged), "the premise: crypto/ed25519 accepts it")
	}

	// So no verifier, keyring or configuration may ever hold such a key.
	for name, h := range smallOrderKeys {
		_, err := NewVerifier(PublicKey{KeyID: "k1", Key: mustHex(t, h)})
		assert.ErrorIs(t, err, ErrPublicKeySmallOrder, name)
	}
	for name, h := range nonCanonicalKeys {
		_, err := NewVerifier(PublicKey{KeyID: "k1", Key: mustHex(t, h)})
		assert.ErrorIs(t, err, ErrPublicKeyNotCanonical, name)
	}
	signer, err := NewSigner("k2", freshKey(t).seed)
	require.NoError(t, err)
	_, err = NewKeyring(signer, PublicKey{KeyID: "k1", Key: identity})
	assert.ErrorIs(t, err, ErrPublicKeySmallOrder)
	// A signer whose public half were swapped for one is refused by the keyring too.
	signer.public = identity
	_, err = NewKeyring(signer)
	assert.ErrorIs(t, err, ErrPublicKeySmallOrder)
}

func TestLoadKeyringRefusesWeakVerifyKeys(t *testing.T) {
	for _, appEnv := range []string{"development", "production"} {
		for name, h := range map[string]string{
			"identity": smallOrderKeys["the identity (order 1)"], "order 8": smallOrderKeys["order 8"],
			"non-canonical": nonCanonicalKeys["y = p + 1 (the identity again)"],
		} {
			t.Run(appEnv+"/"+name, func(t *testing.T) {
				clearKeyEnv(t)
				t.Setenv(EnvSigningKey, base64.StdEncoding.EncodeToString(freshKey(t).seed))
				t.Setenv(EnvSigningKeyID, "k2")
				t.Setenv(EnvVerifyKeys, "k1:"+base64.RawURLEncoding.EncodeToString(mustHex(t, h)))
				_, err := loadKeys(t, appEnv)
				require.Error(t, err)
				assert.Contains(t, err.Error(), EnvVerifyKeys)
				assert.Contains(t, err.Error(), "entry 1")
			})
		}
	}
}

// A private key pasted into the wrong variable must not come back in the startup error,
// which is printed to the terminal and the log.
func TestLoadKeyringNeverEchoesAMisplacedSecret(t *testing.T) {
	good := freshKey(t)
	for _, appEnv := range []string{"development", "production"} {
		leaked := freshKey(t)
		for _, spelling := range []string{
			base64.StdEncoding.EncodeToString(leaked.seed), base64.RawURLEncoding.EncodeToString(leaked.seed),
			hex.EncodeToString(leaked.seed), base64.StdEncoding.EncodeToString(leaked.private),
		} {
			cases := map[string]func(){
				"the seed as the key ID":                   func() { t.Setenv(EnvSigningKeyID, spelling) },
				"the seed as a verify entry with no colon": func() { t.Setenv(EnvVerifyKeys, spelling) },
				"the seed as the ID of a verify entry": func() {
					t.Setenv(EnvVerifyKeys, spelling+":"+base64.RawURLEncoding.EncodeToString(mustHex(t, TestPublicKeyHex)))
				},
				"the seed among good entries": func() {
					t.Setenv(EnvVerifyKeys, "k7:"+base64.RawURLEncoding.EncodeToString(good.private.Public().(ed25519.PublicKey))+","+spelling)
				},
				"the seed as the key ID and as a verify ID": func() { t.Setenv(EnvSigningKeyID, spelling); t.Setenv(EnvVerifyKeys, spelling+":x") },
			}
			for name, set := range cases {
				clearKeyEnv(t)
				t.Setenv(EnvSigningKey, base64.StdEncoding.EncodeToString(good.seed))
				set()
				_, err := loadKeys(t, appEnv)
				require.Error(t, err, name)
				leaked.assertAbsent(t, name, err.Error())
				assert.NotContains(t, err.Error(), spelling, name)
				// Not even a recognisable piece of it.
				assert.NotContains(t, err.Error(), spelling[:12], name)
				assert.True(t, strings.Contains(err.Error(), EnvSigningKeyID) || strings.Contains(err.Error(), EnvVerifyKeys), "%s: the variable is named: %v", name, err)
			}
		}
	}
	// The signing seed itself as a verify key: 32 bytes, so it has the shape of a public
	// key, and everything in the verify set is sent to gate devices. Refused, by position.
	for _, appEnv := range []string{"development", "production"} {
		clearKeyEnv(t)
		seed := base64.StdEncoding.EncodeToString(good.seed)
		t.Setenv(EnvSigningKey, seed)
		t.Setenv(EnvSigningKeyID, "k2")
		t.Setenv(EnvVerifyKeys, "k1:"+seed)
		_, err := loadKeys(t, appEnv)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "entry 1 (k1) is the private signing key")
		good.assertAbsent(t, "the signing seed as a verify key", err.Error())
	}

	// Errors from the constructors themselves say nothing of the value either.
	secret := base64.StdEncoding.EncodeToString(good.seed)
	_, err := NewSigner(secret, good.seed)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret[:12])
	_, err = ParseKeyID(secret)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret[:12])
	_, err = NewVerifier(PublicKey{KeyID: secret, Key: good.private.Public().(ed25519.PublicKey)})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), secret[:12])
}

func TestQRCache(t *testing.T) {
	c := NewQRCache(3)
	token := func(i int) string { return "TLT1.AAAA" + strings.Repeat("B", i) + ".CCCC" }

	first, err := c.PNG(token(1), 256)
	require.NoError(t, err)
	direct, err := QRPNG(token(1), 256)
	require.NoError(t, err)
	assert.Equal(t, direct, first, "the cached image is the image")
	again, err := c.PNG(token(1), 256)
	require.NoError(t, err)
	assert.Equal(t, first, again)
	assert.Equal(t, 1, c.Draws(), "the second request drew nothing")

	// Another size of the same token is another image.
	big, err := c.PNG(token(1), 512)
	require.NoError(t, err)
	assert.NotEqual(t, first, big)
	assert.Equal(t, 2, c.Draws())

	// Bounded: the least recently used goes first.
	_, _ = c.PNG(token(2), 256)
	_, _ = c.PNG(token(1), 256) // touch: (1,256) is now the newest
	_, _ = c.PNG(token(3), 256) // evicts (1,512)
	assert.Equal(t, 3, c.Len())
	assert.Equal(t, 4, c.Draws())
	_, _ = c.PNG(token(1), 256)
	assert.Equal(t, 4, c.Draws(), "kept: it was used recently")
	_, _ = c.PNG(token(1), 512)
	assert.Equal(t, 5, c.Draws(), "dropped: it was the oldest")
	assert.Equal(t, 3, c.Len())

	// A size outside the range is an error and is not cached.
	_, err = c.PNG(token(1), 10_000)
	require.Error(t, err)
	assert.Equal(t, 3, c.Len())

	// Many readers at once.
	done := make(chan struct{})
	for g := 0; g < 8; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 50; i++ {
				png, err := c.PNG(token(i%5), 128)
				if err != nil || len(png) == 0 {
					t.Error("draw failed")
					return
				}
			}
		}()
	}
	for g := 0; g < 8; g++ {
		<-done
	}
	assert.LessOrEqual(t, c.Len(), 3)
}
