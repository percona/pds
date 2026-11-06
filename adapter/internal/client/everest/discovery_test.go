package everest_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
)

func TestPinnedTokenPathsMatchOpenEverestDev4(t *testing.T) {
	assert.Equal(t, "2.0.0-dev.4", everest.PinnedRelease)
	assert.Equal(t, "/v1/auth/token", everest.AuthTokenPath)
	assert.Equal(t, "/v1/auth/revoke", everest.AuthRevokePath)
}

func TestPinnedReleaseMatchesTheChart(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "charts", "pds")
	values, err := os.ReadFile(filepath.Join(root, "values.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(values), "version: "+everest.PinnedRelease)

	chart, err := os.ReadFile(filepath.Join(root, "Chart.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(chart), "version: "+everest.PinnedRelease)
}

func TestGetMonitoringConfigUsesANamespacedPath(t *testing.T) {
	client, got := newRoutedEverest(t, map[string]route{
		"GET /v1/clusters/main/namespaces/pds/monitoring-configs/pmm": {
			status: http.StatusOK,
			body:   []byte(`{"metadata":{"name":"pmm"}}`),
		},
	})

	config, err := client.GetMonitoringConfig(context.Background(), testNamespace, "pmm")
	require.NoError(t, err)
	assert.Equal(t, "pmm", config.Metadata.Name)
	assert.Equal(t, "/v1/clusters/main/namespaces/pds/monitoring-configs/pmm", got.path)
}

type route struct {
	status int
	body   []byte
}

type lastCall struct {
	method string
	path   string
	auth   string
	body   []byte
}

func newRoutedEverest(t *testing.T, routes map[string]route) (*everest.Client, *lastCall) {
	t.Helper()

	got := &lastCall{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method = r.Method
		got.path = r.URL.Path
		got.auth = r.Header.Get("Authorization")
		got.body, _ = io.ReadAll(r.Body)

		match, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"unregistered"}`))

			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(match.status)
		_, _ = w.Write(match.body)
	}))
	t.Cleanup(srv.Close)

	client, err := everest.New(everest.Options{BaseURL: srv.URL})
	require.NoError(t, err)

	return client, got
}
