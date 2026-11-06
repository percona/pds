package mcp_test

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/mcp"
)

const (
	databasePath = "/api/databases/" + orders
	backupsPath  = databasePath + "/backups"

	environmentsPath = "/api/environments"
)

// databaseAt is the database as the API answers while it is in a phase.
func databaseAt(status, message string, connected bool) map[string]any {
	d := databaseFixture()
	d["status"], d["message"] = status, message
	if !connected {
		delete(d, "connection")
	}

	return d
}

func backupAt(id, status string) map[string]any {
	return map[string]any{"backups": []any{
		map[string]any{"id": "other", "status": "succeeded", "sizeBytes": 1, "scheduled": false},
		map[string]any{"id": id, "status": status, "sizeBytes": 2048, "scheduled": false, "createdAt": "2026-09-08T13:00:00Z"},
	}}
}

func environmentAt(name, status string) map[string]any {
	return map[string]any{"hub": true, "environments": []any{
		map[string]any{"name": "other", "status": "connected"},
		map[string]any{"name": name, "status": status, "team": "default", "monitoring": true},
	}}
}

func waitFor(t *testing.T, api *fakeAPI, clock *manualClock, tool string, args map[string]any) map[string]any {
	t.Helper()

	res := callTool(t, connect(t, api, mcp.WithClock(clock)), tool, args)

	require.False(t, res.IsError, text(t, res))

	return structured(t, res)
}

func TestWaitForDatabaseReadySucceedsOnceReadyWithAConnection(t *testing.T) {
	clock := newManualClock()
	api := newFakeAPI(nil)
	api.then(databasePath,
		ok(databaseAt("pending", "", false)),
		ok(databaseAt("ready", "", false)),
		ok(databaseAt("ready", "", true)),
	)

	got := waitFor(t, api, clock, "wait_for_database", map[string]any{"id": orders, "until": "ready"})

	assert.Equal(t, "succeeded", got["outcome"])
	assert.Equal(t, orders, got["id"])
	assert.Equal(t, "ready", got["status"])
	assert.EqualValues(t, 10, got["waitedSeconds"])
	assert.Equal(t, "orders-dev.pds.svc", got["database"].(map[string]any)["connection"].(map[string]any)["host"])
	assert.Len(t, api.sent(), 3, "ready without a connection is not done")
	assert.Equal(t, []time.Duration{5 * time.Second, 5 * time.Second}, clock.sleeps(), "it polls every five seconds")
}

func TestWaitForDatabaseWithExpectKeepsWaitingForTheSizes(t *testing.T) {
	scaled := databaseAt("ready", "", true)
	scaled["memoryGb"] = 4

	t.Run("sizes that never match stay in progress to the deadline", func(t *testing.T) {
		clock := newManualClock()
		api := newFakeAPI(map[string]reply{databasePath: ok(databaseAt("ready", "", true))})

		got := waitFor(t, api, clock, "wait_for_database", map[string]any{
			"id": orders, "until": "ready", "expect": map[string]any{"memoryGb": 4, "members": 1},
		})

		assert.Equal(t, "in_progress", got["outcome"])
		assert.Equal(t, "ready", got["status"])
		assert.Equal(t, "The database is ready, but its size is not the one you asked for yet.", got["message"])
		assert.EqualValues(t, 30, got["waitedSeconds"])
		assert.Len(t, api.sent(), 7, "once at the start and then every five seconds up to the deadline")
	})

	t.Run("it succeeds when the sizes arrive", func(t *testing.T) {
		clock := newManualClock()
		api := newFakeAPI(nil)
		api.then(databasePath, ok(databaseAt("ready", "", true)), ok(scaled))

		got := waitFor(t, api, clock, "wait_for_database", map[string]any{
			"id": orders, "until": "ready", "expect": map[string]any{"memoryGb": 4, "cpu": 1, "storageGb": 20, "members": 1},
		})

		assert.Equal(t, "succeeded", got["outcome"])
		assert.EqualValues(t, 5, got["waitedSeconds"])
		assert.EqualValues(t, 4, got["database"].(map[string]any)["memoryGb"])
		assert.NotContains(t, got, "message")
	})
}

