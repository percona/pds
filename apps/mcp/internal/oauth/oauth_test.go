package oauth_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/percona/pds/apps/mcp/internal/fakecp"
	"github.com/percona/pds/apps/mcp/internal/oauth"
	"github.com/percona/pds/apps/mcp/internal/tokenstore"
)

func TestChallengeIsTheS256OfTheVerifier(t *testing.T) {
	// The worked example of the PKCE standard.
	got := oauth.Challenge("dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk")
	if want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"; got != want {
		t.Errorf("Challenge = %q, want %q", got, want)
	}
}

func TestEachVerifierIsLongAndNew(t *testing.T) {
	a, b := oauth.NewVerifier(), oauth.NewVerifier()
	if a == b || len(a) < 43 || len(a) > 128 {
		t.Errorf("verifiers %q and %q are not 43 to 128 distinct characters", a, b)
	}
}

func discover(t *testing.T, cp *fakecp.Server) oauth.Server {
	t.Helper()
	server, err := oauth.Discover(t.Context(), oauth.NewHTTPClient(cp.URL, "test", time.Minute), cp.URL)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	return server
}

func TestDiscoveryReadsTheEndpointsOfTheControlPlane(t *testing.T) {
	cp := fakecp.New(t)
	server := discover(t, cp)

	if server.Issuer != cp.URL || server.TokenEndpoint != cp.URL+"/oauth/token" ||
		server.AuthorizationEndpoint != cp.URL+"/oauth/authorize" || server.Resource != cp.URL+"/mcp" ||
		server.RevocationEndpoint != cp.URL+"/oauth/revoke" {
		t.Errorf("Discover = %+v", server)
	}
}

func TestDiscoveryRefusesWhatCouldSendATokenElsewhere(t *testing.T) {
	cases := map[string]func(*fakecp.Server){
		"issuer is another origin": func(cp *fakecp.Server) {
			cp.Configure(func(c *fakecp.Config) { c.Issuer = "https://evil.example" })
		},
		"no S256": func(cp *fakecp.Server) {
			cp.Configure(func(c *fakecp.Config) {
				c.Mutate = func(m map[string]any) { m["code_challenge_methods_supported"] = []string{"plain"} }
			})
		},
		"token endpoint on another origin": func(cp *fakecp.Server) {
			cp.Configure(func(c *fakecp.Config) {
				c.Mutate = func(m map[string]any) { m["token_endpoint"] = "https://evil.example/token" }
			})
		},
		"authorization endpoint on another origin": func(cp *fakecp.Server) {
			cp.Configure(func(c *fakecp.Config) {
				c.Mutate = func(m map[string]any) { m["authorization_endpoint"] = "https://evil.example/authorize" }
			})
		},
		"revocation endpoint on another origin": func(cp *fakecp.Server) {
			cp.Configure(func(c *fakecp.Config) {
				c.Mutate = func(m map[string]any) { m["revocation_endpoint"] = "https://evil.example/revoke" }
			})
		},
		"metadata names another issuer": func(cp *fakecp.Server) {
			cp.Configure(func(c *fakecp.Config) { c.Mutate = func(m map[string]any) { m["issuer"] = "https://evil.example" } })
		},
	}
	for name, change := range cases {
		cp := fakecp.New(t)
		change(cp)
		if _, err := oauth.Discover(t.Context(), oauth.NewHTTPClient(cp.URL, "test", time.Minute), cp.URL); err == nil {
			t.Errorf("%s: discovery went ahead", name)
		}
	}
}

func TestAControlPlaneWithNoMCPEndpointIsReported(t *testing.T) {
	cp := fakecp.New(t)
	cp.Configure(func(c *fakecp.Config) { c.MCPOff = true })

	_, err := oauth.Discover(t.Context(), oauth.NewHTTPClient(cp.URL, "test", time.Minute), cp.URL)
	if !errors.Is(err, oauth.ErrNotServed) {
		t.Errorf("err = %v, want ErrNotServed", err)
	}
}

