package mcp_test

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
)

var readTools = []string{
	"get_activity", "get_cloud_connection", "get_database", "get_database_alerts", "get_database_history", "get_database_usage",
	"get_environment", "get_environment_history", "get_metrics", "get_support_ticket", "list_activity", "list_alerts",
	"list_backups", "list_cloud_connections", "list_databases", "list_environments", "list_support_tickets",
	"wait_for_backup", "wait_for_database", "wait_for_environment",
}

func catalogAPI() *fakeAPI {
	return newFakeAPI(map[string]reply{
		"/api/databases":                                ok(databaseListFixture()),
		"/api/databases/spoke-a/pds.orders-dev":         ok(databaseFixture()),
		"/api/databases/spoke-a/pds.orders-dev/backups": ok(backupsFixture()),
		"/api/databases/spoke-a/pds.orders-dev/metrics": ok(metricsFixture()),
		"/api/activity":                                 ok(activityFixture()),
		"/api/activity/" + activityID:                   ok(activityDetailFixture()),
		"/api/databases/spoke-a/pds.orders-dev/history": ok(historyFixture()),
		"/api/databases/spoke-a/pds.orders-dev/usage":   ok(usageFixture()),
		"/api/databases/spoke-a/pds.orders-dev/alerts":  ok(databaseAlertsFixture()),
		"/api/alerts":                       ok(alertsFixture()),
		"/api/environments/spoke-a/history": ok(environmentHistoryFixture()),
		"/api/environments":                 ok(environmentsFixture()),
		"/api/environments/orders":          ok(managedEnvironmentFixture()),
		"/api/cloud-connections":            ok(map[string]any{"cloudConnections": []any{cloudConnectionFixture()}}),
		"/api/cloud-connections/" + cloudID: ok(cloudConnectionFixture()),
		"/api/support/tickets":              ok(ticketListFixture()),
		"/api/support/tickets/" + ticket:    ok(ticketFixture()),
	})
}

func TestToolsListHasTheReadToolsInNameOrder(t *testing.T) {
	session := connect(t, catalogAPI())

	assert.Equal(t, readTools, names(listedTools(t, session)))
	assert.True(t, sort.StringsAreSorted(readTools))
}

func TestToolsListIsTheSameOnEveryConnection(t *testing.T) {
	first := names(listedTools(t, connect(t, catalogAPI())))

	for range 5 {
		assert.Equal(t, first, names(listedTools(t, connect(t, catalogAPI()))))
	}
}

func TestEveryReadToolIsMarkedReadOnlyAndClosedWorld(t *testing.T) {
	for _, tool := range listedTools(t, connect(t, catalogAPI())) {
		require.NotNil(t, tool.Annotations, tool.Name)
		assert.True(t, tool.Annotations.ReadOnlyHint, tool.Name)
		assert.True(t, tool.Annotations.IdempotentHint, tool.Name)
		require.NotNil(t, tool.Annotations.DestructiveHint, tool.Name)
		assert.False(t, *tool.Annotations.DestructiveHint, tool.Name)
		require.NotNil(t, tool.Annotations.OpenWorldHint, tool.Name)
		assert.False(t, *tool.Annotations.OpenWorldHint, tool.Name)
		assert.NotEmpty(t, tool.Title, tool.Name)
		assert.NotNil(t, tool.OutputSchema, tool.Name)
	}
}

func TestToolDescriptionsAreTheCatalogsWords(t *testing.T) {
	catalog := catalogDescriptions(t)

	for _, tool := range listedTools(t, connectAt(t, catalogAPI(), auth.AccessChangeDelete)) {
		want, found := catalog[tool.Name]
		require.True(t, found, "%s is in the catalog", tool.Name)
		assert.Equal(t, want, tool.Description, tool.Name)
	}
}

