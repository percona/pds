package auth_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
)

func TestNewTokenIsFreshEveryTimeAndHashesToItsRow(t *testing.T) {
	first, firstHash, err := auth.NewToken()
	require.NoError(t, err)
	second, secondHash, err := auth.NewToken()
	require.NoError(t, err)

	assert.NotEqual(t, first, second, "two tokens are never the same")
	assert.NotEqual(t, firstHash, secondHash)
	assert.Equal(t, firstHash, auth.HashToken(first),
		"the hash stored for a token is the hash a later lookup computes")
	assert.NotContains(t, firstHash, first, "the hash does not carry the token")
	assert.Len(t, firstHash, 64, "a SHA-256 hash in hex")
}

// TestNewTokenIsURLSafe matters because the token travels in the invite link
// the customer clicks. A value needing escaping would break the copy and paste
// path that link exists for.
func TestNewTokenIsURLSafe(t *testing.T) {
	token, _, err := auth.NewToken()
	require.NoError(t, err)

	for _, r := range token {
		isSafe := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '_'
		assert.True(t, isSafe, "unexpected character %q in %q", r, token)
	}
	assert.GreaterOrEqual(t, len(token), 43, "at least 256 bits of entropy")
}

func TestHashTokenOfNothingIsNotTheHashOfSomething(t *testing.T) {
	assert.NotEqual(t, auth.HashToken(""), auth.HashToken("a"))
}
