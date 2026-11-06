package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/mcp"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
)

// The tests here run the registry against the real router, with the real
// middleware, Team scope and handlers, and only the stores faked.

const namespace = "pds"

// logBuffer is the log of a whole world: the router's and the registry's. A
// test reads it to prove nothing secret was written.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.buf.String()
}

func (l *logBuffer) logger() *slog.Logger {
	return log.New(l, log.FormatJSON, slog.LevelDebug)
}

// teamRows answers the two reads the router makes of the Organization's Teams.
// Anything else it was not built for panics, so a test that reaches one says so.
type teamRows struct {
	service.IdentityStore
	teams []store.Team
}

func (r teamRows) Teams(_ context.Context, organization uint) ([]store.Team, error) {
	var out []store.Team
	for _, team := range r.teams {
		if team.OrganizationID == organization {
			out = append(out, team)
		}
	}

	return out, nil
}

func (r teamRows) Team(_ context.Context, organization uint, name string) (store.Team, error) {
	for _, team := range r.teams {
		if team.OrganizationID == organization && team.Name == name {
			return team, nil
		}
	}

	return store.Team{}, store.ErrTeamUnknown
}

// teamWorld is acme with two Teams. Each has its own environment, hub and
// activity rows, the way the router keeps them apart.
//
//	default (1)  acme-env      ann (user)   root (admin)
//	payments (2) pay-env       bob (user)
type teamWorld struct {
	srv  *api.Server
	logs *logBuffer
}

// newTeamWorld builds the world. A test that needs more of the router than the
// stores wires it through with: each function may add to the dependencies.
func newTeamWorld(t *testing.T, with ...func(*api.Deps)) *teamWorld {
	t.Helper()

	logs := &logBuffer{}
	rows := teamRows{teams: []store.Team{
		{ID: 1, OrganizationID: 1, Name: "default"},
		{ID: 2, OrganizationID: 1, Name: "payments"},
	}}
	environments := map[uint]string{1: "acme-env", 2: "pay-env"}

	registry := hub.NewRegistry(func(_ context.Context, team uint) (*hub.Service, error) {
		now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
		inv := hub.NewInventory(time.Minute)
		inv.Register(environments[team], namespace)
		inv.ApplyHeartbeat(hub.Heartbeat{Environment: environments[team], Namespace: namespace, At: now})

		return hub.NewService(hub.NewMemoryStore(), inv, hub.Options{
			Namespace:     namespace,
			BackupStorage: "backups",
			Now:           func() time.Time { return now },
			Tokens:        hub.NewTokenStore(nil, time.Hour),
		}), nil
	})
	activity := service.NewActivity(nil, service.ActivityOptions{})
	activity.UseBook(service.NewMemoryBook(nil))
	databases := service.NewDatabase(engine.NewRegistry(), service.WithRecorder(activity), service.WithHubFor(
		func(ctx context.Context) (*hub.Service, error) {
			team, _ := auth.TeamFromContext(ctx)

			return registry.Get(ctx, team)
		}))

	identity := service.NewIdentity(rows, service.WithPasswordCost(bcrypt.MinCost))
	deps := api.Deps{
		Config:          config.Config{Addr: ":8080", Env: config.EnvDev, EverestNamespace: namespace, HubPublicURL: "https://hub.example"},
		Logger:          logs.logger(),
		Auth:            api.NewSessionAuthenticator(identity, "operator-sentinel"),
		Identity:        identity,
		Databases:       databases,
		Activity:        activity,
		ActivityDetails: service.NewActivityDetails(activity, databases),
	}
	for _, add := range with {
		add(&deps)
	}
	srv := api.NewServer(deps)

	return &teamWorld{srv: srv, logs: logs}
}

func caller(role string, user, team uint, teamName string) auth.Caller {
	return auth.Caller{
		Identity: auth.Identity{
			Subject: "user",
			Scope: auth.Scope{
				UserID: user, OrganizationID: 1, OrganizationName: "acme", Role: role,
				Team: auth.Team{ID: team, Name: teamName},
			},
			Client: auth.Client{Kind: auth.ClientMCP},
		},
		Access: auth.AccessRead,
	}
}

