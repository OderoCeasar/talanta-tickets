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
		"empty":             "",
		"plain text":        "hunter2",
		"bcrypt":            "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy",
		"argon2i":           withPart(1, "argon2i"),
		"wrong version":     withPart(2, "v=16"),
		"missing params":    withPart(3, "m=19456"),
		"zero memory":       withPart(3, "m=0,t=2,p=1"),
		"huge memory":       withPart(3, "m=4194304,t=2,p=1"),
		"huge time":         withPart(3, "m=19456,t=1000,p=1"),
		"salt not base64":   withPart(4, "!!!"),
		"hash not base64":   withPart(5, "!!!"),
		"empty hash":        withPart(5, ""),
		"too few sections":  strings.Join(parts[:5], "$"),
		"too many sections": good + "$extra",
	}
	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			ok, err := VerifyPassword("pw", encoded)
			require.ErrorIs(t, err, ErrInvalidHash)
			assert.False(t, ok)
		})
	}
}