func TestToolInputsAreTheCatalogsInputs(t *testing.T) {
	want := map[string]struct {
		properties []string
		required   []string
	}{
		"list_databases":       {[]string{"environment"}, nil},
		"get_database":         {[]string{"id"}, []string{"id"}},
		"list_backups":         {[]string{"id"}, []string{"id"}},
		"get_metrics":          {[]string{"id", "includePoints", "metrics", "window"}, []string{"id"}},
		"list_activity":        {[]string{"databaseId", "limit", "since"}, nil},
		"list_environments":    {nil, nil},
		"list_support_tickets": {[]string{"limit", "offset"}, nil},
		"get_support_ticket":   {[]string{"number"}, []string{"number"}},
		"wait_for_database":    {[]string{"expect", "id", "timeoutSeconds", "until"}, []string{"id", "until"}},
		"wait_for_backup":      {[]string{"backupId", "id", "timeoutSeconds"}, []string{"id", "backupId"}},
		"wait_for_environment": {[]string{"name", "timeoutSeconds", "until"}, []string{"name"}},

		"list_cloud_connections": {nil, nil},
		"get_cloud_connection":   {[]string{"id"}, []string{"id"}},
		"get_environment":        {[]string{"name"}, []string{"name"}},

		"get_database_history":    {[]string{"cursor", "id", "kind", "limit", "since", "until"}, []string{"id"}},
		"get_database_usage":      {[]string{"id", "limit", "since", "until"}, []string{"id"}},
		"get_environment_history": {[]string{"name"}, []string{"name"}},
		"list_alerts":             {[]string{"limit"}, nil},
		"get_database_alerts":     {[]string{"id"}, []string{"id"}},
		"get_activity":            {[]string{"id"}, []string{"id"}},

		"create_database": {
			[]string{"access", "acknowledgeOpenToInternet", "cpu", "engine", "environment", "memoryGb", "name", "profile", "sourceRanges", "storageGb"},
			[]string{"name", "environment", "profile"},
		},
		"scale_database":      {[]string{"cpu", "id", "members", "memoryGb", "storageGb"}, []string{"id"}},
		"backup_database":     {[]string{"id"}, []string{"id"}},
		"set_backup_schedule": {[]string{"enabled", "hour", "id", "minute", "period", "retentionCopies", "weekday"}, []string{"id", "enabled"}},
		"restore_database":    {[]string{"backupId", "id", "name"}, []string{"id", "backupId", "name"}},
		"delete_database":     {[]string{"confirm", "id"}, []string{"id"}},

		"create_support_ticket":   {[]string{"databaseId", "description", "subject"}, []string{"subject", "description"}},
		"reply_to_support_ticket": {[]string{"body", "number"}, []string{"number", "body"}},
		"create_environment":      {[]string{"name"}, []string{"name"}},

		"start_cloud_connection":     {nil, nil},
		"create_managed_environment": {[]string{"connectionId", "name", "region"}, []string{"connectionId", "name", "region"}},
		"retry_environment_run":      {[]string{"environment"}, []string{"environment"}},
		"upgrade_environment":        {[]string{"environment", "kind"}, []string{"environment", "kind"}},
		"delete_managed_environment": {[]string{"confirm", "environment"}, []string{"environment"}},
		"rebuild_environment":        {[]string{"environment"}, []string{"environment"}},
		"remove_cloud_connection":    {[]string{"confirm", "id"}, []string{"id"}},
	}

	for _, tool := range listedTools(t, connectAt(t, catalogAPI(), auth.AccessChangeDelete)) {
		schema, ok := tool.InputSchema.(map[string]any)
		require.True(t, ok, tool.Name)
		properties, _ := schema["properties"].(map[string]any)
		var got []string
		for name := range properties {
			got = append(got, name)
		}
		sort.Strings(got)
		var required []string
		if list, ok := schema["required"].([]any); ok {
			for _, name := range list {
				required = append(required, name.(string))
			}
		}

		assert.Equal(t, want[tool.Name].properties, got, tool.Name)
		assert.Equal(t, want[tool.Name].required, required, tool.Name)
		assert.NotContains(t, schema, "default", "a default in a schema crashes the SDK on null arguments")
	}
}

