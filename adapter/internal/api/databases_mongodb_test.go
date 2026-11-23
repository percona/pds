package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// beatEngines is a heartbeat from an agent that names the engines it serves.
func beatEngines(t *testing.T, srv http.Handler, engines ...string) {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"environment": "spoke-a",
		"namespace":   "pds",
		"engines":     engines,
		"at":          time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func createMongoDBRequest(t *testing.T, srv http.Handler, name, extra string) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, srv, http.MethodPost, "/api/databases", strings.NewReader(
		`{"name":"`+name+`","engine":"mongodb","profile":"dev","environment":"spoke-a"`+extra+`}`))
}

func TestAPICreatesAMongoDBDatabase(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatEngines(t, srv, "mongodb", "mysql", "postgresql")

	rec := createMongoDBRequest(t, srv, "orders", "")

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, "mongodb", body["engine"])
	assert.Equal(t, "cluster", body["access"].(map[string]any)["visibility"])
}

func TestAPIRefusesAMongoDBNameOf23Characters(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatEngines(t, srv, "mongodb")

	rec := createMongoDBRequest(t, srv, strings.Repeat("a", 23), "")

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "A MongoDB database name must be between 1 and 22 characters.", decodeBody(t, rec)["message"])
}

func TestAPIRefusesAccessFromOutsideForMongoDB(t *testing.T) {
	for _, extra := range []string{
		`,"access":"private"`,
		`,"access":"public","sourceRanges":["203.0.113.0/24"]`,
	} {
		srv := newHubTestServer(t, "secret")
		beatEngines(t, srv, "mongodb")

		rec := createMongoDBRequest(t, srv, "orders", extra)

		require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Equal(t, "Access from outside the Kubernetes cluster is not available for MongoDB yet.",
			decodeBody(t, rec)["message"])
		assert.Equal(t, http.StatusNotFound, do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.orders", nil).Code,
			"nothing is written for a refused create")
	}
}

func TestAPIAsksForAnUpdateWhenTheEnvironmentHasNoMongoDBProvider(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatEngines(t, srv, "mysql", "postgresql")

	rec := createMongoDBRequest(t, srv, "orders", "")

	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, "This environment does not run MongoDB yet. Update the environment, then try again.",
		decodeBody(t, rec)["message"])
}
