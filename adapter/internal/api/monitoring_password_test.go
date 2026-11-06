package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/service"
)

const monitoringPasswordPath = "/api/environments/spoke-a/monitoring/password"

// spokeBeat posts one heartbeat as the spoke agent would and returns the reply.
func spokeBeat(t *testing.T, srv http.Handler, body map[string]any) map[string]any {
	t.Helper()

	payload, err := json.Marshal(body)
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var reply map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &reply))

	return reply
}

// askForPassword calls the route in the background, the way a browser click
// does, and hands back the channel its answer arrives on.
func askForPassword(t *testing.T, srv http.Handler, ctx context.Context) <-chan *httptest.ResponseRecorder {
	t.Helper()

	out := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, monitoringPasswordPath, nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		out <- rec
	}()

	return out
}

// awaitSecretRequest beats until the hub hands the spoke the request, which is
// what a real agent does every few seconds.
func awaitSecretRequest(t *testing.T, srv http.Handler) map[string]any {
	t.Helper()

	var found map[string]any
	require.Eventually(t, func() bool {
		reply := spokeBeat(t, srv, map[string]any{"environment": "spoke-a"})
		secrets, _ := reply["secrets"].([]any)
		if len(secrets) == 0 {
			return false
		}
		found, _ = secrets[0].(map[string]any)

		return found != nil
	}, 2*time.Second, 10*time.Millisecond)

	return found
}

func TestMonitoringPasswordIsRelayedFromTheSpoke(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	answer := askForPassword(t, srv, context.Background())

	request := awaitSecretRequest(t, srv)
	assert.Equal(t, "pmm-admin-password", request["kind"])
	spokeBeat(t, srv, map[string]any{
		"environment":   "spoke-a",
		"secretResults": []map[string]any{{"id": request["id"], "value": "s3cret-pw"}},
	})

	rec := <-answer
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "s3cret-pw", decodeBody(t, rec)["password"])
}

func TestMonitoringPasswordIsHandedToTheSpokeOnce(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	answer := askForPassword(t, srv, context.Background())

	request := awaitSecretRequest(t, srv)
	again := spokeBeat(t, srv, map[string]any{"environment": "spoke-a"})
	assert.Empty(t, again["secrets"], "a request already handed out is not sent again")

	spokeBeat(t, srv, map[string]any{
		"environment":   "spoke-a",
		"secretResults": []map[string]any{{"id": request["id"], "value": "pw"}},
	})
	<-answer
}

func TestMonitoringPasswordTimesOutWhenTheSpokeIsSilent(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	rec := <-askForPassword(t, srv, ctx)

	assert.Equal(t, http.StatusGatewayTimeout, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Contains(t, rec.Body.String(), "did not answer")
}

func TestMonitoringPasswordReportsAFailureTheSpokeHad(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	answer := askForPassword(t, srv, context.Background())

	request := awaitSecretRequest(t, srv)
	spokeBeat(t, srv, map[string]any{
		"environment": "spoke-a",
		"secretResults": []map[string]any{{
			"id": request["id"], "error": `secrets "pmm-secret" is forbidden: User system:serviceaccount:x`,
		}},
	})

	rec := <-answer
	assert.Equal(t, http.StatusBadGateway, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	// The environment's own error text is a cause. The customer reads a sentence.
	assert.NotContains(t, rec.Body.String(), "forbidden")
	assert.NotContains(t, rec.Body.String(), "serviceaccount")
}

func TestMonitoringPasswordForAnUnknownEnvironment(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	rec := do(t, srv, http.MethodGet, "/api/environments/nope/monitoring/password", nil)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// Reading the password is recorded so a customer can see who copied it, and the
// record never holds the value.
func TestMonitoringPasswordReadIsRecordedWithoutTheValue(t *testing.T) {
	activity := service.NewActivity(nil, service.ActivityOptions{})
	srv := buildHubTestServer(t, "secret", true, nil, activity)
	answer := askForPassword(t, srv, context.Background())

	request := awaitSecretRequest(t, srv)
	spokeBeat(t, srv, map[string]any{
		"environment":   "spoke-a",
		"secretResults": []map[string]any{{"id": request["id"], "value": "s3cret-pw"}},
	})
	require.Equal(t, http.StatusOK, (<-answer).Code)

	page, err := activity.List(context.Background(), service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, service.ActionMonitoringPassword, page.Entries[0].Action)
	assert.Equal(t, "spoke-a", page.Entries[0].Target)
	assert.Equal(t, service.OutcomeSucceeded, page.Entries[0].Outcome)
	raw, err := json.Marshal(page.Entries[0])
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "s3cret-pw")
}

func TestMonitoringPasswordFailureIsRecordedToo(t *testing.T) {
	activity := service.NewActivity(nil, service.ActivityOptions{})
	srv := buildHubTestServer(t, "secret", true, nil, activity)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	<-askForPassword(t, srv, ctx)

	page, err := activity.List(context.Background(), service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, service.OutcomeFailed, page.Entries[0].Outcome)
}

func monitoringAccess(t *testing.T, srv http.Handler) map[string]any {
	t.Helper()

	rec := do(t, srv, http.MethodGet, "/api/environments/spoke-a/monitoring", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	access, ok := decodeBody(t, rec)["access"].(map[string]any)
	require.True(t, ok)

	return access
}

// Until the cloud assigns an address the only way in is the tunnel, and the
// panel needs to tell "pending" from "not exposed" itself.
func TestMonitoringAccessHasNoAddressUntilTheEnvironmentReportsOne(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	access := monitoringAccess(t, srv)

	assert.Empty(t, access["address"])
	assert.Equal(t, "https://localhost:8443", access["url"])
	assert.Equal(t, "https://localhost:8443", access["tunnelUrl"])
}

func TestMonitoringAccessPointsAtTheAddressTheEnvironmentReported(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	spokeBeat(t, srv, map[string]any{"environment": "spoke-a", "monitoringAddress": "pmm.elb.example.com"})

	access := monitoringAccess(t, srv)

	assert.Equal(t, "pmm.elb.example.com", access["address"])
	assert.Equal(t, "https://pmm.elb.example.com", access["url"])
	// The tunnel stays available for anyone with environment access, and its
	// address does not move when the load balancer's does.
	assert.Contains(t, access["portForwardCommand"], "port-forward")
	assert.Equal(t, "https://localhost:8443", access["tunnelUrl"])
}

func TestMonitoringAccessFollowsTheAddressWhenItGoesAway(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	spokeBeat(t, srv, map[string]any{"environment": "spoke-a", "monitoringAddress": "pmm.elb.example.com"})
	spokeBeat(t, srv, map[string]any{"environment": "spoke-a"})

	access := monitoringAccess(t, srv)

	assert.Empty(t, access["address"])
	assert.Equal(t, "https://localhost:8443", access["url"])
}
