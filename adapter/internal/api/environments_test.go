package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/hub"
)

func TestListEnvironmentsAnswersHub(t *testing.T) {
	srv := newTestServerWithoutEverest(t)
	rec := do(t, srv, http.MethodGet, "/api/environments", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["hub"])
}

func TestListEnvironmentsHubEmptyStillHub(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")
	rec := do(t, srv, http.MethodGet, "/api/environments", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["hub"])
	environments, ok := body["environments"].([]any)
	require.True(t, ok)
	assert.Empty(t, environments)
}

func TestConnectGuideOnHub(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")
	rec := do(t, srv, http.MethodGet, "/api/connect", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["hub"])
	assert.Equal(t, "https://hub.example", body["hubUrl"])
	_, hasToken := body["agentToken"]
	assert.False(t, hasToken)
	assert.Equal(t, "https://github.com/example/pds.git", body["gitUrl"])
	assert.Equal(t, testEverestNamespace, body["namespace"])
	assert.Equal(t, "us-west-2", body["clusterRegion"])
}

func TestConnectGuideOmitsHubURLWhenUnset(t *testing.T) {
	srv := newHubTestServerWithoutPublicURL(t, "secret")
	req := httptest.NewRequest(http.MethodGet, "/api/connect", nil)
	req.Host = "evil.example"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["hub"])
	_, hasURL := body["hubUrl"]
	assert.False(t, hasURL)
}

func TestIssueJoinTokenRequiresConfiguredPublicURL(t *testing.T) {
	srv := newHubTestServerWithoutPublicURL(t, "secret")
	req := httptest.NewRequest(http.MethodPost, "/api/connect/tokens", strings.NewReader(`{"name":"eks-testing"}`))
	req.Host = "evil.example"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testHubOperatorToken)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestIssueJoinTokenIgnoresRequestHost(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")
	req := httptest.NewRequest(http.MethodPost, "/api/connect/tokens", strings.NewReader(`{"name":"eks-testing"}`))
	req.Host = "evil.example"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+testHubOperatorToken)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusCreated, rec.Code)
	body := decodeBody(t, rec)
	token, ok := body["token"].(string)
	require.True(t, ok)
	assert.Equal(t, "https://hub.example", body["hubUrl"])
	assert.Equal(t,
		"curl -fsSL 'https://hub.example/api/internal/connect/bootstrap.sh' | bash -s -- --token '"+token+"'",
		body["command"],
	)
	assert.NotContains(t, rec.Body.String(), "evil.example")
}

func TestIssueJoinTokenIncludesExpiryWhenTTLIsSet(t *testing.T) {
	srv := newHubTestServerWithJoinTTL(t, "secret", 24*time.Hour)
	before := time.Now().UTC()
	rec := do(t, srv, http.MethodPost, "/api/connect/tokens", strings.NewReader(`{"name":"eks-testing"}`))
	after := time.Now().UTC()
	assert.Equal(t, http.StatusCreated, rec.Code)
	body := decodeBody(t, rec)
	raw, ok := body["expiresAt"].(string)
	require.True(t, ok, "expiresAt: %v", body["expiresAt"])
	expires, err := time.Parse(time.RFC3339Nano, raw)
	require.NoError(t, err)
	assert.True(t, !expires.Before(before.Add(24*time.Hour).Add(-2*time.Second)))
	assert.True(t, !expires.After(after.Add(24*time.Hour).Add(2*time.Second)))
}

func TestIssueJoinTokenRejectsReservedName(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	rec := do(t, srv, http.MethodPost, "/api/connect/tokens", strings.NewReader(`{"name":"connect"}`))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "taken by a page")
}

func TestIssueJoinTokenRequiresOperatorToken(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")

	missing := httptest.NewRequest(http.MethodPost, "/api/connect/tokens", strings.NewReader(`{"name":"eks-testing"}`))
	missing.Header.Set("Content-Type", "application/json")
	missingRec := httptest.NewRecorder()
	srv.ServeHTTP(missingRec, missing)
	assert.Equal(t, http.StatusUnauthorized, missingRec.Code)

	wrong := httptest.NewRequest(http.MethodPost, "/api/connect/tokens", strings.NewReader(`{"name":"eks-testing"}`))
	wrong.Header.Set("Content-Type", "application/json")
	wrong.Header.Set("Authorization", "Bearer wrong-operator")
	wrongRec := httptest.NewRecorder()
	srv.ServeHTTP(wrongRec, wrong)
	assert.Equal(t, http.StatusUnauthorized, wrongRec.Code)

	raw := httptest.NewRequest(http.MethodPost, "/api/connect/tokens", strings.NewReader(`{"name":"eks-testing"}`))
	raw.Header.Set("Content-Type", "application/json")
	raw.Header.Set("Authorization", "secret")
	rawRec := httptest.NewRecorder()
	srv.ServeHTTP(rawRec, raw)
	assert.Equal(t, http.StatusUnauthorized, rawRec.Code)

	glued := httptest.NewRequest(http.MethodPost, "/api/connect/tokens", strings.NewReader(`{"name":"eks-testing"}`))
	glued.Header.Set("Content-Type", "application/json")
	glued.Header.Set("Authorization", "Bearersecret")
	gluedRec := httptest.NewRecorder()
	srv.ServeHTTP(gluedRec, glued)
	assert.Equal(t, http.StatusUnauthorized, gluedRec.Code)

	ok := do(t, srv, http.MethodPost, "/api/connect/tokens", strings.NewReader(`{"name":"eks-testing"}`))
	assert.Equal(t, http.StatusCreated, ok.Code)
}

