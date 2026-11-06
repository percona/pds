package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	"github.com/percona/pds/adapter/internal/client/clientmeta"
	"github.com/percona/pds/adapter/internal/features"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/mcp"
	"github.com/percona/pds/adapter/internal/oauth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
	"github.com/percona/pds/adapter/internal/testutil"
)

const (
	oauthTestIssuer = "https://hub.example"
	oauthTestClient = "https://agent.example/oauth/client.json"
	oauthTestRedir  = "http://127.0.0.1:43211/callback"
	oauthTestVerify = "0123456789012345678901234567890123456789012"
)

type staticAILimits struct{ settings store.AISettings }

func (s staticAILimits) AI(context.Context, uint) (store.AISettings, error) { return s.settings, nil }

type documentFetcher struct {
	err    error
	called []string
}

func (d *documentFetcher) Fetch(_ context.Context, id string) (clientmeta.Document, error) {
	d.called = append(d.called, id)
	if d.err != nil {
		return clientmeta.Document{}, d.err
	}

	return clientmeta.Document{
		ClientID: id, ClientName: "Test Agent", RedirectURIs: []string{"http://127.0.0.1/callback", "https://agent.example/cb"},
	}, nil
}

type oauthFixture struct {
	srv      *api.Server
	svc      *service.OAuth
	mem      *testutil.MemoryOAuthStore
	rows     service.OAuthStore
	docs     *documentFetcher
	cookie   *http.Cookie
	identity auth.Identity
	// logs is everything the adapter logged, at every level.
	logs *bytes.Buffer
	// skew moves the OAuth service's clock forward, in nanoseconds.
	skew *atomic.Int64
}

func newOAuthFixture(t *testing.T, flags map[string]bool) oauthFixture {
	t.Helper()

	mem := testutil.NewMemoryOAuthStore()

	return newOAuthFixtureOn(t, flags, mem, mem)
}

// newOAuthFixtureOn is the fixture over any set of rows: the in-memory ones,
// or PostgreSQL's.
func newOAuthFixtureOn(t *testing.T, flags map[string]bool, oauthRows service.OAuthStore, mem *testutil.MemoryOAuthStore) oauthFixture {
	t.Helper()

	f := oauthFixture{rows: oauthRows, mem: mem, docs: &documentFetcher{}, logs: &bytes.Buffer{}, skew: &atomic.Int64{}}
	srv, identity, people := newLoginServer(t, func(d *api.Deps) {
		set, err := features.New(flags)
		require.NoError(t, err)
		f.svc = service.NewOAuth(oauthRows, d.Identity,
			staticAILimits{store.AISettings{Access: "change_delete", Passwords: true, SQLWrites: true}}, f.docs, oauthTestIssuer,
			service.WithOAuthClock(func() time.Time { return time.Now().Add(time.Duration(f.skew.Load())) }))
		d.Logger = log.New(f.logs, log.FormatJSON, slog.LevelDebug)
		d.MCP = mcp.New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.srv.ServeHTTP(w, r) }), d.Logger, "test")
		d.Features, d.OAuth = set, f.svc
	})
	f.srv = srv
	f.cookie = signIn(t, srv, loginName, loginPassword)

	who, err := identity.Resolve(t.Context(), f.cookie.Value)
	require.NoError(t, err)
	f.identity = who
	people.teams = []store.Team{{ID: who.Scope.Team.ID, OrganizationID: who.Scope.OrganizationID, Name: who.Scope.Team.Name}}

	return f
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))

	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func authorizeQuery(overrides map[string]string) url.Values {
	q := url.Values{
		"response_type": {"code"}, "client_id": {oauthTestClient}, "redirect_uri": {oauthTestRedir},
		"code_challenge": {pkceChallenge(oauthTestVerify)}, "code_challenge_method": {"S256"},
		"state": {"abc123"}, "scope": {"pds:read pds:change"}, "resource": {oauthTestIssuer + "/mcp"},
	}
	for k, v := range overrides {
		if v == "" {
			q.Del(k)
		} else {
			q.Set(k, v)
		}
	}

	return q
}

func (f oauthFixture) get(t *testing.T, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, target, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)

	return rec
}

func (f oauthFixture) postForm(t *testing.T, path string, form url.Values, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.RemoteAddr = "203.0.113.7:5000"
	for _, m := range mutate {
		m(req)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)

	return rec
}

func (f oauthFixture) authorize(t *testing.T, overrides map[string]string, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()

	return f.get(t, "/oauth/authorize?"+authorizeQuery(overrides).Encode(), cookie)
}

