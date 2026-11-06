package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/clientmeta"
	"github.com/percona/pds/adapter/internal/oauth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
	"github.com/percona/pds/adapter/internal/testutil"
)

const (
	oauthIssuer   = "https://pds.example"
	oauthResource = "https://pds.example/mcp"
	cimdClient    = "https://claude.example/oauth/client.json"
	loopback      = "http://localhost:51234/callback"
	verifier      = "0123456789012345678901234567890123456789012"
)

func challenge(v string) string {
	sum := sha256.Sum256([]byte(v))

	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type oauthRig struct {
	svc      *service.OAuth
	rows     *testutil.MemoryOAuthStore
	people   *fakeOAuthPeople
	limits   *fakeOAuthLimits
	docs     *fakeClientDocs
	now      time.Time
	client   string
	redirect string
	user     auth.Identity
	admin    auth.Identity
	address  string
}

func newOAuthRig(t *testing.T, opts ...service.OAuthOption) *oauthRig {
	t.Helper()

	r := &oauthRig{
		rows: testutil.NewMemoryOAuthStore(),
		now:  time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC),
		people: &fakeOAuthPeople{
			placements: map[uint]store.Placement{
				7: {UserID: 7, Username: "Ada", OrganizationID: 3, OrganizationName: "Acme", Role: auth.RoleUser, TeamID: 5, TeamName: "payments"},
				8: {UserID: 8, Username: "Grace", OrganizationID: 3, OrganizationName: "Acme", Role: auth.RoleAdmin, TeamID: 5, TeamName: "payments"},
			},
			teams: map[uint][]auth.Team{3: {{ID: 5, Name: "payments"}, {ID: 6, Name: "search"}}},
		},
		limits: &fakeOAuthLimits{settings: store.AISettings{Access: "change_delete", Passwords: true, SQLWrites: true}},
		docs: &fakeClientDocs{docs: map[string]clientmeta.Document{
			cimdClient: {ClientID: cimdClient, ClientName: "Claude Code", RedirectURIs: []string{"http://localhost/callback", "http://127.0.0.1/callback"}},
		}},
		address: "203.0.113.9",
		client:  cimdClient, redirect: loopback,
	}
	scope := func(p store.Placement) auth.Scope {
		return auth.Scope{
			UserID: p.UserID, UserName: p.Username, OrganizationID: p.OrganizationID,
			OrganizationName: p.OrganizationName, Role: p.Role, Team: auth.Team{ID: p.TeamID, Name: p.TeamName},
		}
	}
	r.user = auth.Identity{Subject: "user:7", Scope: scope(r.people.placements[7]), Method: store.SignInOIDC}
	r.admin = auth.Identity{Subject: "user:8", Scope: scope(r.people.placements[8]), Method: store.SignInOIDC}

	all := append([]service.OAuthOption{
		service.WithOAuthClock(func() time.Time { return r.now }),
	}, opts...)
	r.svc = service.NewOAuth(r.rows, r.people, r.limits, r.docs, oauthIssuer, all...)

	return r
}

func (r *oauthRig) request(overrides map[string]string) service.AuthorizationRequest {
	req := service.AuthorizationRequest{
		ClientID: r.client, RedirectURI: r.redirect, ResponseType: "code",
		CodeChallenge: challenge(verifier), CodeChallengeMethod: "S256", State: "state-1",
		Scope: "pds:read pds:change", Resource: oauthResource,
	}
	for k, v := range overrides {
		switch k {
		case "client_id":
			req.ClientID = v
		case "redirect_uri":
			req.RedirectURI = v
		case "response_type":
			req.ResponseType = v
		case "code_challenge":
			req.CodeChallenge = v
		case "code_challenge_method":
			req.CodeChallengeMethod = v
		case "state":
			req.State = v
		case "scope":
			req.Scope = v
		case "resource":
			req.Resource = v
		}
	}

	return req
}

// consented walks a request through authorize and consent and returns the
// code the redirect carried.
func (r *oauthRig) consented(t *testing.T, who auth.Identity, overrides map[string]string, consent service.Consent) string {
	t.Helper()

	token, aerr := r.svc.StartAuthorization(t.Context(), who, r.request(overrides))
	require.Nil(t, aerr)
	redirect, err := r.svc.Approve(t.Context(), who, token, consent)
	require.NoError(t, err)

	u, err := url.Parse(redirect)
	require.NoError(t, err)
	code := u.Query().Get("code")
	require.NotEmpty(t, code)
	assert.Equal(t, "state-1", u.Query().Get("state"))
	assert.Equal(t, oauthIssuer, u.Query().Get("iss"))

	return code
}

func (r *oauthRig) consentFor(who auth.Identity, g oauth.Grants) service.Consent {
	return service.Consent{OrganizationID: who.Scope.OrganizationID, TeamID: who.Scope.Team.ID, Grants: g}
}

func (r *oauthRig) exchange(code string) (service.TokenResponse, *service.OAuthError) {
	return r.svc.ExchangeCode(context.Background(), r.address, service.TokenRequest{
		ClientID: r.client, Code: code, RedirectURI: r.redirect, CodeVerifier: verifier, Resource: oauthResource,
	})
}

// useLocalClient makes the rig speak as the PDS local client, the only one a
// person may let fetch passwords or write with SQL.
func (r *oauthRig) useLocalClient() {
	r.client, r.redirect = oauth.LocalClientID, "http://127.0.0.1:51234/callback"
}

