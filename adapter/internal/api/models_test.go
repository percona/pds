package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListModelsRequiresProvider(t *testing.T) {
	rec := do(t, newConnectionsServer(t, &memStore{}), http.MethodGet, "/api/assistant/models", nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	decoded := decodeBody(t, rec)
	assert.Equal(t, "ErrValidation", decoded["code"])
	assert.Contains(t, decoded["message"], "provider")
}

func TestListModelsRejectsUnknownProvider(t *testing.T) {
	rec := do(t, newConnectionsServer(t, &memStore{}), http.MethodGet, "/api/assistant/models?provider=bedrock", nil)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	decoded := decodeBody(t, rec)
	assert.Equal(t, "ErrValidation", decoded["code"])
	assert.Equal(t, "Provider must be anthropic or cursor.", decoded["message"])
}

func TestListModelsAnthropicUnavailable(t *testing.T) {
	rec := do(t, newConnectionsServer(t, &memStore{}), http.MethodGet, "/api/assistant/models?provider=anthropic", nil)

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	decoded := decodeBody(t, rec)
	assert.Equal(t, "ErrNotImplemented", decoded["code"])
	assert.Equal(t, "Chat needs an Anthropic key, which is not configured.", decoded["message"])
}

func TestListModelsCursorUnavailable(t *testing.T) {
	rec := do(t, newConnectionsServer(t, &memStore{}), http.MethodGet, "/api/assistant/models?provider=cursor", nil)

	require.Equal(t, http.StatusNotImplemented, rec.Code)
	decoded := decodeBody(t, rec)
	assert.Equal(t, "ErrNotImplemented", decoded["code"])
	assert.Equal(t, "Chat needs a Cursor key, which is not configured.", decoded["message"])
}

func TestListModelsRefusesARemovedProvider(t *testing.T) {
	for _, provider := range []string{"openai", "gemini", "openrouter"} {
		t.Run(provider, func(t *testing.T) {
			rec := do(t, newConnectionsServer(t, &memStore{}), http.MethodGet, "/api/assistant/models?provider="+provider, nil)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			decoded := decodeBody(t, rec)
			assert.Equal(t, "ErrValidation", decoded["code"])
			assert.Equal(t, "Provider must be anthropic or cursor.", decoded["message"])
		})
	}
}

func TestListModelsAnthropicReturnsModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"claude-sonnet-4-20250514","display_name":"Claude Sonnet 4"}]}`))
	}))
	t.Cleanup(srv.Close)

	store := &memStore{secrets: map[string]map[string]string{
		"pds/pds-assistant": {
			"ANTHROPIC_API_KEY":  "sk-ant-test",
			"ANTHROPIC_BASE_URL": srv.URL,
		},
	}}
	rec := do(t, newConnectionsServer(t, store), http.MethodGet, "/api/assistant/models?provider=anthropic", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	decoded := decodeBody(t, rec)
	models, ok := decoded["models"].([]any)
	require.True(t, ok)
	require.Len(t, models, 1)
	first := models[0].(map[string]any)
	assert.Equal(t, "claude-sonnet-4-20250514", first["id"])
	assert.Equal(t, "Claude Sonnet 4", first["displayName"])
	assert.NotContains(t, rec.Body.String(), "sk-ant-test")
}

func TestListModelsAnthropicEmptyArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)

	store := &memStore{secrets: map[string]map[string]string{
		"pds/pds-assistant": {
			"ANTHROPIC_API_KEY":  "sk-ant-test",
			"ANTHROPIC_BASE_URL": srv.URL,
		},
	}}
	rec := do(t, newConnectionsServer(t, store), http.MethodGet, "/api/assistant/models?provider=anthropic", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"models":[]`)
	assert.NotContains(t, rec.Body.String(), `"models":null`)
}

func TestListModelsAnthropicMarksTheDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"claude-opus-5-5","display_name":"Claude Opus 5.5"},
			{"id":"claude-sonnet-5-5","display_name":"Claude Sonnet 5.5"},
			{"id":"claude-sonnet-4-20250514","display_name":"Claude Sonnet 4"}
		]}`))
	}))
	t.Cleanup(srv.Close)

	store := &memStore{secrets: map[string]map[string]string{
		"pds/pds-assistant": {
			"ANTHROPIC_API_KEY":  "sk-ant-test",
			"ANTHROPIC_BASE_URL": srv.URL,
		},
	}}
	rec := do(t, newConnectionsServer(t, store), http.MethodGet, "/api/assistant/models?provider=anthropic", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	models := decodeBody(t, rec)["models"].([]any)
	require.Len(t, models, 3)
	defaults := map[string]any{}
	for _, item := range models {
		model := item.(map[string]any)
		defaults[model["id"].(string)] = model["default"]
	}
	assert.Equal(t, true, defaults["claude-sonnet-5-5"])
	assert.NotEqual(t, true, defaults["claude-opus-5-5"])
	assert.NotEqual(t, true, defaults["claude-sonnet-4-20250514"])
}
