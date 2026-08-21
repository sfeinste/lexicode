package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// The Argon2id parameters, in one place (S05). These are the RFC 9106 "memory-constrained
// interactive" shape tuned for a login endpoint on a laptop-class machine: one pass over 64 MiB
// with four lanes costs tens of milliseconds — slow enough to make offline guessing expensive,
// fast enough that logging in never feels broken. They are written into every hash's PHC
// string, so changing them here affects new hashes only; old ones keep verifying with the
// parameters they were created with.
const (
	argonTime    = 1         // passes
	argonMemory  = 64 * 1024 // KiB = 64 MiB
	argonThreads = 4         // lanes
	argonSaltLen = 16        // bytes
	argonKeyLen  = 32        // bytes
)

// ErrBadHash is returned by VerifyPassword when the stored hash is not a well-formed argon2id
// PHC string. Seed fixtures deliberately store one of these so nobody can log in as them.
var ErrBadHash = errors.New("stored password hash is not a valid argon2id hash")

// HashPassword hashes a password with the package parameters and a fresh random salt, returning
// the PHC-format string that goes into users.password_hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the stored PHC-format hash. The comparison is
// constant-time. A malformed hash returns ErrBadHash; a mere mismatch returns (false, nil).
func VerifyPassword(hash, password string) (bool, error) {
	// $argon2id$v=19$m=65536,t=1,p=4$<salt>$<key> → ["", "argon2id", "v=19", params, salt, key]
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, ErrBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, ErrBadHash
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false, ErrBadHash
	}
	if m == 0 || t == 0 || p == 0 {
		return false, ErrBadHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, ErrBadHash
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, ErrBadHash
	}
	got := argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}
