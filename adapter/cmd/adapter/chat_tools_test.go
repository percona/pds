package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLateHandlerRefusesUntilTheRouterIsSetThenServesIt(t *testing.T) {
	late := &lateHandler{}

	before := httptest.NewRecorder()
	late.ServeHTTP(before, httptest.NewRequest(http.MethodGet, "/api/databases", nil))
	assert.Equal(t, http.StatusServiceUnavailable, before.Code)

	late.set(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) }))

	after := httptest.NewRecorder()
	late.ServeHTTP(after, httptest.NewRequest(http.MethodGet, "/api/databases", nil))
	assert.Equal(t, http.StatusTeapot, after.Code)
}
