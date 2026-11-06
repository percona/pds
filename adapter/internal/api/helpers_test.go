package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
)

// TestMain points the default logger at a redacting discard handler. Calling
// Respond outside the router falls back to the default logger, and a test run
// must never print a cause that carries a credential.
func TestMain(m *testing.M) {
	slog.SetDefault(log.New(io.Discard, log.FormatJSON, slog.LevelDebug))

	os.Exit(m.Run())
}

// newTestServer builds the router with test configuration and no database
// service. Database routes that need a hub use newHubTestServer.
func newTestServer(t *testing.T) *api.Server {
	t.Helper()

	return newTestServerWithoutEverest(t)
}

// newSupportServer builds the router with the support service wired to a fake
// Percona Connector. A server built without one answers 501 on the support
// routes, which is the deployment with no ServiceNow Secret.
func newSupportServer(t *testing.T, fake *fakeServiceNow) *api.Server {
	t.Helper()

	client := fake.client(t)

	return api.NewServer(api.Deps{
		Config: testConfig(),
		Logger: log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:   auth.NewStub(),
		Support: service.NewSupport(client,
			service.WithSupportClock(func() time.Time { return supportNow }),
			service.WithSupportPortalURL(client.PortalURL()),
		),
	})
}

// newTestServerWithoutEverest builds the router with no engine registered.
func newTestServerWithoutEverest(t *testing.T) *api.Server {
	t.Helper()

	return api.NewServer(api.Deps{
		Config: testConfig(),
		Logger: log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:   auth.NewStub(),
	})
}

// testEverestNamespace is the Everest managed namespace the fake serves.
const testEverestNamespace = "pds"

// testBackupStorage is the Everest BackupStorage the test deployment has
// registered. The shipped default is empty, because nothing in this repository
// creates one, so the backup routes need it set to be exercised at all.
const testBackupStorage = "backups"

func testConfig() config.Config {
	return config.Config{
		Addr:             ":8080",
		Env:              config.EnvDev,
		EverestNamespace: testEverestNamespace,
		HubOperatorToken: testHubOperatorToken,
		HubGitDir:        "/tmp/hub-git",
		HubPublicURL:     "https://hub.example",
	}
}

func newHubTestServer(t *testing.T, token string) *api.Server {
	t.Helper()

	return newHubTestServerWithInventory(t, token, true)
}

func newEmptyHubTestServer(t *testing.T, token string) *api.Server {
	t.Helper()

	return newHubTestServerWithInventory(t, token, false)
}

func newHubTestServerWithInventory(t *testing.T, token string, seedSpoke bool) *api.Server {
	t.Helper()

	return newHubTestServerWithConfig(t, token, seedSpoke, nil)
}

func newHubTestServerWithoutPublicURL(t *testing.T, token string) *api.Server {
	t.Helper()

	return newHubTestServerWithConfig(t, token, false, func(cfg *config.Config) {
		cfg.HubPublicURL = ""
	})
}

func newHubTestServerWithJoinTTL(t *testing.T, token string, ttl time.Duration) *api.Server {
	t.Helper()

	return newHubTestServerWithConfig(t, token, false, func(cfg *config.Config) {
		cfg.JoinTokenTTL = ttl
	})
}

func newHubTestServerWithConfig(t *testing.T, token string, seedSpoke bool, mutate func(*config.Config)) *api.Server {
	t.Helper()

	return buildHubTestServer(t, token, seedSpoke, mutate, nil)
}

// newHubTestServerWithBook is a hub whose history book the test holds, so a
// route test can seed samples or read back what a heartbeat stored.
func newHubTestServerWithBook(t *testing.T, token string, book service.HistoryBook) *api.Server {
	t.Helper()

	activity := service.NewActivity(nil, service.ActivityOptions{})
	activity.UseBook(book)

	return buildHubTestServer(t, token, true, nil, activity)
}

func buildHubTestServer(
	t *testing.T,
	token string,
	seedSpoke bool,
	mutate func(*config.Config),
	activity *service.Activity,
) *api.Server {
	t.Helper()

	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	if seedSpoke {
		inv.Register("spoke-a", "pds")
	}
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	cfg := config.Config{
		Addr:                 ":8080",
		Env:                  config.EnvDev,
		EverestNamespace:     testEverestNamespace,
		HubGitDir:            "/tmp/hub-git",
		HubAgentToken:        token,
		HubOperatorToken:     testHubOperatorToken,
		HubAgentEnvironments: []string{"spoke-a", "eks-testing"},
		HubPublicURL:         "https://hub.example",
		HubGitURL:            "https://github.com/example/pds.git",
	}
	if mutate != nil {
		mutate(&cfg)
	}
	hubSvc := hub.NewService(store, inv, hub.Options{
		Namespace:     testEverestNamespace,
		BackupStorage: testBackupStorage,
		Now:           func() time.Time { return now },
		Tokens:        hub.NewTokenStore(nil, cfg.JoinTokenTTL),
	})
	opts := []service.Option{service.WithHub(hubSvc)}
	if activity != nil {
		opts = append(opts, service.WithRecorder(activity))
	}
	databases := service.NewDatabase(engine.NewRegistry(), opts...)
	var details *service.ActivityDetails
	if activity != nil {
		details = service.NewActivityDetails(activity, databases)
	}

	return api.NewServer(api.Deps{
		Config:          cfg,
		Logger:          log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:            auth.NewStub(),
		Databases:       databases,
		Activity:        activity,
		ActivityDetails: details,
	})
}

