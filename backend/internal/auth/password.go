package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id cost for new hashes: OWASP's baseline of 19 MiB, 2 passes, 1 lane. Only organisers
// and admins have passwords, so logins are rare, but the API shares a small server with
// Postgres and a larger memory cost would let a burst of login attempts starve it.
const (
	argonMemoryKiB = 19 * 1024
	argonTime      = 2
	argonThreads   = 1
	argonSaltLen   = 16
	argonKeyLen    = 32
)

// Upper bounds accepted when reading a stored hash. The cost comes from the hash string, so
// without a ceiling a tampered row could make one login attempt allocate gigabytes. 64 MiB is
// over three times what HashPassword writes, which leaves room to raise the cost later.
const (
	maxArgonMemoryKiB = 64 * 1024
	maxArgonTime      = 16
	maxArgonThreads   = 16
	maxArgonKeyLen    = 128
)

// ErrInvalidHash means the stored value is not an argon2id hash this code can check. It is a
// data problem, not a wrong password.
var ErrInvalidHash = errors.New("auth: not a supported argon2id hash")

// ErrEmptyPassword is returned by HashPassword for an empty password.
var ErrEmptyPassword = errors.New("auth: password is empty")

// HashPassword returns an argon2id hash of password in PHC string format:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
//
// The cost is written into the string, so it can be raised later without breaking old hashes.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", ErrEmptyPassword
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: read salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches encoded, a string made by HashPassword.
// A wrong password is (false, nil). An error means encoded could not be checked at all.
func VerifyPassword(password, encoded string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, ErrInvalidHash
	}

	// Each field is parsed whole. Sscanf would stop at the first byte it does not understand
	// and accept "v=19junk", so two different strings could name the same hash.
	version, ok := phcUint(parts[2], "v=", 32)
	if !ok || version != argon2.Version {
		return false, ErrInvalidHash
	}

	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false, ErrInvalidHash
	}
	memory, okM := phcUint(params[0], "m=", 32)
	time, okT := phcUint(params[1], "t=", 32)
	threads, okP := phcUint(params[2], "p=", 8)
	if !okM || !okT || !okP {
		return false, ErrInvalidHash
	}
	if memory == 0 || memory > maxArgonMemoryKiB || time == 0 || time > maxArgonTime ||
		threads == 0 || threads > maxArgonThreads {
		return false, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false, ErrInvalidHash
	}
	want, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(want) == 0 || len(want) > maxArgonKeyLen {
		return false, ErrInvalidHash
	}

	got := argon2.IDKey([]byte(password), salt, uint32(time), uint32(memory), uint8(threads), uint32(len(want)))
	// Constant time, so how many leading bytes matched cannot be measured from outside.
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// phcUint reads one "name=number" field of a PHC string: the exact prefix, then decimal digits
// and nothing else (no sign, no spaces, no leading zero, no trailing bytes).
func phcUint(field, prefix string, bits int) (uint64, bool) {
	digits, found := strings.CutPrefix(field, prefix)
	if !found || digits == "" || (len(digits) > 1 && digits[0] == '0') {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(digits, 10, bits)
	return n, err == nil
}