func (r *oauthRig) tokens(t *testing.T) service.TokenResponse {
	t.Helper()

	code := r.consented(t, r.user, nil, r.consentFor(r.user, oauth.Grants{Access: auth.AccessChange}))
	resp, err := r.exchange(code)
	require.Nil(t, err)

	return resp
}

func (r *oauthRig) refresh(token string) (service.TokenResponse, *service.OAuthError) {
	return r.svc.Refresh(context.Background(), r.address, service.RefreshRequest{ClientID: r.client, RefreshToken: token, Resource: oauthResource})
}

func TestTheFullFlowEndsInTokensThatResolveToTheApprovingPerson(t *testing.T) {
	r := newOAuthRig(t)

	resp := r.tokens(t)

	assert.Equal(t, "Bearer", resp.TokenType)
	assert.Equal(t, 3600, resp.ExpiresIn)
	assert.NotEmpty(t, resp.AccessToken)
	assert.NotEmpty(t, resp.RefreshToken)
	assert.NotEqual(t, resp.AccessToken, resp.RefreshToken)

	agent, err := r.svc.Authenticate(t.Context(), resp.AccessToken, "/mcp")
	require.NoError(t, err)
	assert.Equal(t, auth.AccessChange, agent.Caller.Access)
	assert.Equal(t, "Claude Code", agent.Caller.Agent)
	assert.Equal(t, auth.ClientMCP, agent.Caller.Identity.Client.Kind)
	assert.Equal(t, "Claude Code", agent.Caller.Identity.Client.Agent)
	assert.Equal(t, uint(7), agent.Caller.Identity.Scope.UserID)
	assert.Equal(t, uint(3), agent.Caller.Identity.Scope.OrganizationID)
	assert.Equal(t, uint(5), agent.Caller.Identity.Scope.Team.ID)
	assert.Equal(t, auth.RoleUser, agent.Caller.Identity.Scope.Role)
	assert.Equal(t, "user:7", agent.Caller.Identity.Subject)
	assert.Equal(t, "claude.example", agent.ClientHost)
	assert.NotEmpty(t, agent.SessionID)
}

func TestACodeWithTheWrongPKCEVerifierIsRefused(t *testing.T) {
	r := newOAuthRig(t)
	code := r.consented(t, r.user, nil, r.consentFor(r.user, oauth.Grants{Access: auth.AccessRead}))

	_, err := r.svc.ExchangeCode(t.Context(), r.address, service.TokenRequest{
		ClientID: cimdClient, Code: code, RedirectURI: loopback, CodeVerifier: strings.Repeat("x", 43), Resource: oauthResource,
	})

	require.NotNil(t, err)
	assert.Equal(t, "invalid_grant", err.Code)
	assert.Zero(t, r.rows.SessionCount(), "no session was made")

	_, again := r.exchange(code)
	require.NotNil(t, again, "the failed attempt spent the code, so guessing verifiers is not possible")
	assert.Equal(t, "invalid_grant", again.Code)
}

func TestACodeBoundToOneClientAndRedirectIsRefusedForAnother(t *testing.T) {
	r := newOAuthRig(t)

	for name, mutate := range map[string]func(*service.TokenRequest){
		"another client":   func(q *service.TokenRequest) { q.ClientID = "dcr_other" },
		"another redirect": func(q *service.TokenRequest) { q.RedirectURI = "http://localhost:51235/callback" },
		"no verifier":      func(q *service.TokenRequest) { q.CodeVerifier = "" },
		"no code":          func(q *service.TokenRequest) { q.Code = "" },
	} {
		code := r.consented(t, r.user, nil, r.consentFor(r.user, oauth.Grants{Access: auth.AccessRead}))
		q := service.TokenRequest{ClientID: cimdClient, Code: code, RedirectURI: loopback, CodeVerifier: verifier, Resource: oauthResource}
		mutate(&q)

		_, err := r.svc.ExchangeCode(t.Context(), r.address, q)

		require.NotNil(t, err, name)
		assert.Contains(t, []string{"invalid_grant", "invalid_request"}, err.Code, name)
	}
	assert.Zero(t, r.rows.SessionCount())
}

func TestARedirectURIThatDoesNotMatchIsRefusedBeforeAnyRedirect(t *testing.T) {
	r := newOAuthRig(t)

	for _, bad := range []string{
		"https://evil.example/callback",
		"http://localhost:5/other",
		"http://evil.example/callback",
		"",
	} {
		token, err := r.svc.StartAuthorization(t.Context(), r.user, r.request(map[string]string{"redirect_uri": bad}))

		require.NotNil(t, err, bad)
		assert.Empty(t, err.RedirectURI, "an error for %q must not be sent to it", bad)
		assert.Empty(t, token)
	}
}

func TestAnUnknownOrUnsafeClientIsRefusedWithoutARedirect(t *testing.T) {
	r := newOAuthRig(t)

	for _, id := range []string{"", "dcr_unknown", "plain-name", "http://claude.example/oauth/client.json", "https://127.0.0.1/c.json"} {
		_, err := r.svc.StartAuthorization(t.Context(), r.user, r.request(map[string]string{"client_id": id}))

		require.NotNil(t, err, id)
		assert.Empty(t, err.RedirectURI, id)
	}

	r.docs.err = clientmeta.ErrNotPublic
	_, err := r.svc.StartAuthorization(t.Context(), r.user, r.request(nil))
	require.NotNil(t, err, "a document on a private address stops the request")
	assert.Empty(t, err.RedirectURI)
}