// approvedCode runs authorize and consent and returns the code the client's
// redirect would carry.
func (f oauthFixture) approvedCode(t *testing.T, grants oauth.Grants) string {
	t.Helper()

	scope := strings.Join(grants.Scopes(), " ")
	rec := f.authorize(t, map[string]string{"scope": scope}, f.cookie)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	consent, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	token := consent.Query().Get("request")
	require.NotEmpty(t, token)

	redirect, err := f.svc.Approve(t.Context(), f.identity, token, service.Consent{
		OrganizationID: f.identity.Scope.OrganizationID, TeamID: f.identity.Scope.Team.ID, Grants: grants,
	})
	require.NoError(t, err)
	back, err := url.Parse(redirect)
	require.NoError(t, err)

	return back.Query().Get("code")
}

func tokenForm(code string) url.Values {
	return url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {oauthTestRedir},
		"client_id": {oauthTestClient}, "code_verifier": {oauthTestVerify}, "resource": {oauthTestIssuer + "/mcp"},
	}
}

func jsonBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())

	return out
}

func withParam(v url.Values, name, value string) url.Values {
	out := url.Values{}
	for k, vals := range v {
		out[k] = append([]string(nil), vals...)
	}
	out.Add(name, value)

	return out
}

func withSet(v url.Values, name, value string) url.Values {
	out := withParam(v, name, value)
	out.Set(name, value)

	return out
}

func TestTheServerPublishesItsMetadata(t *testing.T) {
	f := newOAuthFixture(t, nil)

	rec := f.get(t, "/.well-known/oauth-authorization-server", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
	meta := jsonBody(t, rec)
	assert.Equal(t, oauthTestIssuer, meta["issuer"])
	assert.Equal(t, oauthTestIssuer+"/oauth/authorize", meta["authorization_endpoint"])
	assert.Equal(t, oauthTestIssuer+"/oauth/token", meta["token_endpoint"])
	assert.Equal(t, oauthTestIssuer+"/oauth/register", meta["registration_endpoint"])
	assert.Equal(t, oauthTestIssuer+"/oauth/revoke", meta["revocation_endpoint"])
	assert.Equal(t, []any{"code"}, meta["response_types_supported"])
	assert.Equal(t, []any{"authorization_code", "refresh_token"}, meta["grant_types_supported"])
	assert.Equal(t, []any{"none"}, meta["token_endpoint_auth_methods_supported"])
	assert.Equal(t, []any{"S256"}, meta["code_challenge_methods_supported"])
	assert.Equal(t, true, meta["client_id_metadata_document_supported"])
	assert.Equal(t, true, meta["authorization_response_iss_parameter_supported"])
	assert.Contains(t, meta["scopes_supported"], "offline_access")
	assert.Contains(t, meta["scopes_supported"], "pds:read")
}

func TestWithTheMCPFlagOffEveryOAuthEndpointAnswers404(t *testing.T) {
	f := newOAuthFixture(t, map[string]bool{features.MCP: false})

	for _, endpoint := range []struct{ method, path string }{
		{http.MethodGet, "/.well-known/oauth-authorization-server"},
		{http.MethodGet, "/oauth/authorize?" + authorizeQuery(nil).Encode()},
		{http.MethodPost, "/oauth/token"},
		{http.MethodPost, "/oauth/register"},
		{http.MethodPost, "/oauth/revoke"},
		{http.MethodGet, "/oauth/token"},
		{http.MethodPost, "/oauth/authorize"},
		{http.MethodGet, "/oauth/anything"},
	} {
		req := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader("{}"))
		req.AddCookie(f.cookie)
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code, "%s %s", endpoint.method, endpoint.path)
	}
	assert.Empty(t, f.docs.called)
}

func TestWithNoAuthorizationServerWiredEveryOAuthEndpointAnswers404(t *testing.T) {
	srv, _, _ := newLoginServer(t)

	for _, path := range []string{"/.well-known/oauth-authorization-server", "/oauth/authorize", "/oauth/token", "/oauth/register", "/oauth/revoke"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		assert.Equal(t, http.StatusNotFound, rec.Code, path)
	}
}

func TestAWrongMethodOnAnOAuthEndpointIsRefused(t *testing.T) {
	f := newOAuthFixture(t, nil)

	rec := f.get(t, "/oauth/token", nil)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, http.MethodPost, rec.Header().Get("Allow"))
}

