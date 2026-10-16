package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// SessionCookieName is the HTTP-only cookie holding the raw session token.
const SessionCookieName = "trakka_session"

// APITokenPrefix starts every API token (see NewAPIToken). It lets a person
// or a secret scanner recognize a leaked token, and lets RequireSession
// tell a Trakka token from another "Authorization: Bearer" value — such as
// the id_token an authenticating reverse proxy (oauth2-proxy) forwards on
// every browser request — which must fall through to the session cookie.
const APITokenPrefix = "trk_"

// apiTokenBytes is an API token's entropy, the same as a session's.
const apiTokenBytes = 32

// MaxAPITokenLength rejects absurd Authorization headers before hashing:
// a real token is len(APITokenPrefix) + 43 characters.
const MaxAPITokenLength = 128

// RandomToken returns a base64url (no padding) encoding of n
// cryptographically random bytes. crypto/rand is used directly (rather
// than promoting the already-indirect github.com/google/uuid dependency)
// since it's the correct CSPRNG primitive for security-sensitive tokens
// and keeps this package stdlib-only besides bcrypt.
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// NewAPIToken returns a new raw API token: APITokenPrefix followed by
// apiTokenBytes of crypto/rand, base64url-encoded. Like a session token it
// is stored only as HashToken's hash.
func NewAPIToken() (string, error) {
	random, err := RandomToken(apiTokenBytes)
	if err != nil {
		return "", err
	}
	return APITokenPrefix + random, nil
}

// HashToken returns the hex-encoded SHA-256 hash of a raw token, for
// storage — sessions are looked up and stored by hash, never by the raw
// value, so a database leak alone can't hand out live sessions.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// NewPKCE generates an RFC 7636 PKCE verifier/challenge pair (S256 method)
// for the OIDC authorization code flow.
func NewPKCE() (verifier, challenge string, err error) {
	verifier, err = RandomToken(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}
