package fakecp

import (
	"net/http"
	"testing"
	"time"

	"github.com/percona/pds/apps/mcp/internal/oauth"
	"github.com/percona/pds/apps/mcp/internal/tokenstore"
)

// SignIn plays a person running pds-mcp login: it runs the browser flow against the fake with the
// scopes asked for, and leaves the refresh token and the scopes the fake granted in the store.
func SignIn(t testing.TB, cp *Server, store *tokenstore.Store, scopes ...string) {
	t.Helper()
	hc := oauth.NewHTTPClient(cp.URL, "test", time.Minute)
	server, err := oauth.Discover(t.Context(), hc, cp.URL)
	if err != nil {
		t.Fatal(err)
	}
	opened := make(chan string, 1)
	go func() {
		if status, err := Browse(t.Context(), <-opened); err != nil || status != http.StatusOK {
			t.Errorf("browse: %d %v", status, err)
		}
	}()
	tokens, err := oauth.Login(t.Context(), oauth.LoginOptions{
		Server: server, HTTP: hc, Scopes: scopes, Timeout: 20 * time.Second,
		Open: func(address string) error { opened <- address; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(cp.URL, tokenstore.Login{RefreshToken: tokens.RefreshToken, Scope: tokens.Scope}); err != nil {
		t.Fatal(err)
	}
}
