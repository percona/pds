package mcp_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/mcp"
)

var changeTools = []string{
	"backup_database", "create_database", "create_environment", "create_support_ticket", "reply_to_support_ticket",
	"restore_database", "scale_database", "set_backup_schedule",
}

const deleteTool = "delete_database"

// callerAt is readCaller with another tier.
func callerAt(access auth.Access) auth.Caller {
	caller := readCaller()
	caller.Access = access

	return caller
}

func connectAt(t *testing.T, api http.Handler, access auth.Access, opts ...mcp.Option) *mcp.Session {
	t.Helper()

	return connectAs(t, api, callerAt(access), discardLogger(), opts...)
}

func sorted(lists ...[]string) []string {
	out := slices.Concat(lists...)
	slices.Sort(out)

	return out
}

// changeAPI answers what the change routes answer, and the reads some of them
// make first.
func changeAPI() *fakeAPI {
	database := databaseFixture()
	database["backupSchedule"] = map[string]any{
		"name": "daily", "enabled": true, "cron": "30 3 * * *", "retentionCopies": 5, "period": "daily", "minute": 30, "hour": 3,
	}
	api := catalogAPI()
	api.routes["/api/databases"] = reply{http.StatusCreated, map[string]any{"id": orders, "name": "orders-dev", "status": "pending"}}
	api.routes[databasePath+"/scale"] = ok(databaseFixture())
	api.routes[databasePath+"/backup"] = reply{http.StatusAccepted, map[string]any{"id": "orders-dev-2", "status": "pending", "sizeBytes": 0, "scheduled": false}}
	api.routes[databasePath+"/backup-schedule"] = ok(database)
	api.routes[databasePath+"/restore"] = reply{http.StatusAccepted, map[string]any{
		"status": "accepted", "environment": "spoke-a", "name": "orders-copy", "id": "spoke-a/pds.orders-copy",
	}}
	api.then(databasePath, ok(databaseFixture()), reply{http.StatusNoContent, nil})
	api.routes["/api/support/tickets"] = reply{http.StatusCreated, ticketFixture()}
	api.routes["/api/support/tickets/"+ticket+"/replies"] = reply{http.StatusNoContent, nil}
	api.routes["/api/connect/tokens"] = reply{http.StatusCreated, map[string]any{"environment": "spoke-b", "expiresAt": "2026-09-08T13:00:00Z"}}

	return api
}

// validCalls is one working call of every change tool.
func validCalls() map[string]map[string]any {
	return map[string]map[string]any{
		"create_database":         {"name": "orders-dev", "environment": "spoke-a", "profile": "dev"},
		"scale_database":          {"id": orders, "cpu": 2, "memoryGb": 4},
		"backup_database":         {"id": orders},
		"set_backup_schedule":     {"id": orders, "enabled": true, "period": "daily", "hour": 3, "minute": 30, "retentionCopies": 5},
		"restore_database":        {"id": orders, "backupId": "orders-dev-1", "name": "orders-copy"},
		"create_support_ticket":   {"subject": "Orders is slow", "description": "It stalls at noon."},
		"reply_to_support_ticket": {"number": ticket, "body": "It is still slow."},
		"create_environment":      {"name": "spoke-b"},
		deleteTool:                {"id": orders, "confirm": "orders-dev"},
	}
}

func TestAReadTierListsNoChangeTool(t *testing.T) {
	session := connectAt(t, changeAPI(), auth.AccessRead)

	assert.Equal(t, readTools, names(listedTools(t, session)))
}

func TestAChangeTierListsTheEightChangeToolsAndNotDelete(t *testing.T) {
	session := connectAt(t, changeAPI(), auth.AccessChange)

	listed := names(listedTools(t, session))

	assert.Equal(t, sorted(readTools, changeTools), listed)
	assert.Len(t, listed, 25)
	assert.NotContains(t, listed, deleteTool)
}

func TestAChangeAndDeleteTierListsEveryTool(t *testing.T) {
	session := connectAt(t, changeAPI(), auth.AccessChangeDelete)

	listed := names(listedTools(t, session))

	assert.Equal(t, sorted(readTools, changeTools, []string{deleteTool}), listed)
	assert.Len(t, listed, 26)
}

func TestACallerWhoseClientIsNotKnownListsNoChangeTool(t *testing.T) {
	for _, kind := range []string{"", "browser"} {
		caller := callerAt(auth.AccessChangeDelete)
		caller.Identity.Client = auth.Client{Kind: kind}

		session := connectAs(t, changeAPI(), caller, discardLogger())

		assert.Equal(t, readTools, names(listedTools(t, session)), "kind %q", kind)
	}
}

func TestAChatCallerListsTheChangeTools(t *testing.T) {
	caller := callerAt(auth.AccessChange)
	caller.Identity.Client = auth.Client{Kind: auth.ClientChat}
	caller.Agent = "Anthropic"

	session := connectAs(t, changeAPI(), caller, discardLogger())

	assert.Equal(t, sorted(readTools, changeTools), names(listedTools(t, session)))
}

