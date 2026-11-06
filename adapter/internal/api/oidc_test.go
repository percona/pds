package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/store"
	"github.com/percona/pds/adapter/internal/testutil"
)

const oidcFlowCookie = "pds_oidc_flow"

// oidcFixture is a login server with OIDC switched on against a fake issuer,
// and one known identity on file at that issuer.
type oidcFixture struct {
	srv    *api.Server
	issuer *testutil.FakeIssuer
	rows   *memoryIdentityStore
}

func newOIDCFixture(t *testing.T) oidcFixture {
	t.Helper()

	issuer := testutil.NewFakeIssuer(t)
	client, err := auth.NewOIDCClient(t.Context(), auth.OIDCConfig{
		Issuer:       issuer.URL,
		ClientID:     testutil.IssuerClientID,
		ClientSecret: testutil.IssuerClientSecret,
	})
	require.NoError(t, err)

	srv, _, rows := newLoginServer(t, func(d *api.Deps) { d.OIDC = client })
	home, err := rows.Placement(t.Context(), loginName)
	require.NoError(t, err)
	rows.addOIDCUser(issuer.URL, "auth0|ada", store.Placement{
		UserID: 41, OrganizationID: home.OrganizationID, OrganizationName: "Acme", Username: "Ada",
		Role: store.RoleUser, TeamID: 5, TeamName: "payments",
	})
	rows.addOIDCUser(issuer.URL, "auth0|elsewhere", store.Placement{
		UserID: 42, OrganizationID: home.OrganizationID + 1, OrganizationName: "Other",
		Role: store.RoleAdmin, TeamID: 6, TeamName: "default",
	})
	issuer.Identity = testutil.IssuerIdentity{
		Subject: "auth0|ada", Email: "ada@example.com", EmailVerified: true, Name: "Ada",
	}

	return oidcFixture{srv: srv, issuer: issuer, rows: rows}
}

func cookieNamed(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == name && cookie.MaxAge >= 0 && cookie.Value != "" {
			return cookie
		}
	}

	return nil
}

// signInThroughIssuer walks the browser's path: start, the issuer, then the
// callback. It returns the callback's response.
func (f oidcFixture) signInThroughIssuer(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	return f.signInThrough(t, "/api/auth/oidc/start")
}

// signInThrough is signInThroughIssuer from a given start address, such as one
// that carries an invitation.
func (f oidcFixture) signInThrough(t *testing.T, startPath string) *httptest.ResponseRecorder {
	t.Helper()

	start := do(t, f.srv, http.MethodGet, startPath, nil)
	require.Equal(t, http.StatusFound, start.Code, "body: %s", start.Body.String())
	flow := cookieNamed(start, oidcFlowCookie)
	require.NotNil(t, flow, "start must remember the flow")

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, start.Header().Get("Location"), nil)
	require.NoError(t, err)
	resp, err := noFollow.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)

	back, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	callback := httptest.NewRequest(http.MethodGet, back.RequestURI(), nil)
	callback.AddCookie(flow)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, callback)

	return rec
}

func TestOIDCSignInOpensASessionForAKnownUser(t *testing.T) {
	f := newOIDCFixture(t)

	rec := f.signInThroughIssuer(t)

	require.Equal(t, http.StatusFound, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, "/app/", rec.Header().Get("Location"))
	session := cookieNamed(rec, auth.SessionCookieName)
	require.NotNil(t, session)
	assert.True(t, session.HttpOnly)
	assert.Equal(t, http.SameSiteLaxMode, session.SameSite)

	who := httptest.NewRecorder()
	f.srv.ServeHTTP(who, withCookie(httptest.NewRequest(http.MethodGet, "/api/auth/session", nil), session))
	require.Equal(t, http.StatusOK, who.Code, "body: %s", who.Body.String())
	body := decodeBody(t, who)
	assert.Equal(t, "Ada", body["user"])
	assert.Equal(t, "Acme", body["organization"])
	assert.Equal(t, "payments", body["team"])
	assert.Equal(t, "user", body["role"])

	opened := httptest.NewRecorder()
	f.srv.ServeHTTP(opened, withCookie(httptest.NewRequest(http.MethodGet, "/api/features", nil), session))
	assert.Equal(t, http.StatusOK, opened.Code)
}

func TestOIDCSignInRefusesAnIdentityWithNoMembership(t *testing.T) {
	f := newOIDCFixture(t)
	f.issuer.Identity = testutil.IssuerIdentity{Subject: "auth0|stranger", Email: "x@example.com", EmailVerified: true}

	rec := f.signInThroughIssuer(t)

	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/app/login?error=no_membership", rec.Header().Get("Location"))
	assert.Nil(t, cookieNamed(rec, auth.SessionCookieName), "a refused identity gets no session")
}

