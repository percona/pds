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
)

// A MongoDB backup reports when it finished and nothing trustworthy about when
// it started, so the list gives the finish time alone.
func TestBackupListOfAFinishOnlyBackupHasNoStartTime(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	created := time.Date(2026, 9, 8, 11, 50, 0, 0, time.UTC)
	finished := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	payload, err := json.Marshal(map[string]any{
		"environment": "spoke-a",
		"namespace":   "pds",
		"at":          finished,
		"instances": []map[string]any{{
			"ID": "spoke-a/pds.orders", "Name": "orders", "Namespace": "pds", "Environment": "spoke-a",
			"Status": map[string]any{"Phase": "ready"},
		}},
		"backups": []map[string]any{
			{"ID": "mongo", "Instance": "orders", "Status": "succeeded", "CreatedAt": created, "FinishedAt": finished, "FinishOnly": true},
			{"ID": "other", "Instance": "orders", "Status": "succeeded", "CreatedAt": created, "FinishedAt": finished},
		},
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	listed := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.orders/backups", nil)

	require.Equal(t, http.StatusOK, listed.Code)
	byID := map[string]map[string]any{}
	for _, row := range decodeBody(t, listed)["backups"].([]any) {
		item := row.(map[string]any)
		byID[item["id"].(string)] = item
	}
	assert.NotContains(t, byID["mongo"], "createdAt")
	assert.Equal(t, finished.Format(time.RFC3339), byID["mongo"]["finishedAt"])
	assert.Contains(t, byID["other"], "createdAt")
}
