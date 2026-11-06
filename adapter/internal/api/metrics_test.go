package api_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const metricsPath = "/api/databases/spoke-a/pds.orders-dev/metrics"

func TestDatabaseMetricsRejectAnUnknownWindow(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	rec := do(t, srv, http.MethodGet, metricsPath+"?window=7d", nil)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, "ErrValidation", body["code"])
	assert.Contains(t, body["message"], "1h")
}

func TestDatabaseMetricsRejectAnUnknownChart(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	rec := do(t, srv, http.MethodGet, metricsPath+"?metrics=latency", nil)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, "ErrValidation", body["code"])
	require.Contains(t, body["message"], "cpu")
}
