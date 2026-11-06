package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

func TestDatabaseErrorMapsAMissingPreset(t *testing.T) {
	got := databaseError(&engine.PresetError{
		Profile: "dev",
		Name:    "pds-mysql-dev",
		Kind:    engine.ErrPresetMissing,
	}, "orders-dev")

	require.NotNil(t, got)
	assert.Equal(t, CodeValidation, got.Code)
	assert.Equal(t, http.StatusBadRequest, got.Status)
	assert.Equal(t, "The Dev profile preset is not installed.", got.Message)
}

func TestDatabaseErrorMapsAnIncompatiblePreset(t *testing.T) {
	got := databaseError(&engine.PresetError{
		Profile: "staging",
		Name:    "pds-mysql-staging",
		Kind:    engine.ErrPresetIncompatible,
	}, "orders-stg")

	require.NotNil(t, got)
	assert.Equal(t, CodeValidation, got.Code)
	assert.Equal(t, http.StatusBadRequest, got.Status)
	assert.Equal(t, "The Staging profile preset is not compatible with this engine.", got.Message)
}

func TestDatabaseErrorMapsAChangedPreset(t *testing.T) {
	got := databaseError(&engine.PresetError{
		Profile: "prod",
		Name:    "pds-mysql-prod",
		Kind:    engine.ErrPresetChanged,
	}, "orders-prod")

	require.NotNil(t, got)
	assert.Equal(t, CodeValidation, got.Code)
	assert.Equal(t, http.StatusBadRequest, got.Status)
	assert.Equal(t, "The Prod profile preset has changed. Contact Percona support.", got.Message)
	assert.NotContains(t, got.Message, "!")
}

func TestDatabaseErrorMapsAnUnreachableEnvironment(t *testing.T) {
	got := databaseError(fmt.Errorf("%w: spoke-a", hub.ErrUnreachable), "orders-dev")

	require.NotNil(t, got)
	assert.Equal(t, CodeConflict, got.Code)
	assert.Equal(t, http.StatusConflict, got.Status)
	assert.Equal(t, "That environment is unreachable. Try again after it reports in.", got.Message)
	assert.NotContains(t, got.Message, "wait")
}

func TestDatabaseErrorSaysACopyIsStillBeingRestored(t *testing.T) {
	got := databaseError(fmt.Errorf("%w: %q", hub.ErrRestoring, "spoke-a/pds.reporting-restore-1"), "reporting-restore-1")

	require.NotNil(t, got)
	assert.Equal(t, CodeConflict, got.Code)
	assert.Equal(t, "This database is still being restored. Try again once the restore finishes.", got.Message)
}

// A restore refused for its engine carries the engine cause too, and must read
// as a restore refusal rather than a create one, no longer claims PostgreSQL is
// the engine that cannot restore, and says what to do instead.
func TestDatabaseErrorSaysWhyARestoreEngineIsRefused(t *testing.T) {
	err := fmt.Errorf("%w: %q: %w", hub.ErrRestoreNotSupported, "mongodb", hub.ErrEngineNotSupported)

	got := databaseError(err, "orders")

	require.NotNil(t, got)
	assert.Equal(t, CodeNotImplemented, got.Code)
	assert.Equal(t, "Restoring into a new database is not available for that engine. Contact Percona support to restore this backup.", got.Message)
	assert.ErrorIs(t, got.Cause, hub.ErrEngineNotSupported)
}

func TestDatabaseErrorMapsTheHubRefusals(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		subject string
		status  int
		code    string
	}{
		{"environment required", hub.ErrEnvironmentRequired, "", http.StatusBadRequest, CodeValidation},
		{"engine", fmt.Errorf("%w: mongodb", hub.ErrEngineNotSupported), "orders", http.StatusNotImplemented, CodeNotImplemented},
		{"restore engine", hub.ErrRestoreNotSupported, "orders", http.StatusNotImplemented, CodeNotImplemented},
		{"not supported", hub.ErrNotSupported, "orders", http.StatusNotImplemented, CodeNotImplemented},
		{"persist", hub.ErrPersist, "orders", http.StatusBadGateway, CodeDownstream},
		{"git host", hub.ErrGitHost, "orders", http.StatusBadGateway, CodeDownstream},
		{"backups", engine.ErrBackupsUnavailable, "orders", http.StatusNotImplemented, CodeNotImplemented},
		{"exists", engine.ErrExists, "orders", http.StatusConflict, CodeConflict},
		{"missing", engine.ErrNotFound, "orders", http.StatusNotFound, CodeNotFound},
		{"invalid name", engine.ErrInvalidName, "orders", http.StatusBadRequest, CodeValidation},
		{"invalid id", engine.ErrInvalidID, "orders", http.StatusBadRequest, CodeValidation},
		{"shrink", engine.ErrStorageShrink, "orders", http.StatusBadRequest, CodeValidation},
		{"empty scale", engine.ErrEmptyScale, "orders", http.StatusBadRequest, CodeValidation},
		{"unknown profile", engine.ErrUnknownProfile, "orders", http.StatusBadRequest, CodeValidation},
		{"invalid namespace", engine.ErrInvalidNamespace, "orders", http.StatusBadRequest, CodeValidation},
		{"reserved environment", hub.ErrInvalidEnvironment, "connect", http.StatusBadRequest, CodeValidation},
		{"bad environment", hub.ErrInvalidEnvironment, "orders", http.StatusBadRequest, CodeValidation},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := databaseError(tc.err, tc.subject)
			require.NotNil(t, got)
			assert.Equal(t, tc.status, got.Status)
			assert.Equal(t, tc.code, got.Code)
			assert.NotEmpty(t, got.Message)
		})
	}
}

func TestDatabaseErrorMapsATimeout(t *testing.T) {
	got := databaseError(fmt.Errorf("get instance: %w", context.DeadlineExceeded), "orders-dev")

	require.NotNil(t, got)
	assert.Equal(t, CodeDownstream, got.Code)
	assert.Equal(t, http.StatusBadGateway, got.Status)
	assert.Equal(t, "The database platform took too long to answer. Try again in a moment.", got.Message)
}

func TestRespondSkipsACanceledRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/databases", nil)
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)

	Respond(rec, req, nil, Downstream(
		"The database platform did not answer. Try again in a moment.",
		context.Canceled,
	))

	assert.Empty(t, rec.Body.String())
}