func TestAuthorizingWithoutASessionSendsThePersonToSignInAndFetchesNothing(t *testing.T) {
	f := newOAuthFixture(t, nil)
	query := authorizeQuery(nil).Encode()

	rec := f.get(t, "/oauth/authorize?"+query, nil)

	require.Equal(t, http.StatusFound, rec.Code)
	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "/app/login", location.Path)
	assert.Equal(t, "/oauth/authorize?"+query, location.Query().Get("next"),
		"sign in comes back to the same request")
	assert.Empty(t, f.docs.called, "the document is not fetched for somebody who has not signed in")
	assert.Zero(t, f.mem.SessionCount())
}

func TestAMalformedRequestIsRefusedBeforeSignIn(t *testing.T) {
	f := newOAuthFixture(t, nil)

	for name, overrides := range map[string]map[string]string{
		"no client":     {"client_id": ""},
		"no redirect":   {"redirect_uri": ""},
		"bad redirect":  {"redirect_uri": "javascript:alert(1)"},
		"no PKCE":       {"code_challenge": ""},
		"plain PKCE":    {"code_challenge_method": "plain"},
		"implicit":      {"response_type": "token"},
		"other target":  {"resource": "https://evil.example/mcp"},
		"unknown scope": {"scope": "admin"},
	} {
		rec := f.authorize(t, overrides, nil)

		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
		assert.Empty(t, rec.Header().Get("Location"), name)
	}
}

func TestADuplicatedParameterIsRefused(t *testing.T) {
	f := newOAuthFixture(t, nil)
	query := authorizeQuery(nil)
	query.Add("redirect_uri", "https://evil.example/cb")

	for _, cookie := range []*http.Cookie{nil, f.cookie} {
		rec := f.get(t, "/oauth/authorize?"+query.Encode(), cookie)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Empty(t, rec.Header().Get("Location"))
	}
}

func TestASignedInPersonIsSentToTheConsentScreenWithAParkedRequest(t *testing.T) {
	f := newOAuthFixture(t, nil)

	rec := f.authorize(t, nil, f.cookie)

	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	location, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "/app/authorize-agent", location.Path)
	token := location.Query().Get("request")
	assert.GreaterOrEqual(t, len(token), 40)
	assert.Equal(t, []string{oauthTestClient}, f.docs.called)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))

	pending, err := f.svc.Pending(t.Context(), f.identity, token)
	require.NoError(t, err)
	assert.Equal(t, "Test Agent", pending.ClientName)
}

func TestARedirectURIThatIsNotTheClientsIsAnErrorPageAndNeverARedirect(t *testing.T) {
	f := newOAuthFixture(t, nil)

	rec := f.authorize(t, map[string]string{"redirect_uri": "https://evil.example/steal"}, f.cookie)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, rec.Header().Get("Location"))
	assert.Contains(t, rec.Header().Get("Content-Type"), "text/html")
	assert.Contains(t, rec.Header().Get("Content-Security-Policy"), "default-src 'none'")
	assert.NotContains(t, rec.Body.String(), "evil.example", "the page never echoes the request")
	assert.NotContains(t, strings.ToLower(rec.Body.String()), "cluster")
}

func TestAClientThatCannotBeIdentifiedIsAnErrorPage(t *testing.T) {
	f := newOAuthFixture(t, nil)
	f.docs.err = clientmeta.ErrNotPublic

	rec := f.authorize(t, nil, f.cookie)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Empty(t, rec.Header().Get("Location"))
}

func TestAProblemAfterTheRedirectIsTrustedGoesBackToTheClientWithStateAndIssuer(t *testing.T) {
	f := newOAuthFixture(t, nil)

	rec := f.authorize(t, map[string]string{"code_challenge_method": "plain"}, f.cookie)

	require.Equal(t, http.StatusFound, rec.Code)
	back, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:43211", back.Host)
	assert.Equal(t, "/callback", back.Path)
	assert.Equal(t, "invalid_request", back.Query().Get("error"))
	assert.Equal(t, "abc123", back.Query().Get("state"))
	assert.Equal(t, oauthTestIssuer, back.Query().Get("iss"))
	assert.Empty(t, back.Query().Get("code"))
}

func TestRegistrationIssuesAPublicClient(t *testing.T) {
	f := newOAuthFixture(t, nil)
	body := `{"client_name":"Cursor","redirect_uris":["http://localhost:8787/callback"],"token_endpoint_auth_method":"none","application_type":"native"}`

	req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	registered := jsonBody(t, rec)
	assert.True(t, strings.HasPrefix(registered["client_id"].(string), "dcr_"))
	assert.Equal(t, "Cursor", registered["client_name"])
	assert.Equal(t, "none", registered["token_endpoint_auth_method"])
	assert.NotContains(t, registered, "client_secret")
	assert.Equal(t, []any{"http://localhost:8787/callback"}, registered["redirect_uris"])
}

