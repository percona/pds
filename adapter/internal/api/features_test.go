package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/mysql"
	"github.com/percona/pds/adapter/internal/features"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
)

// newFeatureServer builds the router with a flag set on a hub. Database ids
// are environment/namespace.name, so a flag test has to use that form.
func newFeatureServer(t *testing.T, values map[string]bool) *api.Server {
	t.Helper()

	set, err := features.New(values)
	require.NoError(t, err)
	t.Cleanup(func() { _ = set.Close(context.Background()) })

	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	hubSvc := hub.NewService(store, inv, hub.Options{
		Namespace:     testEverestNamespace,
		BackupStorage: testBackupStorage,
		Now:           func() time.Time { return now },
	})

	return api.NewServer(api.Deps{
		Config:    testConfig(),
		Logger:    log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:      auth.NewStub(),
		Databases: service.NewDatabase(engine.NewRegistry(), service.WithHub(hubSvc)),
		Features:  set,
	})
}

// oneRoute names a request that a surface flag has to be able to turn off.
type oneRoute struct {
	flag   string
	method string
	target string
}

// gatedRoutes is every route a flag guards.
var gatedRoutes = []oneRoute{
	{features.Databases, http.MethodGet, "/api/databases"},
	{features.Clusters, http.MethodPost, "/api/connect/tokens"},
	{features.Clusters, http.MethodDelete, "/api/environments/spoke-a"},
	{features.Clusters, http.MethodGet, "/api/connect"},
	{features.Backups, http.MethodGet, "/api/databases/spoke-a/pds.db/backups"},
	{features.Connections, http.MethodGet, "/api/connections"},
	{features.Support, http.MethodGet, "/api/support/tickets"},
	{features.SupportAccess, http.MethodGet, "/api/environments/spoke-a/support-access"},
	{features.SupportAccess, http.MethodPost, "/api/environments/spoke-a/support-access"},
	{features.SupportAccess, http.MethodPatch, "/api/environments/spoke-a/support-access"},
	{features.SupportAccess, http.MethodDelete, "/api/environments/spoke-a/support-access"},
	{features.Chat, http.MethodPost, "/api/chat"},
	{features.Chat, http.MethodGet, "/api/chat/conversations"},
	{features.Chat, http.MethodGet, "/api/chat/conversations/abc"},
	{features.Chat, http.MethodDelete, "/api/chat/conversations/abc"},
	{features.Assistant, http.MethodGet, "/api/assistant/models"},
	{features.Metrics, http.MethodGet, "/api/databases/spoke-a/pds.db/metrics"},
	{features.Activity, http.MethodGet, "/api/activity"},
}

func TestDatabaseAlertsRouteRefusesAnIdWithoutAnEnvironment(t *testing.T) {
	srv := newFeatureServer(t, nil)

	// A one-segment id is not a database route.
	t.Run("single id", func(t *testing.T) {
		rec := do(t, srv, http.MethodGet, "/api/databases/pds.db/alerts", nil)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		body := decodeBody(t, rec)
		assert.Equal(t, "ErrValidation", body["code"])
		assert.Equal(t, "That database id is not valid.", body["message"])
		assert.NotContains(t, rec.Body.String(), "checkedAt")
		assert.NotContains(t, rec.Body.String(), "pendingReason")
		assert.NotContains(t, rec.Body.String(), "turned off")
	})

	t.Run("unknown database", func(t *testing.T) {
		rec := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.db/alerts", nil)

		assert.Equal(t, http.StatusNotFound, rec.Code)
		body := decodeBody(t, rec)
		assert.Equal(t, "ErrNotFound", body["code"])
		assert.Equal(t, "No database named `db`.", body["message"])
		assert.NotContains(t, rec.Body.String(), "checkedAt")
		assert.NotContains(t, rec.Body.String(), "pendingReason")
		assert.NotContains(t, rec.Body.String(), "turned off")
	})
}

func TestLogsRouteIsGone(t *testing.T) {
	srv := newFeatureServer(t, nil)

	t.Run("single id", func(t *testing.T) {
		rec := do(t, srv, http.MethodGet, "/api/databases/pds.db/logs", nil)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		body := decodeBody(t, rec)
		assert.Equal(t, "ErrValidation", body["code"])
		assert.Equal(t, "That database id is not valid.", body["message"])
		assert.NotContains(t, rec.Body.String(), "entries")
		assert.NotContains(t, rec.Body.String(), "turned off")
	})

	t.Run("unknown database", func(t *testing.T) {
		rec := do(t, srv, http.MethodGet, "/api/databases/spoke-a/pds.db/logs", nil)

		assert.Equal(t, http.StatusNotFound, rec.Code)
		body := decodeBody(t, rec)
		assert.Equal(t, "ErrNotFound", body["code"])
		assert.Equal(t, "That endpoint does not exist.", body["message"])
		assert.NotContains(t, rec.Body.String(), "entries")
		assert.NotContains(t, rec.Body.String(), "turned off")
	})
}