func TestWaitForDatabaseGivesFailedWithItsMessageAndNoError(t *testing.T) {
	clock := newManualClock()
	api := newFakeAPI(map[string]reply{databasePath: ok(databaseAt("failed", "The volume could not be created.", false))})

	res := callTool(t, connect(t, api, mcp.WithClock(clock)), "wait_for_database", map[string]any{"id": orders, "until": "ready"})

	assert.False(t, res.IsError, "a reached failed is an observation")
	got := structured(t, res)
	assert.Equal(t, "failed", got["outcome"])
	assert.Equal(t, "failed", got["status"])
	assert.Equal(t, "The volume could not be created.", got["message"])
	assert.EqualValues(t, 0, got["waitedSeconds"])
	assert.Len(t, api.sent(), 1, "it stops at the first poll that says failed")
	assert.Empty(t, clock.sleeps())
}

func TestWaitForDatabaseStalledStaysInProgressWithTheBlockingReason(t *testing.T) {
	clock := newManualClock()
	api := newFakeAPI(map[string]reply{databasePath: ok(databaseAt("pending", "A member cannot be placed: no node has room.", false))})

	got := waitFor(t, api, clock, "wait_for_database", map[string]any{"id": orders, "until": "ready"})

	assert.Equal(t, "in_progress", got["outcome"])
	assert.Equal(t, "pending", got["status"])
	assert.Equal(t, "A member cannot be placed: no node has room.", got["message"])
	assert.EqualValues(t, 30, got["waitedSeconds"])
}

func TestWaitForDatabaseDeletedSucceedsOnNotFound(t *testing.T) {
	clock := newManualClock()
	api := newFakeAPI(nil)
	api.then(databasePath,
		ok(databaseAt("ready", "", true)),
		ok(databaseAt("deleting", "", true)),
		failed(http.StatusNotFound, "ErrNotFound", "That database does not exist."),
	)

	got := waitFor(t, api, clock, "wait_for_database", map[string]any{"id": orders, "until": "deleted"})

	assert.Equal(t, "succeeded", got["outcome"])
	assert.EqualValues(t, 10, got["waitedSeconds"])
	assert.NotContains(t, got, "database", "the database is gone")
	assert.NotContains(t, got, "status")
	assert.NotContains(t, got, "message")
}

func TestWaitForDatabaseDeletedStaysInProgressWhileItExists(t *testing.T) {
	clock := newManualClock()
	api := newFakeAPI(map[string]reply{databasePath: ok(databaseAt("failed", "Deleting.", true))})

	got := waitFor(t, api, clock, "wait_for_database", map[string]any{"id": orders, "until": "deleted", "timeoutSeconds": 10})

	assert.Equal(t, "in_progress", got["outcome"], "failed is not the answer to a wait for deleted")
	assert.Equal(t, "failed", got["status"])
	assert.EqualValues(t, 10, got["waitedSeconds"])
	assert.Len(t, api.sent(), 3)
}

func TestWaitForDatabaseReadyOnAMissingDatabaseIsAnError(t *testing.T) {
	clock := newManualClock()
	api := newFakeAPI(map[string]reply{databasePath: failed(http.StatusNotFound, "ErrNotFound", "That database does not exist.")})

	res := callTool(t, connect(t, api, mcp.WithClock(clock)), "wait_for_database", map[string]any{"id": orders, "until": "ready"})

	require.True(t, res.IsError)
	assert.Equal(t, "not_found", structured(t, res)["kind"])
	assert.Len(t, api.sent(), 1)
}

