package everest_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
)

func TestAPIErrorUnwrapsAnUnauthorizedStatus(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"message":"invalid or expired jwt"}`)
		}))

		client, err := everest.New(everest.Options{BaseURL: server.URL, Token: "stale"})
		require.NoError(t, err)

		_, err = client.ListInstances(context.Background(), testNamespace)
		require.Error(t, err)
		assert.ErrorIs(t, err, everest.ErrUnauthorized, "status %d", status)

		server.Close()
	}
}

// The token is a session that expires while the adapter is running, so a 401 is
// a reason to get a new one rather than a reason to fail the request.
func TestATokenSourceRefreshesOnceAfterA401(t *testing.T) {
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"message":"invalid or expired jwt"}`)

			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"items":[]}`)
	}))
	defer server.Close()

	tokens := []string{"stale", "fresh"}
	asked := 0
	client, err := everest.New(everest.Options{
		BaseURL: server.URL,
		TokenSource: func(_ context.Context, refresh bool) (string, error) {
			if refresh && asked < len(tokens)-1 {
				asked++
			}

			return tokens[asked], nil
		},
	})
	require.NoError(t, err)

	_, err = client.ListInstances(context.Background(), testNamespace)
	require.NoError(t, err, "the retry with a fresh token carries the call")

	assert.Equal(t, []string{"Bearer stale", "Bearer fresh"}, seen,
		"one attempt with what was cached, one with what replaced it")
}

func TestARefreshedTokenThatIsStillRefusedStops(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"invalid or expired jwt"}`)
	}))
	defer server.Close()

	client, err := everest.New(everest.Options{
		BaseURL:     server.URL,
		TokenSource: func(context.Context, bool) (string, error) { return "same", nil },
	})
	require.NoError(t, err)

	_, err = client.ListInstances(context.Background(), testNamespace)
	require.Error(t, err)
	assert.ErrorIs(t, err, everest.ErrUnauthorized)
	assert.Equal(t, 1, calls,
		"a source that hands back the same token is not retried into a loop")
}

func TestSessionTradesTheAdminPasswordForAToken(t *testing.T) {
	var body map[string]any
	path := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"minted-jwt","token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()

	client, err := everest.New(everest.Options{BaseURL: server.URL})
	require.NoError(t, err)

	token, err := client.Session(context.Background(), "admin", "hunter2")
	require.NoError(t, err)

	assert.Equal(t, "minted-jwt", token)
	assert.Equal(t, everest.AuthTokenPath, path)
	assert.Equal(t, "password", body["grant_type"])
	assert.Equal(t, "admin", body["username"])
	assert.Equal(t, "hunter2", body["password"])
}

func TestSessionNeedsBothCredentials(t *testing.T) {
	client, err := everest.New(everest.Options{BaseURL: "https://everest.invalid"})
	require.NoError(t, err)

	_, err = client.Session(context.Background(), "", "hunter2")
	require.Error(t, err)

	_, err = client.Session(context.Background(), "admin", " ")
	require.Error(t, err)
}

// Logging in is how a token source gets its first token, so the login itself
// must not ask a token source for one. A source that mints through Session
// would otherwise call into itself, and it holds a lock while minting, so that
// is a deadlock rather than merely a loop.
func TestSessionDoesNotAskTheTokenSource(t *testing.T) {
	sent := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"minted-jwt","token_type":"Bearer","expires_in":3600}`)
	}))
	defer server.Close()

	asked := 0
	client, err := everest.New(everest.Options{
		BaseURL: server.URL,
		TokenSource: func(context.Context, bool) (string, error) {
			asked++

			return "should-not-be-used", nil
		},
	})
	require.NoError(t, err)

	token, err := client.Session(context.Background(), "admin", "hunter2")
	require.NoError(t, err)

	assert.Equal(t, "minted-jwt", token)
	assert.Equal(t, 0, asked, "the login does not need a token to ask for a token")
	assert.Empty(t, sent, "and it sends no Authorization header")
}
