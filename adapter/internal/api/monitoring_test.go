package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMonitoringOnAHubReturnsSettingsAndAccess(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	rec := do(t, srv, http.MethodGet, "/api/environments/spoke-a/monitoring", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := decodeBody(t, rec)
	settings, ok := body["settings"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "cluster", settings["access"])
	assert.NotContains(t, settings, "sourceRanges")
	assert.EqualValues(t, 5, settings["storageGi"])

	access, ok := body["access"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "admin", access["username"])
	// Monitoring runs in the platform namespace. The configured namespace is
	// where databases live, and the Secret is not there.
	assert.NotEqual(t, "everest-system", testEverestNamespace)
	assert.Contains(t, access["passwordCommand"], "get secret pmm-secret -n everest-system ")
	assert.Contains(t, access["portForwardCommand"], "port-forward -n everest-system ")
	// The password has no trailing newline, so the command adds one. Without it
	// some shells print a marker after the output, and that marker gets copied
	// along with the password.
	assert.True(t, strings.HasSuffix(access["passwordCommand"].(string), "; echo"), "password command: %v", access["passwordCommand"])
	assert.Equal(t, "https://localhost:8443", access["url"])
}

// The hub never holds an environment's monitoring password. It says where the
// Secret is and leaves the reading to someone with that environment's kubeconfig.
func TestMonitoringNeverCarriesAPassword(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	rec := do(t, srv, http.MethodGet, "/api/environments/spoke-a/monitoring", nil)
	require.Equal(t, http.StatusOK, rec.Code)

	assert.NotContains(t, strings.ToLower(rec.Body.String()), `"password"`)
}

func TestMonitoringSettingsCanBeChanged(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	put := do(t, srv, http.MethodPut, "/api/environments/spoke-a/monitoring", strings.NewReader(
		`{"access":"public","sourceRanges":["203.0.113.0/24"],"cpuMillicores":2000,"memoryMi":2048,"storageGi":20}`))
	require.Equal(t, http.StatusOK, put.Code, put.Body.String())

	get := do(t, srv, http.MethodGet, "/api/environments/spoke-a/monitoring", nil)
	require.Equal(t, http.StatusOK, get.Code)
	settings, ok := decodeBody(t, get)["settings"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "public", settings["access"])
	assert.Equal(t, []any{"203.0.113.0/24"}, settings["sourceRanges"])
	assert.EqualValues(t, 20, settings["storageGi"])
	assert.EqualValues(t, 2000, settings["cpuMillicores"])
}

func TestMonitoringRejectsSettingsOutOfBounds(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	rec := do(t, srv, http.MethodPut, "/api/environments/spoke-a/monitoring", strings.NewReader(
		`{"access":"cluster","cpuMillicores":1000,"memoryMi":1024,"storageGi":1}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "storage")
}

func TestMonitoringRejectsUnknownFields(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	rec := do(t, srv, http.MethodPut, "/api/environments/spoke-a/monitoring", strings.NewReader(
		`{"access":"cluster","cpuMillicores":1000,"memoryMi":1024,"storageGi":5,"namespace":"kube-system"}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestMonitoringForAnUnknownEnvironment(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	assert.Equal(t, http.StatusNotFound, do(t, srv, http.MethodGet, "/api/environments/nope/monitoring", nil).Code)
	put := do(t, srv, http.MethodPut, "/api/environments/nope/monitoring", strings.NewReader(
		`{"access":"cluster","cpuMillicores":1000,"memoryMi":1024,"storageGi":5}`))
	assert.Equal(t, http.StatusNotFound, put.Code)
}

func TestMonitoringChangesNeedTheOperatorToken(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	req := httptest.NewRequest(http.MethodPut, "/api/environments/spoke-a/monitoring", strings.NewReader(
		`{"access":"cluster","cpuMillicores":1000,"memoryMi":1024,"storageGi":5}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMonitoringRefusesPublicAccessWithNoAllowedAddresses(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	rec := do(t, srv, http.MethodPut, "/api/environments/spoke-a/monitoring", strings.NewReader(
		`{"access":"public","cpuMillicores":1000,"memoryMi":1024,"storageGi":5}`))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "at least one allowed address")
	// The refused change left monitoring closed.
	get := do(t, srv, http.MethodGet, "/api/environments/spoke-a/monitoring", nil)
	settings, ok := decodeBody(t, get)["settings"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "cluster", settings["access"])
}

func TestMonitoringNamesTheAddressItCannotRead(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	rec := do(t, srv, http.MethodPut, "/api/environments/spoke-a/monitoring", strings.NewReader(
		`{"access":"private","sourceRanges":["office"],"cpuMillicores":1000,"memoryMi":1024,"storageGi":5}`))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "office")
}

func TestMonitoringPrivateAccessReadsBackThePrivateRanges(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	put := do(t, srv, http.MethodPut, "/api/environments/spoke-a/monitoring", strings.NewReader(
		`{"access":"private","cpuMillicores":1000,"memoryMi":1024,"storageGi":5}`))
	require.Equal(t, http.StatusOK, put.Code, put.Body.String())

	settings, ok := decodeBody(t, put)["settings"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "private", settings["access"])
	assert.Equal(t, []any{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}, settings["sourceRanges"])
}

// The old fields no longer mean anything, so a client still sending them is
// told rather than silently ignored.
func TestMonitoringRefusesTheRetiredServiceFields(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	rec := do(t, srv, http.MethodPut, "/api/environments/spoke-a/monitoring", strings.NewReader(
		`{"access":"cluster","serviceType":"NodePort","cpuMillicores":1000,"memoryMi":1024,"storageGi":5}`))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