func TestWaitForBackupFollowsTheBackupToItsEnd(t *testing.T) {
	cases := []struct {
		name    string
		answers []reply
		outcome string
		waited  int
		status  string
	}{
		{"succeeded", []reply{ok(backupAt("b1", "running")), ok(backupAt("b1", "succeeded"))}, "succeeded", 5, "succeeded"},
		{"failed", []reply{ok(backupAt("b1", "failed"))}, "failed", 0, "failed"},
		{"still running", []reply{ok(backupAt("b1", "running"))}, "in_progress", 30, "running"},
		{"pending then running", []reply{ok(backupAt("b1", "pending")), ok(backupAt("b1", "running"))}, "in_progress", 30, "running"},
		{"appears late", []reply{ok(backupAt("other", "running")), ok(backupAt("b1", "succeeded"))}, "succeeded", 5, "succeeded"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := newManualClock()
			api := newFakeAPI(nil)
			api.then(backupsPath, tc.answers...)

			res := callTool(t, connect(t, api, mcp.WithClock(clock)), "wait_for_backup", map[string]any{"id": orders, "backupId": "b1"})

			require.False(t, res.IsError, text(t, res))
			got := structured(t, res)
			assert.Equal(t, tc.outcome, got["outcome"])
			assert.Equal(t, orders, got["id"])
			assert.Equal(t, "b1", got["backupId"])
			assert.EqualValues(t, tc.waited, got["waitedSeconds"])
			assert.Equal(t, tc.status, got["backup"].(map[string]any)["status"])
			assert.Equal(t, "b1", got["backup"].(map[string]any)["id"])
		})
	}
}

func TestWaitForBackupGivesUnknownWhenTheBackupNeverAppears(t *testing.T) {
	clock := newManualClock()
	api := newFakeAPI(map[string]reply{backupsPath: ok(backupsFixture())})

	got := waitFor(t, api, clock, "wait_for_backup", map[string]any{"id": orders, "backupId": "missing", "timeoutSeconds": 15})

	assert.Equal(t, "unknown", got["outcome"])
	assert.NotContains(t, got, "backup")
	assert.EqualValues(t, 15, got["waitedSeconds"])
	assert.Len(t, api.sent(), 4)
}

func TestWaitForBackupWithBackupsNotSetUpIsAnError(t *testing.T) {
	clock := newManualClock()
	api := newFakeAPI(map[string]reply{backupsPath: failed(http.StatusNotImplemented, "ErrNotImplemented", "Backups are not set up for this environment.")})

	res := callTool(t, connect(t, api, mcp.WithClock(clock)), "wait_for_backup", map[string]any{"id": orders, "backupId": "b1"})

	require.True(t, res.IsError)
	assert.Equal(t, "not_available", structured(t, res)["kind"])
	assert.Equal(t, "Backups are not set up for this environment.", structured(t, res)["message"])
}

func TestWaitForEnvironmentGivesNotFoundInProgressAndConnected(t *testing.T) {
	cases := []struct {
		name    string
		answers []reply
		outcome string
		status  string
		waited  int
	}{
		{"never listed", []reply{ok(environmentsFixture())}, "not_found", "", 30},
		{"listed but not connected", []reply{ok(environmentAt("new-env", "waiting"))}, "in_progress", "waiting", 30},
		{"connects", []reply{ok(environmentAt("new-env", "waiting")), ok(environmentAt("new-env", "connected"))}, "connected", "connected", 5},
		{"appears and connects", []reply{ok(environmentsFixture()), ok(environmentAt("new-env", "connected"))}, "connected", "connected", 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := newManualClock()
			api := newFakeAPI(nil)
			api.then("/api/environments", tc.answers...)

			got := waitFor(t, api, clock, "wait_for_environment", map[string]any{"name": "new-env"})

			assert.Equal(t, tc.outcome, got["outcome"])
			assert.Equal(t, "new-env", got["name"])
			assert.EqualValues(t, tc.waited, got["waitedSeconds"])
			if tc.status == "" {
				assert.NotContains(t, got, "status")
			} else {
				assert.Equal(t, tc.status, got["status"])
			}
		})
	}
}

