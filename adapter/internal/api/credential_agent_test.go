package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/features"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/mcp"
	"github.com/percona/pds/adapter/internal/oauth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
	"github.com/percona/pds/adapter/internal/testutil"
)

const (
	localRedirect = "http://127.0.0.1:51234/callback"
	// The password sits in the connection string of every public database the
	// world seeds. Nothing but the credential route's own body may carry it.
	worldPassword = "sentinel-db-password-7c1e"
)

// mutableLimits is the Organization's AI settings, which a test can turn off
// between two calls the way an admin does.
type mutableLimits struct {
	current atomic.Pointer[store.AISettings]
}

func (m *mutableLimits) set(settings store.AISettings) { m.current.Store(&settings) }

func (m *mutableLimits) AI(context.Context, uint) (store.AISettings, error) {
	return *m.current.Load(), nil
}

// credentialWorld is the scope world, with the agent authorization server and
// the MCP endpoint in front of it. acme's default Team holds acme-env and its
// payments Team holds pay-env, and each has one public database with a
// connection string.
type credentialWorld struct {
	*scopeWorld
	svc    *service.OAuth
	rows   *testutil.MemoryOAuthStore
	limits *mutableLimits
	logs   *bytes.Buffer
}

func publicDatabase(environment, name string) hub.Heartbeat {
	return hub.Heartbeat{
		Environment: environment,
		Namespace:   testEverestNamespace, PrivateAccess: true,
		At: time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC),
		Instances: []engine.Instance{{
			Name: name, Namespace: testEverestNamespace, Engine: "mysql",
			Exposure: engine.Exposure{External: true, Address: "198.51.100.20"},
			Connection: &engine.Connection{
				Host: "198.51.100.20", Port: 3306, Username: "root",
				URI: "mysql://root:" + worldPassword + "@198.51.100.20:3306",
			},
		}},
	}
}

func newCredentialWorld(t *testing.T) *credentialWorld {
	t.Helper()

	w := &credentialWorld{
		rows:   testutil.NewMemoryOAuthStore(),
		limits: &mutableLimits{},
		logs:   &bytes.Buffer{},
	}
	w.limits.set(store.AISettings{Access: "change_delete", Passwords: true, SQLWrites: true})

	w.scopeWorld = newScopeWorld(t, "", func(d *api.Deps, identity *service.Identity) {
		set, err := features.New(nil)
		require.NoError(t, err)
		w.svc = service.NewOAuth(w.rows, identity, w.limits, &documentFetcher{}, oauthTestIssuer)
		d.Logger = log.New(w.logs, log.FormatJSON, slog.LevelDebug)
		d.MCP = mcp.New(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) { w.srv.ServeHTTP(rw, r) }), d.Logger, "test")
		d.Features, d.OAuth = set, w.svc
	})

	for team, name := range map[uint]string{1: "acme-db", 2: "pay-db"} {
		svc, err := w.hubs.Get(t.Context(), team)
		require.NoError(t, err)
		svc.ApplyHeartbeat(publicDatabase(teamEnvironments[team], name))
	}

	return w
}

// token signs the person in, has them consent to grants for the Team, and
// returns the access and refresh tokens the local client would hold.
func (w *credentialWorld) token(t *testing.T, login string, team uint, grants oauth.Grants) (access, refresh string) {
	t.Helper()

	who, err := w.identity.Resolve(t.Context(), w.as(t, login).Value)
	require.NoError(t, err)

	pending, aerr := w.svc.StartAuthorization(t.Context(), who, service.AuthorizationRequest{
		ClientID: oauth.LocalClientID, RedirectURI: localRedirect, ResponseType: "code",
		CodeChallenge: pkceChallenge(oauthTestVerify), CodeChallengeMethod: "S256", State: "s",
		Scope: strings.Join(grants.Scopes(), " "), Resource: oauthTestIssuer + "/mcp",
	})
	require.Nil(t, aerr)
	redirect, err := w.svc.Approve(t.Context(), who, pending, service.Consent{
		OrganizationID: who.Scope.OrganizationID, TeamID: team, Grants: grants,
	})
	require.NoError(t, err)
	back, err := url.Parse(redirect)
	require.NoError(t, err)

	resp, oerr := w.svc.ExchangeCode(t.Context(), "203.0.113.7:5000", service.TokenRequest{
		ClientID: oauth.LocalClientID, Code: back.Query().Get("code"), RedirectURI: localRedirect,
		CodeVerifier: oauthTestVerify, Resource: oauthTestIssuer + "/mcp",
	})
	require.Nil(t, oerr)

	return resp.AccessToken, resp.RefreshToken
}

