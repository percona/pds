package oauth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/oauth"
)

func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))

	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestPKCEAcceptsOnlyTheVerifierThatMadeTheChallenge(t *testing.T) {
	verifier := strings.Repeat("a", 43)
	challenge := challengeFor(verifier)

	assert.True(t, oauth.VerifyPKCE(challenge, verifier))
	assert.False(t, oauth.VerifyPKCE(challenge, strings.Repeat("b", 43)))
	assert.False(t, oauth.VerifyPKCE(challenge, ""))
	assert.False(t, oauth.VerifyPKCE("", ""), "an empty challenge never matches")
	assert.False(t, oauth.VerifyPKCE(challenge, verifier+"!"), "a verifier with a reserved character is refused")
	assert.False(t, oauth.VerifyPKCE(challenge, strings.Repeat("a", 42)), "a verifier under 43 characters is refused")
	assert.False(t, oauth.VerifyPKCE(challengeFor(strings.Repeat("a", 129)), strings.Repeat("a", 129)),
		"a verifier over 128 characters is refused")
}

func TestChallengeMustBeAnS256Digest(t *testing.T) {
	assert.True(t, oauth.ValidChallenge(challengeFor("x")))
	assert.False(t, oauth.ValidChallenge("plain-text-verifier"))
	assert.False(t, oauth.ValidChallenge(""))
	assert.False(t, oauth.ValidChallenge(challengeFor("x")+"="))
}

func TestRedirectURIRulesAtRegistration(t *testing.T) {
	for _, ok := range []string{
		"https://claude.ai/api/mcp/auth_callback",
		"http://localhost/callback",
		"http://localhost:8787/callback",
		"http://127.0.0.1:33418/",
		"http://[::1]:5000/cb",
		"https://example.com/cb?x=1",
	} {
		require.NoError(t, oauth.ValidateRedirectURI(ok), ok)
	}

	for _, bad := range []string{
		"",
		"http://example.com/cb",
		"http://localhost.evil.example/cb",
		"ftp://example.com/cb",
		"cursor://callback",
		"javascript:alert(1)",
		"https://example.com/cb#frag",
		"https://user:pass@example.com/cb",
		"https:///cb",
		"/relative",
		"https://example.com/" + strings.Repeat("a", 2100),
		"http://0.0.0.0/cb",
		"http://127.0.0.2/cb",
	} {
		require.Error(t, oauth.ValidateRedirectURI(bad), bad)
	}
}

func TestRedirectMatchingIsExactExceptForTheLoopbackPort(t *testing.T) {
	registered := []string{
		"https://claude.ai/api/mcp/auth_callback",
		"http://localhost/callback",
		"http://127.0.0.1/callback",
		"http://[::1]/callback",
	}

	for _, ok := range []string{
		"https://claude.ai/api/mcp/auth_callback",
		"http://localhost:51234/callback",
		"http://127.0.0.1:33418/callback",
		"http://[::1]:7/callback",
		"http://localhost/callback",
	} {
		assert.True(t, oauth.RedirectAllowed(registered, ok), ok)
	}

	for _, bad := range []string{
		"https://claude.ai/api/mcp/auth_callback/",
		"https://claude.ai/api/mcp/auth_callback?x=1",
		"https://claude.ai:444/api/mcp/auth_callback",
		"https://CLAUDE.ai.evil.example/api/mcp/auth_callback",
		"http://claude.ai/api/mcp/auth_callback",
		"http://localhost:5/other",
		"http://localhost:5/callback?x=1",
		"https://localhost:5/callback",
		"http://localhost.evil.example/callback",
		"http://localhost@evil.example/callback",
		"http://127.0.0.2:5/callback",
		"",
	} {
		assert.False(t, oauth.RedirectAllowed(registered, bad), bad)
	}

	assert.False(t, oauth.RedirectAllowed([]string{"http://localhost/callback"}, "http://127.0.0.1:5/callback"),
		"localhost and 127.0.0.1 are different registrations")
	assert.False(t, oauth.RedirectAllowed([]string{"https://app.example/cb"}, "https://app.example:443/cb"),
		"an explicit default port is a different string")
}

func TestLoopbackRedirectIsRecognised(t *testing.T) {
	assert.True(t, oauth.IsLoopbackRedirect("http://localhost:1/cb"))
	assert.True(t, oauth.IsLoopbackRedirect("http://127.0.0.1/cb"))
	assert.True(t, oauth.IsLoopbackRedirect("http://[::1]/cb"))
	assert.False(t, oauth.IsLoopbackRedirect("https://example.com/cb"))
}

func TestScopesGrantsAndTheirOrder(t *testing.T) {
	assert.Equal(t, []string{"pds:read"}, oauth.Grants{Access: auth.AccessRead}.Scopes())
	assert.Equal(t, []string{"pds:read", "pds:change"}, oauth.Grants{Access: auth.AccessChange}.Scopes())
	assert.Equal(t, []string{"pds:read", "pds:change", "pds:delete", "pds:credentials", "pds:sql_write"},
		oauth.Grants{Access: auth.AccessChangeDelete, Passwords: true, SQLWrites: true}.Scopes())
	assert.Equal(t, []string{"pds:read"}, oauth.Grants{Access: "nonsense"}.Scopes(),
		"an unknown tier reads as read, never as more")
}

