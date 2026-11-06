package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/service"
)

func TestHistoryRoutesReadTheBook(t *testing.T) {
	book := service.NewMemoryBook(fixedTeam(1))
	srv := newHubTestServerWithBook(t, "secret", book)
	beatHub(t, srv, "secret")

	rec := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.missing/history", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)

	ctx := context.Background()
	require.NoError(t, book.RecordPhase(ctx, service.JoinStep{
		Environment: "spoke-a", RunID: "run-1", Phase: "installing", Outcome: "failed",
		StartedAt: time.Now().Add(-time.Minute), FinishedAt: time.Now(), DurationMS: 60000,
		Output: "password=hunter2",
	}))

	created := do(t, srv, http.MethodPost, "/api/databases", strings.NewReader(
		`{"name":"orders","profile":"dev","engine":"mysql","environment":"spoke-a"}`))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())
	id, _ := decodeBody(t, created)["id"].(string)
	require.Equal(t, "spoke-a/pds.orders", id)
	require.NoError(t, book.RecordEntry(ctx, service.HistoryEntry{
		DatabaseID: id, Kind: service.HistoryBackup, At: time.Now().UTC(),
		ObjectID: "bak-1", Outcome: engine.BackupSucceeded, SizeBytes: 40,
	}))

	history := do(t, srv, http.MethodGet, "/api/databases/"+id+"/history?kind=backup", nil)
	require.Equal(t, http.StatusOK, history.Code, history.Body.String())
	assert.Contains(t, history.Body.String(), "bak-1")

	_, err := book.RecordUsage(ctx, service.UsagePoint{
		DatabaseID: id, At: time.Now().UTC().Add(6 * time.Minute),
		StorageBytes: ptrInt(99), MetricsReason: "metrics-server did not answer",
	})
	require.NoError(t, err)
	usage := do(t, srv, http.MethodGet, "/api/databases/"+id+"/usage", nil)
	require.Equal(t, http.StatusOK, usage.Code, usage.Body.String())
	assert.Contains(t, usage.Body.String(), "metrics-server did not answer")
	assert.NotContains(t, usage.Body.String(), "hunter2")

	bad := do(t, srv, http.MethodGet, "/api/databases/"+id+"/history?since=yesterday", nil)
	assert.Equal(t, http.StatusBadRequest, bad.Code)

	plain, _ := newActivityServer(t)
	missing := do(t, plain, http.MethodGet, "/api/databases/spoke-a/pds.orders/history", nil)
	assert.Equal(t, http.StatusNotImplemented, missing.Code)

	badName := do(t, srv, http.MethodGet, "/api/environments/NOT_A_NAME/history", nil)
	assert.Equal(t, http.StatusBadRequest, badName.Code)

	joinReq := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/join",
		strings.NewReader(`{"phase":"installing","outcome":"started"}`))
	joinReq.Header.Set("Authorization", "Bearer secret")
	joinRec := httptest.NewRecorder()
	srv.ServeHTTP(joinRec, joinReq)
	join := joinRec
	assert.Equal(t, http.StatusBadRequest, join.Code)
	assert.Contains(t, join.Body.String(), "run id")

	joins := do(t, srv, http.MethodGet, "/api/environments/spoke-a/history", nil)
	require.Equal(t, http.StatusOK, joins.Code, joins.Body.String())
	assert.Contains(t, joins.Body.String(), "installing")
	assert.NotContains(t, joins.Body.String(), "hunter2")
}

func ptrInt(v int64) *int64 { return &v }
