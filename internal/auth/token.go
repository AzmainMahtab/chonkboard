package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

// tokenBytes is the entropy in a session token and in a CSRF token. 256 bits is
// far past guessable and still a short cookie value.
const tokenBytes = 32

// mintToken returns a new session token and the value to store for it.
//
// Only the hash is ever written to the database. A dump of the sessions table
// therefore yields hashes, not live sessions — the same reason passwords are
// hashed, applied to the thing that stands in for a password on every request.
//
// SHA-256 with no salt and no stretching is correct here and would be wrong for a
// password: the input is 256 bits of system entropy, so there is no dictionary to
// run and nothing for a slow hash to buy.
func mintToken() (raw, hashed string, err error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", apperrors.Internal(err, "generate session token")
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, hashToken(raw), nil
}

// hashToken is how a cookie value becomes a database lookup key.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// mintCSRFToken returns a per-session token for the double-submit check.
func mintCSRFToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", apperrors.Internal(err, "generate csrf token")
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
