package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
)

func TestHealthzReturnsOK(t *testing.T) {
	srv := newTestServer(t)

	rec := do(t, srv, http.MethodGet, "/healthz", nil)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, map[string]any{"status": "ok"}, decodeBody(t, rec))
}

func TestHealthzSetsRequestID(t *testing.T) {
	srv := newTestServer(t)

	rec := do(t, srv, http.MethodGet, "/healthz", nil)

	assert.NotEmpty(t, rec.Header().Get("X-Request-Id"))
}

func TestHealthzEchoesIncomingRequestID(t *testing.T) {
	srv := newTestServer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-Id", "req-from-ingress")
	srv.ServeHTTP(rec, req)

	assert.Equal(t, "req-from-ingress", rec.Header().Get("X-Request-Id"))
}

func TestHealthzIsReachableWithoutAuth(t *testing.T) {
	srv := newTestServer(t)

	rec := do(t, srv, http.MethodGet, "/healthz", nil)

	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
}

// fakeStore stands in for the hub database. Readiness is the only thing the
// handler asks it, so the fake answers with whatever the case needs.
type fakeStore struct{ err error }

func (f fakeStore) Ready(context.Context) error { return f.err }

func TestReadyzWithoutAStoreIsOK(t *testing.T) {
	srv := newTestServer(t)

	rec := do(t, srv, http.MethodGet, "/readyz", nil)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, map[string]any{"status": "ok"}, decodeBody(t, rec))
}

func TestReadyzWaitsForTheStore(t *testing.T) {
	tests := []struct {
		name       string
		store      fakeStore
		wantStatus int
		wantCode   string
	}{
		{
			name:       "connected and migrated",
			store:      fakeStore{},
			wantStatus: http.StatusOK,
		},
		{
			name:       "unreachable",
			store:      fakeStore{err: errors.New("dial tcp 10.0.0.9:5432: connect: connection refused")},
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   api.CodeDownstream,
		},
		{
			name:       "schema not reconciled yet",
			store:      fakeStore{err: errors.New("schema has not been migrated")},
			wantStatus: http.StatusServiceUnavailable,
			wantCode:   api.CodeDownstream,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServerWithStore(t, tc.store)

			rec := do(t, srv, http.MethodGet, "/readyz", nil)

			assert.Equal(t, tc.wantStatus, rec.Code)
			if tc.wantCode == "" {
				return
			}

			body := decodeBody(t, rec)
			assert.Equal(t, tc.wantCode, body["code"])
		})
	}
}

func TestReadyzKeepsTheStoreFailureOutOfTheBody(t *testing.T) {
	srv := newTestServerWithStore(t, fakeStore{
		err: errors.New(`failed to connect to "user=hub password=hunter2 host=db"`),
	})

	rec := do(t, srv, http.MethodGet, "/readyz", nil)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.NotContains(t, rec.Body.String(), "hunter2")
}

func TestHealthzStaysUpWhileTheStoreIsDown(t *testing.T) {
	srv := newTestServerWithStore(t, fakeStore{err: errors.New("connection refused")})

	rec := do(t, srv, http.MethodGet, "/healthz", nil)

	assert.Equal(t, http.StatusOK, rec.Code, "a database outage must not restart a healthy adapter")
}

func TestReadyzWithAStoreIsReachableWithoutAuth(t *testing.T) {
	srv := newTestServerWithStore(t, fakeStore{})

	rec := do(t, srv, http.MethodGet, "/readyz", nil)

	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
}