func TestRegistrationRefusalsUseTheRFC7591Errors(t *testing.T) {
	f := newOAuthFixture(t, nil)

	for name, tc := range map[string]struct{ body, code string }{
		"an http redirect":   {`{"redirect_uris":["http://evil.example/cb"]}`, "invalid_redirect_uri"},
		"no redirect":        {`{"client_name":"x"}`, "invalid_redirect_uri"},
		"a secret method":    {`{"redirect_uris":["http://localhost/cb"],"token_endpoint_auth_method":"client_secret_post"}`, "invalid_client_metadata"},
		"not json":           {`redirect_uris=1`, "invalid_client_metadata"},
		"redirects not text": {`{"redirect_uris":[1]}`, "invalid_client_metadata"},
	} {
		req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
		assert.Equal(t, tc.code, jsonBody(t, rec)["error"], name)
	}
}

func TestRegistrationIsRateLimitedPerAddress(t *testing.T) {
	f := newOAuthFixture(t, nil)
	register := func() int {
		req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(`{"redirect_uris":["http://localhost/cb"]}`))
		req.RemoteAddr = "198.51.100.4:1"
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, req)

		return rec.Code
	}

	for range 10 {
		require.Equal(t, http.StatusCreated, register())
	}
	assert.Equal(t, http.StatusTooManyRequests, register())
}

func TestTheTokenEndpointExchangesACodeAndTheResponseNamesTheGrants(t *testing.T) {
	f := newOAuthFixture(t, nil)
	code := f.approvedCode(t, oauth.Grants{Access: auth.AccessChange})

	rec := f.postForm(t, "/oauth/token", tokenForm(code))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "no-cache", rec.Header().Get("Pragma"))
	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
	body := jsonBody(t, rec)
	assert.Equal(t, "Bearer", body["token_type"])
	assert.EqualValues(t, 3600, body["expires_in"])
	assert.Equal(t, "pds:read pds:change", body["scope"])
	assert.NotEmpty(t, body["access_token"])
	assert.NotEmpty(t, body["refresh_token"])
}

func TestAWrongPKCEVerifierIsInvalidGrantOverHTTP(t *testing.T) {
	f := newOAuthFixture(t, nil)
	code := f.approvedCode(t, oauth.Grants{Access: auth.AccessRead})
	form := tokenForm(code)
	form.Set("code_verifier", strings.Repeat("z", 43))

	rec := f.postForm(t, "/oauth/token", form)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_grant", jsonBody(t, rec)["error"])
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

func TestACodeWorksOnceOverHTTP(t *testing.T) {
	f := newOAuthFixture(t, nil)
	code := f.approvedCode(t, oauth.Grants{Access: auth.AccessRead})

	assert.Equal(t, http.StatusOK, f.postForm(t, "/oauth/token", tokenForm(code)).Code)
	again := f.postForm(t, "/oauth/token", tokenForm(code))

	assert.Equal(t, http.StatusBadRequest, again.Code)
	assert.Equal(t, "invalid_grant", jsonBody(t, again)["error"])
	assert.Zero(t, f.mem.SessionCount(), "the replay ended the session the first use made")
}

func TestARefreshTokenRotatesOverHTTP(t *testing.T) {
	f := newOAuthFixture(t, nil)
	first := jsonBody(t, f.postForm(t, "/oauth/token", tokenForm(f.approvedCode(t, oauth.Grants{Access: auth.AccessRead}))))
	refresh := func(token string) *httptest.ResponseRecorder {
		return f.postForm(t, "/oauth/token", url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {token}, "client_id": {oauthTestClient},
		})
	}

	second := refresh(first["refresh_token"].(string))
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	assert.NotEqual(t, first["refresh_token"], jsonBody(t, second)["refresh_token"])

	old := refresh(first["refresh_token"].(string))
	assert.Equal(t, http.StatusBadRequest, old.Code)
	assert.Equal(t, "invalid_grant", jsonBody(t, old)["error"])
}

