package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/features"
	"github.com/percona/pds/adapter/internal/oauth"
)

const (
	protectedResourceMetadata = oauthTestIssuer + "/.well-known/oauth-protected-resource/mcp"
	// changeTool is a tool only the change tier is served.
	changeTool = "create_database"
)

// agentTokens runs authorize, consent and the token exchange for grants.
func (f oauthFixture) agentTokens(t *testing.T, grants oauth.Grants) (access, refresh string) {
	t.Helper()

	body := jsonBody(t, f.postForm(t, "/oauth/token", tokenForm(f.approvedCode(t, grants))))

	return body["access_token"].(string), body["refresh_token"].(string)
}

// rpc posts one JSON-RPC message to /mcp the way a streamable HTTP client does.
func (f oauthFixture) rpc(t *testing.T, bearer string, message string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(message))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for _, m := range mutate {
		m(req)
	}
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)

	return rec
}

const (
	listTools = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	initCall  = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
)

// toolNames reads a tools/list answer, which is JSON or one server sent event.
func toolNames(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload := rec.Body.String()
	if _, after, ok := strings.Cut(payload, "data: "); ok {
		payload = strings.SplitN(after, "\n", 2)[0]
	}
	var out struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	require.NoError(t, json.Unmarshal([]byte(payload), &out), rec.Body.String())
	names := make([]string, 0, len(out.Result.Tools))
	for _, tool := range out.Result.Tools {
		names = append(names, tool.Name)
	}

	return names
}

func TestMCPWithoutATokenIsChallengedWithTheProtectedResourceMetadata(t *testing.T) {
	f := newOAuthFixture(t, nil)

	for _, message := range []string{initCall, listTools} {
		rec := f.rpc(t, "", message)

		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Equal(t, `Bearer resource_metadata="`+protectedResourceMetadata+`", scope="pds:read"`,
			rec.Header().Get("WWW-Authenticate"), "no token is not an invalid token")
		assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	}
}

func TestMCPRefusesATokenThatIsNotAnAccessToken(t *testing.T) {
	f := newOAuthFixture(t, nil)
	_, refresh := f.agentTokens(t, oauth.Grants{Access: auth.AccessChange})

	for name, bearer := range map[string]string{"garbage": "not-a-token", "a refresh token": refresh} {
		rec := f.rpc(t, bearer, listTools)

		assert.Equal(t, http.StatusUnauthorized, rec.Code, name)
		assert.Equal(t, `Bearer error="invalid_token", resource_metadata="`+protectedResourceMetadata+`", scope="pds:read"`,
			rec.Header().Get("WWW-Authenticate"), name)
	}
}

func TestMCPRefusesAnExpiredTokenAndEndsAtOnceOnRevocation(t *testing.T) {
	f := newOAuthFixture(t, nil)
	access, _ := f.agentTokens(t, oauth.Grants{Access: auth.AccessRead})
	require.Equal(t, http.StatusOK, f.rpc(t, access, listTools).Code)

	f.skew.Store(int64(2 * time.Hour))
	expired := f.rpc(t, access, listTools)
	assert.Equal(t, http.StatusUnauthorized, expired.Code)
	assert.Contains(t, expired.Header().Get("WWW-Authenticate"), `error="invalid_token"`)

	f.skew.Store(0)
	revokable, refresh := f.agentTokens(t, oauth.Grants{Access: auth.AccessRead})
	require.Equal(t, http.StatusOK, f.rpc(t, revokable, listTools).Code)
	require.Equal(t, http.StatusOK, f.postForm(t, "/oauth/revoke",
		url.Values{"token": {refresh}, "client_id": {oauthTestClient}}).Code)

	revoked := f.rpc(t, revokable, listTools)
	assert.Equal(t, http.StatusUnauthorized, revoked.Code)
	assert.Contains(t, revoked.Header().Get("WWW-Authenticate"), `error="invalid_token"`)
}

func TestMCPNeverReadsACookieOrAQueryParameter(t *testing.T) {
	f := newOAuthFixture(t, nil)
	access, _ := f.agentTokens(t, oauth.Grants{Access: auth.AccessRead})

	withCookie := f.rpc(t, "", listTools, func(r *http.Request) { r.AddCookie(f.cookie) })
	assert.Equal(t, http.StatusUnauthorized, withCookie.Code, "a console session is not a credential here")

	inQuery := f.rpc(t, "", listTools, func(r *http.Request) { r.URL.RawQuery = "access_token=" + url.QueryEscape(access) })
	assert.Equal(t, http.StatusUnauthorized, inQuery.Code, "a token in the address is not read")

	wrongScheme := f.rpc(t, "", listTools, func(r *http.Request) { r.Header.Set("Authorization", "Basic "+access) })
	assert.Equal(t, http.StatusUnauthorized, wrongScheme.Code)

	both := f.rpc(t, access, listTools, func(r *http.Request) { r.AddCookie(f.cookie) })
	assert.Equal(t, http.StatusOK, both.Code, "the token decides, whatever else the request carries")
}

func TestMCPServesTheToolsOfTheTokensTier(t *testing.T) {
	f := newOAuthFixture(t, nil)

	read, _ := f.agentTokens(t, oauth.Grants{Access: auth.AccessRead})
	readTools := toolNames(t, f.rpc(t, read, listTools))
	assert.Contains(t, readTools, "list_databases")
	assert.NotContains(t, readTools, changeTool)
	assert.NotContains(t, readTools, "delete_database")

	change, _ := f.agentTokens(t, oauth.Grants{Access: auth.AccessChange})
	changeTools := toolNames(t, f.rpc(t, change, listTools))
	assert.Contains(t, changeTools, changeTool)
	assert.NotContains(t, changeTools, "delete_database")
	assert.Greater(t, len(changeTools), len(readTools))

	both, _ := f.agentTokens(t, oauth.Grants{Access: auth.AccessChangeDelete})
	assert.Contains(t, toolNames(t, f.rpc(t, both, listTools)), "delete_database")
}