func TestAToolAboveTheTierIsRefusedAsNotAllowedWithNoRequest(t *testing.T) {
	cases := []struct {
		access auth.Access
		tools  []string
		want   string
	}{
		{auth.AccessRead, append([]string{deleteTool}, changeTools...), "Changes are not allowed in this session. Tell the user, and do not try again."},
		{auth.AccessChange, []string{deleteTool}, "Deleting is not allowed in this session. Tell the user, and do not try again."},
	}
	calls := validCalls()
	for _, tc := range cases {
		for _, tool := range tc.tools {
			t.Run(string(tc.access)+" "+tool, func(t *testing.T) {
				api := changeAPI()
				session := connectAt(t, api, tc.access)

				res := callTool(t, session, tool, calls[tool])

				require.True(t, res.IsError)
				body := structured(t, res)
				assert.Equal(t, "not_allowed", body["kind"])
				assert.Equal(t, tc.want, body["message"])
				assert.Equal(t, false, body["retryable"])
				assert.NotContains(t, body, "requestId", "no request was made")
				assert.JSONEq(t, text(t, res), mustJSON(t, body))
				assertNoRequest(t, api)
			})
		}
	}
}

func TestACallerWhoseClientIsNotKnownIsRefusedAChangeToo(t *testing.T) {
	caller := callerAt(auth.AccessChangeDelete)
	caller.Identity.Client = auth.Client{}
	api := changeAPI()
	session := connectAs(t, api, caller, discardLogger())

	res := callTool(t, session, "backup_database", map[string]any{"id": orders})

	require.True(t, res.IsError)
	assert.Equal(t, "not_allowed", structured(t, res)["kind"])
	assertNoRequest(t, api)
}

func TestAToolWithinTheTierIsStillServed(t *testing.T) {
	api := changeAPI()
	session := connectAt(t, api, auth.AccessChange)

	res := callTool(t, session, "backup_database", map[string]any{"id": orders})

	assert.False(t, res.IsError, text(t, res))
	assert.Len(t, api.sent(), 1)
}

func TestAToolThatDoesNotExistIsNotAnsweredAsNotAllowed(t *testing.T) {
	session := connectAt(t, changeAPI(), auth.AccessRead)

	_, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "drop_database"})

	assert.Error(t, err)
}

func TestEachChangeToolSendsTheAPIsRequest(t *testing.T) {
	cases := []struct {
		tool   string
		args   map[string]any
		method string
		uri    string
		body   string
	}{
		{
			"create_database", map[string]any{"name": "orders-dev", "environment": "spoke-a", "profile": "dev"},
			http.MethodPost, "/api/databases", `{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a"}`,
		},
		{
			"create_database", map[string]any{
				"name": "pg", "engine": "postgresql", "environment": "spoke-a", "profile": "prod", "cpu": 8, "memoryGb": 32, "storageGb": 500,
				"access": "public", "sourceRanges": []any{"203.0.113.0/24"},
			},
			http.MethodPost, "/api/databases",
			`{"name":"pg","engine":"postgresql","profile":"prod","environment":"spoke-a","cpu":8,"memory":32,"storage":500,"access":"public","sourceRanges":["203.0.113.0/24"]}`,
		},
		{
			"scale_database", map[string]any{"id": orders, "cpu": 2, "memoryGb": 4, "storageGb": 40, "members": 3},
			http.MethodPost, databasePath + "/scale", `{"cpu":2,"memory":4,"storage":40,"replicas":3}`,
		},
		{"scale_database", map[string]any{"id": orders, "members": 3}, http.MethodPost, databasePath + "/scale", `{"replicas":3}`},
		{"backup_database", map[string]any{"id": orders}, http.MethodPost, databasePath + "/backup", ``},
		{
			"set_backup_schedule", map[string]any{"id": orders, "enabled": true, "period": "weekly", "hour": 3, "weekday": 0, "minute": 15, "retentionCopies": 9},
			http.MethodPost, databasePath + "/backup-schedule", `{"enabled":true,"period":"weekly","minute":15,"hour":3,"weekday":0,"retentionCopies":9}`,
		},
		{
			"set_backup_schedule", map[string]any{"id": orders, "enabled": false},
			http.MethodPost, databasePath + "/backup-schedule", `{"enabled":false}`,
		},
		{
			"restore_database", map[string]any{"id": orders, "backupId": "orders-dev-1", "name": "orders-copy"},
			http.MethodPost, databasePath + "/restore", `{"backupId":"orders-dev-1","name":"orders-copy"}`,
		},
		{
			"create_support_ticket", map[string]any{"subject": "Orders is slow", "description": "It stalls at noon."},
			http.MethodPost, "/api/support/tickets", `{"subject":"Orders is slow","description":"It stalls at noon."}`,
		},
		{
			"reply_to_support_ticket", map[string]any{"number": ticket, "body": "It is still slow."},
			http.MethodPost, "/api/support/tickets/" + ticket + "/replies", `{"body":"It is still slow."}`,
		},
		{"create_environment", map[string]any{"name": "spoke-b"}, http.MethodPost, "/api/connect/tokens", `{"name":"spoke-b"}`},
		{deleteTool, map[string]any{"id": orders, "confirm": "orders-dev"}, http.MethodDelete, databasePath, ``},
	}
	for _, tc := range cases {
		t.Run(tc.tool+" "+tc.body, func(t *testing.T) {
			api := changeAPI()
			session := connectAt(t, api, auth.AccessChangeDelete)

			res := callTool(t, session, tc.tool, tc.args)

			require.False(t, res.IsError, text(t, res))
			sent := api.sent()
			require.NotEmpty(t, sent)
			last := sent[len(sent)-1]
			assert.Equal(t, tc.method, last.Method)
			assert.Equal(t, tc.uri, last.URI)
			if tc.body == "" {
				assert.Empty(t, last.Body)
			} else {
				assert.JSONEq(t, tc.body, last.Body)
			}
			for _, request := range sent[:len(sent)-1] {
				assert.Equal(t, http.MethodGet, request.Method, "only reads come before the change")
			}
			assert.Equal(t, callerAt(auth.AccessChangeDelete), last.Caller, "the change is made as the caller, who names the client and agent")
			assert.Empty(t, last.Cookie)
			assert.Empty(t, last.Authorization)
			if tc.body != "" {
				assert.Equal(t, "application/json", last.ContentType)
			}
		})
	}
}