type stubGitHost struct {
	err    error
	keyErr error
	got    []string
	keys   []string
}

func (s *stubGitHost) EnsureRepo(_ context.Context, environment string) error {
	s.got = append(s.got, environment)
	return s.err
}

func (s *stubGitHost) EnsureCloneKey(_ context.Context, environment string) (string, string, error) {
	if s.keyErr != nil {
		return "", "", s.keyErr
	}
	s.keys = append(s.keys, environment)
	return stubCloneIdentity, stubClonePub, nil
}

func (s *stubGitHost) EnsureDeployKey(context.Context, string, string) error {
	return s.keyErr
}

func (s *stubGitHost) RevokeCloneKey(context.Context, string) error {
	return s.keyErr
}

const (
	stubCloneIdentity = "-----BEGIN OPENSSH PRIVATE KEY-----\ntest\n-----END OPENSSH PRIVATE KEY-----\n"
	stubClonePub      = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAItesthub pds-flux\n"
)

func newGitHubHubTestServer(t *testing.T, token string, host hub.GitHost) *api.Server {
	t.Helper()

	return newGitHubHubTestServerWithRegistry(t, token, host, "ghs_packages")
}

func newGitHubHubTestServerWithRegistry(t *testing.T, token string, host hub.GitHost, registryToken string) *api.Server {
	t.Helper()

	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	hubSvc := hub.NewService(store, inv, hub.Options{
		Namespace:     testEverestNamespace,
		BackupStorage: testBackupStorage,
		Now:           func() time.Time { return now },
		GitHost:       host,
	})
	databases := service.NewDatabase(engine.NewRegistry(), service.WithHub(hubSvc))

	return api.NewServer(api.Deps{
		Config: config.Config{
			Addr:                 ":8080",
			Env:                  config.EnvDev,
			EverestNamespace:     testEverestNamespace,
			HubAgentToken:        token,
			HubOperatorToken:     testHubOperatorToken,
			HubAgentEnvironments: []string{"spoke-a", "eks-testing"},
			HubPublicURL:         "https://hub.example",
			HubGitHubOrg:         "percona-cloud",
			HubGitHubToken:       "ghs_test",
			HubRegistryToken:     registryToken,
			HubAgentImage:        "ghcr.io/example/pds-adapter:github-hub",
		},
		Logger:    log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:      auth.NewStub(),
		Databases: databases,
	})
}

// testHubOperatorToken is the write-guard secret hub test servers expect.
const testHubOperatorToken = "test-operator"

// do runs one request against the test server and returns the recorder.
func do(t *testing.T, srv http.Handler, method, target string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(method, target, body)
	if hubOperatorRoute(method, target) {
		req.Header.Set("Authorization", "Bearer "+testHubOperatorToken)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	return rec
}

func hubOperatorRoute(method, target string) bool {
	path := strings.SplitN(target, "?", 2)[0]
	switch method {
	case http.MethodPost:
		if path == "/api/connect/tokens" || path == "/api/databases" {
			return true
		}
		if strings.HasPrefix(path, "/api/environments/") && strings.HasSuffix(path, "/support-access") {
			return true
		}
		if strings.HasPrefix(path, "/api/databases/") {
			return strings.HasSuffix(path, "/scale") ||
				strings.HasSuffix(path, "/backup") ||
				strings.HasSuffix(path, "/backup-schedule") ||
				strings.HasSuffix(path, "/restore")
		}
	case http.MethodDelete:
		if strings.HasPrefix(path, "/api/environments/") ||
			strings.HasPrefix(path, "/api/databases/") ||
			strings.HasPrefix(path, "/api/connections/") {
			return true
		}
	case http.MethodPatch:
		return strings.HasPrefix(path, "/api/environments/") && strings.HasSuffix(path, "/support-access")
	case http.MethodPut:
		return strings.HasPrefix(path, "/api/connections/") ||
			strings.HasPrefix(path, "/api/environments/")
	}

	return false
}

// decodeBody unmarshals a JSON response body into a generic map.
func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), "body: %s", rec.Body.String())

	return out
}

// newTestServerWithStore builds the router with a stand-in for the adapter's
// own database, so readiness can be driven without a live PostgreSQL.
func newTestServerWithStore(t *testing.T, store api.ReadyChecker) *api.Server {
	t.Helper()

	return api.NewServer(api.Deps{
		Config: testConfig(),
		Logger: log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:   auth.NewStub(),
		Store:  store,
	})
}
