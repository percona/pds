package auth_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
)

var testSessionKey = []byte("0123456789abcdef0123456789abcdef")

func TestSignedSessionIDVerifiesBack(t *testing.T) {
	signed := auth.SignSessionID(testSessionKey, "session-id")

	assert.Contains(t, signed, "session-id")
	got, ok := auth.VerifySessionID(testSessionKey, signed)
	require.True(t, ok)
	assert.Equal(t, "session-id", got)
}

func TestVerifySessionIDRefusesWhatItDidNotSign(t *testing.T) {
	signed := auth.SignSessionID(testSessionKey, "session-id")

	tests := []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"no signature", "session-id"},
		{"empty signature", "session-id."},
		{"empty id", "." + strings.SplitN(signed, ".", 2)[1]},
		{"tampered id", "session-idx." + strings.SplitN(signed, ".", 2)[1]},
		{"tampered signature", strings.SplitN(signed, ".", 2)[0] + ".AAAA"},
		{"signature only", "." + "AAAA"},
		{"junk", "not-a-cookie-at-all"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := auth.VerifySessionID(testSessionKey, tc.value)

			assert.False(t, ok)
		})
	}
}

// TestVerifySessionIDRefusesAnotherDeploymentsSignature is what the signature
// is for: a cookie minted against a different key is refused without a
// database read.
func TestVerifySessionIDRefusesAnotherDeploymentsSignature(t *testing.T) {
	signed := auth.SignSessionID([]byte("fedcba9876543210fedcba9876543210"), "session-id")

	_, ok := auth.VerifySessionID(testSessionKey, signed)

	assert.False(t, ok)
}

func TestVerifySessionIDRefusesEveryCookieWhenThereIsNoKey(t *testing.T) {
	signed := auth.SignSessionID(testSessionKey, "session-id")

	_, ok := auth.VerifySessionID(nil, signed)

	assert.False(t, ok, "no key means nothing was signed here")
}

func TestIdentityRidesTheContext(t *testing.T) {
	ctx := auth.WithIdentity(t.Context(), auth.Identity{
		Subject: "acme", Scope: auth.Scope{Team: auth.Team{ID: 5, Name: "default"}},
	})

	team, ok := auth.TeamFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, uint(5), team)

	// The subject helper predates this and records who did what. An identity
	// has to satisfy it too, or the activity log would go back to blank.
	subject, ok := auth.SubjectFromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, "acme", subject)
}

func TestNoIdentityOnAPlainContext(t *testing.T) {
	_, ok := auth.TeamFromContext(t.Context())
	assert.False(t, ok)

	_, ok = auth.SubjectFromContext(t.Context())
	assert.False(t, ok)
}

func TestAgentRidesTheContextWithTheClient(t *testing.T) {
	ctx := auth.WithIdentity(t.Context(), auth.Identity{
		Subject: "acme",
		Client:  auth.Client{Kind: auth.ClientChat, Agent: "Anthropic"},
	})

	client := auth.ClientFromContext(ctx)

	assert.Equal(t, auth.ClientChat, client.Kind)
	assert.Equal(t, "Anthropic", client.Agent)
}

func TestValidAgentAcceptsTheNamesAProgramCallsItself(t *testing.T) {
	for _, name := range []string{"Cursor", "Claude Code", "Anthropic", "VS Code 1.99", "my-agent_2.0+x", strings.Repeat("a", 64)} {
		assert.True(t, auth.ValidAgent(name), name)
	}
}

func TestValidAgentRefusesWhatCouldBeMarkupOrTooLong(t *testing.T) {
	for _, name := range []string{"", " Cursor", "Cursor ", "<b>x</b>", "a\nb", "name\"quoted", "tab\there", "Curs\u00f6r", strings.Repeat("a", 65)} {
		assert.False(t, auth.ValidAgent(name), name)
	}
}