func (w *credentialWorld) read(bearer, target string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for _, m := range mutate {
		m(req)
	}
	rec := httptest.NewRecorder()
	w.srv.ServeHTTP(rec, req)

	return rec
}

func (w *credentialWorld) activityRows(t *testing.T, cookie *http.Cookie) []map[string]any {
	t.Helper()

	rec := w.call(t, cookie, http.MethodGet, "/api/activity", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		Entries []map[string]any `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))

	return out.Entries
}

const (
	acmeCredential = "/api/databases/acme-env/pds.acme-db/credential"
	payCredential  = "/api/databases/pay-env/pds.pay-db/credential"
)

var passwordGrants = oauth.Grants{Access: auth.AccessRead, Passwords: true}

func TestAnAgentWithThePasswordsGrantReadsTheConnectionString(t *testing.T) {
	w := newCredentialWorld(t)
	access, _ := w.token(t, "ann", 1, passwordGrants)

	rec := w.read(access, acmeCredential)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "mysql://root:"+worldPassword+"@198.51.100.20:3306", decodeBody(t, rec)["uri"])
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "no-cache", rec.Header().Get("Pragma"))
}

func TestAnAgentWithoutThePasswordsGrantIsToldSoInPlainWords(t *testing.T) {
	w := newCredentialWorld(t)
	access, _ := w.token(t, "ann", 1, oauth.Grants{Access: auth.AccessChangeDelete})

	rec := w.read(access, acmeCredential)

	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, api.CodeForbidden, body["code"])
	assert.Equal(t, "This agent is not allowed to read database passwords. Ask an organization admin to allow them, then sign in again.", body["message"])
	assert.NotContains(t, rec.Body.String(), worldPassword)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

func TestTurningThePasswordsSwitchOffRefusesTheNextCall(t *testing.T) {
	w := newCredentialWorld(t)
	access, _ := w.token(t, "ann", 1, passwordGrants)
	require.Equal(t, http.StatusOK, w.read(access, acmeCredential).Code)

	w.limits.set(store.AISettings{Access: "change_delete", Passwords: false, SQLWrites: true})

	rec := w.read(access, acmeCredential)
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), worldPassword)

	w.limits.set(store.AISettings{Access: "change_delete", Passwords: true})
	assert.Equal(t, http.StatusOK, w.read(access, acmeCredential).Code, "the grant stayed on the session, so turning the switch back on restores it")
}

func TestARevokedSessionIsRefusedAtOnce(t *testing.T) {
	w := newCredentialWorld(t)
	access, refresh := w.token(t, "ann", 1, passwordGrants)
	require.Equal(t, http.StatusOK, w.read(access, acmeCredential).Code)

	require.Nil(t, w.svc.Revoke(t.Context(), "203.0.113.7:5000", service.RevokeRequest{Token: refresh, ClientID: oauth.LocalClientID}))

	rec := w.read(access, acmeCredential)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Header().Get("WWW-Authenticate"), `error="invalid_token"`)
	assert.NotContains(t, rec.Body.String(), worldPassword)
}

func TestATokenBoundToOneTeamNeverReadsAnotherTeamsDatabase(t *testing.T) {
	w := newCredentialWorld(t)

	// root is an Organization admin, who may act in any Team with a console
	// session. A token is bound to the Team it was approved for.
	access, _ := w.token(t, "root", 1, passwordGrants)
	require.Equal(t, http.StatusOK, w.read(access, acmeCredential).Code)

	other := w.read(access, payCredential)
	assert.Equal(t, http.StatusNotFound, other.Code, other.Body.String())
	assert.NotContains(t, other.Body.String(), worldPassword)

	named := w.read(access, acmeCredential+"?team=payments")
	assert.Equal(t, http.StatusNotFound, named.Code, "a token cannot name another Team")
	assert.NotContains(t, named.Body.String(), worldPassword)

	assert.Equal(t, http.StatusOK, w.call(t, w.as(t, "root"), http.MethodGet, payCredential, "").Code,
		"the same admin's console session still reaches both Teams")

	userAccess, _ := w.token(t, "bob", 2, passwordGrants)
	assert.Equal(t, http.StatusNotFound, w.read(userAccess, acmeCredential).Code)
	assert.Equal(t, http.StatusOK, w.read(userAccess, payCredential).Code)
}

func TestTheCredentialRouteRefusesTokensThatAreNotAnAccessTokenForIt(t *testing.T) {
	w := newCredentialWorld(t)
	_, refresh := w.token(t, "ann", 1, passwordGrants)

	for name, bearer := range map[string]string{"a refresh token": refresh, "garbage": "not-a-token"} {
		rec := w.read(bearer, acmeCredential)

		assert.Equal(t, http.StatusUnauthorized, rec.Code, name)
		assert.Contains(t, rec.Header().Get("WWW-Authenticate"), `error="invalid_token"`, name)
		assert.NotContains(t, rec.Body.String(), worldPassword, name)
	}

	cookie := w.as(t, "ann")
	both := w.read("not-a-token", acmeCredential, func(r *http.Request) { r.AddCookie(cookie) })
	assert.Equal(t, http.StatusUnauthorized, both.Code, "a bad token is not rescued by a cookie")

	noToken := w.read("", acmeCredential)
	assert.Equal(t, http.StatusUnauthorized, noToken.Code)
}

func TestTheConsoleSessionStillReadsTheCredential(t *testing.T) {
	w := newCredentialWorld(t)

	rec := w.call(t, w.as(t, "ann"), http.MethodGet, acmeCredential, "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

func TestAnAccessTokenReadsNothingElseOnTheAPI(t *testing.T) {
	w := newCredentialWorld(t)
	access, _ := w.token(t, "ann", 1, passwordGrants)

	for _, target := range []string{"/api/databases", "/api/databases/acme-env/pds.acme-db", "/api/organization/ai"} {
		rec := w.read(access, target)

		assert.Equal(t, http.StatusUnauthorized, rec.Code, target)
		assert.NotContains(t, rec.Body.String(), worldPassword, target)
	}
}

func TestNoCallerBuiltInProcessReadsACredential(t *testing.T) {
	w := newCredentialWorld(t)
	who, err := w.identity.Resolve(t.Context(), w.as(t, "root").Value)
	require.NoError(t, err)
	who.Client = auth.Client{Kind: auth.ClientMCP, Agent: "Claude Code"}

	req := httptest.NewRequest(http.MethodGet, acmeCredential, nil).
		WithContext(auth.WithCaller(t.Context(), auth.Caller{Identity: who, Access: auth.AccessChangeDelete, Agent: "Claude Code"}))
	rec := httptest.NewRecorder()
	w.srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), worldPassword)
}

func TestTheCredentialIsNeverLoggedOrRecorded(t *testing.T) {
	w := newCredentialWorld(t)
	access, refresh := w.token(t, "ann", 1, passwordGrants)
	noGrant, _ := w.token(t, "ann", 1, oauth.Grants{Access: auth.AccessRead})

	var bodies strings.Builder
	for _, rec := range []*httptest.ResponseRecorder{
		w.read(access, acmeCredential),
		w.read(noGrant, acmeCredential),
		w.read(refresh, acmeCredential),
		w.read(access, payCredential),
		w.read(access, "/api/databases/acme-env/pds.missing/credential"),
	} {
		bodies.WriteString(rec.Body.String())
	}
	w.limits.set(store.AISettings{Access: "change_delete"})
	bodies.WriteString(w.read(access, acmeCredential).Body.String())

	for _, secret := range []string{access, refresh, noGrant} {
		assert.NotContains(t, w.logs.String(), secret)
	}
	assert.NotContains(t, w.logs.String(), worldPassword, "the connection string is not logged")
	assert.NotContains(t, strings.ReplaceAll(bodies.String(), "mysql://root:"+worldPassword+"@198.51.100.20:3306", ""), worldPassword)

	activity := w.call(t, w.as(t, "ann"), http.MethodGet, "/api/activity", "").Body.String()
	assert.NotContains(t, activity, worldPassword)
}

func TestEveryCredentialReadByAnAgentIsInTheActivityLogWithItsName(t *testing.T) {
	w := newCredentialWorld(t)
	access, _ := w.token(t, "ann", 1, passwordGrants)
	require.Equal(t, http.StatusOK, w.read(access, acmeCredential).Code)

	rows := w.activityRows(t, w.as(t, "ann"))

	require.Len(t, rows, 1)
	row := rows[0]
	assert.Equal(t, "database.credential-read", row["action"])
	assert.Equal(t, "mcp", row["client"])
	assert.Equal(t, oauth.LocalClientName, row["agent"])
	assert.Equal(t, "Ann Lee", row["actor"])
	assert.Equal(t, "acme-env/pds.acme-db", row["databaseId"])
	assert.Equal(t, "succeeded", row["outcome"])
	assert.NotContains(t, mustJSON(t, row), worldPassword)
}

func TestAReadThatFailsIsRecordedAsFailed(t *testing.T) {
	w := newCredentialWorld(t)
	access, _ := w.token(t, "ann", 1, passwordGrants)
	svc, err := w.hubs.Get(t.Context(), 1)
	require.NoError(t, err)
	beat := publicDatabase("acme-env", "acme-db")
	beat.Instances[0].Connection.URI = ""
	svc.ApplyHeartbeat(beat)

	require.Equal(t, http.StatusNotFound, w.read(access, acmeCredential).Code)

	rows := w.activityRows(t, w.as(t, "ann"))
	require.Len(t, rows, 1)
	assert.Equal(t, "failed", rows[0]["outcome"])
	assert.Equal(t, oauth.LocalClientName, rows[0]["agent"])
}

func TestAConsoleReadIsRecordedWithoutAnAgent(t *testing.T) {
	w := newCredentialWorld(t)
	cookie := w.as(t, "ann")
	require.Equal(t, http.StatusOK, w.call(t, cookie, http.MethodGet, acmeCredential, "").Code)

	rows := w.activityRows(t, cookie)

	require.Len(t, rows, 1)
	assert.Equal(t, "database.credential-read", rows[0]["action"])
	assert.Empty(t, rows[0]["agent"])
	assert.Empty(t, rows[0]["client"])
}

func TestNoToolListAtAnyTierIncludesACredentialTool(t *testing.T) {
	w := newCredentialWorld(t)

	for _, grants := range []oauth.Grants{
		{Access: auth.AccessRead},
		{Access: auth.AccessChange},
		{Access: auth.AccessChangeDelete},
		{Access: auth.AccessChangeDelete, Passwords: true, SQLWrites: true},
	} {
		access, _ := w.token(t, "root", 1, grants)
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(listTools))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+access)
		rec := httptest.NewRecorder()
		w.srv.ServeHTTP(rec, req)

		names := toolNames(t, rec)
		require.NotEmpty(t, names)
		for _, name := range names {
			for _, word := range []string{"credential", "password", "secret", "connection_string", "uri"} {
				assert.NotContains(t, name, word, "tier %v", grants.Scopes())
			}
		}
		assert.NotContains(t, rec.Body.String(), "/credential", "no tool is described as reaching the credential route")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()

	out, err := json.Marshal(v)
	require.NoError(t, err)

	return string(out)
}

func TestAnAgentCannotListEveryTeamOnTheCredentialRoute(t *testing.T) {
	w := newCredentialWorld(t)
	access, _ := w.token(t, "root", 1, passwordGrants)

	rec := w.read(access, acmeCredential+"?team=all")

	assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	assert.Equal(t, "This agent works in the team it signed in for only.", decodeBody(t, rec)["message"])
}