var (
	ann  = caller(auth.RoleUser, 2, 1, "default")
	bob  = caller(auth.RoleUser, 3, 2, "payments")
	root = caller(auth.RoleAdmin, 1, 1, "default")
)

// createDatabase makes a database the way the console does, as the caller.
func (w *teamWorld) createDatabase(t *testing.T, as auth.Caller, environment, name string) {
	t.Helper()

	body := `{"name":"` + name + `","engine":"mysql","profile":"dev","environment":"` + environment + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	w.srv.ServeHTTP(rec, req.WithContext(auth.WithCaller(req.Context(), as)))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}

func TestTheReadToolsAnswerFromTheRealRouter(t *testing.T) {
	world := newTeamWorld(t)
	world.createDatabase(t, ann, "acme-env", "orders-dev")
	session := connectAs(t, world.srv, ann, world.logs.logger())

	environments := callTool(t, session, "list_environments", nil)
	list := callTool(t, session, "list_databases", nil)
	one := callTool(t, session, "get_database", map[string]any{"id": "acme-env/pds.orders-dev"})
	metrics := callTool(t, session, "get_metrics", map[string]any{"id": "acme-env/pds.orders-dev"})
	activity := callTool(t, session, "list_activity", nil)
	backups := callTool(t, session, "list_backups", map[string]any{"id": "acme-env/pds.orders-dev"})

	for _, res := range []*sdk.CallToolResult{environments, list, one, metrics, activity, backups} {
		assert.False(t, res.IsError, text(t, res))
	}
	assert.Equal(t, "acme-env", structured(t, environments)["environments"].([]any)[0].(map[string]any)["name"])
	rows := structured(t, list)["databases"].([]any)
	require.Len(t, rows, 1)
	assert.Equal(t, "acme-env/pds.orders-dev", rows[0].(map[string]any)["id"])
	assert.Equal(t, "orders-dev", structured(t, one)["name"])
	assert.Equal(t, "pending", structured(t, one)["status"])
	assert.Equal(t, true, structured(t, metrics)["pending"], "no monitoring is connected, so the metrics are pending")
	assert.Equal(t, []any{}, structured(t, backups)["backups"], "a new database has no backups")
	entries := structured(t, activity)["entries"].([]any)
	require.Len(t, entries, 1)
	assert.Equal(t, "database.create", entries[0].(map[string]any)["action"])
	assert.Equal(t, "mcp", entries[0].(map[string]any)["client"], "a change by this caller is recorded as made through the MCP")
}

func TestAChangeByTheChatIsRecordedWithTheClientAndTheAgent(t *testing.T) {
	world := newTeamWorld(t)
	chat := ann
	chat.Identity.Client = auth.Client{Kind: auth.ClientChat}
	chat.Agent = "Anthropic"
	world.createDatabase(t, chat, "acme-env", "orders-dev")
	session := connectAs(t, world.srv, chat, world.logs.logger())

	activity := callTool(t, session, "list_activity", nil)

	assert.False(t, activity.IsError, text(t, activity))
	entries := structured(t, activity)["entries"].([]any)
	require.Len(t, entries, 1)
	entry := entries[0].(map[string]any)
	assert.Equal(t, "chat", entry["client"])
	assert.Equal(t, "Anthropic", entry["agent"])
}

func TestARealAnswerFitsTheOutputSchema(t *testing.T) {
	world := newTeamWorld(t)
	world.createDatabase(t, ann, "acme-env", "orders-dev")
	session := connectAs(t, world.srv, ann, world.logs.logger())
	schemas := map[string]any{}
	for _, tool := range listedTools(t, session) {
		schemas[tool.Name] = tool.OutputSchema
	}

	for tool, args := range map[string]map[string]any{
		"list_environments": nil,
		"list_databases":    nil,
		"get_database":      {"id": "acme-env/pds.orders-dev"},
		"get_metrics":       {"id": "acme-env/pds.orders-dev"},
		"list_backups":      {"id": "acme-env/pds.orders-dev"},
		"list_activity":     nil,
	} {
		res := callTool(t, session, tool, args)

		require.False(t, res.IsError, "%s: %s", tool, text(t, res))
		assert.NoError(t, validates(t, schemas[tool], structured(t, res)), tool)
	}
}