func TestDisconnectEnvironmentRequiresOperatorToken(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")
	issued := do(t, srv, http.MethodPost, "/api/connect/tokens", strings.NewReader(`{"name":"eks-testing"}`))
	require.Equal(t, http.StatusCreated, issued.Code)

	missing := httptest.NewRequest(http.MethodDelete, "/api/environments/eks-testing", nil)
	missingRec := httptest.NewRecorder()
	srv.ServeHTTP(missingRec, missing)
	assert.Equal(t, http.StatusUnauthorized, missingRec.Code)

	ok := do(t, srv, http.MethodDelete, "/api/environments/eks-testing", nil)
	assert.Equal(t, http.StatusNoContent, ok.Code)
}

func TestHubDatabaseWritesRequireOperatorToken(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatHub(t, srv, "secret")
	req := httptest.NewRequest(http.MethodPost, "/api/databases", strings.NewReader(
		`{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	ok := do(t, srv, http.MethodPost, "/api/databases", strings.NewReader(
		`{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a"}`))
	assert.Equal(t, http.StatusCreated, ok.Code)
}

func TestHubConnectionWritesRequireOperatorToken(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	req := httptest.NewRequest(http.MethodPut, "/api/connections/assistant", strings.NewReader(
		`{"apiKey":"sk-test"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHeartbeatUnauthorizedWithoutToken(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	rec := do(t, srv, http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewBufferString(`{}`))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHeartbeatRejectsABodyOverOneMiB(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	payload := bytes.Repeat([]byte("a"), (1<<20)+1)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	assert.Contains(t, rec.Body.String(), "1 MiB")
}

func TestHeartbeatThenListConnected(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatHub(t, srv, "secret")

	list := do(t, srv, http.MethodGet, "/api/environments", nil)
	assert.Equal(t, http.StatusOK, list.Code)
	body := decodeBody(t, list)
	environments := body["environments"].([]any)
	require.Len(t, environments, 1)
	row := environments[0].(map[string]any)
	assert.Equal(t, "spoke-a", row["name"])
	assert.Equal(t, "connected", row["status"])
}

func TestCreateRequiresEnvironmentInHubMode(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatHub(t, srv, "secret")

	rec := do(t, srv, http.MethodPost, "/api/databases", bytes.NewBufferString(
		`{"name":"orders-dev","engine":"mysql","profile":"dev"}`,
	))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, "Pick an environment.", body["message"])
}

func TestCreateAndGetHubDatabase(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatHub(t, srv, "secret")

	rec := do(t, srv, http.MethodPost, "/api/databases", bytes.NewBufferString(
		`{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a"}`,
	))
	assert.Equal(t, http.StatusCreated, rec.Code)
	created := decodeBody(t, rec)
	assert.Equal(t, "spoke-a/pds.orders-dev", created["id"])
	assert.Equal(t, "spoke-a", created["environment"])
	assert.Equal(t, "pending", created["status"])
	assert.Equal(t, "dev", created["profile"])

	got := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.orders-dev", nil)
	assert.Equal(t, http.StatusOK, got.Code)
	assert.Equal(t, "orders-dev", decodeBody(t, got)["name"])
	assert.Equal(t, "dev", decodeBody(t, got)["profile"])

	listed := do(t, srv, http.MethodGet, "/api/databases?environment=spoke-a", nil)
	assert.Equal(t, http.StatusOK, listed.Code)
	items := decodeBody(t, listed)["databases"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, "dev", items[0].(map[string]any)["profile"])
}

func TestCreateOnUnreachableEnvironmentConflicts(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	rec := do(t, srv, http.MethodPost, "/api/databases", bytes.NewBufferString(
		`{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a"}`,
	))
	assert.Equal(t, http.StatusConflict, rec.Code)
}

func beatHub(t *testing.T, srv http.Handler, token string) {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"environment": "spoke-a",
		"namespace":   "pds",
		"at":          time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestIssueJoinTokenAndHeartbeat(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")
	rec := do(t, srv, http.MethodPost, "/api/connect/tokens", bytes.NewBufferString(`{"name":"eks-testing"}`))
	assert.Equal(t, http.StatusCreated, rec.Code)
	body := decodeBody(t, rec)
	token, ok := body["token"].(string)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(token, "jnt_"))
	assert.Equal(t, "eks-testing", body["environment"])
	assert.Equal(t,
		"curl -fsSL 'https://hub.example/api/internal/connect/bootstrap.sh' | bash -s -- --token '"+token+"'",
		body["command"],
	)
	_, hasCreate := body["createCommand"]
	assert.False(t, hasCreate)

	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/other/heartbeat", bytes.NewBufferString(`{"environment":"other"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	wrong := httptest.NewRecorder()
	srv.ServeHTTP(wrong, req)
	assert.Equal(t, http.StatusUnauthorized, wrong.Code)

	beat := httptest.NewRequest(http.MethodPost, "/api/internal/environments/eks-testing/heartbeat", bytes.NewBufferString(
		`{"environment":"eks-testing","namespace":"pds"}`,
	))
	beat.Header.Set("Authorization", "Bearer "+token)
	beat.Header.Set("Content-Type", "application/json")
	got := httptest.NewRecorder()
	srv.ServeHTTP(got, beat)
	assert.Equal(t, http.StatusOK, got.Code)

	list := do(t, srv, http.MethodGet, "/api/environments", nil)
	environments := decodeBody(t, list)["environments"].([]any)
	require.Len(t, environments, 1)
	assert.Equal(t, "eks-testing", environments[0].(map[string]any)["name"])
	assert.Equal(t, "connected", environments[0].(map[string]any)["status"])
}

func TestHeartbeatRewritesInjectedInstanceIDs(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	payload, err := json.Marshal(map[string]any{
		"environment": "spoke-a",
		"namespace":   "pds",
		"instances": []map[string]any{{
			"ID":          "spoke-b/other-ns.orders-dev",
			"Name":        "orders-dev",
			"Namespace":   "other-ns",
			"Environment": "spoke-b",
			"Status":      map[string]any{"Phase": "ready"},
		}},
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	got := do(t, srv, http.MethodGet, "/api/databases/spoke-a/other-ns.orders-dev", nil)
	assert.Equal(t, http.StatusOK, got.Code)
	body := decodeBody(t, got)
	assert.Equal(t, "spoke-a/other-ns.orders-dev", body["id"])
	assert.Equal(t, "spoke-a", body["environment"])

	foreign := do(t, srv, http.MethodGet, "/api/databases/spoke-b/other-ns.orders-dev", nil)
	assert.Equal(t, http.StatusNotFound, foreign.Code)
}

func TestHeartbeatAfterDisconnectDoesNotRevive(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")
	issued := do(t, srv, http.MethodPost, "/api/connect/tokens", bytes.NewBufferString(`{"name":"eks-testing"}`))
	require.Equal(t, http.StatusCreated, issued.Code)
	token := decodeBody(t, issued)["token"].(string)

	beat := httptest.NewRequest(http.MethodPost, "/api/internal/environments/eks-testing/heartbeat", bytes.NewBufferString(
		`{"environment":"eks-testing","namespace":"pds"}`,
	))
	beat.Header.Set("Authorization", "Bearer "+token)
	beat.Header.Set("Content-Type", "application/json")
	joined := httptest.NewRecorder()
	srv.ServeHTTP(joined, beat)
	require.Equal(t, http.StatusOK, joined.Code)

	gone := do(t, srv, http.MethodDelete, "/api/environments/eks-testing", nil)
	require.Equal(t, http.StatusNoContent, gone.Code)

	again := httptest.NewRequest(http.MethodPost, "/api/internal/environments/eks-testing/heartbeat", bytes.NewBufferString(
		`{"environment":"eks-testing","namespace":"pds"}`,
	))
	again.Header.Set("Authorization", "Bearer "+token)
	again.Header.Set("Content-Type", "application/json")
	revived := httptest.NewRecorder()
	srv.ServeHTTP(revived, again)
	assert.Equal(t, http.StatusUnauthorized, revived.Code)

	list := do(t, srv, http.MethodGet, "/api/environments", nil)
	require.Equal(t, http.StatusOK, list.Code)
	assert.Empty(t, decodeBody(t, list)["environments"])
}

func TestConnectCreateEnvironmentIsGone(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")
	rec := do(t, srv, http.MethodGet, "/api/internal/connect/create-cluster.sh", nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestConnectBootstrapBakesHubURL(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")
	rec := do(t, srv, http.MethodGet, "/api/internal/connect/bootstrap.sh", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `HUB_URL='https://hub.example'`)
	assert.Contains(t, rec.Body.String(), `GIT_URL='https://github.com/example/pds.git'`)
	assert.Contains(t, rec.Body.String(), `REGION='us-west-2'`)
	assert.Contains(t, rec.Body.String(), "eksctl create cluster --config-file")
	assert.Contains(t, rec.Body.String(), "gateway: ${nat}")
	assert.Contains(t, rec.Body.String(), `nat="Single"`)
	assert.Contains(t, rec.Body.String(), "--public-nodes")
	assert.Contains(t, rec.Body.String(), "upgrade --install")
	assert.Contains(t, rec.Body.String(), "installing Percona Database Service")
	assert.Contains(t, rec.Body.String(), "installing the component that applies this environment's databases from the control plane")
	assert.Contains(t, rec.Body.String(), "kind: GitRepository")
	assert.Contains(t, rec.Body.String(), "create secret generic hub-github")
	assert.Contains(t, rec.Body.String(), "secretRef")
	assert.Contains(t, rec.Body.String(), `json.load(sys.stdin).get("gitPassword")`)
	assert.Contains(t, rec.Body.String(), `json.load(sys.stdin).get("gitIdentity")`)
	assert.Contains(t, rec.Body.String(), `json.load(sys.stdin).get("registry")`)
	assert.Contains(t, rec.Body.String(), "create secret generic")
	assert.Contains(t, rec.Body.String(), "dockerconfigjson")
	assert.Contains(t, rec.Body.String(), "--from-file=AWS_ACCESS_KEY_ID=")
	assert.Contains(t, rec.Body.String(), "serviceAccountName: hub-instances")
	assert.Contains(t, rec.Body.String(), "load_enrollment")
	assert.Contains(t, rec.Body.String(), "imagePullSecrets")
	assert.Contains(t, rec.Body.String(), "flux-system")
	assert.Contains(t, rec.Body.String(), "ADAPTER_ROLE")
	assert.Contains(t, rec.Body.String(), "stack:")
	assert.Contains(t, rec.Body.String(), "existingSecret: pds-s3")
	// The storage the spoke installs depends on the join: an AWS spoke gets
	// the bucket this run created, a local one keeps the chart's MinIO.
	assert.Contains(t, rec.Body.String(), "everestBackupStorage: ${backup_storage}")
	assert.Contains(t, rec.Body.String(), `backup_storage="s3"`)
	assert.Contains(t, rec.Body.String(), "ensure_spoke_bucket")
	// A restore creates a database on the environment it is asked from, so the
	// join script has no restore flag to carry.
	assert.NotContains(t, rec.Body.String(), "--restore")
	assert.Contains(t, rec.Body.String(), "Deleting resources this run created")
	assert.Contains(t, rec.Body.String(), "eksctl delete cluster")
	assert.Contains(t, rec.Body.String(), "CREATED_CLUSTER")
	assert.Contains(t, rec.Body.String(), "--set ui.enabled=false")
	assert.Contains(t, rec.Body.String(), "--set ui.replicaCount=0")
	assert.Contains(t, rec.Body.String(), "iam get-role")
	assert.NotContains(t, rec.Body.String(), "${HOME}/cnmo")
	assert.NotContains(t, rec.Body.String(), "cnmo-worktrees")
	assert.NotContains(t, rec.Body.String(), "oci://ghcr.io/percona/charts/pds")
	assert.NotContains(t, rec.Body.String(), "__HUB_URL__")
	assert.NotContains(t, rec.Body.String(), "__GIT_URL__")
	assert.NotContains(t, rec.Body.String(), "__DEFAULT_REGION__")
}

func TestConnectBootstrapRejectsAShellHost(t *testing.T) {
	srv := newHubTestServerWithoutPublicURL(t, "secret")
	req := httptest.NewRequest(http.MethodGet, "/api/internal/connect/bootstrap.sh", nil)
	req.Host = "$(id)"
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.NotContains(t, rec.Body.String(), "$(id)")
	assert.NotContains(t, rec.Body.String(), "HUB_URL=")
}

func TestConnectChartRequiresPackagedFile(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")
	rec := do(t, srv, http.MethodGet, "/api/internal/connect/pds.tgz", nil)
	if rec.Code == http.StatusOK {
		assert.Greater(t, rec.Body.Len(), 100)
		return
	}
	assert.Equal(t, http.StatusNotImplemented, rec.Code)
	assert.Contains(t, rec.Body.String(), "PDS_CHART")
}

func TestConnectEnrollmentRequiresJoinToken(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")
	rec := do(t, srv, http.MethodGet, "/api/internal/connect/enrollment", nil)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	issued := do(t, srv, http.MethodPost, "/api/connect/tokens", bytes.NewBufferString(`{"name":"eks-testing"}`))
	token := decodeBody(t, issued)["token"].(string)

	raw := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	raw.Header.Set("Authorization", token)
	rawRec := httptest.NewRecorder()
	srv.ServeHTTP(rawRec, raw)
	assert.Equal(t, http.StatusUnauthorized, rawRec.Code)

	glued := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	glued.Header.Set("Authorization", "Bearer"+token)
	gluedRec := httptest.NewRecorder()
	srv.ServeHTTP(gluedRec, glued)
	assert.Equal(t, http.StatusUnauthorized, gluedRec.Code)

	req := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	got := httptest.NewRecorder()
	srv.ServeHTTP(got, req)
	assert.Equal(t, http.StatusOK, got.Code)
	assert.Equal(t, "no-store", got.Header().Get("Cache-Control"))
	body := decodeBody(t, got)
	assert.Equal(t, "eks-testing", body["environment"])
	assert.Equal(t, "https://hub.example", body["hubUrl"])
	assert.Equal(t, "https://github.com/example/pds.git", body["gitUrl"])
	_, hasPassword := body["gitPassword"]
	assert.False(t, hasPassword)
	agent, ok := body["agentToken"].(string)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(agent, "agt_"))
	assert.NotEqual(t, token, agent)

	again := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	again.Header.Set("Authorization", "Bearer "+token)
	replay := httptest.NewRecorder()
	srv.ServeHTTP(replay, again)
	assert.Equal(t, http.StatusUnauthorized, replay.Code, "a second enrollment must not mint another agent credential")

	withAgent := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	withAgent.Header.Set("Authorization", "Bearer "+agent)
	agentRec := httptest.NewRecorder()
	srv.ServeHTTP(agentRec, withAgent)
	assert.Equal(t, http.StatusUnauthorized, agentRec.Code)
}

func TestConnectEnrollmentPreviewDoesNotConsumeInvitation(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")
	issued := do(t, srv, http.MethodPost, "/api/connect/tokens", bytes.NewBufferString(`{"name":"eks-testing"}`))
	token := decodeBody(t, issued)["token"].(string)

	req := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment?preview=1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	got := httptest.NewRecorder()
	srv.ServeHTTP(got, req)
	require.Equal(t, http.StatusOK, got.Code)
	body := decodeBody(t, got)
	assert.Equal(t, "eks-testing", body["environment"])
	_, hasAgent := body["agentToken"]
	assert.False(t, hasAgent, "a preview must not mint an agent credential")
	_, hasIdentity := body["gitIdentity"]
	assert.False(t, hasIdentity, "a preview must not mint clone keys")

	again := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	again.Header.Set("Authorization", "Bearer "+token)
	enroll := httptest.NewRecorder()
	srv.ServeHTTP(enroll, again)
	require.Equal(t, http.StatusOK, enroll.Code, "preview must leave the invitation usable")
	agent, ok := decodeBody(t, enroll)["agentToken"].(string)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(agent, "agt_"))
}

func TestConnectEnrollmentAgentTokenHeartbeats(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")
	issued := do(t, srv, http.MethodPost, "/api/connect/tokens", bytes.NewBufferString(`{"name":"eks-testing"}`))
	token := decodeBody(t, issued)["token"].(string)

	req := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	got := httptest.NewRecorder()
	srv.ServeHTTP(got, req)
	require.Equal(t, http.StatusOK, got.Code)
	agent := decodeBody(t, got)["agentToken"].(string)

	agentBeat := httptest.NewRequest(http.MethodPost, "/api/internal/environments/eks-testing/heartbeat", bytes.NewBufferString(
		`{"environment":"eks-testing","namespace":"pds"}`,
	))
	agentBeat.Header.Set("Authorization", "Bearer "+agent)
	agentBeat.Header.Set("Content-Type", "application/json")
	agentRec := httptest.NewRecorder()
	srv.ServeHTTP(agentRec, agentBeat)
	assert.Equal(t, http.StatusOK, agentRec.Code)

	joinBeat := httptest.NewRequest(http.MethodPost, "/api/internal/environments/eks-testing/heartbeat", bytes.NewBufferString(
		`{"environment":"eks-testing","namespace":"pds"}`,
	))
	joinBeat.Header.Set("Authorization", "Bearer "+token)
	joinBeat.Header.Set("Content-Type", "application/json")
	joinRec := httptest.NewRecorder()
	srv.ServeHTTP(joinRec, joinBeat)
	assert.Equal(t, http.StatusUnauthorized, joinRec.Code)

	again := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	again.Header.Set("Authorization", "Bearer "+token)
	replay := httptest.NewRecorder()
	srv.ServeHTTP(replay, again)
	assert.Equal(t, http.StatusUnauthorized, replay.Code)
}

func TestConnectEnrollmentRejectsARedeemedJoinToken(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")
	issued := do(t, srv, http.MethodPost, "/api/connect/tokens", bytes.NewBufferString(`{"name":"eks-testing"}`))
	token := decodeBody(t, issued)["token"].(string)

	beat := httptest.NewRequest(http.MethodPost, "/api/internal/environments/eks-testing/heartbeat", bytes.NewBufferString(
		`{"environment":"eks-testing","namespace":"pds"}`,
	))
	beat.Header.Set("Authorization", "Bearer "+token)
	beat.Header.Set("Content-Type", "application/json")
	beatRec := httptest.NewRecorder()
	srv.ServeHTTP(beatRec, beat)
	require.Equal(t, http.StatusOK, beatRec.Code)

	req := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	got := httptest.NewRecorder()
	srv.ServeHTTP(got, req)
	assert.Equal(t, http.StatusUnauthorized, got.Code)
}

func TestConnectGuideGitHubOrg(t *testing.T) {
	srv := newGitHubHubTestServer(t, "secret", &stubGitHost{})
	rec := do(t, srv, http.MethodGet, "/api/connect", nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["hub"])
	assert.Equal(t, "percona-cloud", body["githubOrg"])
	_, hasGit := body["gitUrl"]
	assert.False(t, hasGit)
	_, hasPassword := body["gitPassword"]
	assert.False(t, hasPassword)
	_, hasRegistry := body["registry"]
	assert.False(t, hasRegistry)
}

func TestConnectEnrollmentGitHubRepoCredentials(t *testing.T) {
	host := &stubGitHost{}
	srv := newGitHubHubTestServer(t, "secret", host)
	issued := do(t, srv, http.MethodPost, "/api/connect/tokens", bytes.NewBufferString(`{"name":"eks-testing"}`))
	assert.Equal(t, http.StatusCreated, issued.Code)
	assert.Equal(t, []string{"eks-testing"}, host.got)
	token := decodeBody(t, issued)["token"].(string)

	req := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	got := httptest.NewRecorder()
	srv.ServeHTTP(got, req)
	assert.Equal(t, http.StatusOK, got.Code)
	assert.Equal(t, "no-store", got.Header().Get("Cache-Control"))
	body := decodeBody(t, got)
	assert.Equal(t, "ssh://git@github.com/percona-cloud/eks-testing.git", body["gitUrl"])
	_, hasUser := body["gitUsername"]
	assert.False(t, hasUser)
	_, hasPassword := body["gitPassword"]
	assert.False(t, hasPassword)
	assert.Equal(t, stubCloneIdentity, body["gitIdentity"])
	assert.Equal(t, stubClonePub, body["gitIdentityPub"])
	agent, ok := body["agentToken"].(string)
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(agent, "agt_"))
	assert.NotEqual(t, token, agent)
	assert.Equal(t, []string{"eks-testing"}, host.keys)
	assert.NotContains(t, got.Body.String(), "ghs_test")
	registry, ok := body["registry"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "ghcr.io", registry["server"])
	assert.Equal(t, "x-access-token", registry["username"])
	assert.Equal(t, "ghs_packages", registry["password"])
	assert.Equal(t, "ghcr.io/example/pds-adapter:github-hub", body["agentImage"])

	list := do(t, srv, http.MethodGet, "/api/environments", nil)
	listed := decodeBody(t, list)
	assert.NotContains(t, listed, "githubOrg")
	_, listedPassword := listed["gitPassword"]
	assert.False(t, listedPassword)
	_, hasRegistry := listed["registry"]
	assert.False(t, hasRegistry)
}

func TestConnectEnrollmentGitHubCloneKeyFailure(t *testing.T) {
	srv := newGitHubHubTestServer(t, "secret", &stubGitHost{keyErr: hub.ErrGitHost})
	issued := do(t, srv, http.MethodPost, "/api/connect/tokens", bytes.NewBufferString(`{"name":"eks-testing"}`))
	assert.Equal(t, http.StatusCreated, issued.Code)
	token := decodeBody(t, issued)["token"].(string)

	req := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	got := httptest.NewRecorder()
	srv.ServeHTTP(got, req)
	assert.Equal(t, http.StatusBadGateway, got.Code)
	body := decodeBody(t, got)
	assert.Equal(t, "ErrDownstream", body["code"])
	assert.NotContains(t, got.Body.String(), "ghs_test")
	assert.NotContains(t, got.Body.String(), stubCloneIdentity)

	retry := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	retry.Header.Set("Authorization", "Bearer "+token)
	retryRec := httptest.NewRecorder()
	srv.ServeHTTP(retryRec, retry)
	assert.Equal(t, http.StatusBadGateway, retryRec.Code, "a clone failure must not consume the invitation")
}

func TestConnectEnrollmentAfterDisconnectRejectsTheToken(t *testing.T) {
	host := &stubGitHost{}
	srv := newGitHubHubTestServer(t, "secret", host)
	issued := do(t, srv, http.MethodPost, "/api/connect/tokens", bytes.NewBufferString(`{"name":"eks-testing"}`))
	require.Equal(t, http.StatusCreated, issued.Code)
	token := decodeBody(t, issued)["token"].(string)

	gone := do(t, srv, http.MethodDelete, "/api/environments/eks-testing", nil)
	require.Equal(t, http.StatusNoContent, gone.Code)

	req := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	got := httptest.NewRecorder()
	srv.ServeHTTP(got, req)
	assert.Equal(t, http.StatusUnauthorized, got.Code)
	assert.Empty(t, host.keys, "a revoked token must not mint a deploy key")
}

func TestConnectEnrollmentOmitsRegistryWhenItWouldReuseTheGitToken(t *testing.T) {
	srv := newGitHubHubTestServerWithRegistry(t, "secret", &stubGitHost{}, "ghs_test")
	issued := do(t, srv, http.MethodPost, "/api/connect/tokens", bytes.NewBufferString(`{"name":"eks-testing"}`))
	token := decodeBody(t, issued)["token"].(string)

	req := httptest.NewRequest(http.MethodGet, "/api/internal/connect/enrollment", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	got := httptest.NewRecorder()
	srv.ServeHTTP(got, req)
	assert.Equal(t, http.StatusOK, got.Code)
	body := decodeBody(t, got)
	_, hasRegistry := body["registry"]
	assert.False(t, hasRegistry)
	assert.NotContains(t, got.Body.String(), "ghs_test")
}

func TestIssueJoinGitHostFailure(t *testing.T) {
	srv := newGitHubHubTestServer(t, "secret", &stubGitHost{err: hub.ErrGitHost})
	rec := do(t, srv, http.MethodPost, "/api/connect/tokens", bytes.NewBufferString(`{"name":"eks-testing"}`))
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, "ErrDownstream", body["code"])
	assert.Contains(t, body["message"], "That environment could not be set up to receive changes.")
	assert.NotContains(t, rec.Body.String(), "ghs_test")
}

func TestGetObservedHubDatabase(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	payload, err := json.Marshal(map[string]any{
		"environment": "spoke-a",
		"namespace":   "pds",
		"at":          time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		"instances": []map[string]any{{
			"ID":          "spoke-a/pds.dev-testing",
			"Name":        "dev-testing",
			"Namespace":   "pds",
			"Environment": "spoke-a",
			"Status":      map[string]any{"Phase": "ready"},
		}},
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	got := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.dev-testing", nil)
	assert.Equal(t, http.StatusOK, got.Code)
	body := decodeBody(t, got)
	assert.Equal(t, "dev-testing", body["name"])
	assert.Equal(t, "spoke-a/pds.dev-testing", body["id"])
	assert.Equal(t, "custom", body["profile"])

	listed := do(t, srv, http.MethodGet, "/api/databases?environment=spoke-a", nil)
	assert.Equal(t, http.StatusOK, listed.Code)
	items := decodeBody(t, listed)["databases"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, "custom", items[0].(map[string]any)["profile"])
}

func TestBackupObservedDatabaseEnqueuesWork(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	payload, err := json.Marshal(map[string]any{
		"environment": "spoke-a",
		"namespace":   "pds",
		"at":          time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		"instances": []map[string]any{{
			"ID":          "spoke-a/pds.dev-testing",
			"Name":        "dev-testing",
			"Namespace":   "pds",
			"Environment": "spoke-a",
			"Status":      map[string]any{"Phase": "ready"},
		}},
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	backup := do(t, srv, http.MethodPost, "/api/databases/spoke-a/pds.dev-testing/backup", nil)
	assert.Equal(t, http.StatusAccepted, backup.Code)
	backupID := decodeBody(t, backup)["id"].(string)

	listed := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.dev-testing/backups", nil)
	assert.Equal(t, http.StatusOK, listed.Code)
	rows := decodeBody(t, listed)["backups"].([]any)
	require.Len(t, rows, 1)
	assert.Equal(t, backupID, rows[0].(map[string]any)["id"])

	beat := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	beat.Header.Set("Authorization", "Bearer secret")
	beat.Header.Set("Content-Type", "application/json")
	reply := httptest.NewRecorder()
	srv.ServeHTTP(reply, beat)
	require.Equal(t, http.StatusOK, reply.Code)
	body := decodeBody(t, reply)
	work, ok := body["work"].([]any)
	require.True(t, ok)
	require.Len(t, work, 1)
	item := work[0].(map[string]any)
	assert.Equal(t, "backup", item["kind"])
	assert.Equal(t, "pds.dev-testing", item["database"])
	assert.Equal(t, backupID, item["backupId"])
}

// TestHubRestoreCreatesADatabaseOnTheSourceEnvironment covers the restore a
// developer asks for: a second database next to the source, from one backup.
func TestHubRestoreCreatesADatabaseOnTheSourceEnvironment(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	seedRestorableSpoke(t, srv)

	got := do(t, srv, http.MethodPost, "/api/databases/spoke-a/pds.dev-testing/restore",
		bytes.NewBufferString(`{"backupId":"dev-testing-ok","name":"dev-testing-r1"}`))
	assert.Equal(t, http.StatusAccepted, got.Code, got.Body.String())
	body := decodeBody(t, got)
	assert.Equal(t, "accepted", body["status"])
	assert.Equal(t, "dev-testing-r1", body["name"])
	assert.Equal(t, "spoke-a", body["environment"])
	assert.Equal(t, "spoke-a/pds.dev-testing-r1", body["id"])

	// A restore builds nothing, so there is no environment to join and no
	// command carrying a join token.
	_, hasCommand := body["command"]
	assert.False(t, hasCommand, "the restore answer still carries a join command")

	// The restored database is the second one the environment now runs.
	listed := do(t, srv, http.MethodGet, "/api/databases?environment=spoke-a", nil)
	require.Equal(t, http.StatusOK, listed.Code)
	assert.Contains(t, listed.Body.String(), "dev-testing-r1")
}

func TestHubRestoreRequiresAName(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	seedRestorableSpoke(t, srv)

	missing := do(t, srv, http.MethodPost, "/api/databases/spoke-a/pds.dev-testing/restore",
		bytes.NewBufferString(`{"backupId":"dev-testing-ok"}`))
	assert.Equal(t, http.StatusBadRequest, missing.Code)
	assert.Contains(t, decodeBody(t, missing)["message"], "name")

	invalid := do(t, srv, http.MethodPost, "/api/databases/spoke-a/pds.dev-testing/restore",
		bytes.NewBufferString(`{"backupId":"dev-testing-ok","name":"Orders Restore"}`))
	assert.Equal(t, http.StatusBadRequest, invalid.Code)
}

// A copy named past what the provider can build a restore job from used to be
// accepted, committed, applied by Flux, and come up as an empty database. The
// API refuses it now, where the developer is still looking at the dialog.
func TestHubRestoreRefusesACopyNameTooLongForTheRestoreJob(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	seedRestorableSpoke(t, srv)

	long := do(t, srv, http.MethodPost, "/api/databases/spoke-a/pds.dev-testing/restore",
		bytes.NewBufferString(`{"backupId":"dev-testing-ok","name":"dev-testing-restore-2"}`))
	assert.Equal(t, http.StatusBadRequest, long.Code)
	assert.Contains(t, decodeBody(t, long)["message"], "19")

	ok := do(t, srv, http.MethodPost, "/api/databases/spoke-a/pds.dev-testing/restore",
		bytes.NewBufferString(`{"backupId":"dev-testing-ok","name":"dev-testing-r2"}`))
	assert.Equal(t, http.StatusAccepted, ok.Code)
}

// TestHubRestoreRefusesANameAlreadyOnTheEnvironment keeps a restore from landing
// on a database someone else depends on, the source included.
func TestHubRestoreRefusesANameAlreadyOnTheEnvironment(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	seedRestorableSpoke(t, srv)

	taken := do(t, srv, http.MethodPost, "/api/databases/spoke-a/pds.dev-testing/restore",
		bytes.NewBufferString(`{"backupId":"dev-testing-ok","name":"dev-testing"}`))
	assert.Equal(t, http.StatusConflict, taken.Code)
	assert.Contains(t, decodeBody(t, taken)["message"], "already")
}

// seedRestorableSpoke reports one ready database with one succeeded backup,
// which is the state a restore needs to be accepted.
func seedRestorableSpoke(t *testing.T, srv http.Handler) {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"environment": "spoke-a",
		"namespace":   "pds",
		"at":          time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		"instances": []map[string]any{{
			"ID":          "spoke-a/pds.dev-testing",
			"Name":        "dev-testing",
			"Namespace":   "pds",
			"Environment": "spoke-a",
			"Status":      map[string]any{"Phase": "ready"},
			// A spoke reports what the instance actually is. Without a
			// catalog profile this is the only size to restore it at.
			"Sizing": map[string]any{
				"Members": 3, "CPUCores": 4, "MemoryGB": 16, "StorageGB": 250,
			},
		}},
		"backups": []map[string]any{{
			"ID":       "dev-testing-ok",
			"Instance": "dev-testing",
			"Status":   "succeeded",
		}},
	})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// TestSharedAgentTokenIsScopedToItsEnvironments covers the wildcard the shared
// token used to be. It was accepted for whatever environment the path named, so
// one spoke could overwrite another's observed state and drain its work queue.
func TestSharedAgentTokenIsScopedToItsEnvironments(t *testing.T) {
	srv := newHubTestServerWithInventory(t, "shared-lab-token", true)

	scoped := httptest.NewRequest(
		http.MethodPost,
		"/api/internal/environments/spoke-a/heartbeat",
		bytes.NewBufferString(`{"environment":"spoke-a"}`),
	)
	scoped.Header.Set("Authorization", "Bearer shared-lab-token")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, scoped)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// Same token, an environment it was never scoped to.
	unscoped := httptest.NewRequest(
		http.MethodPost,
		"/api/internal/environments/spoke-prod/heartbeat",
		bytes.NewBufferString(`{"environment":"spoke-prod"}`),
	)
	unscoped.Header.Set("Authorization", "Bearer shared-lab-token")
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, unscoped)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
}

// TestHeartbeatRefusesANameThatIsNotALabel covers the names the inventory and
// its ConfigMap filter out when they read back. Accepting one registered a row
// that never persisted and that nothing could evict.
func TestHeartbeatRefusesANameThatIsNotALabel(t *testing.T) {
	srv := newHubTestServerWithInventory(t, "shared-lab-token", true)

	for _, name := range []string{"EKS%20Test", "Not-A-Label", "spoke_a", ".."} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/internal/environments/"+name+"/heartbeat",
				bytes.NewBufferString(`{}`),
			)
			req.Header.Set("Authorization", "Bearer shared-lab-token")
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, api.CodeValidation, decodeBody(t, rec)["code"])
		})
	}

	// None of them reached the environment list.
	rec := do(t, srv, http.MethodGet, "/api/environments", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "Not-A-Label")
}

// TestConnectBootstrapFailsBeforeItBuildsAnEnvironment covers the ordering that
// decides what a failed join costs. The chart and the agent image are the two
// things most likely to be missing, and both used to be checked after eksctl
// had already built a control plane and a node group.
func TestConnectBootstrapFailsBeforeItBuildsAnEnvironment(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	rec := do(t, srv, http.MethodGet, "/api/internal/connect/bootstrap.sh", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	preflight := strings.Index(body, "the control plane did not provide an agent image")
	resolve := strings.Index(body, "\nresolve_chart\n")
	create := strings.Index(body, "  ensure_cluster\n")

	require.Positive(t, preflight, "the image preflight is missing")
	require.Positive(t, resolve, "the chart is never resolved at the top level")
	require.Positive(t, create, "the environment is never created")

	assert.Less(t, preflight, create, "the image check must run before eksctl")
	assert.Less(t, resolve, create, "the chart must resolve before eksctl")

	// A released chart is opt in. The published one tracks the default
	// branch, which has no agent role, so a spoke installed from it could
	// not heartbeat.
	assert.Contains(t, body, "PDS_CHART_OCI")
	assert.NotContains(t, body, "oci://ghcr.io/percona/charts/pds")
}

// TestConnectBootstrapSupportsALocalJoin covers the path that makes the
// feature testable without an AWS bill: joining an environment that already
// exists, with no EKS cluster and no S3 bucket created.
func TestConnectBootstrapSupportsALocalJoin(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	rec := do(t, srv, http.MethodGet, "/api/internal/connect/bootstrap.sh", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	assert.Contains(t, body, "--local")
	assert.Contains(t, body, "joining the Kubernetes cluster kubectl points at")
	// The AWS tools are not a prerequisite for an environment that already runs.
	assert.Contains(t, body, `tools=(curl kubectl python3 helm tar awk)`)
	assert.Contains(t, body, `tools+=(aws eksctl)`)
	// A local environment has no bucket, so it keeps the chart's MinIO.
	assert.Contains(t, body, `backup_storage="minio"`)
}

// The environments list is polled by every client on every page. It answers what
// the page renders and nothing else, so the public address of this control
// plane and the Git org behind it are not handed out on the way past.
func TestListEnvironmentsOmitsHubAddresses(t *testing.T) {
	srv := newEmptyHubTestServer(t, "secret")

	rec := do(t, srv, http.MethodGet, "/api/environments", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeBody(t, rec)

	assert.NotContains(t, body, "hubUrl")
	assert.NotContains(t, body, "githubOrg")
	assert.NotContains(t, body, "gitUrl")
	assert.Equal(t, true, body["hub"])
	assert.Contains(t, body, "environments")
}

// TestSetHubBackupScheduleAnswersWithTheWrittenSchedule covers hub mode: the
// environment reports a schedule only after it applies the manifest, so the
// response states the schedule the hub just wrote rather than the old one.
func TestSetHubBackupScheduleAnswersWithTheWrittenSchedule(t *testing.T) {
	srv := newHubTestServer(t, "secret")
	beatHub(t, srv, "secret")

	rec := do(t, srv, http.MethodPost, "/api/databases", bytes.NewBufferString(
		`{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a"}`,
	))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())

	rec = do(t, srv, http.MethodPost, "/api/databases/spoke-a/pds.orders-dev/backup-schedule", bytes.NewBufferString(
		`{"period":"weekly","minute":30,"hour":4,"weekday":6,"retentionCopies":5}`,
	))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	schedule, ok := decodeBody(t, rec)["backupSchedule"].(map[string]any)
	require.True(t, ok, "body: %s", rec.Body.String())
	assert.Equal(t, true, schedule["enabled"])
	assert.Equal(t, "30 4 * * 6", schedule["cron"])
	assert.Equal(t, "weekly", schedule["period"])
	assert.Equal(t, float64(5), schedule["retentionCopies"])

	rec = do(t, srv, http.MethodPost, "/api/databases/spoke-a/pds.orders-dev/backup-schedule", bytes.NewBufferString(
		`{"period":"weekly","hour":4}`,
	))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, decodeBody(t, rec)["message"], "`weekday`")
}

// A database created before its environment reports monitoring is written
// without the monitoring component and never gets it, so a client that wants a
// monitored database waits for monitoring to be true first.
func TestListEnvironmentsReportsMonitoringOnceTheEnvironmentHasIt(t *testing.T) {
	srv := newHubTestServer(t, "secret")

	beatHubMonitoring(t, srv, "secret", "")
	assert.Equal(t, false, listedEnvironment(t, srv, "spoke-a")["monitoring"])

	beatHubMonitoring(t, srv, "secret", "pmm")
	assert.Equal(t, true, listedEnvironment(t, srv, "spoke-a")["monitoring"])
}

func beatHubMonitoring(t *testing.T, srv http.Handler, token, monitoringConfig string) {
	t.Helper()

	payload, err := json.Marshal(map[string]any{
		"environment":      "spoke-a",
		"namespace":        "pds",
		"at":               time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		"monitoringConfig": monitoringConfig,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func listedEnvironment(t *testing.T, srv http.Handler, name string) map[string]any {
	t.Helper()

	rec := do(t, srv, http.MethodGet, "/api/environments", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	environments, ok := decodeBody(t, rec)["environments"].([]any)
	require.True(t, ok)
	for _, item := range environments {
		if row, ok := item.(map[string]any); ok && row["name"] == name {
			return row
		}
	}
	require.Failf(t, "environment not listed", "%s is not in %s", name, rec.Body.String())

	return nil
}
