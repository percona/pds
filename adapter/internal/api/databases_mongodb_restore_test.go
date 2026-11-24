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

// seedMongoDBSource reports a ready MongoDB database with one succeeded and
// one pending backup.
func seedMongoDBSource(t *testing.T, srv http.Handler) {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"environment": "spoke-a",
		"namespace":   "pds",
		"engines":     []string{"mongodb", "mysql", "postgresql"},
		"at":          time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		"instances": []map[string]any{{
			"ID": "spoke-a/pds.orders", "Name": "orders", "Engine": "mongodb", "Namespace": "pds",
			"Environment": "spoke-a", "Profile": "dev", "Status": map[string]any{"Phase": "ready"},
		}},
		"backups": []map[string]any{
			{"ID": "orders-ok", "Instance": "orders", "Status": "succeeded"},
			{"ID": "orders-pending", "Instance": "orders", "Status": "pending"},
		},
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func restoreMongoDBRequest(t *testing.T, srv http.Handler, backupID, name string) *httptest.ResponseRecorder {
	t.Helper()

	return do(t, srv, http.MethodPost, "/api/databases/spoke-a/pds.orders/restore",
		bytes.NewBufferString(`{"backupId":"`+backupID+`","name":"`+name+`"}`))
}

func TestAPIRestoresAMongoDBBackupIntoANewDatabase(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	seedMongoDBSource(t, srv)

	rec := restoreMongoDBRequest(t, srv, "orders-ok", "orders-copy")

	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Equal(t, "spoke-a/pds.orders-copy", decodeBody(t, rec)["id"])
}

func TestAPIRefusesAMongoDBCopyNameOf23Characters(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	seedMongoDBSource(t, srv)

	rec := restoreMongoDBRequest(t, srv, "orders-ok", strings.Repeat("a", 23))

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "A MongoDB database name must be between 1 and 22 characters.", decodeBody(t, rec)["message"])
	assert.Equal(t, http.StatusAccepted, restoreMongoDBRequest(t, srv, "orders-ok", strings.Repeat("a", 22)).Code)
}

func TestAPIRefusesToRestoreAMongoDBBackupThatHasNotSucceeded(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	seedMongoDBSource(t, srv)

	rec := restoreMongoDBRequest(t, srv, "orders-pending", "orders-copy")

	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, http.StatusNotFound, do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.orders-copy", nil).Code,
		"nothing is written for a refused restore")
}