func TestPlatformStatusRouteIsGone(t *testing.T) {
	srv := newFeatureServer(t, nil)

	rec := do(t, srv, http.MethodGet, "/api/platform/status", nil)

	assert.Equal(t, http.StatusNotFound, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, "ErrNotFound", body["code"])
	assert.Equal(t, "That endpoint does not exist.", body["message"])
	assert.NotContains(t, rec.Body.String(), "Platform checks")
	assert.NotContains(t, rec.Body.String(), "canCreate")
	assert.NotContains(t, rec.Body.String(), "turned off")
}

func TestInvestigateRouteIsGone(t *testing.T) {
	srv := newFeatureServer(t, nil)

	// Both id shapes used to post here. The handler is gone, so neither path
	// answers with an investigation or with the flag gate. On the single-id
	// shape the second segment is a hub id, and that resource has no POST.
	t.Run("single id", func(t *testing.T) {
		rec := do(t, srv, http.MethodPost, "/api/databases/pds.db/investigate", strings.NewReader(`{}`))

		assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		body := decodeBody(t, rec)
		assert.Equal(t, "ErrValidation", body["code"])
		assert.Equal(t, "That method is not allowed on this endpoint.", body["message"])
		assert.NotContains(t, rec.Body.String(), "sentence")
		assert.NotContains(t, rec.Body.String(), "Investigate")
		assert.NotContains(t, rec.Body.String(), "turned off")
	})

	t.Run("unknown database", func(t *testing.T) {
		rec := do(t, srv, http.MethodPost, "/api/databases/spoke-a/pds.db/investigate", strings.NewReader(`{}`))

		assert.Equal(t, http.StatusNotFound, rec.Code)
		body := decodeBody(t, rec)
		assert.Equal(t, "ErrNotFound", body["code"])
		assert.Equal(t, "That endpoint does not exist.", body["message"])
		assert.NotContains(t, rec.Body.String(), "sentence")
		assert.NotContains(t, rec.Body.String(), "Investigate")
		assert.NotContains(t, rec.Body.String(), "turned off")
	})
}

func TestAlertAnalysisRouteIsGone(t *testing.T) {
	srv := newFeatureServer(t, nil)

	for _, method := range []string{http.MethodGet, http.MethodPut} {
		t.Run(method, func(t *testing.T) {
			rec := do(t, srv, method, "/api/alert-analysis", strings.NewReader(`{}`))

			assert.Equal(t, http.StatusNotFound, rec.Code)
			body := decodeBody(t, rec)
			assert.Equal(t, "ErrNotFound", body["code"])
			assert.Equal(t, "That endpoint does not exist.", body["message"])
			assert.NotContains(t, rec.Body.String(), "minSeverity")
			assert.NotContains(t, rec.Body.String(), "turned off")
		})
	}
}

func TestSurfaceOffAnswersNotImplemented(t *testing.T) {
	for _, route := range gatedRoutes {
		t.Run(route.flag, func(t *testing.T) {
			srv := newFeatureServer(t, map[string]bool{route.flag: false})

			rec := do(t, srv, route.method, route.target, nil)

			assert.Equal(t, http.StatusNotImplemented, rec.Code)
			body := decodeBody(t, rec)
			assert.Equal(t, "ErrNotImplemented", body["code"])
			assert.Contains(t, body["message"], "turned off",
				"the message says the surface is off, not that a downstream is missing")
		})
	}
}

func TestEverySurfaceOnLeavesTheRoutesReachable(t *testing.T) {
	srv := newFeatureServer(t, nil)

	for _, route := range gatedRoutes {
		t.Run(route.flag, func(t *testing.T) {
			rec := do(t, srv, route.method, route.target, nil)

			// Without downstreams most of these still fail, but never with the
			// message the flag gate writes.
			if rec.Code == http.StatusNotImplemented {
				body := decodeBody(t, rec)
				message, _ := body["message"].(string)
				assert.NotContains(t, message, "turned off")
			}
		})
	}
}

func TestDatabasesOffAlsoClosesItsNestedRoutes(t *testing.T) {
	srv := newFeatureServer(t, map[string]bool{features.Databases: false})

	for _, target := range []string{
		"/api/databases/spoke-a/pds.db",
		"/api/databases/spoke-a/pds.db/credential",
		"/api/databases/spoke-a/pds.db/backups",
		"/api/databases/spoke-a/pds.db/metrics",
	} {
		rec := do(t, srv, http.MethodGet, target, nil)

		assert.Equal(t, http.StatusNotImplemented, rec.Code, target)
		assert.Contains(t, decodeBody(t, rec)["message"], "turned off", target)
	}
}

