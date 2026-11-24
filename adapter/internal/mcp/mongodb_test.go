package mcp_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/mcp"
)

// createTool is create_database as a client lists it, with its properties.
func createTool(t *testing.T, session *mcp.Session) (*sdk.Tool, map[string]any) {
	t.Helper()

	for _, tool := range listedTools(t, session) {
		if tool.Name == "create_database" {
			schema, ok := tool.InputSchema.(map[string]any)
			require.True(t, ok)

			return tool, schema["properties"].(map[string]any)
		}
	}
	require.Fail(t, "create_database is not listed")

	return nil, nil
}

func TestTheEngineListOffersMongoDBOnlyWhenTheSwitchIsOn(t *testing.T) {
	for _, on := range []bool{false, true} {
		tool, properties := createTool(t, connectAt(t, nil, auth.AccessChange, mcp.WithMongoDB(on)))

		enum := properties["engine"].(map[string]any)["enum"]
		name := properties["name"].(map[string]any)["description"].(string)
		if !on {
			assert.Equal(t, []any{"mysql", "postgresql"}, enum)
			assert.Equal(t, "Lowercase letters, digits and dashes, at most 63 characters, 37 for postgresql.", name)
			assert.NotContains(t, strings.ToLower(tool.Description), "mongodb")

			continue
		}
		assert.Equal(t, []any{"mysql", "postgresql", "mongodb"}, enum)
		assert.Equal(t, "Lowercase letters, digits and dashes, at most 63 characters, 37 for postgresql, 22 for mongodb.", name)
		assert.Contains(t, tool.Description, "Create a MySQL, PostgreSQL or MongoDB database")
		assert.Contains(t, tool.Description, "engine is mysql, postgresql or mongodb")
		assert.Contains(t, tool.Description, "no access from outside the Kubernetes cluster")
	}
}

func TestTheSwitchIsOnByDefault(t *testing.T) {
	assert.True(t, mcp.MongoDBEnabled)
	_, properties := createTool(t, connectAt(t, nil, auth.AccessChange))

	assert.Equal(t, []any{"mysql", "postgresql", "mongodb"}, properties["engine"].(map[string]any)["enum"])
	assert.Equal(t, "MySQL, PostgreSQL and MongoDB", mcp.OfferedEngines())
}

func TestTheNewDatabasePromptDescriptionNamesTheOfferedEngines(t *testing.T) {
	for _, prompt := range listedPrompts(t, connectAt(t, nil, auth.AccessChange)) {
		if prompt.Name == "new_database" {
			assert.Contains(t, prompt.Description, "Create a MySQL, PostgreSQL or MongoDB database")

			return
		}
	}
	t.Fatal("new_database is not listed")
}

func TestTheGuidanceSaysWhatDiffersForMongoDBOnlyWhenTheSwitchIsOn(t *testing.T) {
	off := connectAt(t, nil, auth.AccessChangeDelete, mcp.WithMongoDB(false)).InitializeResult().Instructions
	on := connectAt(t, nil, auth.AccessChangeDelete, mcp.WithMongoDB(true)).InitializeResult().Instructions

	assert.NotContains(t, strings.ToLower(off), "mongodb")
	assert.Contains(t, off, "runs MySQL and PostgreSQL databases")
	assert.Contains(t, on, "runs MySQL, PostgreSQL and MongoDB databases")
	for _, fact := range []string{
		"no access from outside the Kubernetes cluster", "databaseAdmin", "command document", "ssl=false",
		"replicaSet=rs0", "22 characters", "Backups are logical",
	} {
		assert.Contains(t, on, fact)
	}
	assert.LessOrEqual(t, len(on), maxInstructionsLength, "Claude Code cuts the instructions here")
	assert.NotContains(t, on, "{{")
}

func TestThePromptTakesMongoDBOnlyWhenTheSwitchIsOn(t *testing.T) {
	args := map[string]string{"engine": "mongodb", "environment": "spoke-a", "name": "orders"}

	_, err := getPrompt(t, connectAt(t, nil, auth.AccessChange, mcp.WithMongoDB(false)), "new_database", args)
	require.Error(t, err)

	on := connectAt(t, nil, auth.AccessChange, mcp.WithMongoDB(true))
	text := promptText(t, on, "new_database", args)
	assert.Contains(t, text, "Use mongodb.")
	assert.Contains(t, text, "no access from outside the Kubernetes cluster")
	assert.NotContains(t, text, "VPC")
	assert.Contains(t, promptText(t, on, "new_database", nil), "MySQL, PostgreSQL or MongoDB")

	for _, prompt := range listedPrompts(t, on) {
		if prompt.Name == "new_database" {
			assert.Equal(t, "The engine, mysql, postgresql or mongodb. Asked for when left out.", prompt.Arguments[0].Description)
		}
	}
}

func asChangeDelete(c auth.Caller) auth.Caller {
	c.Access = auth.AccessChangeDelete

	return c
}