func TestScopesRequestedBoundWhatConsentMayGrant(t *testing.T) {
	read, err := oauth.ParseScopes("pds:read offline_access")
	require.NoError(t, err)
	assert.True(t, read.Allows(oauth.Grants{Access: auth.AccessRead}))
	assert.False(t, read.Allows(oauth.Grants{Access: auth.AccessChange}))
	assert.False(t, read.Allows(oauth.Grants{Access: auth.AccessRead, Passwords: true}))

	change, err := oauth.ParseScopes("pds:change pds:sql_write")
	require.NoError(t, err)
	assert.True(t, change.Allows(oauth.Grants{Access: auth.AccessChange, SQLWrites: true}))
	assert.True(t, change.Allows(oauth.Grants{Access: auth.AccessRead}), "consent may narrow")
	assert.False(t, change.Allows(oauth.Grants{Access: auth.AccessChangeDelete}))

	all, err := oauth.ParseScopes("")
	require.NoError(t, err)
	assert.True(t, all.Allows(oauth.Grants{Access: auth.AccessChangeDelete, Passwords: true, SQLWrites: true}),
		"no scope asks for everything the server offers, and consent narrows it")

	onlyRefresh, err := oauth.ParseScopes("offline_access")
	require.NoError(t, err)
	assert.True(t, onlyRefresh.Allows(oauth.Grants{Access: auth.AccessRead}))
	assert.False(t, onlyRefresh.Allows(oauth.Grants{Access: auth.AccessChange}))

	_, err = oauth.ParseScopes("pds:read admin")
	require.ErrorIs(t, err, oauth.ErrUnknownScope)
}

func TestSupportedScopesDoNotAdvertiseRefreshInTheResourceList(t *testing.T) {
	assert.Contains(t, oauth.AuthorizationServerScopes(), "offline_access")
	assert.NotContains(t, oauth.ResourceScopes(), "offline_access")
}

func TestCanonicalResourceIgnoresCaseDefaultPortAndTrailingSlash(t *testing.T) {
	for in, want := range map[string]string{
		"https://pds.example/mcp":      "https://pds.example/mcp",
		"HTTPS://PDS.Example/mcp":      "https://pds.example/mcp",
		"https://pds.example:443/mcp/": "https://pds.example/mcp",
		"http://localhost:8080/mcp":    "http://localhost:8080/mcp",
	} {
		got, ok := oauth.CanonicalResource(in)
		require.True(t, ok, in)
		assert.Equal(t, want, got, in)
	}

	for _, bad := range []string{"", "pds.example/mcp", "https://pds.example/mcp#x", "https://pds.example/mcp?x=1", "https://u@pds.example/mcp"} {
		_, ok := oauth.CanonicalResource(bad)
		assert.False(t, ok, bad)
	}
}

func TestNamesAreCleanedBeforeTheyAreKeptOrShown(t *testing.T) {
	assert.Equal(t, "Claude Code", oauth.DisplayName("  Claude\u0000 Code\n"))
	assert.Equal(t, "Visual Studio Code", oauth.DisplayName("Visual  Studio   Code"))
	assert.Equal(t, strings.Repeat("a", 64), oauth.DisplayName(strings.Repeat("a", 200)))
	assert.Equal(t, "", oauth.DisplayName("\u202e\u200b"), "direction overrides and zero width marks are dropped")

	assert.Equal(t, "Claude Code", oauth.AgentName("Claude Code", "claude.ai"))
	assert.Equal(t, "script alert 1 script", oauth.AgentName("<script>alert(1)</script>", "x.example"))
	assert.Equal(t, "app.example", oauth.AgentName("\u202e", "app.example"))
	assert.True(t, auth.ValidAgent(oauth.AgentName("!!!", "")), "even nothing usable yields a valid agent name")
	assert.True(t, auth.ValidAgent(oauth.AgentName(strings.Repeat("a b ", 50), "")))
}

func TestATokenIsBoundToTheMCPEndpointAndTheCredentialRoute(t *testing.T) {
	for _, ok := range []string{"/mcp", "/mcp/", "/api/databases/pds.orders/db-1/credential"} {
		assert.True(t, oauth.TokenRoute(ok), ok)
	}
	for _, bad := range []string{
		"", "/", "/mcpx", "/mcp/tools", "/mcp/../api/databases", "/api/databases", "/api/databases/a/b",
		"/api/databases/a/credential", "/api/databases/a/b/credential/x", "/api/databases//b/credential", "/api/databases/../b/credential", "/api/databases/a/../credential",
		"/api/environments", "/oauth/token",
	} {
		assert.False(t, oauth.TokenRoute(bad), bad)
	}
}

func TestOnlyTheCredentialRouteIsTheCredentialRoute(t *testing.T) {
	assert.True(t, oauth.CredentialRoute("/api/databases/pds.orders/db-1/credential"))
	assert.False(t, oauth.CredentialRoute("/mcp"))
	assert.False(t, oauth.CredentialRoute("/api/databases/a/b"))
}