func TestMCPRefusesADirectCallToAToolAboveTheTier(t *testing.T) {
	f := newOAuthFixture(t, nil)
	read, _ := f.agentTokens(t, oauth.Grants{Access: auth.AccessRead})

	rec := f.rpc(t, read, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"`+changeTool+
		`","arguments":{"name":"made-by-read","engine":"mysql","profile":"small"}}}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "not_allowed")
	assert.Contains(t, rec.Body.String(), `"isError":true`)

	list := f.get(t, "/api/databases", nil)
	assert.NotContains(t, list.Body.String(), "made-by-read")
}

func TestMCPServesAnInitializeToAValidToken(t *testing.T) {
	f := newOAuthFixture(t, nil)
	access, _ := f.agentTokens(t, oauth.Grants{Access: auth.AccessRead})

	rec := f.rpc(t, access, initCall)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"name":"pds"`)
}

func TestMCPAcceptsOnlyItsOwnOrigin(t *testing.T) {
	f := newOAuthFixture(t, nil)
	access, _ := f.agentTokens(t, oauth.Grants{Access: auth.AccessRead})
	withOrigin := func(origin string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Origin", origin) }
	}

	for _, origin := range []string{"https://evil.example", "http://hub.example", "https://hub.example:8443", "null", "http://localhost:3000"} {
		rec := f.rpc(t, access, listTools, withOrigin(origin))
		assert.Equal(t, http.StatusForbidden, rec.Code, origin)
		assert.Empty(t, rec.Header().Get("Access-Control-Allow-Origin"), origin)
	}
	assert.Equal(t, http.StatusForbidden, f.rpc(t, "", listTools, withOrigin("https://evil.example")).Code,
		"the origin is judged before the token, so a page cannot probe for a valid one")

	assert.Equal(t, http.StatusOK, f.rpc(t, access, listTools, withOrigin(oauthTestIssuer)).Code)
	assert.Equal(t, http.StatusOK, f.rpc(t, access, listTools).Code, "a client that is not a browser sends no origin")
}

func TestMCPOnlyAnswersPost(t *testing.T) {
	f := newOAuthFixture(t, nil)
	access, _ := f.agentTokens(t, oauth.Grants{Access: auth.AccessRead})

	for _, method := range []string{http.MethodGet, http.MethodDelete} {
		req := httptest.NewRequest(method, "/mcp", nil)
		req.Header.Set("Authorization", "Bearer "+access)
		req.Header.Set("Accept", "text/event-stream")
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, method)
	}
}

func TestMCPWithTheFlagOffAnswers404ToEveryone(t *testing.T) {
	f := newOAuthFixture(t, map[string]bool{features.MCP: false})

	for _, path := range []string{"/mcp", "/mcp/", "/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		for _, bearer := range []string{"", "anything"} {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(listTools))
			if bearer != "" {
				req.Header.Set("Authorization", "Bearer "+bearer)
			}
			rec := httptest.NewRecorder()
			f.srv.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusNotFound, rec.Code, path)
			assert.Empty(t, rec.Header().Get("WWW-Authenticate"), path)
		}
	}
}

func TestMCPWithNoAuthorizationServerAnswers404(t *testing.T) {
	srv, _, _ := newLoginServer(t)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(listTools)))

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestTheProtectedResourceMetadataNamesTheEndpointAndItsAuthorizationServer(t *testing.T) {
	f := newOAuthFixture(t, nil)

	for _, path := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
		rec := f.get(t, path, nil)

		require.Equal(t, http.StatusOK, rec.Code, path)
		assert.Equal(t, "*", rec.Header().Get("Access-Control-Allow-Origin"))
		meta := jsonBody(t, rec)
		assert.Equal(t, oauthTestIssuer+"/mcp", meta["resource"], path)
		assert.Equal(t, []any{oauthTestIssuer}, meta["authorization_servers"], path)
		assert.Equal(t, []any{"header"}, meta["bearer_methods_supported"], path)
		assert.Contains(t, meta["scopes_supported"], "pds:read")
		assert.NotContains(t, meta["scopes_supported"], "offline_access")
	}
	assert.Equal(t, http.StatusNotFound, f.get(t, "/.well-known/oauth-protected-resource/other", nil).Code,
		"metadata for a resource this server does not serve is not served")
}

func TestMCPLeaksNoTokenInAResponseOrALogLine(t *testing.T) {
	f := newOAuthFixture(t, nil)
	access, refresh := f.agentTokens(t, oauth.Grants{Access: auth.AccessChange})
	const sentinel = "sentinel-credential-9f3a"

	var bodies strings.Builder
	for _, rec := range []*httptest.ResponseRecorder{
		f.rpc(t, access, listTools),
		f.rpc(t, refresh, listTools),
		f.rpc(t, sentinel, listTools),
		f.rpc(t, "", listTools, func(r *http.Request) { r.URL.RawQuery = "access_token=" + sentinel }),
		f.rpc(t, access, listTools, func(r *http.Request) { r.Header.Set("Origin", "https://"+sentinel+".example") }),
	} {
		bodies.WriteString(rec.Body.String())
		for _, values := range rec.Header() {
			bodies.WriteString(strings.Join(values, " "))
		}
	}

	for _, secret := range []string{access, refresh, sentinel} {
		assert.NotContains(t, bodies.String(), secret)
		assert.NotContains(t, f.logs.String(), secret)
	}
}
