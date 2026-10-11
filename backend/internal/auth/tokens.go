package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/OderoCeasar/talanta-tickets/backend/internal/domain"
)

const (
	// AccessTokenTTL is short because an access token cannot be revoked: a stolen one is
	// good until it runs out.
	AccessTokenTTL = 15 * time.Minute
	// MFATokenTTL is how long a user has to type their authenticator code after the password.
	MFATokenTTL = 5 * time.Minute
)

// Tokens issues and checks the two kinds of signed token: the access token (a bearer JWT)
// and the MFA token that links the password step of a staff sign-in to the TOTP step.
//
// The two are signed with different keys derived from the one secret, so an MFA token can
// never pass as an access token, whatever its claims say.
type Tokens struct {
	accessKey []byte
	mfaKey    []byte
	now       func() time.Time
	parser    *jwt.Parser
}

// NewTokens builds a token issuer from the JWT secret. A nil clock means time.Now.
func NewTokens(secret []byte, now func() time.Time) *Tokens {
	if now == nil {
		now = time.Now
	}
	return &Tokens{
		// The access key is the secret itself, so the tokens are ordinary HS256 JWTs.
		accessKey: secret,
		mfaKey:    DeriveKey(secret, "talanta:mfa-token:v1"),
		now:       now,
		parser: jwt.NewParser(
			// Only HS256. Without this a token with alg "none", or an RS256 token "signed"
			// with our secret as the public key, could be accepted.
			jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
			jwt.WithExpirationRequired(),
			jwt.WithIssuedAt(),
			jwt.WithTimeFunc(now),
		),
	}
}

// DeriveKey makes an independent key for one purpose from the master secret, so the same
// bytes are never used for two jobs.
func DeriveKey(secret []byte, purpose string) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(purpose))
	return mac.Sum(nil)
}

type accessClaims struct {
	Role string `json:"role"`
	// SID is the refresh-token family (the sign-in) this token belongs to.
	SID string `json:"sid,omitempty"`
	jwt.RegisteredClaims
}

// IssueAccess signs a 15-minute access token: sub, role, sid, iat, exp, jti.
func (t *Tokens) IssueAccess(userID int64, role domain.Role, sessionID string) (string, error) {
	now := t.now()
	jti, err := randomToken(16)
	if err != nil {
		return "", err
	}
	claims := accessClaims{
		Role: string(role),
		SID:  sessionID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(userID, 10),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(AccessTokenTTL)),
			ID:        jti,
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.accessKey)
	if err != nil {
		return "", fmt.Errorf("auth: sign access token: %w", err)
	}
	return signed, nil
}

// VerifyAccess checks an access token and returns who it is. The error is
// domain.ErrTokenExpired for a good token past its time (the client should refresh) and
// domain.ErrUnauthenticated for anything else.
func (t *Tokens) VerifyAccess(token string) (domain.Principal, error) {
	var claims accessClaims
	_, err := t.parser.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) { return t.accessKey, nil })
	if err != nil {
		// Expired is reported only when the signature was good: ErrTokenExpired comes from
		// claim validation, which runs after the signature check.
		if errors.Is(err, jwt.ErrTokenExpired) && !errors.Is(err, jwt.ErrTokenSignatureInvalid) {
			return domain.Principal{}, domain.ErrTokenExpired
		}
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	userID, err := strconv.ParseInt(claims.Subject, 10, 64)
	role := domain.Role(claims.Role)
	if err != nil || userID < 1 || !role.Valid() {
		return domain.Principal{}, domain.ErrUnauthenticated
	}
	return domain.Principal{UserID: userID, Role: role, SessionID: claims.SID}, nil
}

// IssueMFA signs the token that proves "this caller just gave the right password for this
// user". It returns the token and its id, which the sign-in guard counts attempts against.
func (t *Tokens) IssueMFA(userID int64) (token, id string, err error) {
	now := t.now()
	id, err = randomToken(16)
	if err != nil {
		return "", "", err
	}
	claims := jwt.RegisteredClaims{
		Subject:   strconv.FormatInt(userID, 10),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(MFATokenTTL)),
		ID:        id,
	}
	token, err = jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.mfaKey)
	if err != nil {
		return "", "", fmt.Errorf("auth: sign mfa token: %w", err)
	}
	return token, id, nil
}

// VerifyMFA checks an MFA token. Every failure is domain.ErrMFATokenInvalid: the client's
// only move is to start again at the password.
func (t *Tokens) VerifyMFA(token string) (userID int64, id string, expiresAt time.Time, err error) {
	var claims jwt.RegisteredClaims
	if _, err := t.parser.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) { return t.mfaKey, nil }); err != nil {
		return 0, "", time.Time{}, domain.ErrMFATokenInvalid
	}
	userID, perr := strconv.ParseInt(claims.Subject, 10, 64)
	if perr != nil || userID < 1 || claims.ID == "" || claims.ExpiresAt == nil {
		return 0, "", time.Time{}, domain.ErrMFATokenInvalid
	}
	return userID, claims.ID, claims.ExpiresAt.Time, nil
}

// randomToken returns n random bytes as unpadded base64url.
func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: read random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken is how opaque tokens (refresh tokens, invitation tokens) are stored. They are
// 256 random bits, so a plain SHA-256 is enough: there is nothing to brute-force.
func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}
