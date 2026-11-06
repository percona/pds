package auth_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/percona/pds/adapter/internal/auth"
)

// testCost is bcrypt's floor. These tests are about the rules around hashing,
// not about how long a hash takes, and the real cost would add seconds per
// call under the race detector.
const testCost = bcrypt.MinCost

func TestHashedPasswordMatchesOnlyItself(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery", testCost)
	require.NoError(t, err)

	assert.NotContains(t, hash, "correct horse battery",
		"the stored value never carries the password")
	assert.True(t, auth.PasswordMatches(hash, "correct horse battery"))
	assert.False(t, auth.PasswordMatches(hash, "correct horse batterz"))
}

func TestHashingTheSamePasswordTwiceGivesDifferentHashes(t *testing.T) {
	first, err := auth.HashPassword("correct horse battery", testCost)
	require.NoError(t, err)
	second, err := auth.HashPassword("correct horse battery", testCost)
	require.NoError(t, err)

	assert.NotEqual(t, first, second, "each hash carries its own salt")
}

// TestPasswordMatchesRefusesABlankHash is the state a login sits in between
// Percona creating it and the customer claiming the invite. A blank hash must
// never match, least of all a blank password.
func TestPasswordMatchesRefusesABlankHash(t *testing.T) {
	assert.False(t, auth.PasswordMatches("", "anything"))
	assert.False(t, auth.PasswordMatches("", ""))
}

// TestDefaultCostIsWhatGetsStored is the one place the real work factor is
// exercised, so a change to the constant has to be deliberate.
func TestDefaultCostIsWhatGetsStored(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery", 0)
	require.NoError(t, err)

	cost, err := bcrypt.Cost([]byte(hash))
	require.NoError(t, err)
	assert.Equal(t, auth.DefaultPasswordCost, cost,
		"a zero cost falls back to the default rather than to bcrypt's own")
}

func TestHashPasswordHonoursTheCostItIsGiven(t *testing.T) {
	hash, err := auth.HashPassword("correct horse battery", testCost)
	require.NoError(t, err)

	cost, err := bcrypt.Cost([]byte(hash))
	require.NoError(t, err)
	assert.Equal(t, testCost, cost)
}

func TestValidatePassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{"long enough", "a long enough secret", false},
		{"exactly the minimum", strings.Repeat("a", 12), false},
		{"exactly the maximum", strings.Repeat("a", 72), false},
		{"too short", "short", true},
		{"empty", "", true},
		// bcrypt reads at most 72 bytes, so a longer password would have its
		// tail silently ignored and a different password would open the
		// account. Refusing it is the only honest answer.
		{"past what bcrypt reads", strings.Repeat("a", 73), true},
		{"multibyte past the byte limit", strings.Repeat("é", 40), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := auth.ValidatePassword(tc.password)

			if tc.wantErr {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)
		})
	}
}

// TestHashPasswordRefusesWhatValidateRefuses keeps the guard on the hashing
// call rather than only on the handler, so no later caller can store a
// truncated password by skipping validation.
func TestHashPasswordRefusesWhatValidateRefuses(t *testing.T) {
	_, err := auth.HashPassword(strings.Repeat("a", 73), testCost)

	require.Error(t, err)
}

func TestValidateUsername(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{"simple", "acme", false},
		{"hyphenated", "acme-corp", false},
		{"digits after a letter", "acme2", false},
		{"exactly the minimum", "abc", false},
		{"exactly the maximum", "a" + strings.Repeat("b", 62), false},
		{"too short", "ab", true},
		{"too long", "a" + strings.Repeat("b", 63), true},
		{"upper case", "Acme", true},
		{"leading digit", "2acme", true},
		{"leading hyphen", "-acme", true},
		{"trailing hyphen", "acme-", true},
		{"underscore", "acme_corp", true},
		{"dot", "acme.corp", true},
		{"space", "acme corp", true},
		{"empty", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := auth.ValidateUsername(tc.id)

			if tc.wantErr {
				require.Error(t, err)

				return
			}
			require.NoError(t, err)
		})
	}
}
