package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// tokenSize is the entropy behind an invite token and a session id, in bytes.
// Both are looked up by hash with no rate limit in front of the lookup itself,
// so they have to be unguessable rather than merely hard to guess.
const tokenSize = 32

// NewToken returns a fresh credential and the hash to store against it. The
// plaintext is returned once and never stored, so a full read of the database
// hands nobody a working token.
func NewToken() (string, string, error) {
	raw := make([]byte, tokenSize)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate token: %w", err)
	}

	// Base64 without padding, in the URL alphabet: an invite token travels in
	// a link the customer clicks, and a value needing escaping would break the
	// copy and paste that link exists for.
	token := base64.RawURLEncoding.EncodeToString(raw)

	return token, HashToken(token), nil
}

// HashToken returns the value stored for a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}