func TestWithTheSwitchOnTheCreateToolMakesAMongoDBDatabase(t *testing.T) {
	world := newTeamWorld(t)
	session := connectAs(t, world.srv, asChangeDelete(ann), world.logs.logger(), mcp.WithMongoDB(true))

	created := callTool(t, session, "create_database", map[string]any{
		"engine": "mongodb", "name": "orders", "environment": "acme-env", "profile": "dev",
	})
	require.False(t, created.IsError, text(t, created))
	assert.Equal(t, "acme-env/pds.orders", structured(t, created)["id"])

	got := structured(t, callTool(t, session, "get_database", map[string]any{"id": "acme-env/pds.orders"}))
	assert.Equal(t, "mongodb", got["engine"])
}

func TestWithTheSwitchOnAMongoDBNameOf23CharactersIsRefusedWithTheHubsSentence(t *testing.T) {
	world := newTeamWorld(t)
	session := connectAs(t, world.srv, asChangeDelete(ann), world.logs.logger(), mcp.WithMongoDB(true))

	res := callTool(t, session, "create_database", map[string]any{
		"engine": "mongodb", "name": strings.Repeat("a", 23), "environment": "acme-env", "profile": "dev",
	})

	require.True(t, res.IsError)
	assert.Contains(t, structured(t, res)["message"], "A MongoDB database name must be between 1 and 22 characters.")
	fits := callTool(t, session, "create_database", map[string]any{
		"engine": "mongodb", "name": strings.Repeat("a", 22), "environment": "acme-env", "profile": "dev",
	})
	assert.False(t, fits.IsError, text(t, fits))
}

func TestWithTheSwitchOffTheCreateToolRefusesMongoDB(t *testing.T) {
	world := newTeamWorld(t)
	session := connectAs(t, world.srv, asChangeDelete(ann), world.logs.logger(), mcp.WithMongoDB(false))

	res := callTool(t, session, "create_database", map[string]any{
		"engine": "mongodb", "name": "orders", "environment": "acme-env", "profile": "dev",
	})

	require.True(t, res.IsError)
	assert.Equal(t, "invalid_input", structured(t, res)["kind"])
}

// createMongoDB makes a MongoDB database the way the API takes it, whatever the
// registry's switch says.
func (w *teamWorld) createMongoDB(t *testing.T, as auth.Caller, environment, name string) {
	t.Helper()

	body := `{"name":"` + name + `","engine":"mongodb","profile":"dev","environment":"` + environment + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/databases", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	w.srv.ServeHTTP(rec, req.WithContext(auth.WithCaller(req.Context(), as)))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}

// The switch decides what can be created. A database that exists is served by
// the read, wait and change tools.
func TestWithTheSwitchOffTheToolsStillWorkOnAMongoDBDatabaseThatExists(t *testing.T) {
	world := newTeamWorld(t)
	world.createMongoDB(t, ann, "acme-env", "orders")
	session := connectAs(t, world.srv, asChangeDelete(ann), world.logs.logger())
	id := "acme-env/pds.orders"

	list := structured(t, callTool(t, session, "list_databases", nil))["databases"].([]any)
	require.Len(t, list, 1)
	assert.Equal(t, "mongodb", list[0].(map[string]any)["engine"])

	for tool, args := range map[string]map[string]any{
		"get_database":        {"id": id},
		"get_metrics":         {"id": id},
		"list_backups":        {"id": id},
		"get_database_usage":  {"id": id},
		"get_database_alerts": {"id": id},
		"scale_database":      {"id": id, "storageGb": 40},
		"wait_for_database":   {"id": id, "until": "ready", "timeoutSeconds": 1},
	} {
		res := callTool(t, session, tool, args)

		assert.False(t, res.IsError, "%s: %s", tool, text(t, res))
	}

	deleted := callTool(t, session, "delete_database", map[string]any{"id": id, "confirm": "orders"})
	assert.False(t, deleted.IsError, text(t, deleted))
}

func TestAMongoDBDatabaseIsDescribedWithItsConnectionAndNoPassword(t *testing.T) {
	srv, logs := newSecretWorld(t)
	session := connectAs(t, srv, readCaller(), logs.logger())

	res := callTool(t, session, "get_database", map[string]any{"id": "spoke-a/pds.orders-mongo"})

	require.False(t, res.IsError, text(t, res))
	got := structured(t, res)
	assert.Equal(t, "mongodb", got["engine"])
	connection := got["connection"].(map[string]any)
	assert.Equal(t, "databaseAdmin", connection["username"])
	assert.Equal(t,
		"mongodb://databaseAdmin:••••••••@"+strings.Join(mongoMembers, ",")+"/admin?replicaSet=rs0&ssl=false",
		connection["maskedUri"])
	assert.NotContains(t, text(t, res), passwordSentinel)
	assert.NotContains(t, logs.String(), passwordSentinel)
}
