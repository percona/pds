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

// The detail payload carries the member list and the mask, and never the
// password. The credential route is the one response that returns the string.
func TestAPIMongoDBConnectionCarriesMembersAndNoPassword(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	const password = "p%40ss"
	const members = "orders-rs0-0.orders-rs0.pds.svc.cluster.local:27017,orders-rs0-1.orders-rs0.pds.svc.cluster.local:27017"
	payload, err := json.Marshal(map[string]any{
		"environment":   "spoke-a",
		"namespace":     "pds",
		"at":            time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		"privateAccess": true,
		"instances": []map[string]any{{
			"Name":      "orders",
			"Namespace": "pds",
			"Engine":    "mongodb",
			"Exposure":  map[string]any{"External": true, "Address": "198.51.100.20"},
			"Connection": map[string]any{
				"Host":        "orders-rs0-0.orders-rs0.pds.svc.cluster.local",
				"Port":        27017,
				"Members":     strings.Split(members, ","),
				"Username":    "databaseAdmin",
				"ServiceName": "orders-rs0-0",
				"URI":         "mongodb://databaseAdmin:" + password + "@" + members + "/admin?replicaSet=rs0&ssl=false",
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

	detail := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.orders", nil)
	require.Equal(t, http.StatusOK, detail.Code, detail.Body.String())
	assert.NotContains(t, detail.Body.String(), password)
	connection := decodeBody(t, detail)["connection"].(map[string]any)
	assert.Equal(t, []any{
		"orders-rs0-0.orders-rs0.pds.svc.cluster.local:27017",
		"orders-rs0-1.orders-rs0.pds.svc.cluster.local:27017",
	}, connection["members"])
	assert.Equal(t, "orders-rs0-0", connection["serviceName"])
	assert.Equal(t,
		"mongodb://databaseAdmin:••••••••@"+members+"/admin?replicaSet=rs0&ssl=false",
		connection["maskedUri"])
}