func TestTheHistoryAlertAndActivityToolsAnswerFromTheRealRouter(t *testing.T) {
	world := newTeamWorld(t)
	world.createDatabase(t, ann, "acme-env", "orders-dev")
	session := connectAs(t, world.srv, ann, world.logs.logger())
	schemas := map[string]any{}
	for _, tool := range listedTools(t, session) {
		schemas[tool.Name] = tool.OutputSchema
	}
	id := "acme-env/pds.orders-dev"

	entry := structured(t, callTool(t, session, "list_activity", nil))["entries"].([]any)[0].(map[string]any)["id"]

	for tool, args := range map[string]map[string]any{
		"get_database_history":    {"id": id},
		"get_database_usage":      {"id": id},
		"get_database_alerts":     {"id": id},
		"get_environment_history": {"name": "acme-env"},
		"list_alerts":             nil,
		"get_activity":            {"id": entry},
	} {
		res := callTool(t, session, tool, args)

		require.False(t, res.IsError, "%s: %s", tool, text(t, res))
		assert.NoError(t, validates(t, schemas[tool], structured(t, res)), tool)
	}

	detail := structured(t, callTool(t, session, "get_activity", map[string]any{"id": entry}))
	assert.Equal(t, "database.create", detail["event"].(map[string]any)["action"])
	assert.Equal(t, "mcp", detail["event"].(map[string]any)["client"])
	alerts := structured(t, callTool(t, session, "list_alerts", nil))
	assert.Equal(t, "acme-env", alerts["environments"].([]any)[0].(map[string]any)["name"])
	assert.NotEqual(t, "live", alerts["environments"].([]any)[0].(map[string]any)["state"], "no monitoring was read, so an empty list says nothing")
}

func TestACallerCannotReadAnotherTeamsHistoryAlertsOrEnvironment(t *testing.T) {
	world := newTeamWorld(t)
	world.createDatabase(t, bob, "pay-env", "payroll")
	session := connectAs(t, world.srv, ann, world.logs.logger())

	for tool, args := range map[string]map[string]any{
		"get_database_history":    {"id": "pay-env/pds.payroll"},
		"get_database_usage":      {"id": "pay-env/pds.payroll"},
		"get_database_alerts":     {"id": "pay-env/pds.payroll"},
		"get_environment_history": {"name": "pay-env"},
	} {
		res := callTool(t, session, tool, args)

		require.True(t, res.IsError, tool)
		assert.Equal(t, "not_found", structured(t, res)["kind"], tool)
	}

	alerts := structured(t, callTool(t, session, "list_alerts", nil))
	require.Len(t, alerts["environments"], 1, "only this Team's environment is listed")
	assert.Equal(t, "acme-env", alerts["environments"].([]any)[0].(map[string]any)["name"])
}

func TestACallerCannotReadADatabaseOutsideItsTeam(t *testing.T) {
	world := newTeamWorld(t)
	world.createDatabase(t, bob, "pay-env", "payroll")
	session := connectAs(t, world.srv, ann, world.logs.logger())

	for tool, args := range map[string]map[string]any{
		"get_database":  {"id": "pay-env/pds.payroll"},
		"list_backups":  {"id": "pay-env/pds.payroll"},
		"get_metrics":   {"id": "pay-env/pds.payroll"},
		"list_activity": {"databaseId": "pay-env/pds.payroll"},
	} {
		res := callTool(t, session, tool, args)

		if tool == "list_activity" {
			require.False(t, res.IsError, text(t, res))
			assert.Empty(t, structured(t, res)["entries"], "another Team's changes are not in this Team's log")

			continue
		}
		require.True(t, res.IsError, tool)
		body := structured(t, res)
		assert.Equal(t, "not_found", body["kind"], tool)
		assert.Equal(t, "That environment does not exist.", body["message"], "the API's own refusal comes back: %s", tool)
	}

	list := callTool(t, session, "list_databases", nil)
	assert.Empty(t, structured(t, list)["databases"])
	environments := callTool(t, session, "list_environments", nil)
	assert.Len(t, structured(t, environments)["environments"], 1)
}