func TestFeatureRouteIsNeverGated(t *testing.T) {
	srv := newFeatureServer(t, map[string]bool{
		features.Databases:   false,
		features.Connections: false,
	})

	rec := do(t, srv, http.MethodGet, "/api/features", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	flags, ok := decodeBody(t, rec)["flags"].(map[string]any)
	require.True(t, ok, "body: %s", rec.Body.String())

	assert.Len(t, flags, len(features.Names()))
	assert.Equal(t, false, flags[features.Databases])
	assert.Equal(t, false, flags[features.Connections])
	assert.Equal(t, true, flags[features.Support])
}

func TestEnvironmentsOffStillReportsHubMode(t *testing.T) {
	set, err := features.New(map[string]bool{features.Clusters: false})
	require.NoError(t, err)
	t.Cleanup(func() { _ = set.Close(context.Background()) })

	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	hubSvc := hub.NewService(store, inv, hub.Options{
		Namespace:     testEverestNamespace,
		BackupStorage: testBackupStorage,
		Now:           func() time.Time { return now },
	})
	srv := api.NewServer(api.Deps{
		Config: config.Config{
			Addr:             ":8080",
			Env:              config.EnvDev,
			EverestNamespace: testEverestNamespace,
			HubGitDir:        "/tmp/hub-git",
			HubAgentToken:    "secret",
			HubOperatorToken: testHubOperatorToken,
			HubPublicURL:     "https://hub.example",
			HubGitURL:        "https://github.com/example/pds.git",
		},
		Logger:    log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:      auth.NewStub(),
		Databases: service.NewDatabase(engine.NewRegistry(), service.WithHub(hubSvc)),
		Features:  set,
	})

	list := do(t, srv, http.MethodGet, "/api/environments", nil)
	assert.Equal(t, http.StatusOK, list.Code)
	body := decodeBody(t, list)
	assert.Equal(t, true, body["hub"])

	mint := do(t, srv, http.MethodPost, "/api/connect/tokens", strings.NewReader(`{"name":"eks-testing"}`))
	assert.Equal(t, http.StatusNotImplemented, mint.Code)
	assert.Contains(t, decodeBody(t, mint)["message"], "turned off")
}

func TestFeatureRouteOmitsARetiredName(t *testing.T) {
	const retired = "retired-surface"
	restore := features.SetRetiredForTest([]string{retired})
	t.Cleanup(restore)

	srv := newFeatureServer(t, nil)

	rec := do(t, srv, http.MethodGet, "/api/features", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	flags, ok := decodeBody(t, rec)["flags"].(map[string]any)
	require.True(t, ok, "body: %s", rec.Body.String())
	_, published := flags[retired]
	assert.False(t, published)
	assert.Len(t, flags, len(features.Names()))
}

func TestFeatureRoutePublishesEveryNameOnByDefault(t *testing.T) {
	srv := newFeatureServer(t, nil)

	rec := do(t, srv, http.MethodGet, "/api/features", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	flags := decodeBody(t, rec)["flags"].(map[string]any)

	for _, name := range features.Names() {
		assert.Equal(t, true, flags[name], "%s should default on", name)
	}
}

func TestFeatureRoutePublishesMCPAndHonorsItsFlag(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values map[string]bool
		want   bool
	}{
		{"default", nil, true},
		{"off", map[string]bool{features.MCP: false}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newFeatureServer(t, tc.values)

			rec := do(t, srv, http.MethodGet, "/api/features", nil)

			require.Equal(t, http.StatusOK, rec.Code)
			flags := decodeBody(t, rec)["flags"].(map[string]any)
			assert.Equal(t, tc.want, flags["mcp"])
		})
	}
}

func TestFeatureRouteOmitsTheRetiredProviderFlags(t *testing.T) {
	srv := newFeatureServer(t, nil)

	rec := do(t, srv, http.MethodGet, "/api/features", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	flags := decodeBody(t, rec)["flags"].(map[string]any)

	for _, name := range features.Retired() {
		_, published := flags[name]
		assert.False(t, published, "%s is retired and absent from GET /api/features", name)
	}
}

func TestWithoutAFlagSetNoRouteIsGated(t *testing.T) {
	// An adapter configured with no flags has to route exactly as it did
	// before flags existed, so nothing here may answer the gate's message.
	registry := engine.NewRegistry()
	registry.Register(mysql.New(newFakeEverest(t).client(t),
		mysql.WithNamespace(testEverestNamespace),
		mysql.WithBackupStorage(testBackupStorage),
	))

	srv := api.NewServer(api.Deps{
		Config:    testConfig(),
		Logger:    log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:      auth.NewStub(),
		Databases: service.NewDatabase(registry),
	})

	for _, route := range gatedRoutes {
		rec := do(t, srv, route.method, route.target, nil)

		if rec.Code == http.StatusNotImplemented {
			message, _ := decodeBody(t, rec)["message"].(string)
			assert.NotContains(t, message, "turned off", route.target)
		}
	}
}

func TestFeatureRouteWithoutAFlagSetServesTheWholeProduct(t *testing.T) {
	// A server built without a set is the deployment that never configured
	// flags, and it must behave as it did before flags existed.
	srv := api.NewServer(api.Deps{
		Config: testConfig(),
		Logger: log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:   auth.NewStub(),
	})

	rec := do(t, srv, http.MethodGet, "/api/features", nil)

	require.Equal(t, http.StatusOK, rec.Code)
	flags := decodeBody(t, rec)["flags"].(map[string]any)

	for _, name := range features.Names() {
		assert.Equal(t, true, flags[name], "%s should be on", name)
	}
}
