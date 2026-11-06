package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
)

func TestHubScaleAndDelete(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatHub(t, srv, "secret")

	created := do(t, srv, http.MethodPost, "/api/databases", strings.NewReader(
		`{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a"}`))
	require.Equal(t, http.StatusCreated, created.Code, created.Body.String())

	scaled := do(t, srv, http.MethodPost, "/api/databases/spoke-a/pds.orders-dev/scale",
		strings.NewReader(`{"cpu":2}`))
	require.Equal(t, http.StatusOK, scaled.Code, scaled.Body.String())

	deleted := do(t, srv, http.MethodDelete, "/api/databases/spoke-a/pds.orders-dev", nil)
	assert.Equal(t, http.StatusNoContent, deleted.Code, deleted.Body.String())

	gone := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.orders-dev", nil)
	assert.Equal(t, http.StatusNotFound, gone.Code, gone.Body.String())
}

func TestOneSegmentDatabaseIDIsRefused(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	rec := do(t, srv, http.MethodGet, "/api/databases/pds.orders", nil)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, api.CodeNotFound, body["code"])
	assert.Equal(t, "That endpoint does not exist.", body["message"])
}

func TestRestoreWithoutANameIsRejected(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	rec := do(t, srv, http.MethodPost, "/api/databases/spoke-a/pds.orders/restore",
		strings.NewReader(`{"backupId":"orders-1"}`))

	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, api.CodeValidation, body["code"])
	assert.Equal(t, "name is required.", body["message"])
}