func TestEachReadToolSendsTheAPIsRequest(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{"list databases", "list_databases", nil, "/api/databases"},
		{"list one environment", "list_databases", map[string]any{"environment": "spoke-a"}, "/api/databases?environment=spoke-a"},
		{"get database", "get_database", map[string]any{"id": orders}, "/api/databases/spoke-a/pds.orders-dev"},
		{"list backups", "list_backups", map[string]any{"id": orders}, "/api/databases/spoke-a/pds.orders-dev/backups"},
		{"metrics", "get_metrics", map[string]any{"id": orders}, "/api/databases/spoke-a/pds.orders-dev/metrics"},
		{
			"metrics with a window and kinds", "get_metrics",
			map[string]any{"id": orders, "window": "24h", "metrics": []any{"cpu", "disk"}},
			"/api/databases/spoke-a/pds.orders-dev/metrics?metrics=cpu%2Cdisk&window=24h",
		},
		{"activity", "list_activity", nil, "/api/activity?limit=50"},
		{
			"activity for one database", "list_activity",
			map[string]any{"databaseId": orders, "limit": 10, "since": "7"},
			"/api/activity?databaseId=spoke-a%2Fpds.orders-dev&limit=10&since=7",
		},
		{"environments", "list_environments", nil, "/api/environments"},
		{"tickets", "list_support_tickets", nil, "/api/support/tickets"},
		{"a page of tickets", "list_support_tickets", map[string]any{"limit": 10, "offset": 20}, "/api/support/tickets?limit=10&offset=20"},
		{"one ticket", "get_support_ticket", map[string]any{"number": ticket}, "/api/support/tickets/" + ticket},
		{"history", "get_database_history", map[string]any{"id": orders}, "/api/databases/spoke-a/pds.orders-dev/history?limit=20"},
		{
			"history with every filter", "get_database_history",
			map[string]any{"id": orders, "kind": "backup", "since": "2026-09-08T00:00:00Z", "until": "2026-09-09T00:00:00Z", "cursor": "00000000000000000042", "limit": 5},
			"/api/databases/spoke-a/pds.orders-dev/history?cursor=00000000000000000042&kind=backup&limit=5&since=2026-09-08T00%3A00%3A00Z&until=2026-09-09T00%3A00%3A00Z",
		},
		{"usage", "get_database_usage", map[string]any{"id": orders}, "/api/databases/spoke-a/pds.orders-dev/usage?limit=20"},
		{
			"usage in a window", "get_database_usage",
			map[string]any{"id": orders, "since": "2026-09-08T00:00:00Z", "until": "2026-09-09T00:00:00Z", "limit": 100},
			"/api/databases/spoke-a/pds.orders-dev/usage?limit=100&since=2026-09-08T00%3A00%3A00Z&until=2026-09-09T00%3A00%3A00Z",
		},
		{"environment history", "get_environment_history", map[string]any{"name": "spoke-a"}, "/api/environments/spoke-a/history"},
		{"alerts", "list_alerts", nil, "/api/alerts"},
		{"database alerts", "get_database_alerts", map[string]any{"id": orders}, "/api/databases/spoke-a/pds.orders-dev/alerts"},
		{"one activity entry", "get_activity", map[string]any{"id": activityID}, "/api/activity/" + activityID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := catalogAPI()
			session := connect(t, api)

			res := callTool(t, session, tc.tool, tc.args)

			require.False(t, res.IsError, text(t, res))
			sent := api.sent()
			require.Len(t, sent, 1)
			assert.Equal(t, http.MethodGet, sent[0].Method)
			assert.Equal(t, tc.want, sent[0].URI)
		})
	}
}

func TestEveryRequestCarriesTheCallerAndNoCredential(t *testing.T) {
	api := catalogAPI()
	session := connect(t, api)

	callTool(t, session, "list_databases", nil)
	callTool(t, session, "get_database", map[string]any{"id": orders})

	sent := api.sent()
	require.Len(t, sent, 2)
	for _, request := range sent {
		assert.True(t, request.HasCaller)
		assert.Equal(t, readCaller(), request.Caller)
		assert.Empty(t, request.Cookie)
		assert.Empty(t, request.Authorization)
		assert.Regexp(t, `^mcp-[0-9a-f]{8}$`, request.RequestID)
	}
	assert.NotEqual(t, sent[0].RequestID, sent[1].RequestID)
}

