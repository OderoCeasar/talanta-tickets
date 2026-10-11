package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$"), hash)

	ok, err := VerifyPassword("correct horse battery staple", hash)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = VerifyPassword("correct horse battery stapler", hash)
	require.NoError(t, err)
	assert.False(t, ok, "a different password must not verify")

	ok, err = VerifyPassword("", hash)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestHashPasswordSaltsEachHash(t *testing.T) {
	a, err := HashPassword("same password")
	require.NoError(t, err)
	b, err := HashPassword("same password")
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "two hashes of one password must differ by salt")
}

func TestHashPasswordRejectsEmpty(t *testing.T) {
	_, err := HashPassword("")
	require.ErrorIs(t, err, ErrEmptyPassword)
}

// A hash made by another implementation, at a different cost, still verifies: the settings are
// read from the string. The value is the output of the reference tool,
// `echo -n password | argon2 somesalt -id -t 2 -m 16 -p 1 -l 32`.
func TestVerifyPasswordReadsParamsFromHash(t *testing.T) {
	const reference = "$argon2id$v=19$m=65536,t=2,p=1$c29tZXNhbHQ$CTFhFdXPJO1aFaMaO6Mm5c8y7cJHAph8ArZWb2GRPPc"

	ok, err := VerifyPassword("password", reference)
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = VerifyPassword("Password", reference)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestVerifyPasswordRejectsMalformedHashes(t *testing.T) {
	good, err := HashPassword("pw")
	require.NoError(t, err)
	parts := strings.Split(good, "$")

	withPart := func(i int, v string) string {
		p := append([]string(nil), parts...)
		p[i] = v
		return strings.Join(p, "$")
	}

	cases := map[string]string{
		"empty":          "",
		"plain text":     "hunter2",
		"bcrypt":         "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy",
		"argon2i":        withPart(1, "argon2i"),
		"wrong version":  withPart(2, "v=16"),
		"missing params": withPart(3, "m=19456"),
		"zero memory":    withPart(3, "m=0,t=2,p=1"),
		"huge memory":    withPart(3, "m=4194304,t=2,p=1"),
		"huge time":      withPart(3, "m=19456,t=1000,p=1"),
		// Strict parsing: every field is read whole, so no two strings name one hash.
		"version with trailing junk": withPart(2, "v=19junk"),
		"version with sign":          withPart(2, "v=+19"),
		"version with space":         withPart(2, "v= 19"),
		"extra param":                withPart(3, "m=19456,t=2,p=1,zzz"),
		"param with trailing junk":   withPart(3, "m=19456,t=2,p=1zzz"),
		"memory with trailing junk":  withPart(3, "m=19456x,t=2,p=1"),
		"params out of order":        withPart(3, "t=2,m=19456,p=1"),
		"leading zero":               withPart(3, "m=019456,t=2,p=1"),
		"negative time":              withPart(3, "m=19456,t=-2,p=1"),
		"empty value":                withPart(3, "m=,t=2,p=1"),
		"threads overflow uint8":     withPart(3, "m=19456,t=2,p=257"),
		// The cap: one KiB over 64 MiB is refused before anything is allocated.
		"memory just over the cap": withPart(3, "m=65537,t=2,p=1"),
		"memory 1 GiB":             withPart(3, "m=1048576,t=2,p=1"),
		"salt not base64":          withPart(4, "!!!"),
		"hash not base64":          withPart(5, "!!!"),
		"empty hash":               withPart(5, ""),
		"too few sections":         strings.Join(parts[:5], "$"),
		"too many sections":        good + "$extra",
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			ok, err := VerifyPassword("pw", encoded)
			require.ErrorIs(t, err, ErrInvalidHash)
			assert.False(t, ok)
		})
	}
}

// The ceiling on memory cost is what stops a tampered users row from turning one login
// attempt into a 1 GiB allocation.
func TestVerifyPasswordMemoryCapIs64MiB(t *testing.T) {
	assert.Equal(t, 64*1024, maxArgonMemoryKiB)

	good, err := HashPassword("pw")
	require.NoError(t, err)
	parts := strings.Split(good, "$")

	// At the cap the hash is still read (and is simply the wrong password)…
	parts[3] = "m=65536,t=2,p=1"
	ok, err := VerifyPassword("pw", strings.Join(parts, "$"))
	require.NoError(t, err)
	assert.False(t, ok)

	// …one step over is refused as malformed.
	parts[3] = "m=65537,t=2,p=1"
	_, err = VerifyPassword("pw", strings.Join(parts, "$"))
	require.ErrorIs(t, err, ErrInvalidHash)
}