func TestTheTokenEndpointRefusesWhatItCannotServe(t *testing.T) {
	f := newOAuthFixture(t, nil)
	code := f.approvedCode(t, oauth.Grants{Access: auth.AccessRead})

	for name, tc := range map[string]struct {
		form   url.Values
		mutate func(*http.Request)
		status int
		code   string
	}{
		"an unknown grant":       {url.Values{"grant_type": {"password"}}, nil, 400, "unsupported_grant_type"},
		"no grant":               {url.Values{}, nil, 400, "invalid_request"},
		"a client secret":        {withParam(tokenForm(code), "client_secret", "s"), nil, 401, "invalid_client"},
		"basic client auth":      {tokenForm(code), func(r *http.Request) { r.SetBasicAuth("a", "b") }, 401, "invalid_client"},
		"another resource":       {withSet(tokenForm(code), "resource", "https://evil.example/mcp"), nil, 400, "invalid_target"},
		"a duplicated parameter": {withParam(tokenForm(code), "code", "second"), nil, 400, "invalid_request"},
	} {
		var mutate []func(*http.Request)
		if tc.mutate != nil {
			mutate = append(mutate, tc.mutate)
		}
		rec := f.postForm(t, "/oauth/token", tc.form, mutate...)

		assert.Equal(t, tc.status, rec.Code, name)
		assert.Equal(t, tc.code, jsonBody(t, rec)["error"], name)
	}

	req := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(`{"grant_type":"authorization_code"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "the token endpoint reads forms")
	assert.Equal(t, "invalid_request", jsonBody(t, rec)["error"])

	query := httptest.NewRequest(http.MethodPost, "/oauth/token?"+tokenForm(code).Encode(), nil)
	query.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec = httptest.NewRecorder()
	f.srv.ServeHTTP(rec, query)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "credentials in the query string are not read")
}

func TestTokenRequestsAreRateLimitedPerAddress(t *testing.T) {
	f := newOAuthFixture(t, nil)

	for range 60 {
		rec := f.postForm(t, "/oauth/token", url.Values{"grant_type": {"authorization_code"}})
		require.Equal(t, http.StatusBadRequest, rec.Code)
	}
	rec := f.postForm(t, "/oauth/token", url.Values{"grant_type": {"authorization_code"}})

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.NotEmpty(t, rec.Header().Get("Retry-After"))
}

func TestRevocationEndsTheSessionOverHTTP(t *testing.T) {
	f := newOAuthFixture(t, nil)
	tokens := jsonBody(t, f.postForm(t, "/oauth/token", tokenForm(f.approvedCode(t, oauth.Grants{Access: auth.AccessRead}))))

	rec := f.postForm(t, "/oauth/revoke", url.Values{"token": {tokens["refresh_token"].(string)}, "client_id": {oauthTestClient}})

	assert.Equal(t, http.StatusOK, rec.Code)
	_, err := f.svc.Authenticate(t.Context(), tokens["access_token"].(string), "/mcp")
	require.ErrorIs(t, err, service.ErrTokenInvalid)

	unknown := f.postForm(t, "/oauth/revoke", url.Values{"token": {"never-issued"}})
	assert.Equal(t, http.StatusOK, unknown.Code, "an unknown token is not an error")
	missing := f.postForm(t, "/oauth/revoke", url.Values{})
	assert.Equal(t, http.StatusBadRequest, missing.Code)
}

func TestAnAccessTokenIsNotASessionOnTheRestOfTheAPI(t *testing.T) {
	f := newOAuthFixture(t, nil)
	tokens := jsonBody(t, f.postForm(t, "/oauth/token", tokenForm(f.approvedCode(t, oauth.Grants{Access: auth.AccessChange}))))
	access := tokens["access_token"].(string)

	for _, path := range []string{
		"/api/databases", "/api/environments", "/api/auth/session", "/api/organization/ai", "/api/activity", "/api/features",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+access)
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusUnauthorized, rec.Code, path)
	}

	for _, path := range []string{"/api/databases", "/api/environments", "/oauth/token"} {
		_, err := f.svc.Authenticate(t.Context(), access, path)
		require.ErrorIs(t, err, service.ErrTokenAudience, path)
	}
	_, err := f.svc.Authenticate(t.Context(), access, "/mcp")
	require.NoError(t, err)
}

func TestNoResponseEverCarriesAToken(t *testing.T) {
	f := newOAuthFixture(t, nil)
	rec := f.authorize(t, nil, f.cookie)
	consent, _ := url.Parse(rec.Header().Get("Location"))
	token := consent.Query().Get("request")

	for _, other := range []*httptest.ResponseRecorder{
		f.get(t, "/.well-known/oauth-authorization-server", nil),
		f.authorize(t, map[string]string{"redirect_uri": "https://evil.example/x"}, f.cookie),
		f.postForm(t, "/oauth/token", tokenForm("bad")),
	} {
		assert.NotContains(t, other.Body.String(), token)
	}
}
