package mcp_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/mcp"
)

func managedList(progress string, failure map[string]any, status string) map[string]any {
	env := managedEnvironmentFixture()
	env["status"] = status
	env["provisioning"] = provisioningFixture(progress, failure)

	return map[string]any{"environments": []any{env}}
}

func TestStartingAConnectionGivesTheConsoleLinkAndNeverACommand(t *testing.T) {
	api := changeAPI()
	session := connectAt(t, api, auth.AccessChange, mcp.WithConsoleURL("https://pds.example"))

	res := callTool(t, session, "start_cloud_connection", nil)

	require.False(t, res.IsError, text(t, res))
	got := structured(t, res)
	assert.Equal(t, "https://pds.example/environments/aws-accounts/new", got["consoleUrl"])
	assert.Len(t, got["cloudConnections"], 1)
	assert.NotContains(t, text(t, res), "ccn_")
	assert.NotContains(t, text(t, res), "curl")
	for _, request := range api.sent() {
		assert.Equal(t, http.MethodGet, request.Method, "nothing is minted")
		assert.NotContains(t, request.URI, "token")
	}
}

func TestACloudConnectionReadsNeverCarryASecretFieldEvenIfTheAPISentOne(t *testing.T) {
	api := catalogAPI()
	leaky := cloudConnectionFixture()
	leaky["externalId"] = "super-secret-external-id"
	leaky["roleArn"] = "arn:aws:iam::123456789012:role/x"
	api.routes["/api/cloud-connections/"+cloudID] = ok(leaky)

	res := callTool(t, connect(t, api), "get_cloud_connection", map[string]any{"id": cloudID})

	require.False(t, res.IsError, text(t, res))
	assert.NotContains(t, text(t, res), "super-secret")
	assert.NotContains(t, text(t, res), "arn:aws")
}

func TestAConnectionIdThatIsNotOneSendsNothing(t *testing.T) {
	api := catalogAPI()

	res := callTool(t, connect(t, api), "get_cloud_connection", map[string]any{"id": "../environments"})

	require.True(t, res.IsError)
	assert.Equal(t, "invalid_input", structured(t, res)["kind"])
	assertNoRequest(t, api)
}

func TestCreatingAManagedEnvironmentSendsTheRouteAndPointsAtTheWait(t *testing.T) {
	api := changeAPI()

	res := callTool(t, connectAt(t, api, auth.AccessChange), "create_managed_environment",
		map[string]any{"connectionId": cloudID, "name": "orders", "region": "eu-west-1"})

	require.False(t, res.IsError, text(t, res))
	got := structured(t, res)
	assert.Equal(t, "orders", got["id"])
	assert.Equal(t, map[string]any{"tool": "wait_for_environment", "arguments": map[string]any{"name": "orders"}}, got["next"])
	sent := api.sent()
	require.Len(t, sent, 1)
	assert.Equal(t, http.MethodPost, sent[0].Method)
	assert.Equal(t, "/api/cloud-connections/"+cloudID+"/environments", sent[0].URI)
	assert.JSONEq(t, `{"name":"orders","region":"eu-west-1"}`, sent[0].Body)
}

func TestRetryReadsTheLatestRunAndRetriesIt(t *testing.T) {
	api := changeAPI()

	res := callTool(t, connectAt(t, api, auth.AccessChange), "retry_environment_run", map[string]any{"environment": "orders"})

	require.False(t, res.IsError, text(t, res))
	var uris []string
	for _, request := range api.sent() {
		uris = append(uris, request.Method+" "+request.URI)
	}
	assert.Equal(t, []string{"GET /api/environments/orders", "POST /api/environments/orders/runs/5/retry"}, uris)
}

func TestRetryIsRefusedWhenNothingFailed(t *testing.T) {
	api := changeAPI()
	ready := managedEnvironmentFixture()
	ready["provisioning"] = provisioningFixture("ready", nil)
	api.routes["/api/environments/orders"] = ok(ready)

	res := callTool(t, connectAt(t, api, auth.AccessChange), "retry_environment_run", map[string]any{"environment": "orders"})

	require.True(t, res.IsError)
	assert.Equal(t, "invalid_input", structured(t, res)["kind"])
	assert.Len(t, api.sent(), 1, "only the read was sent")
}