func TestEachReadResultMatchesTheCatalogShape(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"list_databases", nil, `{"databases":[{"id":"spoke-a/pds.orders-dev","name":"orders-dev","engine":"mysql","profile":"dev","status":"ready","members":1,"environment":"spoke-a"}]}`},
		{"get_database", map[string]any{"id": orders}, `{
			"id":"spoke-a/pds.orders-dev","name":"orders-dev","engine":"mysql","profile":"dev","namespace":"pds","environment":"spoke-a",
			"status":"ready","members":1,"cpu":1,"memoryGb":2,"storageGb":20,
			"createdAt":"2026-09-08T12:00:00Z","lastBackupAt":"2026-09-08T13:00:00Z",
			"backupSchedule":{"name":"daily","enabled":true,"cron":"0 2 * * *","retentionCopies":7,"period":"daily","minute":0,"hour":2},
			"connection":{"host":"orders-dev.pds.svc","port":3306,"username":"root","maskedUri":"mysql://root:••••••••@orders-dev.pds.svc:3306","secretName":"orders-dev-secret","serviceName":"orders-dev"},
			"access":{"external":false,"visibility":"cluster"}}`},
		{"list_backups", map[string]any{"id": orders}, `{"backups":[{"id":"orders-dev-1","status":"succeeded","sizeBytes":1048576,"scheduled":true,"createdAt":"2026-09-08T13:00:00Z","finishedAt":"2026-09-08T13:05:00Z"}]}`},
		{"get_metrics", map[string]any{"id": orders}, `{
			"databaseId":"spoke-a/pds.orders-dev","window":"1h","start":"2026-09-08T11:00:00Z","end":"2026-09-08T12:00:00Z","stepSeconds":60,"pending":false,
			"metrics":[{"kind":"cpu","unit":"cores","pending":false,"current":0.4,"min":0.1,"max":0.9,"average":0.5,"trend":"steady"}]}`},
		{"get_metrics", map[string]any{"id": orders, "includePoints": true}, `{
			"databaseId":"spoke-a/pds.orders-dev","window":"1h","start":"2026-09-08T11:00:00Z","end":"2026-09-08T12:00:00Z","stepSeconds":60,"pending":false,
			"metrics":[{"kind":"cpu","unit":"cores","pending":false,"current":0.4,"min":0.1,"max":0.9,"average":0.5,"trend":"steady","points":[{"t":"2026-09-08T11:00:00Z","v":0.4}]}]}`},
		{"list_activity", nil, `{"entries":[{"id":"7","at":"2026-09-08T12:00:00Z","actor":"ann","requestId":"mcp-0a1b2c3d","action":"database.create","databaseId":"spoke-a/pds.orders-dev","target":"orders-dev","outcome":"succeeded","detail":"mysql, dev","resourceKind":"database","resourceName":"orders-dev","client":"mcp","agent":"Claude Code"}],"cursor":"7","truncated":false}`},
		{"list_environments", nil, `{"environments":[{"name":"spoke-a","namespace":"pds","status":"connected","lastSeen":"2026-09-08T12:00:00Z","origin":"connected","state":"ready"}]}`},
		{"get_environment", map[string]any{"name": "orders"}, `{"name":"orders","status":"pending","origin":"managed","state":"failed","connectionId":"0123456789ab","region":"eu-west-1",
			"provisioning":{"run":{"id":5,"kind":"create","status":"running","startedAt":"2026-09-08T12:00:00Z"},"progress":"failed","phases":[
			{"name":"Creating network and Kubernetes cluster","status":"failed"},{"name":"Connecting to the environment","status":"pending"},
			{"name":"Installing PDS","status":"pending"},{"name":"Finishing","status":"pending"}],
			"failure":{"sentence":"AWS account 123456789012 has reached the quota for Elastic IP addresses per Region in eu-west-1. Raise the quota in AWS, then retry.","action":"retry",
			"refusal":{"reason":"quota","service":"Amazon EC2","item":"Elastic IP addresses per Region","region":"eu-west-1"}}}}`},
		{"list_cloud_connections", nil, `{"cloudConnections":[{"id":"0123456789ab","accountId":"123456789012","alias":"acme","state":"ready","allowedRegions":["eu-west-1"],"environments":[]}]}`},
		{"get_cloud_connection", map[string]any{"id": cloudID}, `{"id":"0123456789ab","accountId":"123456789012","alias":"acme","state":"ready","allowedRegions":["eu-west-1"],"environments":[]}`},
		{"list_support_tickets", nil, `{"tickets":[{"number":"CS0061234","state":"Open","priority":"3","subject":"Orders is slow","openedAt":"2026-09-08T12:00:00Z","updatedAt":"2026-09-08T12:30:00Z"}],"portalUrl":"https://support.example/portal"}`},
		{"get_database_history", map[string]any{"id": orders}, `{"entries":[
			{"id":"00000000000000000009","at":"2026-09-08T13:05:00Z","kind":"backup","outcome":"succeeded","objectId":"orders-dev-1","sizeBytes":1048576,"durationMs":300000},
			{"id":"00000000000000000008","at":"2026-09-08T12:30:00Z","kind":"transition","fromPhase":"pending","toPhase":"ready","message":"Ready."},
			{"id":"00000000000000000007","at":"2026-09-08T12:00:00Z","kind":"action","actor":"ann","client":"mcp","agent":"Claude Code","action":"database.create","target":"orders-dev","outcome":"accepted","detail":"mysql, dev"}],
			"cursor":"00000000000000000007","truncated":true}`},
		{"get_database_usage", map[string]any{"id": orders}, `{"samples":[
			{"at":"2026-09-08T12:05:00Z","cpuAvg":0.25,"cpuMax":0.5,"memoryAvg":536870912,"memoryMax":671088640,"storageBytes":21474836480},
			{"at":"2026-09-08T12:00:00Z","storageBytes":21474836480,"metricsReason":"metrics-server did not answer"}]}`},
		{"get_environment_history", map[string]any{"name": "spoke-a"}, `{"runs":[{"runId":"run-1","phases":[
			{"phase":"preflight","outcome":"ok","startedAt":"2026-09-08T11:00:00Z","finishedAt":"2026-09-08T11:00:02Z","durationMs":2000},
			{"phase":"install","outcome":"failed","startedAt":"2026-09-08T11:00:02Z","finishedAt":"2026-09-08T11:01:00Z","durationMs":58000,"output":"the release did not become ready"}]}]}`},
		{"list_alerts", nil, `{"environments":[
			{"name":"spoke-a","state":"live","readAt":"2026-09-08T12:00:00Z","total":2,"shown":2},
			{"name":"spoke-b","state":"off","reason":"Monitoring is not set up on this environment.","total":0,"shown":0}],
			"alerts":[
			{"id":"spoke-a/f1","environment":"spoke-a","databaseId":"spoke-a/pds.orders-dev","rule":"service_down","severity":"critical","summary":"orders-dev is down","startsAt":"2026-09-08T11:55:00Z"},
			{"id":"spoke-a/f2","environment":"spoke-a","rule":"disk_high","severity":"warning","summary":"Disk is 90% full","startsAt":"2026-09-08T11:00:00Z"}],
			"criticals":1,"total":2,"truncated":false}`},
		{"get_database_alerts", map[string]any{"id": orders}, `{"databaseId":"spoke-a/pds.orders-dev",
			"environment":{"name":"spoke-a","state":"live","readAt":"2026-09-08T12:00:00Z","total":2,"shown":2},
			"alerts":[{"id":"spoke-a/f1","environment":"spoke-a","databaseId":"spoke-a/pds.orders-dev","rule":"service_down","severity":"critical","summary":"orders-dev is down","startsAt":"2026-09-08T11:55:00Z"}]}`},
		{"get_activity", map[string]any{"id": activityID}, `{
			"event":{"id":"00000000000000000007","at":"2026-09-08T12:00:00Z","actor":"ann","requestId":"mcp-0a1b2c3d","action":"database.create","databaseId":"spoke-a/pds.orders-dev","target":"orders-dev","outcome":"accepted","detail":"mysql, dev","resourceKind":"database","resourceName":"orders-dev","client":"mcp","agent":"Claude Code"},
			"resource":{"kind":"database","name":"orders-dev","namespace":"pds","state":"ready","startedAt":"2026-09-08T12:00:01Z","endedAt":"2026-09-08T12:30:00Z"},
			"timeline":[
			{"at":"2026-09-08T12:00:00Z","source":"request","type":"accepted","message":"database.create"},
			{"at":"2026-09-08T12:10:00Z","source":"kubernetes","type":"Normal","reason":"Created","message":"Created pod orders-dev-0","count":1}],
			"notes":["The database is ready."],"truncated":false}`},
		{"get_support_ticket", map[string]any{"number": ticket}, `{"number":"CS0061234","state":"Open","priority":"3","subject":"Orders is slow","openedAt":"2026-09-08T12:00:00Z","updatedAt":"2026-09-08T12:30:00Z","description":"It stalls at noon.","comments":[{"at":"2026-09-08T12:10:00Z","author":"Percona","label":"Support","body":"Looking."}],"portalUrl":"https://support.example/portal/CS0061234"}`},
	}

	session := connect(t, catalogAPI())
	schemas := map[string]any{}
	for _, tool := range listedTools(t, session) {
		schemas[tool.Name] = tool.OutputSchema
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			res := callTool(t, session, tc.tool, tc.args)

			require.False(t, res.IsError, text(t, res))
			assert.JSONEq(t, tc.want, text(t, res), "the text block is the compact JSON")
			assert.NoError(t, validates(t, schemas[tc.tool], structured(t, res)), "structured content fits the output schema")
			assert.Equal(t, strings.TrimSpace(text(t, res)), text(t, res), "the text is compact, not pretty printed")
			assert.NotContains(t, text(t, res), "\n")
		})
	}
}

