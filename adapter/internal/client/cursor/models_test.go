package cursor_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/cursor"
)

func TestListModelsSendsBasicAuthAndParsesModels(t *testing.T) {
	var authHeader string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/v1/models", r.URL.Path)
		authHeader = r.Header.Get("Authorization")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{"id":"composer-2","displayName":"Composer 2"}]}`))
	}))
	t.Cleanup(srv.Close)

	got, err := cursor.ListModels(context.Background(), cursor.Options{
		BaseURL: srv.URL,
		APIKey:  "cursor-key-test",
	})
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(authHeader, "Basic "))
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(authHeader, "Basic "))
	require.NoError(t, err)
	assert.Equal(t, "cursor-key-test:", string(decoded))

	assert.Equal(t, []cursor.Model{{
		ID:          "composer-2",
		DisplayName: "Composer 2",
	}}, got)
}

func TestListModelsUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	_, err := cursor.ListModels(context.Background(), cursor.Options{
		BaseURL: srv.URL,
		APIKey:  "cursor-key-test",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, cursor.ErrUnauthorized)
}
