package mcp_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnEnvironmentNameOfTheWrongShapeIsRefusedWithNoRequest(t *testing.T) {
	names := []string{
		"", "Spoke-A", "spoke_a", "-spoke", "spoke-", "spoke a", "spoke/a", "../spoke-a", "spoke-a/history",
		"spoke-a?team=all", "spoke-a#x", "spoke-a\n", "%2e%2e", strings.Repeat("a", 64), "connect", "new",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			api := catalogAPI()
			session := connect(t, api)

			res := callTool(t, session, "get_environment_history", map[string]any{"name": name})

			require.True(t, res.IsError)
			body := structured(t, res)
			assert.Equal(t, "invalid_input", body["kind"])
			assert.Equal(t, "That is not an environment name. Copy a name exactly as list_environments returns it.", body["message"])
			assert.NotContains(t, body, "requestId", "no request was made")
			assertNoRequest(t, api)
		})
	}
}

func alertRows(count int) map[string]any {
	alerts := make([]any, 0, count)
	for i := range count {
		severity := "warning"
		if i < 2 {
			severity = "critical"
		}
		alerts = append(alerts, alertFixture(fmt.Sprintf("f%02d", i), "rule", severity, fmt.Sprintf("Alert %d", i), "2026-09-08T11:00:00Z", ""))
	}

	return map[string]any{
		"environments": []any{map[string]any{"name": "spoke-a", "state": "live", "total": count, "shown": count}},
		"alerts":       alerts, "criticals": 2,
	}
}

func alertIDs(t *testing.T, res map[string]any) []string {
	t.Helper()

	var ids []string
	for _, row := range res["alerts"].([]any) {
		ids = append(ids, row.(map[string]any)["id"].(string))
	}

	return ids
}

func TestListAlertsReturnsAtMostItsLimitInTheRoutesOrder(t *testing.T) {
	cases := []struct {
		name      string
		args      map[string]any
		want      int
		truncated bool
	}{
		{"the limit is the default of 25", nil, 25, true},
		{"fewer alerts than the limit", map[string]any{"limit": 100}, 30, false},
		{"exactly the limit", map[string]any{"limit": 30}, 30, false},
		{"a small limit", map[string]any{"limit": 3}, 3, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI(map[string]reply{"/api/alerts": ok(alertRows(30))})
			session := connect(t, api)

			res := callTool(t, session, "list_alerts", tc.args)

			require.False(t, res.IsError, text(t, res))
			body := structured(t, res)
			ids := alertIDs(t, body)
			require.Len(t, ids, tc.want)
			for i, id := range ids {
				assert.Equal(t, fmt.Sprintf("spoke-a/f%02d", i), id, "the route's order, with its most severe first")
			}
			assert.Equal(t, tc.truncated, body["truncated"])
			assert.EqualValues(t, 30, body["total"], "the total is the route's, before the limit")
			assert.EqualValues(t, 2, body["criticals"])
			sent := api.sent()
			require.Len(t, sent, 1)
			assert.Equal(t, "/api/alerts", sent[0].URI, "the route takes no limit, so none is sent")
		})
	}
}

func TestAnEnvironmentThatCannotBeReadKeepsItsStateAndReason(t *testing.T) {
	session := connect(t, catalogAPI())

	res := callTool(t, session, "list_alerts", nil)

	environments := structured(t, res)["environments"].([]any)
	off := environments[1].(map[string]any)
	assert.Equal(t, "off", off["state"])
	assert.Equal(t, "Monitoring is not set up on this environment.", off["reason"])
	assert.NotContains(t, off, "action", "a console link means nothing to an agent")
	assert.NotContains(t, off, "readAt", "an environment that never reported has no time")
}

func TestAJoinStepLogIsClippedToItsEnd(t *testing.T) {
	long := strings.Repeat("x", 600) + "THE-END"
	runs := environmentHistoryFixture()
	steps := runs["runs"].([]any)[0].(map[string]any)["phases"].([]any)
	steps[1].(map[string]any)["output"] = "  " + long + "\n"
	steps[0].(map[string]any)["output"] = "é" + strings.Repeat("é", 700)
	session := connect(t, newFakeAPI(map[string]reply{"/api/environments/spoke-a/history": ok(runs)}))

	res := callTool(t, session, "get_environment_history", map[string]any{"name": "spoke-a"})

	require.False(t, res.IsError, text(t, res))
	phases := structured(t, res)["runs"].([]any)[0].(map[string]any)["phases"].([]any)
	last := phases[1].(map[string]any)["output"].(string)
	assert.Len(t, last, 500)
	assert.True(t, strings.HasSuffix(last, "THE-END"))
	assert.Equal(t, strings.Repeat("é", 500), phases[0].(map[string]any)["output"], "cut on a character, never inside one")
}

