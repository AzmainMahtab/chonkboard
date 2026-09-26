// Package password defines the password hashing port and its Argon2id
// implementation.
//
// Lifted from go-kit/internal/shared/password/password.go, which is where this
// workspace's reviewed version lives. Argon2id is the Password Hashing
// Competition winner, resists both GPU cracking and side-channel attacks, and is
// what OWASP recommends.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

var (
	// ErrInvalidHash means the stored string is not a PHC-encoded Argon2id hash.
	ErrInvalidHash = errors.New("invalid password hash format")
	// ErrIncompatibleVersion means the hash was produced by a different Argon2
	// version. Rejected rather than guessed at.
	ErrIncompatibleVersion = errors.New("incompatible argon2 version")
	// ErrMismatchedPassword means the password is wrong. It is the only outcome
	// a caller should ever surface, and never in a message that distinguishes it
	// from an unknown account.
	ErrMismatchedPassword = errors.New("password does not match")
)

// Hasher is the port. The auth service depends on this, not on Argon2id, so a
// test can substitute something cheap.
type Hasher interface {
	Hash(password string) (string, error)
	Verify(password, encodedHash string) error
}

// Argon2idHasher implements Hasher.
type Argon2idHasher struct {
	time    uint32
	memory  uint32
	threads uint8
	keyLen  uint32
	saltLen uint32
}

// NewArgon2id returns a hasher with the OWASP-recommended parameters.
//
// 64MB and three passes is roughly 50ms per call on a modern core. That is the
// point — it is what makes an offline attack on a leaked hash expensive — and it
// is also why no test outside this package may use the real hasher.
func NewArgon2id() *Argon2idHasher {
	return &Argon2idHasher{
		time:    3,
		memory:  64 * 1024, // 64 MB
		threads: 4,
		keyLen:  32,
		saltLen: 16,
	}
}

// Hash produces a PHC-encoded hash, salt included, so the parameters travel with
// the value and can be raised later without invalidating existing hashes.
func (h *Argon2idHasher) Hash(password string) (string, error) {
	salt := make([]byte, h.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: generate salt: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt, h.time, h.memory, h.threads, h.keyLen)

	return fmt.Sprintf(
		"$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.memory, h.time, h.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// Verify compares a plaintext password against an encoded hash in constant time.
//
// The parameters come from the stored hash rather than from this struct, so a
// hash written with older settings still verifies after the settings are raised.
func (h *Argon2idHasher) Verify(password, encodedHash string) error {
	p, salt, hash, err := decodeHash(encodedHash)
	if err != nil {
		return err
	}

	other := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen)

	if subtle.ConstantTimeEq(int32(len(hash)), int32(len(other))) == 0 {
		return ErrMismatchedPassword
	}
	if subtle.ConstantTimeCompare(hash, other) == 1 {
		return nil
	}
	return ErrMismatchedPassword
}

type params struct {
	memory  uint32
	time    uint32
	threads uint8
	keyLen  uint32
}

func decodeHash(encodedHash string) (*params, []byte, []byte, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 {
		return nil, nil, nil, ErrInvalidHash
	}
	if parts[1] != "argon2id" {
		return nil, nil, nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return nil, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return nil, nil, nil, ErrIncompatibleVersion
	}

	p := &params{}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return nil, nil, nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, nil, nil, ErrInvalidHash
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return nil, nil, nil, ErrInvalidHash
	}
	p.keyLen = uint32(len(hash))

	return p, salt, hash, nil
}