func TestOIDCCallbackRefusesALoginItDidNotStart(t *testing.T) {
	f := newOIDCFixture(t)

	tests := []struct {
		name   string
		target string
		flow   bool
	}{
		{"no flow cookie", "/api/auth/oidc/callback?code=abc&state=abc", false},
		{"state from another flow", "/api/auth/oidc/callback?code=abc&state=forged", true},
		{"issuer reported an error", "/api/auth/oidc/callback?error=access_denied&state=abc", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start := do(t, f.srv, http.MethodGet, "/api/auth/oidc/start", nil)
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			if tc.flow {
				req.AddCookie(cookieNamed(start, oidcFlowCookie))
			}
			rec := httptest.NewRecorder()
			f.srv.ServeHTTP(rec, req)

			require.Equal(t, http.StatusFound, rec.Code)
			assert.Equal(t, "/app/login?error=sign_in_failed", rec.Header().Get("Location"))
			assert.Nil(t, cookieNamed(rec, auth.SessionCookieName))
		})
	}
}

func TestOIDCLogoutEndsTheSessionAndTheIssuersToo(t *testing.T) {
	f := newOIDCFixture(t)
	session := cookieNamed(f.signInThroughIssuer(t), auth.SessionCookieName)
	require.NotNil(t, session)

	req := withCookie(httptest.NewRequest(http.MethodPost, "/api/auth/oidc/logout", nil), session)
	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code)
	to, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, f.issuer.URL+"/logout", to.Scheme+"://"+to.Host+to.Path)
	assert.Equal(t, "https://hub.example/", to.Query().Get("post_logout_redirect_uri"))

	gone := httptest.NewRecorder()
	f.srv.ServeHTTP(gone, withCookie(httptest.NewRequest(http.MethodGet, "/api/features", nil), session))
	assert.Equal(t, http.StatusUnauthorized, gone.Code, "the adapter session must end with the issuer's")
}

func TestOIDCStartIsNotFoundWithoutAnIssuer(t *testing.T) {
	srv, _, _ := newLoginServer(t)

	for target, method := range map[string]string{
		"/api/auth/oidc/start":    http.MethodGet,
		"/api/auth/oidc/callback": http.MethodGet,
		"/api/auth/oidc/logout":   http.MethodPost,
	} {
		rec := do(t, srv, method, target, nil)

		assert.Equal(t, http.StatusNotFound, rec.Code, target)
	}
}

func TestSignInMethodsListPasswordAlone(t *testing.T) {
	srv, _, _ := newLoginServer(t)

	rec := do(t, srv, http.MethodGet, "/api/auth/methods", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []any{"password"}, decodeBody(t, rec)["methods"])
}

func TestSignInMethodsListOIDCBesideThePassword(t *testing.T) {
	f := newOIDCFixture(t)

	rec := do(t, f.srv, http.MethodGet, "/api/auth/methods", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []any{"oidc", "password"}, decodeBody(t, rec)["methods"])
}

func TestPasswordLoginStillWorksBesideOIDC(t *testing.T) {
	f := newOIDCFixture(t)

	cookie := signIn(t, f.srv, loginName, loginPassword)

	assert.NotEmpty(t, cookie.Value)
}

func TestOIDCSignInRefusesAnIdentityOfAnotherOrganizationOnAPinnedDeployment(t *testing.T) {
	f := newOIDCFixture(t)
	f.issuer.Identity = testutil.IssuerIdentity{Subject: "auth0|elsewhere", Email: "e@example.com", EmailVerified: true}

	rec := f.signInThroughIssuer(t)

	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/app/login?error=no_membership", rec.Header().Get("Location"))
	assert.Nil(t, cookieNamed(rec, auth.SessionCookieName))
}

func TestOIDCLogoutRefusesAGetSoALinkCannotSignAnybodyOut(t *testing.T) {
	f := newOIDCFixture(t)
	session := cookieNamed(f.signInThroughIssuer(t), auth.SessionCookieName)
	require.NotNil(t, session)

	rec := httptest.NewRecorder()
	f.srv.ServeHTTP(rec, withCookie(httptest.NewRequest(http.MethodGet, "/api/auth/oidc/logout", nil), session))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

	still := httptest.NewRecorder()
	f.srv.ServeHTTP(still, withCookie(httptest.NewRequest(http.MethodGet, "/api/features", nil), session))
	assert.Equal(t, http.StatusOK, still.Code)
}

