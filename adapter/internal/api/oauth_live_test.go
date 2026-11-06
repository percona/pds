package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/oauth"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

// TestTheWholeFlowRunsAgainstPostgres drives register, authorize, consent, the
// code exchange, a refresh, use of the access token and revocation over the
// real tables, so what the in-memory rows cannot show (the atomic steps, the
// column types) is exercised too.
func TestTheWholeFlowRunsAgainstPostgres(t *testing.T) {
	url0 := strings.TrimSpace(os.Getenv("ADAPTER_TEST_DATABASE_URL"))
	if url0 == "" {
		t.Skip("set ADAPTER_TEST_DATABASE_URL to run the authorization server against a live PostgreSQL")
	}

	db, err := store.Open(t.Context(), store.Options{URL: url0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.Migrate(t.Context()))

	f := newOAuthFixtureOn(t, nil, db.OAuth(), nil)

	// A client registers itself.
	register := httpPostJSON(t, f, "/oauth/register",
		`{"client_name":"Live Agent","redirect_uris":["http://localhost:8787/callback"],"token_endpoint_auth_method":"none"}`)
	require.Equal(t, http.StatusCreated, register.Code, register.Body.String())
	clientID := jsonBody(t, register)["client_id"].(string)

	// It sends the person to authorize. The person signs in, consents, and the
	// client gets a code.
	query := authorizeQuery(map[string]string{"client_id": clientID, "redirect_uri": "http://localhost:8787/callback"})
	rec := f.get(t, "/oauth/authorize?"+query.Encode(), f.cookie)
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	consent, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	requestToken := consent.Query().Get("request")

	redirect, err := f.svc.Approve(t.Context(), f.identity, requestToken, service.Consent{
		OrganizationID: f.identity.Scope.OrganizationID, TeamID: f.identity.Scope.Team.ID,
		Grants: oauth.Grants{Access: auth.AccessChange},
	})
	require.NoError(t, err)
	back, err := url.Parse(redirect)
	require.NoError(t, err)
	assert.Equal(t, "abc123", back.Query().Get("state"))

	// The code is exchanged, and the access token resolves to the person who
	// approved it.
	form := tokenForm(back.Query().Get("code"))
	form.Set("client_id", clientID)
	form.Set("redirect_uri", "http://localhost:8787/callback")
	exchanged := f.postForm(t, "/oauth/token", form)
	require.Equal(t, http.StatusOK, exchanged.Code, exchanged.Body.String())
	tokens := jsonBody(t, exchanged)
	assert.Equal(t, "pds:read pds:change", tokens["scope"])

	agent, err := f.svc.Authenticate(t.Context(), tokens["access_token"].(string), "/mcp")
	require.NoError(t, err)
	assert.Equal(t, f.identity.Scope.UserID, agent.Caller.Identity.Scope.UserID)
	assert.Equal(t, auth.AccessChange, agent.Caller.Access)
	assert.Equal(t, "Live Agent", agent.Caller.Agent)

	// A second use of the code is refused and ends the session it made.
	assert.Equal(t, http.StatusBadRequest, f.postForm(t, "/oauth/token", form).Code)
	_, err = f.svc.Authenticate(t.Context(), tokens["access_token"].(string), "/mcp")
	require.ErrorIs(t, err, service.ErrTokenInvalid)

	// A refresh rotates, and the retired refresh token ends the session.
	own := jsonBody(t, f.postForm(t, "/oauth/token", tokenForm(f.approvedCode(t, oauth.Grants{Access: auth.AccessRead}))))
	refresh := func(token string) *httptest.ResponseRecorder {
		return f.postForm(t, "/oauth/token", url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {token}, "client_id": {oauthTestClient},
		})
	}
	second := refresh(own["refresh_token"].(string))
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	newest := jsonBody(t, second)
	_, err = f.svc.Authenticate(t.Context(), newest["access_token"].(string), "/mcp")
	require.NoError(t, err)

	assert.Equal(t, http.StatusBadRequest, refresh(own["refresh_token"].(string)).Code)
	_, err = f.svc.Authenticate(t.Context(), newest["access_token"].(string), "/mcp")
	require.ErrorIs(t, err, service.ErrTokenInvalid)
}

func httpPostJSON(t *testing.T, f oauthFixture, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.77:1"
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)

	return rec
}