func TestProblemsAfterTheRedirectIsTrustedAreReportedToIt(t *testing.T) {
	r := newOAuthRig(t)

	for name, tc := range map[string]struct {
		overrides map[string]string
		code      string
	}{
		"no PKCE":          {map[string]string{"code_challenge": ""}, "invalid_request"},
		"plain PKCE":       {map[string]string{"code_challenge_method": "plain"}, "invalid_request"},
		"no method":        {map[string]string{"code_challenge_method": ""}, "invalid_request"},
		"short challenge":  {map[string]string{"code_challenge": "abc"}, "invalid_request"},
		"implicit":         {map[string]string{"response_type": "token"}, "unsupported_response_type"},
		"another resource": {map[string]string{"resource": "https://other.example/mcp"}, "invalid_target"},
		"unknown scope":    {map[string]string{"scope": "pds:read admin"}, "invalid_scope"},
	} {
		_, err := r.svc.StartAuthorization(t.Context(), r.user, r.request(tc.overrides))

		require.NotNil(t, err, name)
		assert.Equal(t, tc.code, err.Code, name)
		assert.Equal(t, loopback, err.RedirectURI, name)
		assert.Equal(t, "state-1", err.State, name)
	}
}

func TestTheResourceMayBeAbsentOrWrittenInAnyCanonicalForm(t *testing.T) {
	r := newOAuthRig(t)

	for _, resource := range []string{"", "https://PDS.example/mcp/", "https://pds.example:443/mcp"} {
		_, err := r.svc.StartAuthorization(t.Context(), r.user, r.request(map[string]string{"resource": resource}))
		assert.Nil(t, err, resource)
	}
}

func TestCheckAuthorizationNeverFetchesAndNeverRedirects(t *testing.T) {
	r := newOAuthRig(t)

	require.Nil(t, r.svc.CheckAuthorization(r.request(nil)))
	assert.Empty(t, r.docs.called, "no document is fetched before the person has signed in")

	for _, overrides := range []map[string]string{
		{"client_id": ""}, {"redirect_uri": "javascript:alert(1)"}, {"response_type": "token"},
		{"code_challenge_method": "plain"}, {"resource": "https://other.example/mcp"}, {"scope": "nope"},
		{"state": strings.Repeat("s", 3000)},
	} {
		err := r.svc.CheckAuthorization(r.request(overrides))
		require.NotNil(t, err, overrides)
		assert.Empty(t, err.RedirectURI, "nothing was validated against a client yet, so nothing is redirected to")
	}
}

func TestACodeWorksOnceAndAReplayEndsTheSessionItBought(t *testing.T) {
	r := newOAuthRig(t)
	code := r.consented(t, r.user, nil, r.consentFor(r.user, oauth.Grants{Access: auth.AccessRead}))

	first, err := r.exchange(code)
	require.Nil(t, err)
	_, still := r.svc.Authenticate(t.Context(), first.AccessToken, "/mcp")
	require.NoError(t, still)

	_, replay := r.exchange(code)

	require.NotNil(t, replay)
	assert.Equal(t, "invalid_grant", replay.Code)
	_, gone := r.svc.Authenticate(t.Context(), first.AccessToken, "/mcp")
	require.Error(t, gone, "a replayed code revokes what the first use made")
	assert.Zero(t, r.rows.SessionCount())
}

func TestACodeExpiresAfterAMinute(t *testing.T) {
	r := newOAuthRig(t)
	code := r.consented(t, r.user, nil, r.consentFor(r.user, oauth.Grants{Access: auth.AccessRead}))

	r.now = r.now.Add(61 * time.Second)
	_, err := r.exchange(code)

	require.NotNil(t, err)
	assert.Equal(t, "invalid_grant", err.Code)
}

func TestARefreshTokenRotatesAndTheOldOneStopsWorking(t *testing.T) {
	r := newOAuthRig(t)
	first := r.tokens(t)

	second, err := r.refresh(first.RefreshToken)
	require.Nil(t, err)
	assert.NotEqual(t, first.RefreshToken, second.RefreshToken)
	assert.NotEqual(t, first.AccessToken, second.AccessToken)
	assert.Equal(t, first.Scope, second.Scope)
	_, authErr := r.svc.Authenticate(t.Context(), second.AccessToken, "/mcp")
	require.NoError(t, authErr)

	_, old := r.refresh(first.RefreshToken)

	require.NotNil(t, old)
	assert.Equal(t, "invalid_grant", old.Code)
}

func TestReusingARetiredRefreshTokenEndsTheWholeSession(t *testing.T) {
	r := newOAuthRig(t)
	first := r.tokens(t)
	second, err := r.refresh(first.RefreshToken)
	require.Nil(t, err)

	_, reuse := r.refresh(first.RefreshToken)

	require.NotNil(t, reuse)
	_, stillAccess := r.svc.Authenticate(t.Context(), second.AccessToken, "/mcp")
	require.Error(t, stillAccess, "the newest access token died with the session")
	_, stillRefresh := r.refresh(second.RefreshToken)
	require.NotNil(t, stillRefresh, "and so did the newest refresh token")
	assert.Equal(t, "invalid_grant", stillRefresh.Code)
}

func TestARefreshNeedsTheClientItWasIssuedTo(t *testing.T) {
	r := newOAuthRig(t)
	tokens := r.tokens(t)

	_, err := r.svc.Refresh(t.Context(), r.address, service.RefreshRequest{ClientID: "dcr_other", RefreshToken: tokens.RefreshToken})

	require.NotNil(t, err)
	assert.Equal(t, "invalid_grant", err.Code)
	_, works := r.refresh(tokens.RefreshToken)
	assert.Nil(t, works, "the refused attempt did not spend the token")
}