func TestTheClientNeverLeavesTheControlPlaneOrigin(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	home := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusFound)
	}))
	defer home.Close()
	client := oauth.NewHTTPClient(home.URL, "test", time.Minute)

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, home.URL+"/x", nil)
	req.Header.Set("Authorization", "Bearer secret")
	if resp, err := client.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Error("a redirect was followed")
	}

	req, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, other.URL+"/direct", nil)
	req.Header.Set("Authorization", "Bearer secret")
	if resp, err := client.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Error("a request to another origin was sent")
	}
	if elsewhere.Load() != 0 {
		t.Errorf("%d requests reached another origin", elsewhere.Load())
	}
}

func TestTheClientNamesItself(t *testing.T) {
	var agent string
	home := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { agent = r.UserAgent() }))
	defer home.Close()

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, home.URL, nil)
	resp, err := oauth.NewHTTPClient(home.URL, "pds-mcp/1.2.3", time.Minute).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if agent != "pds-mcp/1.2.3" {
		t.Errorf("User-Agent = %q", agent)
	}
}

// browser plays the person's browser, and records what the command asked it to open.
type browser struct {
	opened chan string
}

func newBrowser() *browser { return &browser{opened: make(chan string, 1)} }

func (b *browser) open(address string) error {
	b.opened <- address

	return nil
}

func login(t *testing.T, cp *fakecp.Server, b *browser, scopes ...string) (oauth.Tokens, error) {
	t.Helper()
	if len(scopes) == 0 {
		scopes = []string{"pds:read", "pds:change", "offline_access"}
	}

	return oauth.Login(t.Context(), oauth.LoginOptions{
		Server: discover(t, cp), HTTP: oauth.NewHTTPClient(cp.URL, "test", time.Minute),
		Scopes: scopes, Open: b.open, Announce: func(string) {}, Timeout: 20 * time.Second,
	})
}

func TestLoginSendsPKCEStateAndTheBuiltInClientAndStoresNothingItShouldNot(t *testing.T) {
	cp := fakecp.New(t)
	b := newBrowser()
	done := make(chan struct{})
	go func() {
		defer close(done)
		address := <-b.opened
		// Login returns once the token exchange is done, which can be before the browser has
		// read the callback page, so the browser outlives the test's context and the test waits.
		if status, err := fakecp.Browse(context.WithoutCancel(t.Context()), address); err != nil || status != http.StatusOK {
			t.Errorf("the callback answered %d, %v", status, err)
		}
	}()
	t.Cleanup(func() { <-done })

	tokens, err := login(t, cp, b)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.Scope != "pds:read pds:change offline_access" {
		t.Errorf("tokens = %+v", tokens)
	}

	forms := cp.Forms()
	if len(forms) != 1 {
		t.Fatalf("%d token requests", len(forms))
	}
	form := forms[0]
	if form.Get("grant_type") != "authorization_code" || form.Get("client_id") != "pds-mcp" ||
		form.Get("resource") != cp.URL+"/mcp" || len(form.Get("code_verifier")) < 43 {
		t.Errorf("token request = %v", form)
	}
	if !strings.HasPrefix(form.Get("redirect_uri"), "http://127.0.0.1:") ||
		!strings.HasSuffix(form.Get("redirect_uri"), "/callback") {
		t.Errorf("redirect_uri = %q", form.Get("redirect_uri"))
	}
}

func TestTheAuthorizationRequestCarriesAnS256ChallengeOfTheVerifierItSendsLater(t *testing.T) {
	cp := fakecp.New(t)
	b := newBrowser()
	var query url.Values
	go func() {
		address := <-b.opened
		u, _ := url.Parse(address)
		query = u.Query()
		_, _ = fakecp.Browse(t.Context(), address)
	}()

	if _, err := login(t, cp, b); err != nil {
		t.Fatal(err)
	}

	if query.Get("code_challenge_method") != "S256" || query.Get("response_type") != "code" ||
		query.Get("client_id") != "pds-mcp" ||
		query.Get("resource") != cp.URL+"/mcp" || len(query.Get("state")) < 16 {
		t.Errorf("authorization request = %v", query)
	}
	if want := oauth.Challenge(cp.Forms()[0].Get("code_verifier")); query.Get("code_challenge") != want {
		t.Errorf("code_challenge = %q, want the S256 of the verifier sent later (%q)", query.Get("code_challenge"), want)
	}
	if got := strings.Fields(query.Get("scope")); strings.Join(got, " ") != "pds:read pds:change offline_access" {
		t.Errorf("scope = %q", query.Get("scope"))
	}
	if query.Has("code_verifier") {
		t.Error("the verifier was sent to the authorization endpoint")
	}
}

