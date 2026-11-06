package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOrganizationUsageIsPrintedForNoSubcommandAndForHelp(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"nothing", nil},
		{"help", []string{"help"}},
		{"long flag", []string{"--help"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer

			require.NoError(t, runOrganization(tc.args, &out))

			printed := out.String()
			assert.Contains(t, printed, "adapter organization create")
			assert.Contains(t, printed, "adapter organization invite")
			assert.Contains(t, printed, "adapter organization list")
		})
	}
}

func TestOrganizationRefusesAnUnknownSubcommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"with an id", []string{"delete", "acme"}},
		// The command is checked before the id, so an unknown one is not
		// answered with advice about a username it does not take.
		{"without an id", []string{"delete"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer

			err := runOrganization(tc.args, &out)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "delete")
			assert.NotContains(t, err.Error(), "needs an organization id")
		})
	}
}

// TestBootstrapRefusesWithoutAPassword keeps the development login from being
// seeded with nothing. The password comes from the environment so it stays out
// of the process list, which means an empty one is the likely mistake.
func TestBootstrapRefusesWithoutAPassword(t *testing.T) {
	t.Setenv("BOOTSTRAP_PASSWORD", "")
	var out bytes.Buffer

	err := runOrganization([]string{"bootstrap", "dev"}, &out)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "BOOTSTRAP_PASSWORD")
}

func TestOrganizationRefusesAMissingOrMalformedID(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"create with no id", []string{"create"}},
		{"create with a malformed id", []string{"create", "Not A Login"}},
		{"invite with no id", []string{"invite"}},
		{"bootstrap with no id", []string{"bootstrap"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer

			err := runOrganization(tc.args, &out)

			// It fails before it ever looks for a database, which is what
			// makes this assertable without one.
			require.Error(t, err)
			assert.NotContains(t, strings.ToLower(err.Error()), "password=")
		})
	}
}

// TestInviteLinkIsBuiltFromThePublicURL keeps the printed link something a
// customer can actually click, and keeps the token out of the path where a
// proxy log would keep it by default.
func TestInviteLinkIsBuiltFromThePublicURL(t *testing.T) {
	tests := []struct {
		name  string
		base  string
		token string
		want  string
	}{
		{
			name:  "plain host",
			base:  "https://hub.example",
			token: "abc123",
			want:  "https://hub.example/app/claim?token=abc123",
		},
		{
			name:  "trailing slash",
			base:  "https://hub.example/",
			token: "abc123",
			want:  "https://hub.example/app/claim?token=abc123",
		},
		{
			name:  "no public url configured",
			base:  "",
			token: "abc123",
			want:  "/app/claim?token=abc123",
		},
		{
			name:  "token needing escaping",
			base:  "https://hub.example",
			token: "a+b/c=",
			want:  "https://hub.example/app/claim?token=a%2Bb%2Fc%3D",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, inviteLink(tc.base, tc.token))
		})
	}
}

func TestOrganizationCommandHasOneName(t *testing.T) {
	assert.True(t, isOrganizationCommand("organization"))
	assert.False(t, isOrganizationCommand("login"))
	assert.False(t, isOrganizationCommand("serve"))
}