func TestAnAccessTokenIsRefusedAsARefreshTokenAndTheReverse(t *testing.T) {
	r := newOAuthRig(t)
	tokens := r.tokens(t)

	_, err := r.refresh(tokens.AccessToken)
	require.NotNil(t, err)
	assert.Equal(t, "invalid_grant", err.Code)

	_, authErr := r.svc.Authenticate(t.Context(), tokens.RefreshToken, "/mcp")
	require.Error(t, authErr, "a refresh token does not open /mcp")
}

func TestAnAccessTokenExpiresAfterAnHour(t *testing.T) {
	r := newOAuthRig(t)
	tokens := r.tokens(t)

	r.now = r.now.Add(time.Hour - time.Second)
	_, err := r.svc.Authenticate(t.Context(), tokens.AccessToken, "/mcp")
	require.NoError(t, err)

	r.now = r.now.Add(2 * time.Second)
	_, err = r.svc.Authenticate(t.Context(), tokens.AccessToken, "/mcp")
	require.ErrorIs(t, err, service.ErrTokenInvalid)
}

func TestRefreshFailsAfterSevenIdleDays(t *testing.T) {
	r := newOAuthRig(t)
	tokens := r.tokens(t)

	r.now = r.now.Add(7*24*time.Hour + time.Minute)
	_, err := r.refresh(tokens.RefreshToken)

	require.NotNil(t, err)
	assert.Equal(t, "invalid_grant", err.Code)
	assert.Zero(t, r.rows.SessionCount(), "the dead session is removed")
}

func TestUseKeepsASessionFromGoingIdleButNotPastThirtyDays(t *testing.T) {
	r := newOAuthRig(t)
	tokens := r.tokens(t)

	for range 4 {
		r.now = r.now.Add(6 * 24 * time.Hour)
		next, err := r.refresh(tokens.RefreshToken)
		require.Nil(t, err, "a refresh every six days keeps the session alive")
		tokens = next
	}
	r.now = r.now.Add(5 * 24 * time.Hour)
	tokens, _ = r.refresh(tokens.RefreshToken)
	require.NotEmpty(t, tokens.RefreshToken, "day 29 is inside the limit")

	r.now = r.now.Add(2 * 24 * time.Hour)
	_, err := r.refresh(tokens.RefreshToken)
	require.NotNil(t, err, "day 31 is past the absolute limit, however recently it was used")
	assert.Equal(t, "invalid_grant", err.Code)
}

func TestAnAccessTokenNeverOutlivesItsSession(t *testing.T) {
	r := newOAuthRig(t)
	tokens := r.tokens(t)

	r.now = r.now.Add(29*24*time.Hour + 30*time.Minute)
	for range 100 {
		next, err := r.refresh(tokens.RefreshToken)
		if err != nil {
			break
		}
		tokens = next
		r.now = r.now.Add(10 * time.Minute)
	}

	r.now = r.now.Add(time.Hour)
	_, err := r.svc.Authenticate(t.Context(), tokens.AccessToken, "/mcp")
	require.Error(t, err)
}

func TestTheTokenResponseScopesAreTheSessionsGrants(t *testing.T) {
	r := newOAuthRig(t)
	r.useLocalClient()

	for name, tc := range map[string]struct {
		grants oauth.Grants
		scope  string
	}{
		"read":        {oauth.Grants{Access: auth.AccessRead}, "pds:read"},
		"change":      {oauth.Grants{Access: auth.AccessChange}, "pds:read pds:change"},
		"everything":  {oauth.Grants{Access: auth.AccessChangeDelete, Passwords: true, SQLWrites: true}, "pds:read pds:change pds:delete pds:credentials pds:sql_write"},
		"credentials": {oauth.Grants{Access: auth.AccessRead, Passwords: true}, "pds:read pds:credentials"},
	} {
		code := r.consented(t, r.admin, map[string]string{"scope": ""}, r.consentFor(r.admin, tc.grants))
		resp, err := r.exchange(code)
		require.Nil(t, err, name)
		assert.Equal(t, tc.scope, resp.Scope, name)

		refreshed, err := r.refresh(resp.RefreshToken)
		require.Nil(t, err, name)
		assert.Equal(t, tc.scope, refreshed.Scope, "a refresh answers the same scopes: "+name)

		agent, authErr := r.svc.Authenticate(t.Context(), resp.AccessToken, "/mcp")
		require.NoError(t, authErr, name)
		assert.Equal(t, strings.Fields(tc.scope), agent.Scopes, name)
		assert.Equal(t, tc.grants.Passwords, agent.Passwords, name)
		assert.Equal(t, tc.grants.SQLWrites, agent.SQLWrites, name)
	}
}

func TestATokenIsRefusedOnEveryRouteButMCPAndTheCredentialRoute(t *testing.T) {
	r := newOAuthRig(t)
	tokens := r.tokens(t)

	for _, ok := range []string{"/mcp", "/mcp/"} {
		_, err := r.svc.Authenticate(t.Context(), tokens.AccessToken, ok)
		assert.NoError(t, err, ok)
	}

	_, scopeErr := r.svc.Authenticate(t.Context(), tokens.AccessToken, "/api/databases/pds.orders/db-1/credential")
	require.ErrorIs(t, scopeErr, service.ErrTokenScope, "the credential route is bound, and needs the passwords grant")

	for _, refused := range []string{
		"/api/databases", "/api/databases/pds.orders/db-1", "/api/databases/pds.orders/db-1/credential/extra",
		"/api/databases/a/credential", "/api/environments", "/api/auth/session", "/api/organization/ai",
		"/api/chat", "/oauth/token", "/mcpx", "/mcp/../api/databases", "/", "",
	} {
		_, err := r.svc.Authenticate(t.Context(), tokens.AccessToken, refused)
		require.ErrorIs(t, err, service.ErrTokenAudience, refused)
	}
}

