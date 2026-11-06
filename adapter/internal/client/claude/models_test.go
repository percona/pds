package claude_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/claude"
)

func TestListModelsSendsHeadersAndParsesModels(t *testing.T) {
	var captured struct {
		APIKey  string
		Version string
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/models", r.URL.Path)
		captured.APIKey = r.Header.Get("x-api-key")
		captured.Version = r.Header.Get("anthropic-version")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-20250514","display_name":"Claude Sonnet 4","type":"model"}]}`))
	}))
	t.Cleanup(srv.Close)

	got, err := claude.ListModels(context.Background(), claude.Options{
		BaseURL: srv.URL,
		APIKey:  "sk-ant-test",
	})
	require.NoError(t, err)

	assert.Equal(t, "sk-ant-test", captured.APIKey)
	assert.Equal(t, "2023-06-01", captured.Version)
	assert.Equal(t, []claude.Model{{
		ID:          "claude-sonnet-4-20250514",
		DisplayName: "Claude Sonnet 4",
	}}, got)
}

func TestListModelsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	_, err := claude.ListModels(context.Background(), claude.Options{
		BaseURL: srv.URL,
		APIKey:  "sk-ant-test",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, claude.ErrUnauthorized)
}

func TestListModelsFallsBackDisplayNameToID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-20250514","type":"model"}]}`))
	}))
	t.Cleanup(srv.Close)

	got, err := claude.ListModels(context.Background(), claude.Options{
		BaseURL: srv.URL,
		APIKey:  "sk-ant-test",
	})
	require.NoError(t, err)
	assert.Equal(t, []claude.Model{{
		ID:          "claude-sonnet-4-20250514",
		DisplayName: "claude-sonnet-4-20250514",
	}}, got)
}

func TestListModelsSkipsBlankID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"","display_name":"Skip me"},{"id":"claude-sonnet-4-20250514","display_name":"Claude Sonnet 4"}]}`))
	}))
	t.Cleanup(srv.Close)

	got, err := claude.ListModels(context.Background(), claude.Options{
		BaseURL: srv.URL,
		APIKey:  "sk-ant-test",
	})
	require.NoError(t, err)
	assert.Equal(t, []claude.Model{{
		ID:          "claude-sonnet-4-20250514",
		DisplayName: "Claude Sonnet 4",
	}}, got)
}
