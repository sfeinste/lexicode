package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// NewToken returns a fresh 256-bit random token, base64url-encoded (43 characters, no padding).
// Session and invite tokens both come from here; the raw value goes to the client and only its
// hash (HashToken) is ever stored.
func NewToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// HashToken is the storable form of a token: lowercase hex SHA-256. sessions.id and
// invites.token_hash hold this, never the raw token — a database read yields no credential.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
