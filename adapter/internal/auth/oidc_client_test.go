package auth_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/testutil"
)

const callbackURL = "https://pds.example/api/auth/oidc/callback"

func newOIDCClient(t *testing.T, issuer *testutil.FakeIssuer) *auth.OIDCClient {
	t.Helper()

	client, err := auth.NewOIDCClient(t.Context(), auth.OIDCConfig{
		Issuer:       issuer.URL,
		ClientID:     testutil.IssuerClientID,
		ClientSecret: testutil.IssuerClientSecret,
	})
	require.NoError(t, err)

	return client
}

// authorize drives the fake issuer's authorize endpoint and returns the code
// and state it redirected back with.
func authorize(t *testing.T, authURL string) (code, state string) {
	t.Helper()

	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, authURL, nil)
	require.NoError(t, err)
	resp, err := noFollow.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)

	back, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)

	return back.Query().Get("code"), back.Query().Get("state")
}

func TestOIDCClientCompletesTheCodeFlow(t *testing.T) {
	issuer := testutil.NewFakeIssuer(t)
	issuer.Identity = testutil.IssuerIdentity{
		Subject: "auth0|42", Email: "ada@example.com", EmailVerified: true, Name: "Ada",
	}
	client := newOIDCClient(t, issuer)

	authURL, flow, err := client.Start(callbackURL, "")
	require.NoError(t, err)
	code, state := authorize(t, authURL)
	require.Equal(t, flow.State, state)

	claims, err := client.Complete(t.Context(), callbackURL, code, flow)

	require.NoError(t, err)
	assert.Equal(t, auth.OIDCClaims{
		Subject: "auth0|42", Email: "ada@example.com", EmailVerified: true, Name: "Ada",
	}, claims)
	assert.Equal(t, issuer.URL, client.Issuer())
}

func TestOIDCClientRefusesATokenForAnotherNonce(t *testing.T) {
	issuer := testutil.NewFakeIssuer(t)
	client := newOIDCClient(t, issuer)

	authURL, flow, err := client.Start(callbackURL, "")
	require.NoError(t, err)
	code, _ := authorize(t, authURL)
	flow.Nonce = "somebody else's nonce"

	_, err = client.Complete(t.Context(), callbackURL, code, flow)

	require.Error(t, err)
}

func TestOIDCClientRefusesAWrongPKCEVerifier(t *testing.T) {
	issuer := testutil.NewFakeIssuer(t)
	client := newOIDCClient(t, issuer)

	authURL, flow, err := client.Start(callbackURL, "")
	require.NoError(t, err)
	code, _ := authorize(t, authURL)
	flow.Verifier = "not the verifier this flow started with"

	_, err = client.Complete(t.Context(), callbackURL, code, flow)

	require.Error(t, err)
}

func TestOIDCClientLogoutURLNamesTheClientAndTheReturnPage(t *testing.T) {
	issuer := testutil.NewFakeIssuer(t)
	client := newOIDCClient(t, issuer)

	got, err := url.Parse(client.LogoutURL("https://pds.example/"))

	require.NoError(t, err)
	assert.Equal(t, issuer.URL+"/logout", got.Scheme+"://"+got.Host+got.Path)
	assert.Equal(t, testutil.IssuerClientID, got.Query().Get("client_id"))
	assert.Equal(t, "https://pds.example/", got.Query().Get("post_logout_redirect_uri"))
}

func TestNewOIDCClientFailsWhenTheIssuerIsUnreachable(t *testing.T) {
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	_, err := auth.NewOIDCClient(t.Context(), auth.OIDCConfig{Issuer: down.URL, ClientID: "x", ClientSecret: "y"})

	require.Error(t, err)
}

func TestOIDCFlowSurvivesTheCookieOnlyWhileSignedAndFresh(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Now()
	flow := auth.OIDCFlow{State: "s", Nonce: "n", Verifier: "v"}
	sealed := auth.SealOIDCFlow(key, flow, now)

	got, ok := auth.OpenOIDCFlow(key, sealed, now.Add(time.Minute))
	require.True(t, ok)
	assert.Equal(t, flow, got)

	_, ok = auth.OpenOIDCFlow(key, sealed, now.Add(auth.OIDCFlowTTL+time.Second))
	assert.False(t, ok, "an old flow must not complete")

	_, ok = auth.OpenOIDCFlow([]byte("another key another key another!"), sealed, now)
	assert.False(t, ok, "a flow sealed with another key must not open")

	_, ok = auth.OpenOIDCFlow(key, "garbage", now)
	assert.False(t, ok)
}

func TestOIDCClientAsksTheIssuerForAConnectionOnlyWhenNamed(t *testing.T) {
	issuer := testutil.NewFakeIssuer(t)
	client := newOIDCClient(t, issuer)

	for _, connection := range []string{"", "github"} {
		authURL, _, err := client.Start(callbackURL, connection)
		require.NoError(t, err)
		authorize(t, authURL)
	}

	asked := issuer.Authorizations()
	require.Len(t, asked, 2)
	for _, query := range asked {
		_, sent := query["connection"]
		assert.Equal(t, query.Get("connection") != "", sent, "an empty connection is not sent")
	}
	assert.ElementsMatch(t, []string{"", "github"}, []string{asked[0].Get("connection"), asked[1].Get("connection")})
}

func TestOIDCConnectionNamesTheProvidersTheConsoleOffers(t *testing.T) {
	google, ok := auth.OIDCConnection("google")
	assert.True(t, ok)
	assert.Equal(t, "google-oauth2", google)

	github, ok := auth.OIDCConnection("github")
	assert.True(t, ok)
	assert.Equal(t, "github", github)

	_, ok = auth.OIDCConnection("facebook")
	assert.False(t, ok)
}