func TestAnEmptyListIsAnEmptyListAndNeverNull(t *testing.T) {
	api := newFakeAPI(map[string]reply{
		"/api/databases":       ok(map[string]any{"databases": nil}),
		"/api/environments":    ok(map[string]any{"hub": true}),
		"/api/support/tickets": ok(map[string]any{}),
		"/api/activity":        ok(map[string]any{"cursor": "", "truncated": false}),

		"/api/databases/spoke-a/pds.orders-dev/history": ok(map[string]any{"truncated": false}),
		"/api/databases/spoke-a/pds.orders-dev/usage":   ok(map[string]any{}),
		"/api/databases/spoke-a/pds.orders-dev/alerts":  ok(map[string]any{"databaseId": orders}),
		"/api/environments/spoke-a/history":             ok(map[string]any{}),
		"/api/alerts":                                   ok(map[string]any{}),
		"/api/activity/" + activityID:                   ok(map[string]any{}),
	})
	session := connect(t, api)

	for tool, call := range map[string]struct {
		args map[string]any
		key  string
	}{
		"get_database_history":    {map[string]any{"id": orders}, "entries"},
		"get_database_usage":      {map[string]any{"id": orders}, "samples"},
		"get_database_alerts":     {map[string]any{"id": orders}, "alerts"},
		"get_environment_history": {map[string]any{"name": "spoke-a"}, "runs"},
		"list_alerts":             {nil, "alerts"},
		"get_activity":            {map[string]any{"id": activityID}, "timeline"},
	} {
		res := callTool(t, session, tool, call.args)

		require.False(t, res.IsError, tool)
		assert.Equal(t, []any{}, structured(t, res)[call.key], tool)
	}

	for tool, key := range map[string]string{
		"list_databases": "databases", "list_environments": "environments",
		"list_support_tickets": "tickets", "list_activity": "entries",
	} {
		res := callTool(t, session, tool, nil)

		require.False(t, res.IsError, tool)
		assert.Equal(t, []any{}, structured(t, res)[key], tool)
	}
}