func TestAnUnknownOrEmptyTokenIsInvalid(t *testing.T) {
	r := newOAuthRig(t)

	for _, token := range []string{"", "nonsense", strings.Repeat("a", 43)} {
		_, err := r.svc.Authenticate(t.Context(), token, "/mcp")
		require.ErrorIs(t, err, service.ErrTokenInvalid, token)
	}
}

func TestRevokingTheSessionEndsAccessAtOnce(t *testing.T) {
	r := newOAuthRig(t)
	tokens := r.tokens(t)

	require.Nil(t, r.svc.Revoke(t.Context(), r.address, service.RevokeRequest{Token: tokens.RefreshToken, ClientID: cimdClient}))

	_, err := r.svc.Authenticate(t.Context(), tokens.AccessToken, "/mcp")
	require.ErrorIs(t, err, service.ErrTokenInvalid)
	_, refreshErr := r.refresh(tokens.RefreshToken)
	require.NotNil(t, refreshErr)
	assert.Equal(t, "invalid_grant", refreshErr.Code)
}

func TestRevokingAnAccessTokenEndsTheSessionToo(t *testing.T) {
	r := newOAuthRig(t)
	tokens := r.tokens(t)

	require.Nil(t, r.svc.Revoke(t.Context(), r.address, service.RevokeRequest{Token: tokens.AccessToken}))

	assert.Zero(t, r.rows.SessionCount())
}

func TestRevocationAnswersTheSameForAnUnknownTokenAndRefusesAnotherClients(t *testing.T) {
	r := newOAuthRig(t)
	tokens := r.tokens(t)

	assert.Nil(t, r.svc.Revoke(t.Context(), r.address, service.RevokeRequest{Token: "never-issued"}))
	assert.NotNil(t, r.svc.Revoke(t.Context(), r.address, service.RevokeRequest{Token: ""}))

	err := r.svc.Revoke(t.Context(), r.address, service.RevokeRequest{Token: tokens.RefreshToken, ClientID: "dcr_other"})
	require.NotNil(t, err)
	assert.Equal(t, "invalid_client", err.Code)
	assert.Equal(t, 1, r.rows.SessionCount(), "another client cannot end the session")
}

func TestRemovingThePersonFromTheOrganizationEndsTheirAgentsAccess(t *testing.T) {
	r := newOAuthRig(t)
	tokens := r.tokens(t)

	r.people.remove(7)

	_, err := r.svc.Authenticate(t.Context(), tokens.AccessToken, "/mcp")
	require.ErrorIs(t, err, service.ErrTokenInvalid)
	_, refreshErr := r.refresh(tokens.RefreshToken)
	require.NotNil(t, refreshErr)
	assert.Equal(t, "invalid_grant", refreshErr.Code)
}

func TestAUserMovedToAnotherTeamLosesTheTeamTheAgentWasBoundTo(t *testing.T) {
	r := newOAuthRig(t)
	tokens := r.tokens(t)

	moved := r.people.placements[7]
	moved.TeamID, moved.TeamName = 6, "search"
	r.people.placements[7] = moved

	_, err := r.svc.Authenticate(t.Context(), tokens.AccessToken, "/mcp")
	require.ErrorIs(t, err, service.ErrTokenInvalid)
}

func TestLoweringTheOrganizationLimitLowersWhatLiveTokensMayDo(t *testing.T) {
	r := newOAuthRig(t)
	r.useLocalClient()
	code := r.consented(t, r.user, map[string]string{"scope": ""},
		r.consentFor(r.user, oauth.Grants{Access: auth.AccessChangeDelete, Passwords: true, SQLWrites: true}))
	resp, err := r.exchange(code)
	require.Nil(t, err)

	r.limits.set(store.AISettings{Access: "read", Passwords: false, SQLWrites: true})
	agent, authErr := r.svc.Authenticate(t.Context(), resp.AccessToken, "/mcp")

	require.NoError(t, authErr)
	assert.Equal(t, auth.AccessRead, agent.Caller.Access)
	assert.False(t, agent.Passwords)
	assert.True(t, agent.SQLWrites)
	assert.Equal(t, []string{"pds:read", "pds:sql_write"}, agent.Scopes,
		"the scopes a request sees are what it may use now")
}