func TestAnUpgradeSendsItsKindAndAConflictPassesThrough(t *testing.T) {
	api := changeAPI()
	session := connectAt(t, api, auth.AccessChange)

	callTool(t, session, "upgrade_environment", map[string]any{"environment": "orders", "kind": "kubernetes"})
	assert.JSONEq(t, `{"kind":"upgrade_kubernetes"}`, api.sent()[0].Body)

	bad := callTool(t, session, "upgrade_environment", map[string]any{"environment": "orders", "kind": "rebuild"})
	require.True(t, bad.IsError, "only pds and kubernetes are kinds an agent upgrades")

	api.routes["POST /api/environments/orders/runs"] = failed(http.StatusConflict, "ErrConflict", "This environment already has a run in progress. Wait for it to finish, or retry it if it failed.")
	busy := callTool(t, session, "upgrade_environment", map[string]any{"environment": "orders", "kind": "pds"})
	require.True(t, busy.IsError)
	assert.Equal(t, "conflict", structured(t, busy)["kind"])
	assert.Equal(t, "This environment already has a run in progress. Wait for it to finish, or retry it if it failed.", structured(t, busy)["message"])
}

func lifecycleFixture() map[string]any {
	return map[string]any{
		"chartVersion": "0.1.0", "pendingChartVersion": "0.2.0", "deferred": true, "deferredUntil": "2026-12-01T00:00:00Z",
		"maintenanceWindow": map[string]any{
			"day": 5, "hour": 22, "timeZone": "Europe/Berlin", "nextStart": "2026-11-20T21:00:00Z", "nextEnd": "2026-11-20T23:00:00Z",
		},
		"templateRevision": 1, "latestTemplateRevision": 2, "templateOutOfDate": true, "templateSummary": "Template revision 1, latest 2",
		"kubernetesVersion": "1.32", "kubernetesUpgradeAvailable": true,
		"drift":          []any{map[string]any{"resource": "PrivateRoute (AWS::EC2::Route)", "detail": "Changed outside PDS"}},
		"driftCheckedAt": "2026-11-20T09:00:00Z", "lost": false,
	}
}

func TestAnEnvironmentReadCarriesItsUpgradesAndDriftForTheAgentToTellTheUser(t *testing.T) {
	api := catalogAPI()
	env := managedEnvironmentFixture()
	env["state"], env["lifecycle"] = "ready", lifecycleFixture()
	api.routes["/api/environments/orders"] = ok(env)

	res := callTool(t, connect(t, api), "get_environment", map[string]any{"name": "orders"})

	require.False(t, res.IsError, text(t, res))
	life, _ := structured(t, res)["lifecycle"].(map[string]any)
	require.NotNil(t, life)
	assert.Equal(t, "0.2.0", life["pendingChartVersion"])
	assert.Equal(t, "Template revision 1, latest 2", life["templateSummary"])
	assert.Equal(t, true, life["kubernetesUpgradeAvailable"])
	assert.Equal(t, []any{map[string]any{"resource": "PrivateRoute (AWS::EC2::Route)", "detail": "Changed outside PDS"}}, life["drift"])
	assert.Equal(t, map[string]any{
		"day": float64(5), "hour": float64(22), "timeZone": "Europe/Berlin", "nextStart": "2026-11-20T21:00:00Z", "nextEnd": "2026-11-20T23:00:00Z",
	}, life["maintenanceWindow"])
}

func TestALostEnvironmentReadsAsLostWithItsReason(t *testing.T) {
	api := catalogAPI()
	env := managedEnvironmentFixture()
	life := lifecycleFixture()
	life["lost"], life["lostReason"], life["lostAt"] = true, "Infrastructure missing", "2026-11-19T03:00:00Z"
	env["state"], env["lifecycle"] = "lost", life
	api.routes["/api/environments"] = ok(map[string]any{"environments": []any{env}})

	res := callTool(t, connect(t, api), "list_environments", nil)

	require.False(t, res.IsError, text(t, res))
	got, _ := structured(t, res)["environments"].([]any)
	require.Len(t, got, 1)
	row, _ := got[0].(map[string]any)
	assert.Equal(t, "lost", row["state"])
	assert.Equal(t, "Infrastructure missing", row["lifecycle"].(map[string]any)["lostReason"])
}

