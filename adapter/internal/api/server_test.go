package api_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/log"
)

func TestUnknownRouteRendersStructuredError(t *testing.T) {
	srv := newTestServer(t)

	rec := do(t, srv, http.MethodGet, "/api/nope", nil)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, api.CodeNotFound, decodeBody(t, rec)["code"])
}

func TestWrongMethodRendersStructuredError(t *testing.T) {
	srv := newTestServer(t)

	rec := do(t, srv, http.MethodPost, "/healthz", strings.NewReader("{}"))

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	assert.Equal(t, api.CodeValidation, decodeBody(t, rec)["code"])
}

func TestPanicIsRecoveredAsInternalError(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, log.FormatJSON, slog.LevelDebug)

	router := chi.NewRouter()
	router.Use(api.RequestID, api.Recoverer(logger))
	router.Get("/boom", func(http.ResponseWriter, *http.Request) {
		panic("password=hunter2")
	})

	rec := httptest.NewRecorder()
	require.NotPanics(t, func() {
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))
	})

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, api.CodeInternal, decodeBody(t, rec)["code"])
	assert.NotContains(t, rec.Body.String(), "hunter2")
	assert.Contains(t, buf.String(), "panic")
}

func TestRequestLoggerRecordsRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, log.FormatJSON, slog.LevelDebug)

	srv := api.NewServer(api.Deps{
		Config: config.Config{Addr: ":8080", Env: config.EnvDev},
		Logger: logger,
		Auth:   auth.NewStub(),
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-Id", "req-42")
	srv.ServeHTTP(rec, req)

	var record map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &record))
	assert.Equal(t, "req-42", record["request_id"])
	assert.Equal(t, "GET", record["method"])
	assert.Equal(t, "/healthz", record["path"])
	assert.Equal(t, float64(http.StatusOK), record["status"])
}

func TestHandlerContextCarriesRequestLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, log.FormatJSON, slog.LevelDebug)

	router := chi.NewRouter()
	router.Use(api.RequestID, api.RequestLogger(logger))
	router.Get("/ctx", func(w http.ResponseWriter, r *http.Request) {
		log.FromContext(r.Context()).Info("inside handler")
		api.Respond(w, r, map[string]string{"ok": "yes"}, nil)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ctx", nil)
	req.Header.Set("X-Request-Id", "req-ctx")
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, buf.String(), "inside handler")
	assert.Contains(t, buf.String(), "req-ctx")
}

func TestNotImplementedLogsOnceAtWarn(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, log.FormatJSON, slog.LevelDebug)

	srv := api.NewServer(api.Deps{
		Config: config.Config{Addr: ":8080", Env: config.EnvDev},
		Logger: logger,
		Auth:   auth.NewStub(),
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/databases/spoke-a/pds.db/history", nil)
	req.Header.Set("X-Request-Id", "req-501")
	srv.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotImplemented, rec.Code)

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2, "one failure record and one completion record")

	for _, line := range lines {
		assert.Equal(t, 1, strings.Count(line, `"request_id"`), "request ID logged once: %s", line)

		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &record))
		assert.Equal(t, "WARN", record["level"], "a route that is not wired yet is not an error: %s", line)
	}
}

func TestStubAuthDoesNotBlockAPIRoutes(t *testing.T) {
	srv := newTestServer(t)

	rec := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.db/history", nil)

	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, http.StatusNotImplemented, rec.Code)
	assert.Equal(t, api.CodeNotImplemented, decodeBody(t, rec)["code"])
}