func TestTheHistoryAndUsageToolsAskForTheirDefaultLimit(t *testing.T) {
	for tool, path := range map[string]string{
		"get_database_history": "/api/databases/spoke-a/pds.orders-dev/history?limit=20",
		"get_database_usage":   "/api/databases/spoke-a/pds.orders-dev/usage?limit=20",
	} {
		api := catalogAPI()

		callTool(t, connect(t, api), tool, map[string]any{"id": orders})

		require.Len(t, api.sent(), 1, tool)
		assert.Equal(t, path, api.sent()[0].URI, tool)
	}
}

func TestTheNewToolsPassTheAPIsRefusalOn(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
		path string
		fail reply
		kind string
		want string
	}{
		{
			"history that is not available", "get_database_history", map[string]any{"id": orders},
			"/api/databases/spoke-a/pds.orders-dev/history",
			failed(501, "ErrNotImplemented", "History is not available."), "not_available", "History is not available.",
		},
		{
			"a time that is not RFC 3339", "get_database_usage", map[string]any{"id": orders, "since": "yesterday"},
			"/api/databases/spoke-a/pds.orders-dev/usage",
			failed(400, "ErrValidation", "since and until must be RFC3339 timestamps."), "invalid_input", "since and until must be RFC3339 timestamps.",
		},
		{
			"an entry that aged out", "get_activity", map[string]any{"id": activityID},
			"/api/activity/" + activityID,
			failed(404, "ErrNotFound", "That activity entry is not in the log."), "not_found", "That activity entry is not in the log.",
		},
		{
			"an environment the caller cannot see", "get_environment_history", map[string]any{"name": "spoke-z"},
			"/api/environments/spoke-z/history",
			failed(404, "ErrNotFound", "That environment does not exist."), "not_found", "That environment does not exist.",
		},
		{
			"alerts that cannot be read", "list_alerts", nil,
			"/api/alerts",
			failed(502, "ErrDownstream", "PMM did not answer."), "unavailable", "The control plane could not complete that request. Try again in a moment.",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			session := connect(t, newFakeAPI(map[string]reply{tc.path: tc.fail}))

			res := callTool(t, session, tc.tool, tc.args)

			require.True(t, res.IsError)
			body := structured(t, res)
			assert.Equal(t, tc.kind, body["kind"])
			assert.Equal(t, tc.want, body["message"])
		})
	}
}

func TestANotFoundHintNamesTheToolThatListsWhatWasGiven(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		path string
		hint string
	}{
		{"get_environment_history", map[string]any{"name": "spoke-z"}, "/api/environments/spoke-z/history", "Call list_environments for the current names."},
		{"get_activity", map[string]any{"id": activityID}, "/api/activity/" + activityID, "Call list_activity for the current ids."},
		{"get_database_history", map[string]any{"id": orders}, "/api/databases/spoke-a/pds.orders-dev/history", "Call list_databases for the current ids."},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			session := connect(t, newFakeAPI(map[string]reply{tc.path: failed(404, "ErrNotFound", "Not there.")}))

			res := callTool(t, session, tc.tool, tc.args)

			require.True(t, res.IsError)
			assert.Equal(t, tc.hint, structured(t, res)["hint"])
		})
	}
}

func TestAJoinStepThatHasNotFinishedHasNoFinishTime(t *testing.T) {
	runs := environmentHistoryFixture()
	step := runs["runs"].([]any)[0].(map[string]any)["phases"].([]any)[1].(map[string]any)
	step["finishedAt"], step["startedAt"] = "0001-01-01T00:00:00Z", "0001-01-01T00:00:00Z"
	session := connect(t, newFakeAPI(map[string]reply{"/api/environments/spoke-a/history": ok(runs)}))

	res := callTool(t, session, "get_environment_history", map[string]any{"name": "spoke-a"})

	require.False(t, res.IsError, text(t, res))
	phases := structured(t, res)["runs"].([]any)[0].(map[string]any)["phases"].([]any)
	assert.NotContains(t, phases[1], "finishedAt")
	assert.NotContains(t, phases[1], "startedAt")
	assert.Contains(t, phases[0], "finishedAt", "a recorded time is kept")
}