func TestWaitingOnALostEnvironmentStopsWithTheLostOutcome(t *testing.T) {
	api := catalogAPI()
	env := managedEnvironmentFixture()
	env["state"], env["lifecycle"] = "lost", map[string]any{"lost": true, "lostReason": "Infrastructure missing", "drift": []any{}}
	env["provisioning"] = provisioningFixture("ready", nil)
	api.routes["/api/environments"] = ok(map[string]any{"environments": []any{env}})

	res := callTool(t, connect(t, api), "wait_for_environment", map[string]any{"name": "orders", "timeoutSeconds": 1})

	require.False(t, res.IsError, text(t, res))
	assert.Equal(t, "lost", structured(t, res)["outcome"])
}

func TestAKubernetesUpgradeSendsTheVersionWhenAskedAndAgentsNeverConfirmANodeGroupReplace(t *testing.T) {
	api := changeAPI()
	session := connectAt(t, api, auth.AccessChange)

	callTool(t, session, "upgrade_environment", map[string]any{"environment": "orders", "kind": "kubernetes", "kubernetesVersion": "1.34"})

	assert.JSONEq(t, `{"kind":"upgrade_kubernetes","kubernetesVersion":"1.34"}`, api.sent()[0].Body)
	assert.NotContains(t, api.sent()[0].Body, "confirmNodeGroupReplace")
}

func TestAPDSUpgradeRefusesAKubernetesVersion(t *testing.T) {
	api := changeAPI()

	res := callTool(t, connectAt(t, api, auth.AccessChange), "upgrade_environment", map[string]any{"environment": "orders", "kind": "pds", "kubernetesVersion": "1.34"})

	require.True(t, res.IsError)
	assert.Equal(t, "invalid_input", structured(t, res)["kind"])
	assertNoRequest(t, api)
}

func TestDeletingAManagedEnvironmentAsksForTheNameAndNeverSendsAScope(t *testing.T) {
	for era, version := range eras {
		api := changeAPI()
		who := answering("accept", map[string]any{"name": "orders"})
		session := connectAsking(t, api, auth.AccessChangeDelete, version, who)

		res := callOn(t, session, "delete_managed_environment", map[string]any{"environment": "orders"})

		require.False(t, res.IsError, era)
		require.Len(t, who.questions(), 1, era)
		sent := api.sent()
		last := sent[len(sent)-1]
		assert.Equal(t, http.MethodDelete, last.Method, era)
		assert.JSONEq(t, `{"confirm":"orders"}`, last.Body, era)
		assert.NotContains(t, last.Body, "pds_only", era)
	}
}

func TestDeletingWithTheWrongNameOrAConnectedEnvironmentSendsNoDelete(t *testing.T) {
	api := changeAPI()
	session := connectAt(t, api, auth.AccessChangeDelete)

	wrong := callTool(t, session, "delete_managed_environment", map[string]any{"environment": "orders", "confirm": "order"})
	require.True(t, wrong.IsError)
	assert.Equal(t, "invalid_input", structured(t, wrong)["kind"])

	api.routes["/api/environments/spoke-a"] = ok(environmentsFixture()["environments"].([]any)[0])
	connected := callTool(t, session, "delete_managed_environment", map[string]any{"environment": "spoke-a", "confirm": "spoke-a"})
	require.True(t, connected.IsError)
	assert.Equal(t, "invalid_input", structured(t, connected)["kind"])

	for _, request := range api.sent() {
		assert.Equal(t, http.MethodGet, request.Method)
	}
}

func TestRemovingAnAWSAccountIsRefusedWhileEnvironmentsRunThroughItAndBeforeAskingAnyone(t *testing.T) {
	api := changeAPI()
	busy := cloudConnectionFixture()
	busy["environments"] = []any{"orders"}
	api.routes["/api/cloud-connections/"+cloudID] = ok(busy)
	who := answering("accept", map[string]any{"name": "acme"})
	session := connectAsking(t, api, auth.AccessChangeDelete, "2026-07-28", who)

	res := callOn(t, session, "remove_cloud_connection", map[string]any{"id": cloudID})

	require.True(t, res.IsError)
	assert.Equal(t, "conflict", structured(t, res)["kind"])
	assert.Empty(t, who.questions())
	assert.Len(t, api.sent(), 1)
}