func TestAHostileIDGivesInvalidInputAndSendsNoRequest(t *testing.T) {
	ids := []string{
		"", "pds.orders-dev", "spoke-a/orders-dev", "../environments/x", "spoke-a/pds.orders-dev/credential",
		"spoke-a/pds.orders-dev/../x.y", "spoke-a/pds.orders-dev?team=all", "spoke-a/pds.orders-dev#x",
		"spoke-a/pds.orders-dev\n", "Spoke-A/pds.orders-dev", "spoke-a//pds.orders-dev", "%2e%2e/x.y",
		"/spoke-a/pds.orders-dev", "spoke-a/pds.orders-dev/", "spoke-a/pds.orders.dev", "spoke-a/pds.orders dev",
	}
	tools := []struct {
		tool string
		key  string
	}{
		{"get_database", "id"}, {"list_backups", "id"}, {"get_metrics", "id"}, {"list_activity", "databaseId"},
		{"get_database_history", "id"}, {"get_database_usage", "id"}, {"get_database_alerts", "id"},
	}
	for _, tool := range tools {
		for _, id := range ids {
			if tool.tool == "list_activity" && id == "" {
				continue // no database named is the whole log
			}
			t.Run(tool.tool+" "+id, func(t *testing.T) {
				api := catalogAPI()
				session := connect(t, api)

				res := callTool(t, session, tool.tool, map[string]any{tool.key: id})

				require.True(t, res.IsError)
				body := structured(t, res)
				assert.Equal(t, "invalid_input", body["kind"])
				assert.False(t, body["retryable"].(bool))
				assert.NotContains(t, body, "requestId", "no request was made")
				assertNoRequest(t, api)
			})
		}
	}
}

