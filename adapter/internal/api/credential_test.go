package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
)

// TestCredentialSaysTheHubHasNone covers the button that could never work.
// The hub strips the connection string on the way in, so the route always
// took the not-provisioned branch and answered 404, which reads like the
// database is still coming up and invites a retry that never succeeds.
func TestCredentialSaysTheHubHasNone(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	rec := do(t, srv, http.MethodGet,
		"/api/databases/spoke-a/pds.orders-dev/credential", nil)

	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, api.CodeNotImplemented, body["code"])
	assert.Contains(t, body["message"], "No password is held here.")
	assert.NotContains(t, body["message"], "hub")
}

// A public database is the one case the hub holds a connection string, because
// that string is what a client outside the environment copies. The detail payload
// still shows the mask.
func TestCredentialReturnsThePublicConnectionString(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	const password = "hunter2"
	payload, err := json.Marshal(map[string]any{
		"environment":   "spoke-a",
		"namespace":     "pds",
		"at":            time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		"privateAccess": true,
		"instances": []map[string]any{{
			"Name":      "orders-ext",
			"Namespace": "pds",
			"Engine":    "mysql",
			"Exposure":  map[string]any{"External": true, "Address": "198.51.100.20"},
			"Connection": map[string]any{
				"Host":     "198.51.100.20",
				"Port":     3306,
				"Username": "root",
				"URI":      "mysql://root:" + password + "@198.51.100.20:3306",
			},
		}},
	})
	require.NoError(t, err)
	beat := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	beat.Header.Set("Authorization", "Bearer secret")
	beat.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, beat)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	detail := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.orders-ext", nil)
	require.Equal(t, http.StatusOK, detail.Code, detail.Body.String())
	assert.NotContains(t, detail.Body.String(), password)
	assert.Contains(t, detail.Body.String(), "••••••••")

	got := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.orders-ext/credential", nil)
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	assert.Equal(t, "mysql://root:"+password+"@198.51.100.20:3306", decodeBody(t, got)["uri"])
}

// After a hub restart the connection string is gone until the environment
// beats again, because it is never stored. The answer says to try again.
func TestCredentialAsksToRetryWhenAPublicDatabaseHasNoConnectionStringYet(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	payload, err := json.Marshal(map[string]any{
		"environment":   "spoke-a",
		"namespace":     "pds",
		"at":            time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		"privateAccess": true,
		"instances": []map[string]any{{
			"Name":       "orders-ext",
			"Namespace":  "pds",
			"Engine":     "mysql",
			"Exposure":   map[string]any{"External": true, "Address": "198.51.100.20"},
			"Connection": map[string]any{"Host": "198.51.100.20", "Port": 3306, "Username": "root"},
		}},
	})
	require.NoError(t, err)
	beat := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	beat.Header.Set("Authorization", "Bearer secret")
	beat.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, beat)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.orders-ext/credential", nil)
	require.Equal(t, http.StatusNotFound, got.Code, got.Body.String())
	assert.Equal(t, "The connection string is not ready yet. Try again in a moment.", decodeBody(t, got)["message"])
}

// A private database is reachable from the customer's VPC only, and the hub
// holds no password for it, so copy is the environment's job like an in-cluster one.
func TestCredentialSaysTheHubHasNoneForAPrivateDatabase(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	payload, err := json.Marshal(map[string]any{
		"environment":   "spoke-a",
		"namespace":     "pds",
		"privateAccess": true,
		"instances": []map[string]any{{
			"Name":      "orders-priv",
			"Namespace": "pds",
			"Engine":    "mysql",
			"Exposure":  map[string]any{"External": true, "Private": true, "Address": "10.0.4.12"},
			"Connection": map[string]any{
				"Host": "10.0.4.12",
				"Port": 3306,
				"URI":  "mysql://root:hunter2@10.0.4.12:3306",
			},
		}},
	})
	require.NoError(t, err)
	beat := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	beat.Header.Set("Authorization", "Bearer secret")
	beat.Header.Set("Content-Type", "application/json")
	beatRec := httptest.NewRecorder()
	srv.ServeHTTP(beatRec, beat)
	require.Equal(t, http.StatusOK, beatRec.Code, beatRec.Body.String())

	detail := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.orders-priv", nil)
	require.Equal(t, http.StatusOK, detail.Code, detail.Body.String())
	assert.Contains(t, detail.Body.String(), `"visibility":"private"`)

	rec := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.orders-priv/credential", nil)
	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "hunter2")
}

// External with no address yet is not a connection string anyone outside the
// environment can use, so the hub still refuses to hold that password.
func TestCredentialRefusesAPendingExternalDatabase(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	payload, err := json.Marshal(map[string]any{
		"environment": "spoke-a",
		"namespace":   "pds",
		"instances": []map[string]any{{
			"Name":      "orders-ext",
			"Namespace": "pds",
			"Exposure":  map[string]any{"External": true},
			"Connection": map[string]any{
				"Host": "orders-ext-haproxy.pds",
				"Port": 3306,
				"URI":  "mysql://root:hunter2@orders-ext-haproxy.pds:3306",
			},
		}},
	})
	require.NoError(t, err)
	beat := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	beat.Header.Set("Authorization", "Bearer secret")
	srv.ServeHTTP(httptest.NewRecorder(), beat)

	rec := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.orders-ext/credential", nil)
	require.Equal(t, http.StatusNotImplemented, rec.Code, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "hunter2")
}