func TestACallerReadsItsOwnTeamsDatabaseAndAnAdminReadsAnotherTeams(t *testing.T) {
	world := newTeamWorld(t)
	world.createDatabase(t, bob, "pay-env", "payroll")

	owner := callTool(t, connectAs(t, world.srv, bob, world.logs.logger()), "get_database", map[string]any{"id": "pay-env/pds.payroll"})
	admin := callTool(t, connectAs(t, world.srv, root, world.logs.logger()), "get_database", map[string]any{"id": "pay-env/pds.payroll"})

	assert.False(t, owner.IsError, text(t, owner))
	assert.False(t, admin.IsError, text(t, admin))
}

func TestSupportThatIsNotSetUpIsNotAvailable(t *testing.T) {
	world := newTeamWorld(t)
	session := connectAs(t, world.srv, ann, world.logs.logger())

	list := callTool(t, session, "list_support_tickets", nil)
	one := callTool(t, session, "get_support_ticket", map[string]any{"number": "CS0061234"})

	assert.Equal(t, "not_available", structured(t, list)["kind"])
	assert.Equal(t, "not_available", structured(t, one)["kind"])
}

// secretWorld is one hub with a public database whose connection string holds
// a password, a hub operator token and an agent token. The router holds each of
// them, so a tool that leaked one has something to leak.
const (
	passwordSentinel = "PASSWORD-SENTINEL-7c1f"
	operatorSentinel = "OPERATOR-SENTINEL-3b9e"
	agentSentinel    = "AGENT-SENTINEL-5d2a"
)

func newSecretWorld(t *testing.T) (*api.Server, *logBuffer) {
	t.Helper()

	logs := &logBuffer{}
	inv := hub.NewInventory(time.Minute)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	hubSvc := hub.NewService(hub.NewMemoryStore(), inv, hub.Options{
		Namespace:     namespace,
		BackupStorage: "backups",
		Now:           func() time.Time { return now },
		Tokens:        hub.NewTokenStore(nil, time.Hour),
	})
	inv.Register("spoke-a", namespace)
	databases := service.NewDatabase(engine.NewRegistry(), service.WithHub(hubSvc))
	srv := api.NewServer(api.Deps{
		Config: config.Config{
			Addr: ":8080", Env: config.EnvDev, EverestNamespace: namespace,
			HubAgentToken: agentSentinel, HubOperatorToken: operatorSentinel,
			HubAgentEnvironments: []string{"spoke-a"}, HubPublicURL: "https://hub.example",
		},
		Logger:    logs.logger(),
		Auth:      auth.NewStub(),
		Databases: databases,
	})

	payload, err := json.Marshal(map[string]any{
		"environment": "spoke-a", "namespace": namespace, "at": now, "privateAccess": true,
		"instances": []map[string]any{{
			"Name": "orders-ext", "Namespace": namespace, "Engine": "mysql", "ID": "spoke-a/pds.orders-ext",
			"Environment": "spoke-a", "Status": map[string]any{"Phase": "ready"},
			"Exposure": map[string]any{"External": true, "Address": "198.51.100.20"},
			"Connection": map[string]any{
				"Host": "198.51.100.20", "Port": 3306, "Username": "root",
				"URI": "mysql://root:" + passwordSentinel + "@198.51.100.20:3306",
			},
		}},
	})
	require.NoError(t, err)
	beat := httptest.NewRequest(http.MethodPost, "/api/internal/environments/spoke-a/heartbeat", bytes.NewReader(payload))
	beat.Header.Set("Authorization", "Bearer "+agentSentinel)
	beat.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, beat)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	// The password is reachable by a route, which is what makes the check mean something.
	credential := httptest.NewRequest(http.MethodGet, "/api/databases/spoke-a/pds.orders-ext/credential", nil)
	got := httptest.NewRecorder()
	srv.ServeHTTP(got, credential)
	require.Contains(t, got.Body.String(), passwordSentinel, "the router does hold the password")

	return srv, logs
}