func TestRemovingAnAWSAccountTakesTheAliasOrTheAccountNumber(t *testing.T) {
	api := changeAPI()
	session := connectAt(t, api, auth.AccessChangeDelete)

	wrong := callTool(t, session, "remove_cloud_connection", map[string]any{"id": cloudID, "confirm": "123456789012"})
	require.True(t, wrong.IsError, "the alias is the name when there is one")

	done := callTool(t, session, "remove_cloud_connection", map[string]any{"id": cloudID, "confirm": "acme"})
	require.False(t, done.IsError, text(t, done))
	sent := api.sent()
	assert.Equal(t, http.MethodDelete, sent[len(sent)-1].Method)
	assert.JSONEq(t, `{"confirm":"acme"}`, sent[len(sent)-1].Body)
}

func TestNoCloudToolIsOfferedForWhatAgentsNeverDo(t *testing.T) {
	listed := names(listedTools(t, connectAt(t, changeAPI(), auth.AccessChangeDelete)))

	for _, name := range listed {
		for _, banned := range []string{"token", "script", "force", "pds_only", "region_trust", "repair", "defer", "maintenance", "external_id"} {
			assert.NotContains(t, name, banned)
		}
	}
}

func TestWaitingForAManagedEnvironmentEndsOnAFailureWithTheRefusal(t *testing.T) {
	api := newFakeAPI(nil)
	failure := managedEnvironmentFixture()["provisioning"].(map[string]any)["failure"].(map[string]any)
	api.then("/api/environments", ok(managedList("failed", failure, "pending")))

	got := waitFor(t, api, newManualClock(), "wait_for_environment", map[string]any{"name": "orders"})

	assert.Equal(t, "failed", got["outcome"])
	assert.EqualValues(t, 0, got["waitedSeconds"])
	prov := got["provisioning"].(map[string]any)
	assert.Equal(t, "quota", prov["failure"].(map[string]any)["refusal"].(map[string]any)["reason"])
}

func TestWaitingKeepsPollingWhileBuildingAndReportsWaitingWhenPausedAtTheDeadline(t *testing.T) {
	building := newFakeAPI(nil)
	building.then("/api/environments", ok(managedList("in_progress", nil, "pending")), ok(managedList("ready", nil, "connected")))

	done := waitFor(t, building, newManualClock(), "wait_for_environment", map[string]any{"name": "orders"})
	assert.Equal(t, "connected", done["outcome"])
	assert.EqualValues(t, 5, done["waitedSeconds"])

	paused := newFakeAPI(nil)
	paused.then("/api/environments", ok(managedList("waiting", nil, "pending")))
	got := waitFor(t, paused, newManualClock(), "wait_for_environment", map[string]any{"name": "orders", "timeoutSeconds": 5})
	assert.Equal(t, "waiting", got["outcome"])
}

func TestWaitingForADeleteEndsWhenTheEnvironmentIsGoneOrBlocked(t *testing.T) {
	gone := newFakeAPI(nil)
	gone.then("/api/environments", ok(managedList("in_progress", nil, "pending")), ok(environmentsFixture()))
	got := waitFor(t, gone, newManualClock(), "wait_for_environment", map[string]any{"name": "orders", "until": "deleted"})
	assert.Equal(t, "deleted", got["outcome"])

	blocked := newFakeAPI(nil)
	blocked.then("/api/environments", ok(managedList("blocked", map[string]any{"sentence": "Two volumes remain in your AWS account.", "action": "retry"}, "pending")))
	stuck := waitFor(t, blocked, newManualClock(), "wait_for_environment", map[string]any{"name": "orders", "until": "deleted"})
	assert.Equal(t, "blocked", stuck["outcome"])
	assert.True(t, strings.Contains(text(t, callTool(t, connect(t, blocked, mcp.WithClock(newManualClock())), "wait_for_environment", map[string]any{"name": "orders", "until": "deleted"})), "Two volumes"))
}
