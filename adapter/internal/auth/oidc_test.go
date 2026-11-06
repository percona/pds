package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/percona/pds/adapter/internal/auth"
)

func TestStubPassesRequestsThrough(t *testing.T) {
	called := false
	handler := auth.NewStub().Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/databases", nil))

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestBearerMatches(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name          string
		want          string
		authorization string
		ok            bool
	}{
		{name: "matching bearer", want: "op-secret", authorization: "Bearer op-secret", ok: true},
		{name: "lowercase bearer", want: "op-secret", authorization: "bearer op-secret", ok: true},
		{name: "wrong token", want: "op-secret", authorization: "Bearer other", ok: false},
		{name: "empty configured token", want: "", authorization: "Bearer op-secret", ok: false},
		{name: "missing header", want: "op-secret", authorization: "", ok: false},
		{name: "bearer without token", want: "op-secret", authorization: "Bearer ", ok: false},
		{name: "not a bearer scheme", want: "op-secret", authorization: "Basic op-secret", ok: false},
		{name: "raw token", want: "op-secret", authorization: "op-secret", ok: false},
		{name: "bearer without separator", want: "op-secret", authorization: "Bearerop-secret", ok: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.ok, auth.BearerMatches(tc.want, tc.authorization))
		})
	}
}
