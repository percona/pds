package oauth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// NewVerifier returns a PKCE code verifier: 32 random bytes, 43 characters.
func NewVerifier() string {
	return randomString(32)
}

// Challenge is the S256 code challenge of a verifier.
func Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))

	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// randomString returns n random bytes as unpadded base64url. The system's random source failing
// leaves no safe way to carry on, so it stops the program.
func randomString(n int) string {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		panic("the system random source failed: " + err.Error())
	}

	return base64.RawURLEncoding.EncodeToString(raw)
}
