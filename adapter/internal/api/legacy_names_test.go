package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
)

func TestTheOldEnvironmentListRouteAnswersWithBothSpellings(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatHub(t, srv, "secret")

	rec := do(t, srv, http.MethodGet, "/api/clusters", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeBody(t, rec)
	require.Len(t, body["environments"], 1)
	assert.Equal(t, body["environments"], body["clusters"])
	assert.Equal(t, true, body["hub"])
}

func TestAnAgentThatHeartbeatsOnTheOldPathAndBodyStaysRegistered(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")
	issued := do(t, srv, http.MethodPost, "/api/connect/tokens", bytes.NewBufferString(`{"name":"spoke-a"}`))
	require.Equal(t, http.StatusCreated, issued.Code)

	req := httptest.NewRequest(http.MethodPost, "/api/internal/clusters/spoke-a/heartbeat",
		bytes.NewBufferString(`{"cluster":"spoke-a","namespace":"pds"}`))
	req.Header.Set("Authorization", "Bearer "+decodeBody(t, issued)["token"].(string))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	listed := decodeBody(t, do(t, srv, http.MethodGet, "/api/environments", nil))
	environments, ok := listed["environments"].([]any)
	require.True(t, ok)
	require.Len(t, environments, 1)
	assert.Equal(t, "spoke-a", environments[0].(map[string]any)["name"])
}

func TestAClientThatSaysClusterCanStillCreateAndListDatabases(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatHub(t, srv, "secret")

	created := do(t, srv, http.MethodPost, "/api/databases", bytes.NewBufferString(
		`{"name":"orders-dev","engine":"mysql","profile":"dev","cluster":"spoke-a"}`))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	body := decodeBody(t, created)
	assert.Equal(t, "spoke-a", body["environment"])
	assert.Equal(t, "spoke-a", body["cluster"], "an older MCP server reads this field")

	listed := do(t, srv, http.MethodGet, "/api/databases?cluster=spoke-a", nil)
	require.Equal(t, http.StatusOK, listed.Code)
	assert.Len(t, decodeBody(t, listed)["databases"], 1)
}

func TestAResponseThatNeverNamesAnEnvironmentIsLeftAlone(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	rec := do(t, srv, http.MethodGet, "/api/features", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), `"cluster"`)
}

func TestLegacyNamesRewritesOnlyWhatTheCurrentNameDoesNotAlreadyCover(t *testing.T) {
	type seen struct {
		Path  string
		Query string
		Body  map[string]any
	}
	tests := []struct {
		name   string
		target string
		body   string
		want   seen
	}{
		{
			"old route, query and field",
			"/api/clusters/spoke-a/monitoring?cluster=spoke-a",
			`{"cluster":"spoke-a","namespace":"pds"}`,
			seen{"/api/environments/spoke-a/monitoring", "environment=spoke-a", map[string]any{"environment": "spoke-a", "namespace": "pds"}},
		},
		{
			"agent route",
			"/api/internal/clusters/spoke-a/join",
			`{"cluster":"spoke-a"}`,
			seen{"/api/internal/environments/spoke-a/join", "", map[string]any{"environment": "spoke-a"}},
		},
		{
			"current names win over old ones",
			"/api/databases?cluster=old&environment=new",
			`{"cluster":"old","environment":"new"}`,
			seen{"/api/databases", "cluster=old&environment=new", map[string]any{"cluster": "old", "environment": "new"}},
		},
		{
			"a path that only starts like the old route",
			"/api/clustersx",
			`{}`,
			seen{"/api/clustersx", "", map[string]any{}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got seen
			handler := api.LegacyNames(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				got.Path, got.Query = r.URL.Path, r.URL.RawQuery
				raw, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.NoError(t, json.Unmarshal(raw, &got.Body))
			}))
			req := httptest.NewRequest(http.MethodPut, tc.target, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")

			handler.ServeHTTP(httptest.NewRecorder(), req)

			assert.Equal(t, tc.want, got)
		})
	}
}

func TestLegacyNamesPassesABodyThatIsNotAnObjectThrough(t *testing.T) {
	var got string
	handler := api.LegacyNames(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got = string(raw)
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/databases", strings.NewReader(`["cluster"]`))
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, `["cluster"]`, got)
}