func TestOIDCStartAsksTheIssuerForTheProviderTheCustomerPicked(t *testing.T) {
	tests := map[string]string{"google": "google-oauth2", "github": "github"}
	for provider, connection := range tests {
		t.Run(provider, func(t *testing.T) {
			f := newOIDCFixture(t)

			rec := f.signInThrough(t, "/api/auth/oidc/start?provider="+provider)

			require.Equal(t, http.StatusFound, rec.Code, "body: %s", rec.Body.String())
			asked := f.issuer.Authorizations()
			require.Len(t, asked, 1)
			assert.Equal(t, connection, asked[0].Get("connection"))
		})
	}
}

func TestOIDCStartSendsNoConnectionWhenNoProviderIsPicked(t *testing.T) {
	f := newOIDCFixture(t)

	f.signInThroughIssuer(t)

	asked := f.issuer.Authorizations()
	require.Len(t, asked, 1)
	assert.NotContains(t, asked[0], "connection")
}

func TestOIDCStartRefusesAProviderItDoesNotOffer(t *testing.T) {
	f := newOIDCFixture(t)

	rec := do(t, f.srv, http.MethodGet, "/api/auth/oidc/start?provider=facebook", nil)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Nil(t, cookieNamed(rec, oidcFlowCookie), "a refused start remembers nothing")
}

func TestOIDCStartSendsAnOrganizationNameThatIsTooLongBackToTheSignInPage(t *testing.T) {
	f := newOIDCFixture(t)
	long := strings.Repeat("a", 201)

	rec := do(t, f.srv, http.MethodGet, "/api/auth/oidc/start?organization="+long, nil)

	require.Equal(t, http.StatusFound, rec.Code)
	assert.Equal(t, "/app/login?error=organization_name_invalid", rec.Header().Get("Location"))
	assert.Nil(t, cookieNamed(rec, oidcFlowCookie))
}

// issuerAccessToken is a token the identity provider signed for a person the
// control plane knows, for an audience and client the adapter used to accept.
func issuerAccessToken(f oidcFixture) string {
	return f.issuer.AccessToken("auth0|ada", "https://pds.example/api", "pds-device", time.Now().Add(time.Hour))
}

func TestTheDeviceFlowExchangeRouteIsGone(t *testing.T) {
	f := newOIDCFixture(t)
	body := `{"accessToken":"` + issuerAccessToken(f) + `","organization":"Acme","client":"mcp","agent":"Cursor"}`

	for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodPut} {
		rec := do(t, f.srv, method, "/api/auth/exchange", strings.NewReader(body))

		assert.Equal(t, http.StatusNotFound, rec.Code, method)
		assert.Nil(t, cookieNamed(rec, auth.SessionCookieName), "nobody was signed in")
	}
	assert.Empty(t, f.rows.sessions, "no session was opened")
}

func TestSignInMethodsCarryNoDeviceFlow(t *testing.T) {
	f := newOIDCFixture(t)

	rec := do(t, f.srv, http.MethodGet, "/api/auth/methods", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeBody(t, rec)
	assert.NotContains(t, body, "deviceFlow")
	assert.Equal(t, []any{"oidc", "password"}, body["methods"])
	assert.NotContains(t, rec.Body.String(), "deviceClientId")
}

func TestAnIdentityProviderAccessTokenIsNotACredentialAnywhere(t *testing.T) {
	f := newOIDCFixture(t)
	token := issuerAccessToken(f)

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/auth/session"},
		{http.MethodGet, "/api/databases"},
		{http.MethodGet, "/api/activity"},
		{http.MethodPost, "/mcp"},
	} {
		req := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		f.srv.ServeHTTP(rec, req)

		assert.Contains(t, []int{http.StatusUnauthorized, http.StatusNotFound}, rec.Code, route.method+" "+route.path)
		assert.Empty(t, f.rows.sessions, "a bearer never opens a session")
	}
}

func TestAnIdentityProviderAccessTokenDoesNotOpenMCP(t *testing.T) {
	f := newOAuthFixture(t, nil)
	issuer := testutil.NewFakeIssuer(t)
	token := issuer.AccessToken("auth0|ada", "https://pds.example/api", "pds-device", time.Now().Add(time.Hour))

	rec := f.rpc(t, token, listTools)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Contains(t, rec.Header().Get("WWW-Authenticate"), `error="invalid_token"`)
}