func TestConsentCannotGrantMoreThanTheOrganizationAllowsOrTheRequestAsked(t *testing.T) {
	r := newOAuthRig(t)
	r.limits.set(store.AISettings{Access: "change", Passwords: false, SQLWrites: false})

	for name, tc := range map[string]struct {
		scope  string
		grants oauth.Grants
	}{
		"above the organization tier":  {"", oauth.Grants{Access: auth.AccessChangeDelete}},
		"passwords not allowed":        {"", oauth.Grants{Access: auth.AccessRead, Passwords: true}},
		"sql writes not allowed":       {"", oauth.Grants{Access: auth.AccessRead, SQLWrites: true}},
		"above what the client asked":  {"pds:read", oauth.Grants{Access: auth.AccessChange}},
		"an access tier nobody knows":  {"", oauth.Grants{Access: "root"}},
		"credentials never requested":  {"pds:read", oauth.Grants{Access: auth.AccessRead, Passwords: true}},
		"sql writes never requested":   {"pds:change", oauth.Grants{Access: auth.AccessChange, SQLWrites: true}},
		"no tier at all":               {"", oauth.Grants{}},
		"above the role's own ceiling": {"", oauth.Grants{Access: auth.AccessChangeDelete, Passwords: true}},
	} {
		token, aerr := r.svc.StartAuthorization(t.Context(), r.user, r.request(map[string]string{"scope": tc.scope}))
		require.Nil(t, aerr, name)

		_, err := r.svc.Approve(t.Context(), r.user, token, r.consentFor(r.user, tc.grants))

		require.ErrorIs(t, err, service.ErrConsentGrants, name)
	}
}

func TestApprovingNeedsMembershipAndATeamThePersonMayActIn(t *testing.T) {
	r := newOAuthRig(t)
	grants := oauth.Grants{Access: auth.AccessRead}
	start := func(who auth.Identity) string {
		token, err := r.svc.StartAuthorization(t.Context(), who, r.request(nil))
		require.Nil(t, err)

		return token
	}

	_, err := r.svc.Approve(t.Context(), r.user, start(r.user), service.Consent{OrganizationID: 4, TeamID: 5, Grants: grants})
	require.ErrorIs(t, err, service.ErrConsentTarget, "an Organization the person is not in")

	_, err = r.svc.Approve(t.Context(), r.user, start(r.user), service.Consent{OrganizationID: 3, TeamID: 6, Grants: grants})
	require.ErrorIs(t, err, service.ErrConsentTarget, "a User acts in their own Team only")

	_, err = r.svc.Approve(t.Context(), r.admin, start(r.admin), service.Consent{OrganizationID: 3, TeamID: 6, Grants: grants})
	require.NoError(t, err, "an admin may pick any Team of the Organization")

	_, err = r.svc.Approve(t.Context(), r.admin, start(r.admin), service.Consent{OrganizationID: 3, TeamID: 99, Grants: grants})
	require.ErrorIs(t, err, service.ErrConsentTarget, "a Team that is not in the Organization")

	_, err = r.svc.Approve(t.Context(), auth.Identity{}, start(r.user), service.Consent{OrganizationID: 3, TeamID: 5, Grants: grants})
	require.ErrorIs(t, err, service.ErrAuthorizationGone, "the operator token is no person and cannot consent")
}

func TestARefusedConsentLeavesTheRequestWaiting(t *testing.T) {
	r := newOAuthRig(t)
	token, _ := r.svc.StartAuthorization(t.Context(), r.user, r.request(nil))

	_, err := r.svc.Approve(t.Context(), r.user, token, r.consentFor(r.user, oauth.Grants{Access: auth.AccessChangeDelete}))
	require.ErrorIs(t, err, service.ErrConsentGrants)

	_, err = r.svc.Approve(t.Context(), r.user, token, r.consentFor(r.user, oauth.Grants{Access: auth.AccessRead}))
	require.NoError(t, err, "the person can correct the choice and try again")
}

func TestOnlyThePersonWhoSignedInCanDecideAParkedRequest(t *testing.T) {
	r := newOAuthRig(t)
	token, _ := r.svc.StartAuthorization(t.Context(), r.user, r.request(nil))

	_, err := r.svc.Approve(t.Context(), r.admin, token, r.consentFor(r.admin, oauth.Grants{Access: auth.AccessRead}))
	require.ErrorIs(t, err, service.ErrAuthorizationGone)
	_, err = r.svc.Deny(t.Context(), r.admin, token)
	require.ErrorIs(t, err, service.ErrAuthorizationGone)
	_, err = r.svc.Pending(t.Context(), r.admin, token)
	require.ErrorIs(t, err, service.ErrAuthorizationGone)

	_, err = r.svc.Approve(t.Context(), r.user, "made-up-token", r.consentFor(r.user, oauth.Grants{Access: auth.AccessRead}))
	require.ErrorIs(t, err, service.ErrAuthorizationGone)
}

func TestAParkedRequestCanBeApprovedOnceAndExpiresAfterTenMinutes(t *testing.T) {
	r := newOAuthRig(t)
	consent := r.consentFor(r.user, oauth.Grants{Access: auth.AccessRead})

	token, _ := r.svc.StartAuthorization(t.Context(), r.user, r.request(nil))
	_, err := r.svc.Approve(t.Context(), r.user, token, consent)
	require.NoError(t, err)
	_, err = r.svc.Approve(t.Context(), r.user, token, consent)
	require.ErrorIs(t, err, service.ErrAuthorizationGone, "a second approval makes no second code")

	token, _ = r.svc.StartAuthorization(t.Context(), r.user, r.request(nil))
	r.now = r.now.Add(11 * time.Minute)
	_, err = r.svc.Approve(t.Context(), r.user, token, consent)
	require.ErrorIs(t, err, service.ErrAuthorizationGone)
}