func TestNoResultErrorOrLogLineCarriesASecret(t *testing.T) {
	srv, logs := newSecretWorld(t)
	session := connectAs(t, srv, readCaller(), logs.logger())
	id := "spoke-a/pds.orders-ext"

	var everything strings.Builder
	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"list_environments", nil},
		{"list_databases", nil},
		{"get_database", map[string]any{"id": id}},
		{"list_backups", map[string]any{"id": id}},
		{"get_metrics", map[string]any{"id": id, "includePoints": true}},
		{"list_activity", nil},
		{"get_database_history", map[string]any{"id": id}},
		{"get_database_usage", map[string]any{"id": id}},
		{"get_database_alerts", map[string]any{"id": id}},
		{"get_environment_history", map[string]any{"name": "spoke-a"}},
		{"list_alerts", nil},
		{"get_activity", map[string]any{"id": "1"}},
		{"list_support_tickets", nil},
		{"get_support_ticket", map[string]any{"number": "CS0061234"}},
		{"get_database", map[string]any{"id": "spoke-a/pds.missing"}},
		{"get_database", map[string]any{"id": id + "/credential"}},
		{"get_database", map[string]any{"id": "../" + id}},
	} {
		res := callTool(t, session, call.tool, call.args)
		everything.WriteString(text(t, res))
		everything.WriteString(mustJSON(t, res.StructuredContent))
	}

	// The tool that exists for the connection is the one that hides the password.
	one := text(t, callTool(t, session, "get_database", map[string]any{"id": id}))
	assert.Contains(t, one, "maskedUri")

	for _, secret := range []string{passwordSentinel, operatorSentinel, agentSentinel} {
		assert.NotContains(t, everything.String(), secret, "a result or an error")
		assert.NotContains(t, logs.String(), secret, "a log line")
	}
}

func TestThereIsNoToolForTheCredentialRoute(t *testing.T) {
	srv, logs := newSecretWorld(t)
	session := connectAs(t, srv, readCaller(), logs.logger())

	for _, tool := range listedTools(t, session) {
		assert.NotContains(t, strings.ToLower(tool.Name+tool.Description), "credential", tool.Name)
		assert.NotContains(t, strings.ToLower(tool.Name), "password", tool.Name)
	}
	_, err := session.CallTool(t.Context(), &sdk.CallToolParams{Name: "get_database_credential"})
	assert.Error(t, err, "the tool does not exist")
}

// A session opened inside a request handler is given that request's context.
// The tool's request must still be routed on its own, and must leave the
// outer request's routing state as it found it.
func TestAToolCalledFromInsideAHandlerLeavesTheOuterRoutingAlone(t *testing.T) {
	inner := chi.NewRouter()
	inner.Get("/api/databases/{id}/{hubID}", func(w http.ResponseWriter, r *http.Request) {
		answer := databaseFixture()
		answer["name"] = chi.URLParam(r, "hubID")
		_ = json.NewEncoder(w).Encode(answer)
	})

	var before, after int
	var got any
	outer := chi.NewRouter()
	outer.Get("/chat/{session}", func(w http.ResponseWriter, r *http.Request) {
		before = len(chi.RouteContext(r.Context()).URLParams.Keys)
		session, err := mcp.New(inner, discardLogger(), "test").Connect(r.Context(), readCaller())
		if err != nil {
			t.Error(err)

			return
		}
		defer func() { _ = session.Close() }()
		res, err := session.CallTool(r.Context(), &sdk.CallToolParams{Name: "get_database", Arguments: map[string]any{"id": orders}})
		if err != nil {
			t.Error(err)

			return
		}
		got = res.StructuredContent
		after = len(chi.RouteContext(r.Context()).URLParams.Keys)
	})

	outer.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/chat/abc", nil))

	assert.Equal(t, before, after, "the outer request's URL parameters are untouched")
	assert.Contains(t, mustJSON(t, got), "pds.orders-dev", "the tool's request was routed on its own")
}