func TestAOneSegmentIDGetsTheDatabaseIDSentence(t *testing.T) {
	session := connect(t, catalogAPI())

	res := callTool(t, session, "get_database", map[string]any{"id": "pds.orders-dev"})

	require.True(t, res.IsError)
	assert.Equal(t, "That is not a database id. Copy an id exactly as list_databases returns it.", structured(t, res)["message"])
}

func TestArgumentsThatBreakTheSchemaAreRefusedWithoutEchoingThem(t *testing.T) {
	const sentinel = "sentinel-value-the-model-sent"
	cases := []struct {
		name string
		tool string
		args map[string]any
		hint string
	}{
		{"a missing id", "get_database", map[string]any{}, ""},
		{"an id that is a number", "get_database", map[string]any{"id": 5}, "Check id."},
		{"a window outside the enum", "get_metrics", map[string]any{"id": orders, "window": sentinel}, "Check window."},
		{"a metric outside the enum", "get_metrics", map[string]any{"id": orders, "metrics": []any{sentinel}}, "Check metrics."},
		{"a limit of zero", "list_activity", map[string]any{"limit": 0}, "Check limit."},
		{"a limit above the maximum", "list_activity", map[string]any{"limit": 501}, "Check limit."},
		{"a limit that is not whole", "list_activity", map[string]any{"limit": 1.5}, "Check limit."},
		{"a ticket limit above the maximum", "list_support_tickets", map[string]any{"limit": 101}, "Check limit."},
		{"a negative offset", "list_support_tickets", map[string]any{"offset": -1}, "Check offset."},
		{"a ticket number with a slash", "get_support_ticket", map[string]any{"number": "../" + sentinel}, "Check number."},
		{"a ticket number that is too short", "get_support_ticket", map[string]any{"number": "A"}, "Check number."},
		{"a ticket number with a space", "get_support_ticket", map[string]any{"number": "CS 1"}, "Check number."},
		{"a history kind outside the enum", "get_database_history", map[string]any{"id": orders, "kind": sentinel}, "Check kind."},
		{"a history limit of zero", "get_database_history", map[string]any{"id": orders, "limit": 0}, "Check limit."},
		{"a history limit above the maximum", "get_database_history", map[string]any{"id": orders, "limit": 101}, "Check limit."},
		{"a usage limit above the maximum", "get_database_usage", map[string]any{"id": orders, "limit": 101}, "Check limit."},
		{"an alerts limit of zero", "list_alerts", map[string]any{"limit": 0}, "Check limit."},
		{"an alerts limit above the maximum", "list_alerts", map[string]any{"limit": 101}, "Check limit."},
		{"an activity id with a slash", "get_activity", map[string]any{"id": "../" + sentinel}, "Check id."},
		{"an activity id that is empty", "get_activity", map[string]any{"id": ""}, "Check id."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := catalogAPI()
			session := connect(t, api)

			res := callTool(t, session, tc.tool, tc.args)

			require.True(t, res.IsError)
			assert.NotContains(t, text(t, res), sentinel)
			body := structured(t, res)
			assert.Equal(t, "invalid_input", body["kind"])
			assert.Equal(t, "The input is not valid. Check it against the tool's input schema.", body["message"])
			if tc.hint != "" {
				assert.Equal(t, tc.hint, body["hint"])
			}
			assertNoRequest(t, api)
		})
	}
}

func TestAnUnknownArgumentIsIgnored(t *testing.T) {
	session := connect(t, catalogAPI())

	res := callTool(t, session, "list_environments", map[string]any{"environment": "spoke-a"})

	assert.False(t, res.IsError, text(t, res))
}