func TestPendingNamesWhatTheConsentScreenMustShow(t *testing.T) {
	r := newOAuthRig(t)
	token, _ := r.svc.StartAuthorization(t.Context(), r.user, r.request(nil))

	pending, err := r.svc.Pending(t.Context(), r.user, token)

	require.NoError(t, err)
	assert.Equal(t, "Claude Code", pending.ClientName)
	assert.Equal(t, "claude.example", pending.ClientHost)
	assert.Equal(t, "cimd", pending.ClientKind)
	assert.Equal(t, "localhost", pending.RedirectHost)
	assert.True(t, pending.LoopbackRedirect)
	assert.Equal(t, auth.AccessChange, pending.Requested.Access)
	assert.Equal(t, r.now.Add(10*time.Minute), pending.ExpiresAt)
}

func TestDenyingSendsAccessDeniedToTheRedirectWithTheStateAndIssuer(t *testing.T) {
	r := newOAuthRig(t)
	token, _ := r.svc.StartAuthorization(t.Context(), r.user, r.request(nil))

	redirect, err := r.svc.Deny(t.Context(), r.user, token)

	require.NoError(t, err)
	u, _ := url.Parse(redirect)
	assert.Equal(t, "localhost:51234", u.Host)
	assert.Equal(t, "access_denied", u.Query().Get("error"))
	assert.Equal(t, "state-1", u.Query().Get("state"))
	assert.Equal(t, oauthIssuer, u.Query().Get("iss"))
	assert.Empty(t, u.Query().Get("code"))
}

func TestARedirectKeepsTheQueryTheClientRegistered(t *testing.T) {
	r := newOAuthRig(t)
	r.docs.docs[cimdClient] = clientmeta.Document{
		ClientID: cimdClient, ClientName: "Claude", RedirectURIs: []string{"https://claude.example/cb?app=1"},
	}
	token, err := r.svc.StartAuthorization(t.Context(), r.user, r.request(map[string]string{"redirect_uri": "https://claude.example/cb?app=1"}))
	require.Nil(t, err)

	redirect, aerr := r.svc.Approve(t.Context(), r.user, token, r.consentFor(r.user, oauth.Grants{Access: auth.AccessRead}))

	require.NoError(t, aerr)
	u, _ := url.Parse(redirect)
	assert.Equal(t, "1", u.Query().Get("app"))
	assert.NotEmpty(t, u.Query().Get("code"))
}

func TestARegisteredClientAuthorizesAndExchangesLikeADocumentClient(t *testing.T) {
	r := newOAuthRig(t)
	client, err := r.svc.Register(t.Context(), r.address, service.ClientRegistration{
		ClientName: "Cursor", RedirectURIs: []string{"http://localhost:8787/callback"},
		TokenEndpointAuthMethod: "none", GrantTypes: []string{"authorization_code", "refresh_token"},
	})
	require.Nil(t, err)
	assert.True(t, strings.HasPrefix(client.ClientID, "dcr_"))
	assert.Equal(t, "Cursor", client.ClientName)

	req := r.request(map[string]string{"client_id": client.ClientID, "redirect_uri": "http://localhost:8787/callback"})
	token, aerr := r.svc.StartAuthorization(t.Context(), r.user, req)
	require.Nil(t, aerr)
	redirect, perr := r.svc.Approve(t.Context(), r.user, token, r.consentFor(r.user, oauth.Grants{Access: auth.AccessRead}))
	require.NoError(t, perr)
	u, _ := url.Parse(redirect)

	pending, _ := r.svc.StartAuthorization(t.Context(), r.user,
		r.request(map[string]string{"client_id": client.ClientID, "redirect_uri": "http://localhost:9999/callback"}))
	assert.NotEmpty(t, pending, "the loopback port is free")
	_, other := r.svc.StartAuthorization(t.Context(), r.user,
		r.request(map[string]string{"client_id": client.ClientID, "redirect_uri": "http://localhost:8787/other"}))
	require.NotNil(t, other, "the path is not")

	resp, xerr := r.svc.ExchangeCode(t.Context(), r.address, service.TokenRequest{
		ClientID: client.ClientID, Code: u.Query().Get("code"), RedirectURI: "http://localhost:8787/callback", CodeVerifier: verifier,
	})
	require.Nil(t, xerr)
	agent, err2 := r.svc.Authenticate(t.Context(), resp.AccessToken, "/mcp")
	require.NoError(t, err2)
	assert.Equal(t, "Cursor", agent.Caller.Agent)
	assert.Equal(t, "dcr", agent.ClientKind)
	assert.Empty(t, agent.ClientHost)
	assert.Empty(t, r.docs.called, "a registered client needs no fetch")
}

func TestRegistrationRefusesWhatItCannotServe(t *testing.T) {
	r := newOAuthRig(t)

	for name, tc := range map[string]struct {
		in   service.ClientRegistration
		code string
	}{
		"no redirect":          {service.ClientRegistration{ClientName: "A"}, "invalid_redirect_uri"},
		"an http redirect":     {service.ClientRegistration{RedirectURIs: []string{"http://evil.example/cb"}}, "invalid_redirect_uri"},
		"a custom scheme":      {service.ClientRegistration{RedirectURIs: []string{"cursor://cb"}}, "invalid_redirect_uri"},
		"too many redirects":   {service.ClientRegistration{RedirectURIs: manyRedirects(11)}, "invalid_redirect_uri"},
		"a client secret":      {service.ClientRegistration{RedirectURIs: []string{"http://localhost/cb"}, TokenEndpointAuthMethod: "client_secret_basic"}, "invalid_client_metadata"},
		"the implicit grant":   {service.ClientRegistration{RedirectURIs: []string{"http://localhost/cb"}, GrantTypes: []string{"implicit"}}, "invalid_client_metadata"},
		"a token response":     {service.ClientRegistration{RedirectURIs: []string{"http://localhost/cb"}, ResponseTypes: []string{"token"}}, "invalid_client_metadata"},
		"a very long redirect": {service.ClientRegistration{RedirectURIs: []string{"https://a.example/" + strings.Repeat("a", 2100)}}, "invalid_redirect_uri"},
	} {
		_, err := r.svc.Register(t.Context(), r.address, tc.in)

		require.NotNil(t, err, name)
		assert.Equal(t, tc.code, err.Code, name)
	}
}