// changer is ann with the change and delete tier, acting as the Chat or as an
// agent the way the control plane builds each of them.
func changer(kind, agent string) auth.Caller {
	out := ann
	out.Access = auth.AccessChangeDelete
	out.Identity.Client = auth.Client{Kind: kind}
	out.Agent = agent

	return out
}

func activityOf(t *testing.T, session *mcp.Session) []map[string]any {
	t.Helper()

	res := callTool(t, session, "list_activity", nil)
	require.False(t, res.IsError, text(t, res))
	var out []map[string]any
	for _, entry := range structured(t, res)["entries"].([]any) {
		out = append(out, entry.(map[string]any))
	}

	return out
}

func TestAChangeThroughTheRegistryRecordsTheClientAndTheAgent(t *testing.T) {
	cases := []struct {
		name   string
		caller auth.Caller
		client string
		agent  string
	}{
		{"the Chat", changer(auth.ClientChat, "Anthropic"), "chat", "Anthropic"},
		{"an external agent", changer(auth.ClientMCP, "Claude Code"), "mcp", "Claude Code"},
		{"an agent that names itself on the caller", func() auth.Caller {
			caller := changer(auth.ClientMCP, "Cursor")
			caller.Identity.Client.Agent = "Something else"

			return caller
		}(), "mcp", "Cursor"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			world := newTeamWorld(t)
			session := connectAs(t, world.srv, tc.caller, world.logs.logger())

			created := callTool(t, session, "create_database", map[string]any{"name": "orders-dev", "environment": "acme-env", "profile": "dev"})
			require.False(t, created.IsError, text(t, created))
			id := structured(t, created)["id"].(string)
			assert.Equal(t, "acme-env/pds.orders-dev", id)
			removed := callTool(t, session, "delete_database", map[string]any{"id": id, "confirm": "orders-dev"})
			require.False(t, removed.IsError, text(t, removed))

			entries := activityOf(t, session)
			require.Len(t, entries, 2)
			assert.Equal(t, "database.delete", entries[0]["action"])
			assert.Equal(t, "database.create", entries[1]["action"])
			for _, entry := range entries {
				assert.Equal(t, tc.client, entry["client"])
				assert.Equal(t, tc.agent, entry["agent"])
			}
		})
	}
}

func TestACreatedDatabaseCanBeFollowedWithTheWaitTheChangeNames(t *testing.T) {
	world := newTeamWorld(t)
	session := connectAs(t, world.srv, changer(auth.ClientChat, "Anthropic"), world.logs.logger())

	created := callTool(t, session, "create_database", map[string]any{"name": "orders-dev", "environment": "acme-env", "profile": "dev"})
	require.False(t, created.IsError, text(t, created))
	next := structured(t, created)["next"].(map[string]any)
	args := next["arguments"].(map[string]any)
	args["timeoutSeconds"] = 1
	waited := callTool(t, session, next["tool"].(string), args)

	require.False(t, waited.IsError, text(t, waited))
	assert.Equal(t, "in_progress", structured(t, waited)["outcome"], "no environment has picked the database up")
	assert.Equal(t, "pending", structured(t, waited)["status"])
}

func TestAChangeIsRefusedForADatabaseOutsideTheCallersTeamAndNothingIsDeleted(t *testing.T) {
	world := newTeamWorld(t)
	world.createDatabase(t, bob, "pay-env", "payroll")
	session := connectAs(t, world.srv, changer(auth.ClientMCP, "Claude Code"), world.logs.logger())

	for tool, args := range map[string]map[string]any{
		"delete_database":     {"id": "pay-env/pds.payroll", "confirm": "payroll"},
		"scale_database":      {"id": "pay-env/pds.payroll", "members": 3},
		"backup_database":     {"id": "pay-env/pds.payroll"},
		"set_backup_schedule": {"id": "pay-env/pds.payroll", "enabled": false},
		"restore_database":    {"id": "pay-env/pds.payroll", "backupId": "b", "name": "copy"},
	} {
		res := callTool(t, session, tool, args)

		require.True(t, res.IsError, tool)
		assert.Equal(t, "not_found", structured(t, res)["kind"], tool)
	}

	owner := connectAs(t, world.srv, bob, world.logs.logger())
	still := callTool(t, owner, "get_database", map[string]any{"id": "pay-env/pds.payroll"})
	assert.False(t, still.IsError, "the other Team's database is still there")
}