func TestTheListenerIsOnTheLoopbackAndClosesAfterTheCallback(t *testing.T) {
	cp := fakecp.New(t)
	b := newBrowser()
	var redirect string
	go func() {
		address := <-b.opened
		u, _ := url.Parse(address)
		redirect = u.Query().Get("redirect_uri")
		_, _ = fakecp.Browse(t.Context(), address)
	}()

	if _, err := login(t, cp, b); err != nil {
		t.Fatal(err)
	}

	u, err := url.Parse(redirect)
	if err != nil || u.Hostname() != "127.0.0.1" || u.Path != "/callback" || u.Port() == "" || u.Port() == "0" {
		t.Fatalf("redirect = %q", redirect)
	}
	conn, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err == nil {
		_ = conn.Close()
		t.Error("the listener still accepts connections after the callback")
	}
}

func TestACallbackWithTheWrongStateIsRefusedAndTheLoginWaitsForTheRightOne(t *testing.T) {
	cp := fakecp.New(t)
	b := newBrowser()
	var wrongStatus int
	go func() {
		address := <-b.opened
		u, _ := url.Parse(address)
		callback := u.Query().Get("redirect_uri") + "?code=stolen&state=wrong&iss=" + url.QueryEscape(cp.URL)
		req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, callback, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			wrongStatus = resp.StatusCode
			_ = resp.Body.Close()
		}
		_, _ = fakecp.Browse(t.Context(), address)
	}()

	tokens, err := login(t, cp, b)
	if err != nil || tokens.RefreshToken == "" {
		t.Fatalf("login = %+v, %v", tokens, err)
	}
	if wrongStatus != http.StatusBadRequest {
		t.Errorf("the wrong state was answered %d, want 400", wrongStatus)
	}
	if len(cp.Forms()) != 1 {
		t.Errorf("the code with the wrong state was exchanged: %d token requests", len(cp.Forms()))
	}
}

func TestACallbackFromAnotherIssuerOrWithoutOneIsRefused(t *testing.T) {
	cp := fakecp.New(t)
	cp.Configure(func(c *fakecp.Config) { c.OmitIss = true })
	b := newBrowser()
	go func() { _, _ = fakecp.Browse(t.Context(), <-b.opened) }()

	if _, err := login(t, cp, b); err == nil {
		t.Error("a response with no iss was accepted")
	}
	if len(cp.Forms()) != 0 {
		t.Error("the code was exchanged")
	}
}

func TestADeniedLoginIsReported(t *testing.T) {
	cp := fakecp.New(t)
	cp.Configure(func(c *fakecp.Config) { c.Deny = true })
	b := newBrowser()
	go func() { _, _ = fakecp.Browse(t.Context(), <-b.opened) }()

	if _, err := login(t, cp, b); !errors.Is(err, oauth.ErrDenied) {
		t.Errorf("err = %v, want ErrDenied", err)
	}
}

func TestLoginGivesUpWhenNobodyComesBack(t *testing.T) {
	cp := fakecp.New(t)
	b := newBrowser()

	_, err := oauth.Login(t.Context(), oauth.LoginOptions{
		Server: discover(t, cp), HTTP: oauth.NewHTTPClient(cp.URL, "test", time.Minute),
		Scopes: []string{"pds:read"}, Open: b.open, Announce: func(string) {}, Timeout: 50 * time.Millisecond,
	})
	if !errors.Is(err, oauth.ErrLoginTimedOut) {
		t.Errorf("err = %v, want ErrLoginTimedOut", err)
	}
}

func TestALoginTheServerWillNotKeepIsRefused(t *testing.T) {
	cp := fakecp.New(t)
	cp.Configure(func(c *fakecp.Config) { c.NoRefreshToken = true })
	b := newBrowser()
	go func() { _, _ = fakecp.Browse(t.Context(), <-b.opened) }()

	if _, err := login(t, cp, b); !errors.Is(err, oauth.ErrNoRefreshToken) {
		t.Errorf("err = %v, want ErrNoRefreshToken", err)
	}
}

