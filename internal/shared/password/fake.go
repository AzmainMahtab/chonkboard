package password

import (
	"strings"
)

// Fake is a Hasher for tests. Real Argon2id costs 64MB and ~50ms per call, which
// makes a suite crawl; every test outside this package uses this instead.
//
// It is deliberately not a real hash, and deliberately not exported from a _test
// file, so that any production use of it is obvious in a diff.
type Fake struct{}

// NewFake returns the test hasher.
func NewFake() Fake { return Fake{} }

const fakePrefix = "fake$"

// Hash returns a marked, reversible encoding. Nothing about it is secret.
func (Fake) Hash(password string) (string, error) {
	return fakePrefix + password, nil
}

// Verify compares the plaintext.
//
// It distinguishes a malformed hash from a wrong password exactly as the real
// hasher does, and that distinction is load-bearing: the auth service reports a
// wrong password as a 401 and a corrupt stored hash as a 500, because the second
// is our bug and reporting it as the first would hide it forever while locking the
// person out. A double that collapsed the two would hide that behaviour too.
func (Fake) Verify(password, encodedHash string) error {
	if !strings.HasPrefix(encodedHash, fakePrefix) {
		return ErrInvalidHash
	}
	if strings.TrimPrefix(encodedHash, fakePrefix) != password {
		return ErrMismatchedPassword
	}
	return nil
}