func TestTheDeadlineHoldsAgainstASlowPoll(t *testing.T) {
	cases := []struct {
		name    string
		slowAt  int32
		outcome string
		message string
		waited  int
	}{
		{"the first poll is slow", 1, "unknown", "The wait ended before the control plane answered. Call get_database to check.", 30},
		{"a later poll is slow", 2, "in_progress", "Waiting for a node.", 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := newManualClock()
			api := newFakeAPI(map[string]reply{databasePath: ok(databaseAt("pending", "Waiting for a node.", false))})
			deadline := clock.Now().Add(time.Duration(tc.waited) * time.Second)
			var polls atomic.Int32
			api.hook = func(r *http.Request) {
				if polls.Add(1) != tc.slowAt {
					return
				}
				// The poll hangs; only the deadline ends it.
				clock.Advance(deadline.Sub(clock.Now()))
				select {
				case <-r.Context().Done():
				case <-time.After(10 * time.Second):
					t.Error("the slow poll outlived the deadline")
				}
			}

			res := callTool(t, connect(t, api, mcp.WithClock(clock)), "wait_for_database", map[string]any{"id": orders, "until": "ready"})

			require.False(t, res.IsError, text(t, res))
			got := structured(t, res)
			assert.Equal(t, tc.outcome, got["outcome"])
			assert.Equal(t, tc.message, got["message"])
			assert.EqualValues(t, tc.waited, got["waitedSeconds"])
			assert.EqualValues(t, tc.slowAt, polls.Load(), "nothing is polled after the deadline")
		})
	}
}

func TestTimeoutSecondsSetsTheDeadline(t *testing.T) {
	clock := newManualClock()
	api := newFakeAPI(map[string]reply{environmentsPath: ok(environmentAt("new-env", "waiting"))})

	got := waitFor(t, api, clock, "wait_for_environment", map[string]any{"name": "new-env", "timeoutSeconds": 12})

	assert.EqualValues(t, 12, got["waitedSeconds"])
	assert.Equal(t, []time.Duration{5 * time.Second, 5 * time.Second, 2 * time.Second}, clock.sleeps(), "the last sleep is cut to the deadline")
	assert.Len(t, api.sent(), 4, "and the poll at the deadline still runs")
}

func TestWaitInputsAreCheckedBeforeAnyRequest(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
	}{
		{"a zero timeout", "wait_for_database", map[string]any{"id": orders, "until": "ready", "timeoutSeconds": 0}},
		{"a timeout above the cap", "wait_for_backup", map[string]any{"id": orders, "backupId": "b1", "timeoutSeconds": 46}},
		{"a timeout that is not whole", "wait_for_environment", map[string]any{"name": "e", "timeoutSeconds": 1.5}},
		{"an until outside the enum", "wait_for_database", map[string]any{"id": orders, "until": "gone"}},
		{"no until", "wait_for_database", map[string]any{"id": orders}},
		{"an expect that is not an object", "wait_for_database", map[string]any{"id": orders, "until": "ready", "expect": 3}},
		{"members that are not whole", "wait_for_database", map[string]any{"id": orders, "until": "ready", "expect": map[string]any{"members": 2.5}}},
		{"no backup id", "wait_for_backup", map[string]any{"id": orders}},
		{"no name", "wait_for_environment", map[string]any{}},
		{"a hostile database id", "wait_for_database", map[string]any{"id": "spoke-a/pds.orders-dev/credential", "until": "ready"}},
		{"a hostile backup database id", "wait_for_backup", map[string]any{"id": "../x", "backupId": "b1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := catalogAPI()

			res := callTool(t, connect(t, api, mcp.WithClock(newManualClock())), tc.tool, tc.args)

			require.True(t, res.IsError)
			assert.Equal(t, "invalid_input", structured(t, res)["kind"])
			assertNoRequest(t, api)
		})
	}
}