// signedIn returns a source whose refresh token came from a real login against the fake.
func signedIn(t *testing.T, cp *fakecp.Server) (*oauth.Source, *tokenstore.Store) {
	t.Helper()
	b := newBrowser()
	go func() { _, _ = fakecp.Browse(t.Context(), <-b.opened) }()
	tokens, err := login(t, cp, b)
	if err != nil {
		t.Fatal(err)
	}

	store := tokenstore.New(filepath.Join(t.TempDir(), "credentials.json"))
	if err := store.Save(cp.URL, tokenstore.Login{RefreshToken: tokens.RefreshToken, Scope: tokens.Scope}); err != nil {
		t.Fatal(err)
	}

	return oauth.NewSource(discover(t, cp), oauth.NewHTTPClient(cp.URL, "test", time.Minute), store), store
}

func TestTheSourceRefreshesOnceAndReusesTheAccessTokenUntilItIsRejected(t *testing.T) {
	cp := fakecp.New(t)
	source, store := signedIn(t, cp)
	before, _ := store.Load(cp.URL)

	first, err := source.Token(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	again, _ := source.Token(t.Context())
	if first != again || len(cp.Forms()) != 2 {
		t.Fatalf("the access token was not reused: %d token requests", len(cp.Forms()))
	}

	second, err := source.Refresh(t.Context(), first)
	if err != nil || second == first {
		t.Fatalf("Refresh = %q, %v", second, err)
	}
	// A rejection of a token another caller already replaced does not refresh again.
	if third, _ := source.Refresh(t.Context(), first); third != second || len(cp.Forms()) != 3 {
		t.Errorf("a stale rejection refreshed again: %d token requests", len(cp.Forms()))
	}

	after, _ := store.Load(cp.URL)
	if after.RefreshToken == before.RefreshToken || after.RefreshToken == "" {
		t.Error("the rotated refresh token was not kept")
	}
}

func TestTheSourceRefreshesAnExpiredAccessTokenBeforeUsingIt(t *testing.T) {
	cp := fakecp.New(t)
	cp.Configure(func(c *fakecp.Config) { c.ExpiresIn = 10 * time.Second })
	source, _ := signedIn(t, cp)

	first, err := source.Token(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	// Ten seconds is inside the margin the source keeps, so the token is treated as expired.
	second, err := source.Token(t.Context())
	if err != nil || second == first {
		t.Errorf("Token = %q, %v: an access token about to expire was reused", second, err)
	}
}

func TestARevokedSessionEndsWithAnError(t *testing.T) {
	cp := fakecp.New(t)
	source, store := signedIn(t, cp)
	cp.RejectRefreshes(1)

	_, err := source.Token(t.Context())
	if !errors.Is(err, oauth.ErrSessionEnded) {
		t.Errorf("err = %v, want ErrSessionEnded", err)
	}
	if _, err := store.Load(cp.URL); err != nil {
		t.Errorf("a refused refresh removed the login: %v", err)
	}
}

func TestNoLoginMeansNoToken(t *testing.T) {
	cp := fakecp.New(t)
	store := tokenstore.New(filepath.Join(t.TempDir(), "credentials.json"))
	source := oauth.NewSource(discover(t, cp), oauth.NewHTTPClient(cp.URL, "test", time.Minute), store)

	if _, err := source.Token(t.Context()); !errors.Is(err, tokenstore.ErrNoLogin) {
		t.Errorf("err = %v, want ErrNoLogin", err)
	}
}

func TestTwoProcessesSharingTheLoginNeverUseOneRefreshTokenTwice(t *testing.T) {
	cp := fakecp.New(t)
	one, store := signedIn(t, cp)
	two := oauth.NewSource(discover(t, cp), oauth.NewHTTPClient(cp.URL, "test", time.Minute), store)

	if _, err := one.Token(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := two.Token(t.Context()); err != nil {
		t.Fatalf("the second process was refused: %v", err)
	}
}

func TestRevokeSendsTheRefreshTokenToTheRevocationEndpoint(t *testing.T) {
	cp := fakecp.New(t)
	if err := oauth.Revoke(t.Context(), discover(t, cp), oauth.NewHTTPClient(cp.URL, "test", time.Minute), "rt-x"); err != nil {
		t.Fatal(err)
	}
	if got := cp.Revoked(); len(got) != 1 || got[0] != "rt-x" {
		t.Errorf("revoked = %v", got)
	}
}
