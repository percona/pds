package api_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
)

func TestAppErrorRendersCodeMessageAndStatus(t *testing.T) {
	tests := []struct {
		name       string
		err        *api.AppError
		wantStatus int
		wantCode   string
	}{
		{
			name:       "validation",
			err:        api.Validation("Name must be lowercase letters, digits, and dashes."),
			wantStatus: http.StatusBadRequest,
			wantCode:   api.CodeValidation,
		},
		{
			name:       "not found",
			err:        api.NotFound("Database not found."),
			wantStatus: http.StatusNotFound,
			wantCode:   api.CodeNotFound,
		},
		{
			name:       "conflict",
			err:        api.Conflict("A database with that name already exists."),
			wantStatus: http.StatusConflict,
			wantCode:   api.CodeConflict,
		},
		{
			name:       "downstream",
			err:        api.Downstream("Could not reach the database platform.", errors.New("dial tcp: refused")),
			wantStatus: http.StatusBadGateway,
			wantCode:   api.CodeDownstream,
		},
		{
			name:       "not implemented",
			err:        api.NotImplemented("Support tickets are not configured."),
			wantStatus: http.StatusNotImplemented,
			wantCode:   api.CodeNotImplemented,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/databases", nil)

			api.Respond(rec, req, nil, tt.err)

			assert.Equal(t, tt.wantStatus, rec.Code)
			assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

			body := decodeBody(t, rec)
			assert.Equal(t, tt.wantCode, body["code"])
			assert.Equal(t, tt.err.Message, body["message"])
			assert.Len(t, body, 2, "error body carries code and message only")
		})
	}
}

func TestCodesMatchSpec(t *testing.T) {
	assert.Equal(t, "ErrValidation", api.CodeValidation)
	assert.Equal(t, "ErrNotFound", api.CodeNotFound)
	assert.Equal(t, "ErrConflict", api.CodeConflict)
	assert.Equal(t, "ErrDownstream", api.CodeDownstream)
	assert.Equal(t, "ErrNotImplemented", api.CodeNotImplemented)
}

func TestCauseIsNeverSerialized(t *testing.T) {
	cause := errors.New("everest 500: token=sn-secret-token password=hunter2")
	appErr := api.Downstream("Could not reach the database platform.", cause)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/databases", nil)

	api.Respond(rec, req, nil, appErr)

	body := rec.Body.String()
	assert.NotContains(t, body, "sn-secret-token")
	assert.NotContains(t, body, "hunter2")
	assert.NotContains(t, body, "everest 500")
	assert.NotContains(t, body, "cause")

	assert.Len(t, decodeBody(t, rec), 2)
}

func TestUnknownErrorsDoNotLeakDetail(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/databases", nil)

	api.Respond(rec, req, nil, errors.New("pq: password authentication failed for user root"))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "password authentication failed")

	body := decodeBody(t, rec)
	assert.Equal(t, api.CodeInternal, body["code"])
	assert.NotEmpty(t, body["message"])
}

func TestAppErrorUnwrapsCause(t *testing.T) {
	cause := errors.New("connection refused")
	appErr := api.Downstream("Could not reach the database platform.", cause)

	require.ErrorIs(t, appErr, cause)
	assert.Contains(t, appErr.Error(), api.CodeDownstream)
	assert.Contains(t, appErr.Error(), "connection refused")
}

func TestWrappedAppErrorIsRendered(t *testing.T) {
	appErr := api.NotFound("Database not found.")
	wrapped := errors.Join(errors.New("service layer"), appErr)

	rec := httptest.NewRecorder()
	api.Respond(rec, httptest.NewRequest(http.MethodGet, "/api/databases/x", nil), nil, wrapped)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, api.CodeNotFound, decodeBody(t, rec)["code"])
}

func TestRespondWritesSuccessJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/databases", nil)

	api.Respond(rec, req, map[string]string{"name": "orders-dev"}, nil)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, map[string]any{"name": "orders-dev"}, decodeBody(t, rec))
}

func TestRespondWritesNoContentWithoutResult(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/api/databases/orders-dev", nil)

	api.Respond(rec, req, nil, nil)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Empty(t, rec.Body.String())
}

func TestForbiddenIsA403WithItsOwnCode(t *testing.T) {
	err := api.Forbidden("Only an organization admin can do that.")

	assert.Equal(t, "ErrForbidden", err.Code)
	assert.Equal(t, http.StatusForbidden, err.Status)
}
