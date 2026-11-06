package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/service"
)

func TestListModelsAnthropic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-20250514","display_name":"Claude Sonnet 4"}]}`))
	}))
	t.Cleanup(srv.Close)

	store := &fakeStore{secrets: map[string]map[string]string{
		"pds/pds-assistant": {
			"ANTHROPIC_API_KEY":  "sk-ant-test",
			"ANTHROPIC_BASE_URL": srv.URL,
		},
	}}
	conns := service.NewConnections(store, service.ConnectionsOptions{
		Namespace:           "pds",
		AssistantSecretName: "pds-assistant",
	})

	got, err := conns.ListModels(context.Background(), "anthropic", srv.Client().Transport)
	require.NoError(t, err)
	assert.Equal(t, []service.Model{{
		ID:          "claude-sonnet-4-20250514",
		DisplayName: "Claude Sonnet 4",
	}}, got)
}

func TestListModelsCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/models", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"composer-2","displayName":"Composer 2"}]}`))
	}))
	t.Cleanup(srv.Close)

	store := &fakeStore{secrets: map[string]map[string]string{
		"pds/pds-cursor": {"CURSOR_API_KEY": "cursor-key"},
	}}
	conns := service.NewConnections(store, service.ConnectionsOptions{
		Namespace:        "pds",
		CursorSecretName: "pds-cursor",
	})

	srvURL, err := url.Parse(srv.URL)
	require.NoError(t, err)
	transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = srvURL.Scheme
		req.URL.Host = srvURL.Host
		return http.DefaultTransport.RoundTrip(req)
	})

	got, err := conns.ListModels(context.Background(), "cursor", transport)
	require.NoError(t, err)
	assert.Equal(t, []service.Model{{
		ID:          "composer-2",
		DisplayName: "Composer 2",
	}}, got)
}

func TestListModelsUnknownProvider(t *testing.T) {
	conns := service.NewConnections(&fakeStore{}, service.ConnectionsOptions{})

	for _, provider := range []string{"bedrock", "openai", "gemini", "openrouter"} {
		_, err := conns.ListModels(context.Background(), provider, nil)
		require.Error(t, err, provider)
		assert.ErrorIs(t, err, service.ErrUnknownProvider, provider)
	}
}

func TestListModelsAnthropicUnavailable(t *testing.T) {
	conns := service.NewConnections(&fakeStore{}, service.ConnectionsOptions{})

	_, err := conns.ListModels(context.Background(), "anthropic", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, service.ErrAssistantUnavailable)
}

func TestListModelsCursorUnavailable(t *testing.T) {
	conns := service.NewConnections(&fakeStore{}, service.ConnectionsOptions{})

	_, err := conns.ListModels(context.Background(), "cursor", nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, service.ErrCursorUnavailable)
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