func TestACallerWithAnAgentNameTheActivityLogCannotKeepCannotChangeAnything(t *testing.T) {
	world := newTeamWorld(t)
	session := connectAs(t, world.srv, changer(auth.ClientMCP, "bad<name>"), world.logs.logger())

	res := callTool(t, session, "create_database", map[string]any{"name": "orders-dev", "environment": "acme-env", "profile": "dev"})

	require.True(t, res.IsError)
	other := connectAs(t, world.srv, ann, world.logs.logger())
	assert.Empty(t, structured(t, callTool(t, other, "list_databases", nil))["databases"], "nothing was created")
}

func TestARealChangeAnswerFitsTheOutputSchema(t *testing.T) {
	world := newTeamWorld(t)
	session := connectAs(t, world.srv, changer(auth.ClientChat, "Anthropic"), world.logs.logger(), mcp.WithConsoleURL("https://pds.example"))
	schemas := map[string]any{}
	for _, tool := range listedTools(t, session) {
		schemas[tool.Name] = tool.OutputSchema
	}

	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"create_database", map[string]any{"name": "orders-dev", "environment": "acme-env", "profile": "dev"}},
		{"create_environment", map[string]any{"name": "spoke-new"}},
		{"delete_database", map[string]any{"id": "acme-env/pds.orders-dev", "confirm": "orders-dev"}},
	} {
		res := callTool(t, session, call.tool, call.args)

		require.False(t, res.IsError, "%s: %s", call.tool, text(t, res))
		assert.NoError(t, validates(t, schemas[call.tool], structured(t, res)), call.tool)
	}
}

func TestTheOtherChangeToolsAreServedByTheRealRouter(t *testing.T) {
	world := newTeamWorld(t)
	session := connectAs(t, world.srv, changer(auth.ClientChat, "Anthropic"), world.logs.logger())
	id := "acme-env/pds.orders-dev"
	require.False(t, callTool(t, session, "create_database", map[string]any{"name": "orders-dev", "environment": "acme-env", "profile": "dev"}).IsError)

	scaled := callTool(t, session, "scale_database", map[string]any{"id": id, "members": 3})
	backup := callTool(t, session, "backup_database", map[string]any{"id": id})
	schedule := callTool(t, session, "set_backup_schedule", map[string]any{"id": id, "enabled": true, "period": "daily", "hour": 3})
	restore := callTool(t, session, "restore_database", map[string]any{"id": id, "backupId": "missing", "name": "copy"})
	missing := callTool(t, session, "scale_database", map[string]any{"id": "acme-env/pds.nothing", "members": 3})

	for _, res := range []*sdk.CallToolResult{scaled, backup, schedule} {
		assert.False(t, res.IsError, text(t, res))
	}
	assert.Equal(t, map[string]any{"members": float64(3)}, structured(t, scaled)["requested"])
	assert.NotEmpty(t, structured(t, backup)["backupId"])
	assert.Equal(t, "daily", structured(t, schedule)["schedule"].(map[string]any)["period"])
	assert.Equal(t, "not_found", structured(t, restore)["kind"])
	assert.Equal(t, "not_found", structured(t, missing)["kind"])
	actions := map[any]bool{}
	for _, entry := range activityOf(t, session) {
		actions[entry["action"]] = true
		assert.Equal(t, "chat", entry["client"])
		assert.Equal(t, "Anthropic", entry["agent"])
	}
	for _, action := range []string{"database.create", "database.scale", "database.backup"} {
		assert.True(t, actions[action], action)
	}
}