func TestEachChangeResultMatchesTheCatalogEnvelope(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{
			"create_database", map[string]any{"name": "orders-dev", "environment": "spoke-a", "profile": "dev"},
			`{"accepted":true,"operation":"create_database","id":"spoke-a/pds.orders-dev","status":"pending",
			"next":{"tool":"wait_for_database","arguments":{"id":"spoke-a/pds.orders-dev","until":"ready"}}}`,
		},
		{
			"scale_database", map[string]any{"id": orders, "cpu": 2, "memoryGb": 4, "members": 3},
			`{"accepted":true,"operation":"scale_database","id":"spoke-a/pds.orders-dev","status":"ready",
			"requested":{"cpu":2,"memoryGb":4,"members":3},
			"database":{"id":"spoke-a/pds.orders-dev","name":"orders-dev","engine":"mysql","profile":"dev","namespace":"pds","environment":"spoke-a",
			"status":"ready","members":1,"cpu":1,"memoryGb":2,"storageGb":20,"createdAt":"2026-09-08T12:00:00Z","lastBackupAt":"2026-09-08T13:00:00Z",
			"backupSchedule":{"name":"daily","enabled":true,"cron":"0 2 * * *","retentionCopies":7,"period":"daily","minute":0,"hour":2},
			"connection":{"host":"orders-dev.pds.svc","port":3306,"username":"root","maskedUri":"mysql://root:••••••••@orders-dev.pds.svc:3306","secretName":"orders-dev-secret","serviceName":"orders-dev"},
			"access":{"external":false,"visibility":"cluster"}},
			"note":"The sizes in database may still be the old ones. Call wait_for_database with expect to follow the change.",
			"next":{"tool":"wait_for_database","arguments":{"id":"spoke-a/pds.orders-dev","until":"ready","expect":{"cpu":2,"memoryGb":4,"members":3}}}}`,
		},
		{
			"backup_database", map[string]any{"id": orders},
			`{"accepted":true,"operation":"backup_database","id":"spoke-a/pds.orders-dev","backupId":"orders-dev-2",
			"next":{"tool":"wait_for_backup","arguments":{"id":"spoke-a/pds.orders-dev","backupId":"orders-dev-2"}}}`,
		},
		{
			"set_backup_schedule", map[string]any{"id": orders, "enabled": true, "period": "daily", "hour": 3, "minute": 30, "retentionCopies": 5},
			`{"accepted":true,"operation":"set_backup_schedule","id":"spoke-a/pds.orders-dev",
			"schedule":{"name":"daily","enabled":true,"cron":"30 3 * * *","retentionCopies":5,"period":"daily","minute":30,"hour":3}}`,
		},
		{
			"restore_database", map[string]any{"id": orders, "backupId": "orders-dev-1", "name": "orders-copy"},
			`{"accepted":true,"operation":"restore_database","id":"spoke-a/pds.orders-copy","status":"accepted",
			"next":{"tool":"wait_for_database","arguments":{"id":"spoke-a/pds.orders-copy","until":"ready"}}}`,
		},
		{
			"create_support_ticket", map[string]any{"subject": "Orders is slow", "description": "It stalls at noon."},
			`{"accepted":true,"operation":"create_support_ticket","id":"CS0061234","status":"Open","portalUrl":"https://support.example/portal/CS0061234"}`,
		},
		{
			"reply_to_support_ticket", map[string]any{"number": ticket, "body": "It is still slow."},
			`{"accepted":true,"operation":"reply_to_support_ticket","id":"CS0061234"}`,
		},
		{
			"create_environment", map[string]any{"name": "spoke-b"},
			`{"accepted":true,"operation":"create_environment","id":"spoke-b","expiresAt":"2026-09-08T13:00:00Z",
			"consoleUrl":"https://pds.example/environments/new",
			"note":"Ask the user to open consoleUrl, enter the same name and run the command it creates. The command is not shown here.",
			"next":{"tool":"wait_for_environment","arguments":{"name":"spoke-b"}}}`,
		},
		{
			deleteTool, map[string]any{"id": orders, "confirm": "orders-dev"},
			`{"accepted":true,"operation":"delete_database","id":"spoke-a/pds.orders-dev",
			"next":{"tool":"wait_for_database","arguments":{"id":"spoke-a/pds.orders-dev","until":"deleted"}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			session := connectAt(t, changeAPI(), auth.AccessChangeDelete, mcp.WithConsoleURL("https://pds.example/"))
			schemas := map[string]any{}
			for _, tool := range listedTools(t, session) {
				schemas[tool.Name] = tool.OutputSchema
			}

			res := callTool(t, session, tc.tool, tc.args)

			require.False(t, res.IsError, text(t, res))
			assert.JSONEq(t, tc.want, text(t, res))
			assert.NoError(t, validates(t, schemas[tc.tool], structured(t, res)), "structured content fits the output schema")
			assert.NotContains(t, text(t, res), "\n")
		})
	}
}

// Every next names a tool and arguments the registry itself accepts.
func TestEveryNextCallIsAcceptedByTheToolItNames(t *testing.T) {
	api := changeAPI()
	session := connectAt(t, api, auth.AccessChangeDelete, mcp.WithConsoleURL("https://pds.example"))
	inputs := map[string]any{}
	for _, tool := range listedTools(t, session) {
		inputs[tool.Name] = tool.InputSchema
	}
	calls := validCalls()
	calls["scale_database"] = map[string]any{"id": orders, "cpu": 2, "memoryGb": 4, "storageGb": 40, "members": 3}

	for tool, args := range calls {
		res := callTool(t, session, tool, args)
		require.False(t, res.IsError, "%s: %s", tool, text(t, res))
		next, found := structured(t, res)["next"].(map[string]any)
		if !found {
			continue
		}

		assert.NoError(t, validates(t, inputs[next["tool"].(string)], next["arguments"].(map[string]any)), "next of %s", tool)
	}
}

func TestAnOpenRangeNeedsTheUsersAcknowledgement(t *testing.T) {
	for _, open := range []string{"0.0.0.0/0", "::/0", " 0.0.0.0/0 ", "0.0.0.0", "::", "10.0.0.0/00", "0::/0"} {
		t.Run(open, func(t *testing.T) {
			api := changeAPI()
			session := connectAt(t, api, auth.AccessChange)
			args := map[string]any{
				"name": "orders-dev", "environment": "spoke-a", "profile": "dev",
				"access": "public", "sourceRanges": []any{"203.0.113.0/24", open},
			}

			res := callTool(t, session, "create_database", args)

			require.True(t, res.IsError)
			body := structured(t, res)
			assert.Equal(t, "confirmation_required", body["kind"])
			assert.Equal(t, false, body["retryable"])
			assert.Equal(t, "These source ranges open the database to the whole internet. Tell the user that any address can connect, and set acknowledgeOpenToInternet to true only if they still want that.", body["message"])
			assert.NotContains(t, body, "requestId")
			assertNoRequest(t, api)

			for _, notTrue := range []any{false, nil} {
				args["acknowledgeOpenToInternet"] = notTrue
				res = callTool(t, session, "create_database", args)
				require.True(t, res.IsError, "%v", notTrue)
			}
			assertNoRequest(t, api)
		})
	}
}

func TestAnOpenRangeTheUserAcknowledgedIsSentWithAWarning(t *testing.T) {
	api := changeAPI()
	session := connectAt(t, api, auth.AccessChange)

	res := callTool(t, session, "create_database", map[string]any{
		"name": "orders-dev", "environment": "spoke-a", "profile": "dev",
		"access": "public", "sourceRanges": []any{"0.0.0.0/0"}, "acknowledgeOpenToInternet": true,
	})

	require.False(t, res.IsError, text(t, res))
	assert.Equal(t, []any{"Any address on the internet can connect to this database."}, structured(t, res)["warnings"])
	sent := api.sent()
	require.Len(t, sent, 1)
	assert.JSONEq(t, `{"name":"orders-dev","engine":"mysql","profile":"dev","environment":"spoke-a","access":"public","sourceRanges":["0.0.0.0/0"]}`, sent[0].Body)
	assert.NotContains(t, sent[0].Body, "acknowledge", "the acknowledgement is the tool's, not the API's")
}

func TestARangeThatIsNotOpenNeedsNoAcknowledgement(t *testing.T) {
	for _, closed := range []string{"203.0.113.0/24", "10.0.0.0/8", "203.0.113.7", "2001:db8::/32", "192.0.2.0/30"} {
		api := changeAPI()
		session := connectAt(t, api, auth.AccessChange)

		res := callTool(t, session, "create_database", map[string]any{
			"name": "orders-dev", "environment": "spoke-a", "profile": "dev", "access": "public", "sourceRanges": []any{closed},
		})

		require.False(t, res.IsError, "%s: %s", closed, text(t, res))
		assert.NotContains(t, structured(t, res), "warnings", closed)
	}
}

func TestAPublicDatabaseNeedsSourceRangesAndNoneIsChosenForIt(t *testing.T) {
	for _, ranges := range []any{nil, []any{}} {
		api := changeAPI()
		session := connectAt(t, api, auth.AccessChange)
		args := map[string]any{"name": "orders-dev", "environment": "spoke-a", "profile": "dev", "access": "public"}
		if ranges != nil {
			args["sourceRanges"] = ranges
		}

		res := callTool(t, session, "create_database", args)

		require.True(t, res.IsError)
		body := structured(t, res)
		assert.Equal(t, "invalid_input", body["kind"])
		assert.Equal(t, "A public database needs sourceRanges. Ask the user which addresses may connect. Do not choose them yourself.", body["message"])
		assertNoRequest(t, api)
	}
}

func TestAPrivateDatabaseIsSentWithNoRangesOfItsOwn(t *testing.T) {
	api := changeAPI()
	session := connectAt(t, api, auth.AccessChange)

	callTool(t, session, "create_database", map[string]any{"name": "orders-dev", "environment": "spoke-a", "profile": "dev"})

	sent := api.sent()
	require.Len(t, sent, 1)
	assert.NotContains(t, sent[0].Body, "sourceRanges")
	assert.NotContains(t, sent[0].Body, "access")
}

func TestDeleteWithAConfirmThatIsNotTheStoredNameSendsNoDelete(t *testing.T) {
	cases := map[string]map[string]any{
		"a different name":    {"id": orders, "confirm": "orders-prod"},
		"the id":              {"id": orders, "confirm": orders},
		"the name in capital": {"id": orders, "confirm": "Orders-Dev"},
		"the name and a tail": {"id": orders, "confirm": "orders-dev "},
		"an empty confirm":    {"id": orders, "confirm": ""},
		"no confirm":          {"id": orders},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			api := changeAPI()
			session := connectAt(t, api, auth.AccessChangeDelete)

			res := callTool(t, session, deleteTool, args)

			require.True(t, res.IsError)
			body := structured(t, res)
			assert.Equal(t, "invalid_input", body["kind"])
			assert.Equal(t, false, body["retryable"])
			assert.Equal(t, "The confirm value does not match the name of this database, so nothing was changed. Ask the user to type the database name to confirm, and pass exactly that.", body["message"])
			for _, request := range api.sent() {
				assert.Equal(t, http.MethodGet, request.Method, "nothing but the read of the stored name was sent")
			}
			assert.Len(t, api.sent(), 1)
		})
	}
}

func TestDeleteReadsTheNameBeforeItDeletes(t *testing.T) {
	api := changeAPI()
	session := connectAt(t, api, auth.AccessChangeDelete)

	res := callTool(t, session, deleteTool, map[string]any{"id": orders, "confirm": "orders-dev"})

	require.False(t, res.IsError, text(t, res))
	sent := api.sent()
	require.Len(t, sent, 2)
	assert.Equal(t, http.MethodGet, sent[0].Method)
	assert.Equal(t, http.MethodDelete, sent[1].Method)
}

func TestDeleteOfADatabaseThatIsNotThereIsNotFoundAndSendsNoDelete(t *testing.T) {
	api := changeAPI()
	api.then(databasePath, failed(http.StatusNotFound, "ErrNotFound", "That database does not exist."))
	session := connectAt(t, api, auth.AccessChangeDelete)

	res := callTool(t, session, deleteTool, map[string]any{"id": orders, "confirm": "orders-dev"})

	require.True(t, res.IsError)
	assert.Equal(t, "not_found", structured(t, res)["kind"])
	assert.Len(t, api.sent(), 1)
}

func TestAHostileIDToAChangeToolGivesInvalidInputAndSendsNoRequest(t *testing.T) {
	ids := []string{
		"", "pds.orders-dev", "../environments/x", "spoke-a/pds.orders-dev/credential", "spoke-a/pds.orders-dev?team=all",
		"spoke-a/pds.orders-dev\n", "Spoke-A/pds.orders-dev", "spoke-a//pds.orders-dev", "%2e%2e/x.y", "spoke-a/pds.orders-dev/",
	}
	tools := []string{"scale_database", "backup_database", "set_backup_schedule", "restore_database", deleteTool}
	for _, tool := range tools {
		for _, id := range ids {
			t.Run(tool+" "+id, func(t *testing.T) {
				api := changeAPI()
				session := connectAt(t, api, auth.AccessChangeDelete)
				args := validCalls()[tool]
				args["id"] = id

				res := callTool(t, session, tool, args)

				require.True(t, res.IsError)
				assert.Equal(t, "invalid_input", structured(t, res)["kind"])
				assertNoRequest(t, api)
			})
		}
	}

	t.Run("a database id of a ticket", func(t *testing.T) {
		api := changeAPI()
		session := connectAt(t, api, auth.AccessChange)

		res := callTool(t, session, "create_support_ticket", map[string]any{"subject": "a", "description": "b", "databaseId": "../x"})

		require.True(t, res.IsError)
		assert.Equal(t, "invalid_input", structured(t, res)["kind"])
		assertNoRequest(t, api)
	})
}

func TestArgumentsThatBreakAChangeToolsSchemaSendNoRequest(t *testing.T) {
	const sentinel = "sentinel-value-the-model-sent"
	long := strings.Repeat("a", 401)
	cases := []struct {
		name string
		tool string
		args map[string]any
		hint string
	}{
		{"an engine outside the enum", "create_database", map[string]any{"name": "a", "environment": "e", "profile": "dev", "engine": sentinel}, "Check engine."},
		{"a profile outside the enum", "create_database", map[string]any{"name": "a", "environment": "e", "profile": sentinel}, "Check profile."},
		{"no profile", "create_database", map[string]any{"name": "a", "environment": "e"}, ""},
		{"no environment", "create_database", map[string]any{"name": "a", "profile": "dev"}, ""},
		{"an access outside the enum", "create_database", map[string]any{"name": "a", "environment": "e", "profile": "dev", "access": sentinel}, "Check access."},
		{"a cpu that is not whole", "create_database", map[string]any{"name": "a", "environment": "e", "profile": "dev", "cpu": 1.5}, "Check cpu."},
		{"thirty three ranges", "create_database", map[string]any{
			"name": "a", "environment": "e", "profile": "dev", "access": "public", "sourceRanges": make([]any, 33),
		}, "Check sourceRanges."},
		{"nothing to scale", "scale_database", map[string]any{"id": orders}, ""},
		{"a memory that is text", "scale_database", map[string]any{"id": orders, "memoryGb": sentinel}, "Check memoryGb."},
		{"no enabled", "set_backup_schedule", map[string]any{"id": orders, "period": "daily", "hour": 3}, ""},
		{"an enabled that is text", "set_backup_schedule", map[string]any{"id": orders, "enabled": sentinel}, "Check enabled."},
		{"a period outside the enum", "set_backup_schedule", map[string]any{"id": orders, "enabled": true, "period": sentinel}, "Check period."},
		{"a minute above 59", "set_backup_schedule", map[string]any{"id": orders, "enabled": true, "period": "hourly", "minute": 60}, "Check minute."},
		{"an hour above 23", "set_backup_schedule", map[string]any{"id": orders, "enabled": true, "period": "daily", "hour": 24}, "Check hour."},
		{"a weekday above 6", "set_backup_schedule", map[string]any{"id": orders, "enabled": true, "period": "weekly", "hour": 1, "weekday": 7}, "Check weekday."},
		{"no copies kept", "set_backup_schedule", map[string]any{"id": orders, "enabled": true, "period": "daily", "hour": 1, "retentionCopies": 0}, "Check retentionCopies."},
		{"thirty one copies kept", "set_backup_schedule", map[string]any{"id": orders, "enabled": true, "period": "daily", "hour": 1, "retentionCopies": 31}, "Check retentionCopies."},
		{"no backup to restore", "restore_database", map[string]any{"id": orders, "name": "orders-copy"}, ""},
		{"a subject over 400 characters", "create_support_ticket", map[string]any{"subject": long, "description": "b"}, "Check subject."},
		{"a description over 40000 characters", "create_support_ticket", map[string]any{"subject": "a", "description": strings.Repeat("a", 40001)}, "Check description."},
		{"a ticket number with a slash", "reply_to_support_ticket", map[string]any{"number": "../" + sentinel, "body": "a"}, "Check number."},
		{"an empty reply", "reply_to_support_ticket", map[string]any{"number": ticket, "body": ""}, "Check body."},
		{"a reply over 40000 characters", "reply_to_support_ticket", map[string]any{"number": ticket, "body": strings.Repeat("a", 40001)}, "Check body."},
		{"no environment name", "create_environment", map[string]any{}, ""},
		{"a confirm that is a number", deleteTool, map[string]any{"id": orders, "confirm": 5}, "Check confirm."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := changeAPI()
			session := connectAt(t, api, auth.AccessChangeDelete)

			res := callTool(t, session, tc.tool, tc.args)

			require.True(t, res.IsError)
			assert.NotContains(t, text(t, res), sentinel)
			body := structured(t, res)
			assert.Equal(t, "invalid_input", body["kind"])
			if tc.hint != "" {
				assert.Equal(t, tc.hint, body["hint"])
			}
			assertNoRequest(t, api)
		})
	}
}

func TestScalingWithNothingToChangeSaysWhatToSet(t *testing.T) {
	api := changeAPI()
	session := connectAt(t, api, auth.AccessChange)

	res := callTool(t, session, "scale_database", map[string]any{"id": orders})

	require.True(t, res.IsError)
	assert.Equal(t, "Set at least one of cpu, memoryGb, storageGb or members.", structured(t, res)["message"])
	assertNoRequest(t, api)
}

func TestASupportTicketAboutADatabaseCarriesWhatTheConsoleSends(t *testing.T) {
	api := changeAPI()
	database := databaseFixture()
	database["version"] = "8.0.36"
	database["message"] = "A member is not ready."
	database["members"] = 3
	api.then(databasePath, ok(database))
	session := connectAt(t, api, auth.AccessChange)

	res := callTool(t, session, "create_support_ticket", map[string]any{"subject": "Orders is slow", "description": "It stalls.", "databaseId": orders})

	require.False(t, res.IsError, text(t, res))
	sent := api.sent()
	require.Len(t, sent, 2)
	assert.Equal(t, http.MethodGet, sent[0].Method)
	assert.Equal(t, databasePath, sent[0].URI)
	assert.JSONEq(t, `{"subject":"Orders is slow","description":"It stalls.","context":{
		"database":"orders-dev","profile":"dev","engine":"mysql","engineVersion":"8.0.36","clusterType":"cluster",
		"namespace":"pds","members":3,"status":"ready","errorMessage":"A member is not ready."}}`, sent[1].Body)
}

func TestASupportTicketAboutAMongoDBDatabaseCarriesItsEngine(t *testing.T) {
	api := changeAPI()
	database := databaseFixture()
	database["engine"] = "mongodb"
	database["version"] = "8.0.4"
	api.then(databasePath, ok(database))
	session := connectAt(t, api, auth.AccessChange)

	res := callTool(t, session, "create_support_ticket", map[string]any{"subject": "a", "description": "b", "databaseId": orders})

	require.False(t, res.IsError, text(t, res))
	assert.Contains(t, api.sent()[1].Body, `"engine":"mongodb","engineVersion":"8.0.4"`)
}

func TestASupportTicketAboutAStandaloneDatabaseSaysSo(t *testing.T) {
	api := changeAPI()
	session := connectAt(t, api, auth.AccessChange)

	callTool(t, session, "create_support_ticket", map[string]any{"subject": "a", "description": "b", "databaseId": orders})

	assert.Contains(t, api.sent()[1].Body, `"clusterType":"standalone"`)
}

func TestASupportTicketAboutADatabaseThatIsNotThereSendsNoTicket(t *testing.T) {
	api := changeAPI()
	api.then(databasePath, failed(http.StatusNotFound, "ErrNotFound", "That database does not exist."))
	session := connectAt(t, api, auth.AccessChange)

	res := callTool(t, session, "create_support_ticket", map[string]any{"subject": "a", "description": "b", "databaseId": orders})

	require.True(t, res.IsError)
	assert.Equal(t, "not_found", structured(t, res)["kind"])
	assert.Len(t, api.sent(), 1, "only the read of the database was sent")
}

func TestReplyingToATicketPostsTheReplyAndReturnsTheEnvelope(t *testing.T) {
	api := changeAPI()
	session := connectAt(t, api, auth.AccessChange)

	res := callTool(t, session, "reply_to_support_ticket", map[string]any{"number": ticket, "body": "It is still slow."})

	require.False(t, res.IsError, text(t, res))
	sent := api.sent()
	require.Len(t, sent, 1)
	assert.Equal(t, http.MethodPost, sent[0].Method)
	assert.Equal(t, "/api/support/tickets/CS0061234/replies", sent[0].URI)
	assert.JSONEq(t, `{"body":"It is still slow."}`, sent[0].Body)
	body := structured(t, res)
	assert.Equal(t, true, body["accepted"])
	assert.Equal(t, "reply_to_support_ticket", body["operation"])
	assert.Equal(t, ticket, body["id"])
}

func TestAChangeThatSupportCannotTakeIsNotAvailable(t *testing.T) {
	api := changeAPI()
	api.routes["/api/support/tickets/"+ticket+"/replies"] = failed(http.StatusNotImplemented, "ErrNotImplemented", "Support tickets need a ServiceNow connection, which is not configured. Use the Percona support portal instead.")
	session := connectAt(t, api, auth.AccessChange)

	res := callTool(t, session, "reply_to_support_ticket", map[string]any{"number": ticket, "body": "It is still slow."})

	require.True(t, res.IsError)
	assert.Equal(t, "not_available", structured(t, res)["kind"])
}

func TestAnEnvironmentThatIsAlreadyListedIsRefusedAndNothingIsRegistered(t *testing.T) {
	api := changeAPI()
	session := connectAt(t, api, auth.AccessChange)

	res := callTool(t, session, "create_environment", map[string]any{"name": "spoke-a"})

	require.True(t, res.IsError)
	body := structured(t, res)
	assert.Equal(t, "conflict", body["kind"])
	assert.Equal(t, "An environment with that name already exists. Choose another name, or use list_environments to see the existing ones.", body["message"])
	sent := api.sent()
	require.Len(t, sent, 1)
	assert.Equal(t, http.MethodGet, sent[0].Method)
}

func TestWithoutAConsoleAddressTheEnvironmentNoteSendsTheUserToTheConsole(t *testing.T) {
	session := connectAt(t, changeAPI(), auth.AccessChange)

	res := callTool(t, session, "create_environment", map[string]any{"name": "spoke-b"})

	require.False(t, res.IsError, text(t, res))
	body := structured(t, res)
	assert.NotContains(t, body, "consoleUrl")
	assert.Equal(t, "Ask the user to add the environment in the console, enter the same name and run the command it creates. The command is not shown here.", body["note"])
}

// The join token and command are the one secret a change route answers with.
// The fake API answers with sentinels in both, and in a field the tool does not
// know, so a tool that passed the body on would show them.
func TestCreateEnvironmentNeverCarriesTheJoinTokenOrCommand(t *testing.T) {
	const (
		tokenSentinel   = "JOIN-TOKEN-SENTINEL-8e41"
		commandSentinel = "curl -fsSL https://hub.example/bootstrap.sh | bash -s -- --token JOIN-TOKEN-SENTINEL-8e41"
	)
	joined := map[string]any{
		"environment": "spoke-b", "token": tokenSentinel, "command": commandSentinel,
		"hubUrl": "https://hub.example/" + tokenSentinel, "region": "us-west-2", "expiresAt": "2026-09-08T13:00:00Z",
	}
	cases := map[string]reply{
		"it worked":              {http.StatusCreated, joined},
		"the answer is garbled":  {http.StatusCreated, map[string]any{"token": tokenSentinel, "command": commandSentinel, "expiresAt": 5}},
		"it is not json":         {http.StatusCreated, commandSentinel},
		"the control plane fell": {http.StatusBadGateway, joined},
		"the request is refused": {http.StatusBadRequest, joined},
		"it is already there":    {http.StatusConflict, joined},
	}
	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			api := changeAPI()
			api.routes["/api/connect/tokens"] = answer
			logs := &logBuffer{}
			session := connectAs(t, api, callerAt(auth.AccessChange), logs.logger(), mcp.WithConsoleURL("https://pds.example"))

			res := callTool(t, session, "create_environment", map[string]any{"name": "spoke-b"})

			everything := text(t, res) + mustJSON(t, res.StructuredContent) + logs.String()
			assert.NotContains(t, everything, tokenSentinel)
			assert.NotContains(t, everything, "bootstrap.sh")
			assert.NotContains(t, everything, "--token")
		})
	}
}

func TestACreatedEnvironmentFromTheRealRouterCarriesNoTokenEither(t *testing.T) {
	world := newTeamWorld(t)
	chat := callerAt(auth.AccessChange)
	chat.Identity = ann.Identity
	session := connectAs(t, world.srv, chat, world.logs.logger(), mcp.WithConsoleURL("https://pds.example"))

	res := callTool(t, session, "create_environment", map[string]any{"name": "spoke-new"})

	require.False(t, res.IsError, text(t, res))
	assert.Equal(t, "spoke-new", structured(t, res)["id"])
	assert.NotContains(t, text(t, res), "bootstrap.sh")
	assert.NotContains(t, text(t, res), "token")
	assert.NotContains(t, world.logs.String(), "bootstrap.sh")
}

func TestAChangeThatTimesOutIsSentOnceAndReportedAsUnknownWithTheCheckToRun(t *testing.T) {
	cases := []struct {
		tool  string
		check string
	}{
		{"create_database", "list_databases"},
		{"scale_database", "get_database"},
		{"backup_database", "list_backups"},
		{"set_backup_schedule", "get_database"},
		{"restore_database", "list_databases"},
		{"create_support_ticket", "list_support_tickets"},
		{"reply_to_support_ticket", "get_support_ticket"},
		{"create_environment", "list_environments"},
		{deleteTool, "get_database"},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			api := changeAPI()
			var changes atomic.Int32
			api.hook = func(r *http.Request) {
				if r.Method != http.MethodGet {
					changes.Add(1)
					<-r.Context().Done()
				}
			}
			session := connectAt(t, api, auth.AccessChangeDelete, withShortTimeout())

			res := callTool(t, session, tc.tool, validCalls()[tc.tool])

			require.True(t, res.IsError)
			body := structured(t, res)
			assert.Equal(t, "timeout", body["kind"])
			assert.Equal(t, "unknown", body["outcome"])
			assert.Equal(t, false, body["retryable"], "a change that may have been made is not offered again")
			assert.Contains(t, body["message"], "Do not repeat it.")
			assert.Contains(t, body["hint"], tc.check)
			assert.Contains(t, body["hint"], "Call "+tc.check)
			assert.NotEmpty(t, body["requestId"])
			assert.Equal(t, int32(1), changes.Load(), "the change was sent exactly once")
		})
	}
}

func TestAChangeAnsweredWithSomethingUnreadableIsNotOfferedAgain(t *testing.T) {
	api := changeAPI()
	api.routes["/api/databases"] = reply{http.StatusCreated, "not an object"}
	session := connectAt(t, api, auth.AccessChange)

	res := callTool(t, session, "create_database", map[string]any{"name": "orders-dev", "environment": "spoke-a", "profile": "dev"})

	require.True(t, res.IsError)
	body := structured(t, res)
	assert.Equal(t, "unavailable", body["kind"])
	assert.Equal(t, "unknown", body["outcome"])
	assert.Equal(t, false, body["retryable"])
	assert.Contains(t, body["hint"], "list_databases")
	assert.Len(t, api.sent(), 1)
}

func TestAChangeTheAPIRefusesKeepsItsKindAndCopy(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		message string
		kind    string
	}{
		{"a 400", 400, "The cpu has to be between 1 and 64.", "invalid_input"},
		{"a 404", 404, "That environment does not exist.", "not_found"},
		{"a 409", 409, "That database is busy.", "conflict"},
		{"a 501", 501, "Backups are not set up for this environment.", "not_available"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := changeAPI()
			api.routes[databasePath+"/backup"] = failed(tc.status, "Err", tc.message)
			session := connectAt(t, api, auth.AccessChange)

			res := callTool(t, session, "backup_database", map[string]any{"id": orders})

			require.True(t, res.IsError)
			body := structured(t, res)
			assert.Equal(t, tc.kind, body["kind"])
			assert.Equal(t, tc.message, body["message"])
			assert.NotContains(t, body, "outcome", "the control plane answered, so the change is known not to have been made")
		})
	}
}

func TestEveryChangeToolIsAnnotatedAsTheCatalogSays(t *testing.T) {
	want := map[string]struct{ idempotent, destructive bool }{
		"create_database": {false, false}, "scale_database": {true, false}, "backup_database": {false, false},
		"set_backup_schedule": {true, false}, "restore_database": {false, false}, "create_support_ticket": {false, false},
		"reply_to_support_ticket": {false, false}, "create_environment": {false, false}, deleteTool: {false, true},
	}

	listed := listedTools(t, connectAt(t, changeAPI(), auth.AccessChangeDelete))

	seenChanges := 0
	for _, tool := range listed {
		expected, isChange := want[tool.Name]
		if !isChange {
			continue
		}
		seenChanges++
		require.NotNil(t, tool.Annotations, tool.Name)
		assert.False(t, tool.Annotations.ReadOnlyHint, tool.Name)
		assert.Equal(t, expected.idempotent, tool.Annotations.IdempotentHint, tool.Name)
		require.NotNil(t, tool.Annotations.DestructiveHint, tool.Name)
		assert.Equal(t, expected.destructive, *tool.Annotations.DestructiveHint, tool.Name)
		require.NotNil(t, tool.Annotations.OpenWorldHint, tool.Name)
		assert.False(t, *tool.Annotations.OpenWorldHint, tool.Name)
		assert.NotEmpty(t, tool.Title, tool.Name)
		assert.NotNil(t, tool.OutputSchema, tool.Name)
	}
	assert.Equal(t, len(want), seenChanges)
}

func TestNoChangeToolTakesACredentialOrAPassword(t *testing.T) {
	changes := append([]string{deleteTool}, changeTools...)
	for _, tool := range listedTools(t, connectAt(t, changeAPI(), auth.AccessChangeDelete)) {
		if !slices.Contains(changes, tool.Name) {
			continue
		}
		body, err := json.Marshal(tool.InputSchema)
		require.NoError(t, err)
		lowered := strings.ToLower(tool.Name + tool.Description + string(body))

		assert.NotContains(t, lowered, "credential", tool.Name)
		assert.NotContains(t, lowered, "password", tool.Name)
		assert.NotContains(t, lowered, "token", tool.Name)
	}
}

func TestANotFoundChangeNamesTheToolThatListsWhatWasGiven(t *testing.T) {
	cases := []struct {
		tool string
		path string
		want string
	}{
		{"restore_database", databasePath + "/restore", "Call list_backups for the backups of that database, or list_databases for its id."},
		{"reply_to_support_ticket", "/api/support/tickets/" + ticket + "/replies", "Call list_support_tickets for the current numbers."},
		{"backup_database", databasePath + "/backup", "Call list_databases for the current ids."},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			api := changeAPI()
			api.routes[tc.path] = failed(http.StatusNotFound, "ErrNotFound", "That was not found.")
			session := connectAt(t, api, auth.AccessChange)

			res := callTool(t, session, tc.tool, validCalls()[tc.tool])

			require.True(t, res.IsError)
			assert.Equal(t, "not_found", structured(t, res)["kind"])
			assert.Equal(t, tc.want, structured(t, res)["hint"])
		})
	}
}