func manyRedirects(n int) []string {
	out := make([]string, 0, n)
	for range n {
		out = append(out, "http://localhost/cb")
	}

	return out
}

func TestRegistrationCleansTheNameAndNeverKeepsAnEmptyOne(t *testing.T) {
	r := newOAuthRig(t)

	client, err := r.svc.Register(t.Context(), r.address, service.ClientRegistration{
		ClientName: "  Evil\u202e Agent\n", RedirectURIs: []string{"http://localhost/cb"},
	})
	require.Nil(t, err)
	assert.Equal(t, "Evil Agent", client.ClientName)

	client, err = r.svc.Register(t.Context(), r.address, service.ClientRegistration{RedirectURIs: []string{"http://localhost/cb"}})
	require.Nil(t, err)
	assert.Equal(t, "Unnamed agent", client.ClientName)
}

func TestRegistrationAndTokenRequestsAreRateLimitedPerAddress(t *testing.T) {
	r := newOAuthRig(t)
	register := func(from string) *service.OAuthError {
		_, err := r.svc.Register(t.Context(), from, service.ClientRegistration{RedirectURIs: []string{"http://localhost/cb"}})

		return err
	}

	for range 10 {
		require.Nil(t, register("198.51.100.1"))
	}
	limited := register("198.51.100.1")
	require.NotNil(t, limited)
	assert.Equal(t, "temporarily_unavailable", limited.Code)
	assert.Nil(t, register("198.51.100.2"), "another address has its own allowance")

	r.now = r.now.Add(time.Hour + time.Second)
	assert.Nil(t, register("198.51.100.1"), "the allowance returns after the window")

	for range 60 {
		_, err := r.svc.ExchangeCode(t.Context(), "198.51.100.7", service.TokenRequest{ClientID: cimdClient, Code: "x", RedirectURI: loopback, CodeVerifier: verifier})
		require.NotNil(t, err)
		require.Equal(t, "invalid_grant", err.Code)
	}
	_, err := r.svc.ExchangeCode(t.Context(), "198.51.100.7", service.TokenRequest{ClientID: cimdClient, Code: "x", RedirectURI: loopback, CodeVerifier: verifier})
	require.NotNil(t, err)
	assert.Equal(t, "temporarily_unavailable", err.Code)

	_, err = r.svc.Refresh(t.Context(), "198.51.100.7", service.RefreshRequest{ClientID: cimdClient, RefreshToken: "x"})
	require.NotNil(t, err)
	assert.Equal(t, "temporarily_unavailable", err.Code, "token and refresh share one allowance")
}

func TestAuthorizingIsRateLimitedPerPerson(t *testing.T) {
	r := newOAuthRig(t)

	for range 30 {
		_, err := r.svc.StartAuthorization(t.Context(), r.user, r.request(nil))
		require.Nil(t, err)
	}
	_, err := r.svc.StartAuthorization(t.Context(), r.user, r.request(nil))
	require.NotNil(t, err)
	assert.Equal(t, "temporarily_unavailable", err.Code)
	assert.Empty(t, err.RedirectURI)

	_, other := r.svc.StartAuthorization(t.Context(), r.admin, r.request(nil))
	assert.Nil(t, other)
}

func TestTheServerNamesItsIssuerAndResource(t *testing.T) {
	r := newOAuthRig(t)

	assert.Equal(t, oauthIssuer, r.svc.Issuer())
	assert.Equal(t, oauthResource, r.svc.Resource())
}

func TestAuthorizingTidiesWhatHasExpired(t *testing.T) {
	r := newOAuthRig(t)

	_, err := r.svc.StartAuthorization(t.Context(), r.user, r.request(nil))

	require.Nil(t, err)
	assert.Equal(t, 1, r.rows.Purges(), "parked requests that were never answered do not pile up")
}

func TestTheCredentialRouteNeedsThePasswordsGrant(t *testing.T) {
	r := newOAuthRig(t)
	r.useLocalClient()
	credential := "/api/databases/pds.orders/db-1/credential"

	readOnly := r.tokens(t)
	_, err := r.svc.Authenticate(t.Context(), readOnly.AccessToken, credential)
	require.ErrorIs(t, err, service.ErrTokenScope, "a token without pds:credentials cannot read a password")
	_, err = r.svc.Authenticate(t.Context(), readOnly.AccessToken, "/mcp")
	require.NoError(t, err)

	code := r.consented(t, r.admin, map[string]string{"scope": ""}, r.consentFor(r.admin, oauth.Grants{Access: auth.AccessRead, Passwords: true}))
	resp, xerr := r.exchange(code)
	require.Nil(t, xerr)
	_, err = r.svc.Authenticate(t.Context(), resp.AccessToken, credential)
	require.NoError(t, err)

	r.limits.set(store.AISettings{Access: "change_delete", Passwords: false, SQLWrites: true})
	_, err = r.svc.Authenticate(t.Context(), resp.AccessToken, credential)
	require.ErrorIs(t, err, service.ErrTokenScope, "the Organization withdrew the permission")
}