func TestWaitToolsPollTheAPIsRoutes(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"wait_for_database", map[string]any{"id": orders, "until": "ready"}, databasePath},
		{"wait_for_backup", map[string]any{"id": orders, "backupId": "orders-dev-1"}, backupsPath},
		{"wait_for_environment", map[string]any{"name": "spoke-a"}, environmentsPath},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			api := catalogAPI()

			res := callTool(t, connect(t, api, mcp.WithClock(newManualClock())), tc.tool, tc.args)

			require.False(t, res.IsError, text(t, res))
			sent := api.sent()
			require.Len(t, sent, 1)
			assert.Equal(t, http.MethodGet, sent[0].Method)
			assert.Equal(t, tc.want, sent[0].URI)
			assert.True(t, sent[0].HasCaller)
			assert.Empty(t, sent[0].Cookie)
		})
	}
}

func TestWaitResultsFitTheirOutputSchemas(t *testing.T) {
	clock := newManualClock()
	api := catalogAPI()
	session := connect(t, api, mcp.WithClock(clock))
	schemas := map[string]any{}
	for _, tool := range listedTools(t, session) {
		schemas[tool.Name] = tool.OutputSchema
	}

	for tool, args := range map[string]map[string]any{
		"wait_for_database":    {"id": orders, "until": "ready"},
		"wait_for_backup":      {"id": orders, "backupId": "orders-dev-1"},
		"wait_for_environment": {"name": "spoke-a"},
	} {
		res := callTool(t, session, tool, args)

		require.False(t, res.IsError, "%s: %s", tool, text(t, res))
		assert.NoError(t, validates(t, schemas[tool], structured(t, res)), tool)
		assert.NotContains(t, text(t, res), "\n", "the text is compact")
	}
}

func TestWaitToolsAnswerFromTheRealRouter(t *testing.T) {
	world := newTeamWorld(t)
	world.createDatabase(t, ann, "acme-env", "orders-dev")
	world.createDatabase(t, bob, "pay-env", "payroll")
	session := connectAs(t, world.srv, ann, world.logs.logger(), mcp.WithClock(newManualClock()))

	pending := callTool(t, session, "wait_for_database", map[string]any{"id": "acme-env/pds.orders-dev", "until": "ready", "timeoutSeconds": 10})
	environment := callTool(t, session, "wait_for_environment", map[string]any{"name": "acme-env", "timeoutSeconds": 5})
	elsewhere := callTool(t, session, "wait_for_database", map[string]any{"id": "pay-env/pds.payroll", "until": "ready"})

	require.False(t, pending.IsError, text(t, pending))
	assert.Equal(t, "in_progress", structured(t, pending)["outcome"], "nothing has published a connection for it")
	assert.Equal(t, "pending", structured(t, pending)["status"])
	assert.EqualValues(t, 10, structured(t, pending)["waitedSeconds"])
	require.False(t, environment.IsError, text(t, environment))
	assert.Equal(t, "connected", structured(t, environment)["outcome"], "the world's environment has sent a heartbeat")
	require.True(t, elsewhere.IsError, "another Team's database is not found")
	assert.Equal(t, "not_found", structured(t, elsewhere)["kind"])
}

func TestAWaitThatIsDoneAtOnceNeedsNoClock(t *testing.T) {
	api := catalogAPI()

	res := callTool(t, connect(t, api), "wait_for_database", map[string]any{"id": orders, "until": "ready"})

	require.False(t, res.IsError, text(t, res))
	assert.Equal(t, "succeeded", structured(t, res)["outcome"])
	assert.Len(t, api.sent(), 1)
}

func TestAFailedPollAfterAnAnswerIsAnErrorNotAnOutcome(t *testing.T) {
	clock := newManualClock()
	api := newFakeAPI(nil)
	api.then(databasePath,
		ok(databaseAt("pending", "", false)),
		failed(http.StatusInternalServerError, "ErrInternal", "internal detail"),
	)

	res := callTool(t, connect(t, api, mcp.WithClock(clock)), "wait_for_database", map[string]any{"id": orders, "until": "ready"})

	require.True(t, res.IsError)
	body := structured(t, res)
	assert.Equal(t, "unavailable", body["kind"])
	assert.True(t, body["retryable"].(bool))
	assert.NotContains(t, text(t, res), "internal detail")
	assert.Len(t, api.sent(), 2)
}