func TestAnAPIRefusalKeepsItsKindAndCopy(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		code      string
		message   string
		kind      string
		want      string
		retryable bool
		hint      string
	}{
		{"a 400", 400, "ErrValidation", "The limit has to be a whole number above zero.", "invalid_input", "The limit has to be a whole number above zero.", false, ""},
		{"a 400 with no message", 400, "ErrValidation", "", "invalid_input", "That request was not accepted. Check the inputs and try again.", false, ""},
		{"a 404", 404, "ErrNotFound", "That database does not exist.", "not_found", "That database does not exist.", false, "Call list_databases for the current ids."},
		{"a 409", 409, "ErrConflict", "That database is busy.", "conflict", "That database is busy.", false, ""},
		{"a 501", 501, "ErrNotImplemented", "Backups are not set up for this environment.", "not_available", "Backups are not set up for this environment.", false, ""},
		{"a 500", 500, "ErrInternal", "Something went wrong on our side. Try again.", "unavailable", "Something went wrong on the control plane. Try again.", true, ""},
		{"a 502 that names a part", 502, "ErrDownstream", "Everest did not answer.", "unavailable", "The control plane could not complete that request. Try again in a moment.", true, ""},
		{"a 401", 401, "ErrValidation", "Sign in to continue.", "unavailable", "The control plane refused that request.", false, ""},
		{"a 403 keeps the API's sentence", 403, "ErrForbidden", "Only an organization admin can do that.", "not_available", "Only an organization admin can do that.", false, ""},
		{"a 503", 503, "ErrDownstream", "", "unavailable", "The control plane could not complete that request. Try again in a moment.", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeAPI(map[string]reply{"/api/databases/spoke-a/pds.orders-dev": failed(tc.status, tc.code, tc.message)})
			session := connect(t, api)

			res := callTool(t, session, "get_database", map[string]any{"id": orders})

			require.True(t, res.IsError)
			body := structured(t, res)
			assert.Equal(t, tc.kind, body["kind"])
			assert.Equal(t, tc.want, body["message"])
			assert.Equal(t, tc.retryable, body["retryable"])
			if tc.hint == "" {
				assert.NotContains(t, body, "hint")
			} else {
				assert.Equal(t, tc.hint, body["hint"])
			}
			assert.JSONEq(t, text(t, res), mustJSON(t, body), "the text is the same envelope")
			sent := api.sent()
			require.Len(t, sent, 1)
			assert.Equal(t, sent[0].RequestID, body["requestId"], "the error quotes the request that was sent")
		})
	}
}

func TestAnAnswerThatIsNotJSONIsUnavailable(t *testing.T) {
	api := newFakeAPI(map[string]reply{"/api/databases/spoke-a/pds.orders-dev": ok("not an object")})
	session := connect(t, api)

	res := callTool(t, session, "get_database", map[string]any{"id": orders})

	require.True(t, res.IsError)
	body := structured(t, res)
	assert.Equal(t, "unavailable", body["kind"])
	assert.Equal(t, "The control plane sent an answer this tool could not read. Try again in a moment.", body["message"])
	assert.Equal(t, true, body["retryable"])
}

func TestARequestThatTakesTooLongTimesOut(t *testing.T) {
	api := catalogAPI()
	api.hook = func(r *http.Request) { <-r.Context().Done() }
	session := connect(t, api, withShortTimeout())

	res := callTool(t, session, "list_databases", nil)

	require.True(t, res.IsError)
	body := structured(t, res)
	assert.Equal(t, "timeout", body["kind"])
	assert.Equal(t, "The control plane took too long to answer. Try again in a moment.", body["message"])
	assert.Equal(t, true, body["retryable"])
}

func TestAPanicInARequestIsAnsweredAndTheServerStaysUp(t *testing.T) {
	api := catalogAPI()
	api.hook = func(r *http.Request) {
		if r.URL.Path == "/api/databases" {
			panic("boom")
		}
	}
	session := connect(t, api)

	res := callTool(t, session, "list_databases", nil)

	require.True(t, res.IsError)
	body := structured(t, res)
	assert.Equal(t, "unavailable", body["kind"])
	assert.Equal(t, "Something went wrong on the control plane. Try again.", body["message"])
	assert.NotContains(t, text(t, res), "boom")

	again := callTool(t, session, "list_environments", nil)
	assert.False(t, again.IsError, "the next call is served")
}

// catalogDescriptions reads each tool's description out of the catalog, so the
// registry and the document cannot drift apart.
func catalogDescriptions(t *testing.T) map[string]string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	path := filepath.Join(filepath.Dir(file), "..", "..", "..", "docs", "mcp-server-tool-catalog.md")
	body, err := os.ReadFile(path)
	require.NoError(t, err)

	heading := regexp.MustCompile(`^### ([a-z_]+)`)
	description := regexp.MustCompile(`^- \*\*Description:\*\* (.+)$`)
	out := map[string]string{}
	current := ""
	for _, line := range strings.Split(string(body), "\n") {
		if match := heading.FindStringSubmatch(line); match != nil {
			current = match[1]

			continue
		}
		if match := description.FindStringSubmatch(line); match != nil && current != "" {
			out[current] = match[1]
		}
	}
	require.NotEmpty(t, out)

	return out
}
