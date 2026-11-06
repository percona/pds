package api_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHubCreateWritesExternalAccessIntoTheManifest(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatHub(t, srv, "secret")

	rec := do(t, srv, http.MethodPost, "/api/databases", bytes.NewBufferString(
		`{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a",`+
			`"access":"public","sourceRanges":["10.0.0.0/8"]}`,
	))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	access := decodeBody(t, rec)["access"].(map[string]any)
	assert.Equal(t, true, access["external"])
	assert.Equal(t, "public", access["visibility"])
	assert.Equal(t, []any{"10.0.0.0/8"}, access["sourceRanges"])
}

func TestHubCreateIsPrivateByDefault(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatHub(t, srv, "secret")

	rec := do(t, srv, http.MethodPost, "/api/databases", bytes.NewBufferString(
		`{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a"}`,
	))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	access := decodeBody(t, rec)["access"].(map[string]any)
	assert.Equal(t, "private", access["visibility"])

	got := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.orders-dev", nil)
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	assert.Contains(t, got.Body.String(), `"access":{"external":true,"visibility":"private"}`)
}

// An agent older than private access reads every load balancer as public.
// Flux applies the manifest whatever the agent says, so the manifest wins.
func TestHubKeepsPrivateAccessWhenAnOlderAgentReportsPublic(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatHub(t, srv, "secret")

	rec := do(t, srv, http.MethodPost, "/api/databases", bytes.NewBufferString(
		`{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a","access":"private"}`,
	))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	// The agent reads the profile off the label, so it reports one, and the
	// manifest must still be read for the exposure.
	beatHubWith(t, srv, "secret", []map[string]any{reportedOrdersDev(map[string]any{
		"Profile":  "dev",
		"Exposure": map[string]any{"External": true, "Address": "internal-a1b2.elb.us-east-1.amazonaws.com"},
	})})

	got := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.orders-dev", nil)
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	access := decodeBody(t, got)["access"].(map[string]any)
	assert.Equal(t, "private", access["visibility"])
	assert.Equal(t, "internal-a1b2.elb.us-east-1.amazonaws.com", access["address"])

	list := do(t, srv, http.MethodGet, "/api/databases?environment=spoke-a", nil)
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	assert.Contains(t, list.Body.String(), `"visibility":"private"`)
	assert.NotContains(t, list.Body.String(), `"visibility":"public"`)
}

func TestHubCreateRefusesABadSourceRange(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatHub(t, srv, "secret")

	rec := do(t, srv, http.MethodPost, "/api/databases", bytes.NewBufferString(
		`{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a",`+
			`"access":"public","sourceRanges":["10.0.0.0/33"]}`,
	))

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "`10.0.0.0/33` is not an IP address or CIDR range.", decodeBody(t, rec)["message"])
}

func TestHubGetBeforeTheFirstHeartbeatReportsExternalAccess(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatHub(t, srv, "secret")

	rec := do(t, srv, http.MethodPost, "/api/databases", bytes.NewBufferString(
		`{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a","access":"public","sourceRanges":["0.0.0.0/0"]}`,
	))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	for _, path := range []string{"/api/databases/spoke-a/pds.orders-dev", "/api/databases?environment=spoke-a"} {
		got := do(t, srv, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, got.Code, got.Body.String())
		assert.Contains(t, got.Body.String(), `"access":{"external":true,"visibility":"public","sourceRanges":["0.0.0.0/0"]}`, path)
	}
}

// beatHubWith sends one heartbeat carrying instances as the spoke agent writes
// them: engine.Instance with its Go field names.
func beatHubWith(t *testing.T, srv http.Handler, token string, instances []map[string]any) {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"environment": "spoke-a",
		"namespace":   "pds",
		"at":          time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		"instances":   instances,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func reportedOrdersDev(extra map[string]any) map[string]any {
	out := map[string]any{
		"ID":          "spoke-a/pds.orders-dev",
		"Name":        "orders-dev",
		"Engine":      "mysql",
		"Namespace":   "pds",
		"Environment": "spoke-a",
		"Status":      map[string]any{"Phase": "ready"},
	}
	maps.Copy(out, extra)

	return out
}

func TestHubReportsTheAddressTheSpokeSees(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatHub(t, srv, "secret")

	rec := do(t, srv, http.MethodPost, "/api/databases", bytes.NewBufferString(
		`{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a","access":"public","sourceRanges":["10.0.0.0/8"]}`,
	))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	beatHubWith(t, srv, "secret", []map[string]any{reportedOrdersDev(map[string]any{
		"Exposure": map[string]any{
			"External":     true,
			"SourceRanges": []string{"10.0.0.0/8"},
			"Address":      "a1b2c3.elb.us-east-1.amazonaws.com",
		},
	})})

	got := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.orders-dev", nil)
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	access := decodeBody(t, got)["access"].(map[string]any)
	assert.Equal(t, "a1b2c3.elb.us-east-1.amazonaws.com", access["address"])
}

// An agent older than external access reports no exposure at all, which must
// not turn a database the hub created as external into an internal one.
func TestHubKeepsExternalAccessWhenAnOlderAgentOmitsIt(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatHub(t, srv, "secret")

	rec := do(t, srv, http.MethodPost, "/api/databases", bytes.NewBufferString(
		`{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a","access":"public","sourceRanges":["10.0.0.0/8"]}`,
	))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	beatHubWith(t, srv, "secret", []map[string]any{reportedOrdersDev(nil)})

	for _, path := range []string{"/api/databases/spoke-a/pds.orders-dev", "/api/databases?environment=spoke-a"} {
		got := do(t, srv, http.MethodGet, path, nil)
		require.Equal(t, http.StatusOK, got.Code, got.Body.String())
		assert.Contains(t, got.Body.String(), `"access":{"external":true,"visibility":"public","sourceRanges":["10.0.0.0/8"]}`, path)
	}
}
