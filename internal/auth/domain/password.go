package domain

import (
	"crypto/rand"
	"math/big"
	"strings"
	"unicode/utf8"

	"github.com/AzmainMahtab/chonkboard/internal/shared/apperrors"
)

// Password length bounds.
//
// The floor is 12 rather than the more common 8 because there is no second
// factor and no email recovery in this system: the password is the whole of the
// credential. The ceiling exists because Argon2id hashes whatever it is given,
// and an unbounded field is a cheap way to make the server do 64MB of work per
// request.
const (
	MinPasswordLength = 12
	MaxPasswordLength = 256
)

// ValidatePassword checks a password a person chose.
//
// Length only, deliberately. Composition rules ("one capital, one digit, one
// symbol") push people towards predictable substitutions and away from length,
// which is the thing that actually matters. This matches current NIST guidance.
func ValidatePassword(password string) error {
	switch {
	case password == "":
		return apperrors.Validation("that password is not valid").
			WithField("password", "is required")
	case utf8.RuneCountInString(password) < MinPasswordLength:
		return apperrors.Validation("that password is too short").
			WithField("password", "must be at least 12 characters")
	case len(password) > MaxPasswordLength:
		return apperrors.Validation("that password is too long").
			WithField("password", "must be shorter than 256 characters")
	}
	return nil
}

// generatedAlphabet omits characters that are easy to confuse when a password is
// read aloud or copied off a screen: 0/O, 1/l/I, and anything needing a shift on
// an unfamiliar keyboard layout. This credential is handed over by a human, so
// transcription errors are a real cost.
const generatedAlphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"

// GeneratedPasswordLength gives roughly 110 bits of entropy over the alphabet
// above, which is far past anything brute-forceable and still short enough to
// dictate over a phone.
const GeneratedPasswordLength = 20

// GeneratePassword returns a random password for the operator to hand over.
//
// It is shown exactly once and never stored in plaintext, so losing it means
// resetting it — which is the intended recovery path and the reason no plaintext
// copy needs to exist anywhere.
func GeneratePassword() (string, error) {
	var b strings.Builder
	b.Grow(GeneratedPasswordLength)

	max := big.NewInt(int64(len(generatedAlphabet)))
	for range GeneratedPasswordLength {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", apperrors.Internal(err, "generate password")
		}
		b.WriteByte(generatedAlphabet[n.Int64()])
	}
	return b.String(), nil
}
