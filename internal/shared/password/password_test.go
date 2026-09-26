package password

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This is the only package whose tests may use the real hasher. 64MB and ~50ms per
// call is the point of Argon2id, and also why everything else uses Fake.

func TestHashAndVerify(t *testing.T) {
	h := NewArgon2id()

	encoded, err := h.Hash("correct horse battery staple")
	require.NoError(t, err)

	assert.NoError(t, h.Verify("correct horse battery staple", encoded))
	assert.ErrorIs(t, h.Verify("Correct horse battery staple", encoded), ErrMismatchedPassword)
	assert.ErrorIs(t, h.Verify("", encoded), ErrMismatchedPassword)
}

func TestHashIsSaltedSoIdenticalPasswordsDiffer(t *testing.T) {
	// Without a per-hash salt, two people with the same password would have the
	// same stored value, and one cracked hash would open both accounts.
	h := NewArgon2id()

	first, err := h.Hash("same password")
	require.NoError(t, err)
	second, err := h.Hash("same password")
	require.NoError(t, err)

	assert.NotEqual(t, first, second)
	assert.NoError(t, h.Verify("same password", first))
	assert.NoError(t, h.Verify("same password", second))
}

func TestEncodedFormCarriesItsParameters(t *testing.T) {
	// The parameters travel with the value, so they can be raised later without
	// invalidating every hash already stored.
	h := NewArgon2id()

	encoded, err := h.Hash("a password")
	require.NoError(t, err)

	parts := strings.Split(encoded, "$")
	require.Len(t, parts, 6)
	assert.Equal(t, "", parts[0])
	assert.Equal(t, "argon2id", parts[1])
	assert.Equal(t, "v=19", parts[2])
	assert.Equal(t, "m=65536,t=3,p=4", parts[3], "the OWASP parameters")
	assert.NotEmpty(t, parts[4], "salt")
	assert.NotEmpty(t, parts[5], "hash")
}

func TestVerifyUsesTheParametersFromTheStoredHash(t *testing.T) {
	// A hash written with weaker settings must still verify after the settings
	// are raised, or raising them locks everybody out.
	weak := &Argon2idHasher{time: 1, memory: 8 * 1024, threads: 1, keyLen: 32, saltLen: 16}
	encoded, err := weak.Hash("a password")
	require.NoError(t, err)

	strong := NewArgon2id()
	assert.NoError(t, strong.Verify("a password", encoded),
		"the stored parameters, not the hasher's, decide")
}

func TestVerifyRejectsMalformedHashes(t *testing.T) {
	h := NewArgon2id()

	tests := map[string]string{
		"empty":               "",
		"not PHC at all":      "hunter2",
		"too few fields":      "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA",
		"wrong algorithm":     "$argon2i$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
		"unparseable version": "$argon2id$vNINETEEN$m=65536,t=3,p=4$c2FsdA$aGFzaA",
		"bad parameters":      "$argon2id$v=19$m=lots,t=3,p=4$c2FsdA$aGFzaA",
		"bad salt base64":     "$argon2id$v=19$m=65536,t=3,p=4$!!!$aGFzaA",
		"bad hash base64":     "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$!!!",
	}

	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			err := h.Verify("anything", encoded)
			require.Error(t, err)
			// Never ErrMismatchedPassword: a malformed stored hash is our bug,
			// and reporting it as a wrong password would hide it forever.
			assert.NotErrorIs(t, err, ErrMismatchedPassword)
			assert.ErrorIs(t, err, ErrInvalidHash)
		})
	}
}

func TestVerifyRejectsAnIncompatibleVersion(t *testing.T) {
	err := NewArgon2id().Verify("anything",
		"$argon2id$v=18$m=65536,t=3,p=4$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaA")
	assert.ErrorIs(t, err, ErrIncompatibleVersion)
}

func TestLongAndUnicodePasswords(t *testing.T) {
	h := NewArgon2id()

	for name, pw := range map[string]string{
		"unicode":      "pässwörd-ünïcødé-🔐",
		"long":         strings.Repeat("x", 200),
		"with spaces":  "  leading and trailing  ",
		"with dollars": "$argon2id$not$a$hash$really",
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := h.Hash(pw)
			require.NoError(t, err)
			assert.NoError(t, h.Verify(pw, encoded))
			assert.ErrorIs(t, h.Verify(pw+"!", encoded), ErrMismatchedPassword)
		})
	}
}

func TestFakeHasherBehavesLikeTheRealOneAtTheBoundary(t *testing.T) {
	// Every other package's tests depend on this, so its contract is worth
	// pinning: same success, same error value.
	f := NewFake()

	encoded, err := f.Hash("a password")
	require.NoError(t, err)
	assert.NoError(t, f.Verify("a password", encoded))
	assert.ErrorIs(t, f.Verify("wrong", encoded), ErrMismatchedPassword)
	// A malformed hash is ErrInvalidHash, not ErrMismatchedPassword -- the same
	// distinction the real hasher makes, and the one the auth service relies on
	// to tell a 401 from a 500.
	assert.ErrorIs(t, f.Verify("a password", "not-a-fake-hash"), ErrInvalidHash)
}

func TestFakeAndRealAreNotInterchangeable(t *testing.T) {
	// A fake hash must not verify under the real hasher, so a fake accidentally
	// wired into production fails closed rather than accepting everything.
	fakeHash, err := NewFake().Hash("a password")
	require.NoError(t, err)

	assert.Error(t, NewArgon2id().Verify("a password", fakeHash))
}

var benchHash string

func BenchmarkHash(b *testing.B) {
	h := NewArgon2id()
	for range b.N {
		benchHash, _ = h.Hash("a representative password")
	}
}
