package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/client/pmm"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/postgresql"
	"github.com/percona/pds/adapter/internal/features"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
	"github.com/percona/pds/adapter/internal/store"
	"github.com/percona/pds/adapter/internal/testutil"
)

// testLogger discards records so a test does not print the adapter's warnings.
func testLogger() *slog.Logger {
	return log.New(io.Discard, log.FormatJSON, slog.LevelDebug)
}

// TestStoreReadyCheckerIsUntypedNilWithoutADatabase is the whole reason that
// function exists. A nil *store.Starter assigned straight to the interface
// would be a non nil interface value holding a nil pointer, and every readiness
// probe would then call through it.
func TestStoreReadyCheckerIsUntypedNilWithoutADatabase(t *testing.T) {
	var absent *store.Starter

	got := storeReadyChecker(absent)

	assert.Nil(t, got, "readiness has to see no store at all, not a nil one it will call")
}

func TestStoreReadyCheckerPassesAStoreThrough(t *testing.T) {
	present := store.Start(t.Context(), store.Options{
		URL: "postgres://hub:pw@127.0.0.1:1/hub?sslmode=disable",
	})
	t.Cleanup(func() { require.NoError(t, present.Close()) })

	got := storeReadyChecker(present)

	require.NotNil(t, got)
	assert.Error(t, got.Ready(t.Context()), "an unreachable database reports through the checker")
}

func TestOpenStoreReturnsNothingWithoutAURL(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{"unset", config.Config{}, false},
		{"blank", config.Config{DatabaseURL: "   "}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := openStore(t.Context(), tc.cfg, testLogger())

			if !tc.want {
				assert.Nil(t, got)
			}
		})
	}
}

// testKey is a usable 32 byte key. The value does not matter, only the length
// and that it is base64.
func testKey() string {
	return base64.StdEncoding.EncodeToString(make([]byte, 32))
}

func TestNewHubStateReturnsNothingWithoutADatabase(t *testing.T) {
	state, err := newHubState(config.Config{}, nil, testLogger())

	require.NoError(t, err)
	assert.Nil(t, state)
}

// TestNewHubStateRefusesAMissingKey is the difference between a hub that says
// what is wrong at start and one that sits out of the Service forever: without
// a key the import cannot seal the clone identities it finds, so hydration
// fails on every retry and readiness never goes green.
func TestNewHubStateRefusesAMissingKey(t *testing.T) {
	starter := store.Start(t.Context(), store.Options{URL: "postgres://hub:pw@127.0.0.1:1/hub?sslmode=disable"})
	t.Cleanup(func() { _ = starter.Close() })

	state, err := newHubState(config.Config{}, starter, testLogger())

	require.Error(t, err)
	assert.Nil(t, state)
	assert.Contains(t, err.Error(), "STATE_ENCRYPTION_KEY")
}

func TestNewHubStateRefusesAnUnusableKey(t *testing.T) {
	starter := store.Start(t.Context(), store.Options{URL: "postgres://hub:pw@127.0.0.1:1/hub?sslmode=disable"})
	t.Cleanup(func() { _ = starter.Close() })

	tests := []struct {
		name string
		cfg  config.Config
	}{
		{"not base64", config.Config{StateEncryptionKey: "not base64 %%%"}},
		{"wrong length", config.Config{StateEncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 16))}},
		{"retired key unusable", config.Config{
			StateEncryptionKey:         testKey(),
			StateEncryptionKeysRetired: []string{"not base64 %%%"},
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newHubState(tc.cfg, starter, testLogger())

			require.Error(t, err)
		})
	}
}

func TestNewHubStateNeverLogsTheKey(t *testing.T) {
	starter := store.Start(t.Context(), store.Options{URL: "postgres://hub:pw@127.0.0.1:1/hub?sslmode=disable"})
	t.Cleanup(func() { _ = starter.Close() })

	key := base64.StdEncoding.EncodeToString([]byte("a key that is thirty two bytes!!"))
	var out bytes.Buffer
	logger := log.New(&out, log.FormatJSON, slog.LevelDebug)

	_, err := newHubState(config.Config{StateEncryptionKey: key}, starter, logger)
	require.NoError(t, err)

	assert.NotContains(t, out.String(), key, "the key opens every stored credential, so it must not reach a log")
	assert.Contains(t, out.String(), "organization")
}

// TestHubPersistsPicksTheDatabaseWhenThereIsOne is the swap this change is
// about. With a database the rows are the store, and the objects are not read.
func TestHubPersistsPicksTheDatabaseWhenThereIsOne(t *testing.T) {
	starter := store.Start(t.Context(), store.Options{URL: "postgres://hub:pw@127.0.0.1:1/hub?sslmode=disable"})
	t.Cleanup(func() { _ = starter.Close() })
	state := store.NewLazy(starter, 1, nil)

	persists := hubPersists(config.Config{}, nil, state, 1)

	assert.IsType(t, &hub.DBInventoryPersist{}, persists.Inventory)
	assert.IsType(t, &hub.DBTokenPersist{}, persists.Tokens)
	assert.IsType(t, &hub.DBWorkPersist{}, persists.Work)
	assert.IsType(t, &hub.DBCloneKeyPersist{}, persists.CloneKeys)
}

// TestHubPersistsKeepsTheObjectsWithoutADatabase is the laptop run: the
// ConfigMaps and Secrets stay the store, when there is a cluster to hold them.
func TestHubPersistsKeepsTheObjectsWithoutADatabase(t *testing.T) {
	none := hubPersists(config.Config{}, nil, nil, 1)

	assert.Nil(t, none.Inventory, "no cluster and no database keeps the state in memory")
	assert.Nil(t, none.Tokens)
}

func TestTokenTeamLookupIsNilWithoutADatabase(t *testing.T) {
	assert.Nil(t, tokenTeamLookup(nil))
}

func TestTokenTeamLookupPassesAStarterThrough(t *testing.T) {
	starter := store.Start(t.Context(), store.Options{URL: "postgres://hub:pw@127.0.0.1:1/hub?sslmode=disable"})
	t.Cleanup(func() { _ = starter.Close() })

	assert.NotNil(t, tokenTeamLookup(starter))
}

// identityIn is a caller acting in the Team.
func identityIn(team uint) auth.Identity {
	return auth.Identity{Scope: auth.Scope{Team: auth.Team{ID: team}}}
}

// fixedTeam is the Team a call with no request Team acts in.
func fixedTeam(team uint) service.TeamFallback {
	return func(context.Context) uint { return team }
}

func TestServedTeamIsNoneWhenEveryOrganizationIsServed(t *testing.T) {
	assert.Nil(t, servedTeam(config.Config{Organization: "*"}, nil))
}

func TestServedTeamWithoutADatabaseIsTheOnlyOne(t *testing.T) {
	fallback := servedTeam(config.Config{}, nil)

	require.NotNil(t, fallback)
	assert.Equal(t, noDatabaseTeam, fallback(t.Context()))
}

func TestServedTeamIsZeroBeforeTheDatabaseConnects(t *testing.T) {
	starter := store.Start(t.Context(), store.Options{
		URL:            "postgres://hub:pw@127.0.0.1:1/hub?sslmode=disable",
		ConnectTimeout: time.Second,
		RetryBackoff:   time.Hour,
	})
	t.Cleanup(func() { _ = starter.Close() })

	fallback := servedTeam(config.Config{Organization: "acme"}, starter)

	require.NotNil(t, fallback)
	assert.Zero(t, fallback(t.Context()), "there is no team to act in until the database answers")
}

func liveTestURL(t *testing.T) string {
	t.Helper()

	url := strings.TrimSpace(os.Getenv("ADAPTER_TEST_DATABASE_URL"))
	if url == "" {
		t.Skip("set ADAPTER_TEST_DATABASE_URL to run the served team lookup against a live PostgreSQL")
	}

	return url
}

func TestServedTeamIsTheOldestTeamOfTheServedOrganization(t *testing.T) {
	url := liveTestURL(t)
	organization := "served-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-"))
	starter := store.Start(t.Context(), store.Options{URL: url, Organization: organization})
	t.Cleanup(func() { _ = starter.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	_, err := store.NewLazy(starter, 0, nil).Wait(ctx)
	require.NoError(t, err)

	got := servedTeam(config.Config{Organization: organization}, starter)(ctx)

	team, err := starter.Store().Identity().ServedTeam(ctx, organization)
	require.NoError(t, err)
	assert.Equal(t, team.ID, got)
	assert.NotZero(t, got)
}

func TestReadyCheckerIsUntypedNilWhenNothingIsChecked(t *testing.T) {
	assert.Nil(t, readyChecker(nil, nil))
}

func TestReadyCheckerReportsTheFirstDependencyThatIsNotUp(t *testing.T) {
	ok := readyStub{}
	bad := readyStub{err: errors.New("not up yet")}

	require.NoError(t, readyChecker(ok, ok).Ready(t.Context()))
	require.Error(t, readyChecker(ok, bad).Ready(t.Context()))
	require.Error(t, readyChecker(bad, ok).Ready(t.Context()))
	require.NoError(t, readyChecker(nil, ok).Ready(t.Context()))
}

// readyStub is one readiness answer.
type readyStub struct{ err error }

func (r readyStub) Ready(context.Context) error { return r.err }

func TestHubReadyCheckerIsUntypedNilWithoutAHydrator(t *testing.T) {
	assert.Nil(t, hubReadyChecker(nil))
}

// A laptop run has no environment client, and that is ordinary: its state is
// kept in the database.
func TestNewDatabaseServiceAllowsAHubOutsideAnEnvironment(t *testing.T) {
	starter := store.Start(t.Context(), store.Options{URL: "postgres://hub:pw@127.0.0.1:1/hub?sslmode=disable"})
	t.Cleanup(func() { _ = starter.Close() })

	cfg := config.Config{HubGitDir: t.TempDir()}

	_, hydrator, err := newDatabaseService(context.Background(), cfg, nil, nil, nil, store.NewLazy(starter, 1, nil), fixedTeam(1), nil, testLogger())
	if hydrator != nil {
		t.Cleanup(func() { _ = hydrator.Close() })
	}

	require.NoError(t, err)
}

func TestNewDatabaseServiceOmitsAHydratorWhenEveryOrganizationIsServed(t *testing.T) {
	cfg := config.Config{HubGitDir: t.TempDir(), Organization: "*"}

	svc, hydrator, err := newDatabaseService(context.Background(), cfg, nil, nil, nil, store.NewLazy(nil, 0, nil), nil, nil, testLogger())
	require.NoError(t, err)
	require.NotNil(t, svc)
	assert.Nil(t, hydrator)
	assert.Nil(t, hubReadyChecker(hydrator))
}

func TestNewDatabaseServiceKeepsAHydratorForANamedOrganization(t *testing.T) {
	starter := store.Start(t.Context(), store.Options{URL: "postgres://hub:pw@127.0.0.1:1/hub?sslmode=disable"})
	t.Cleanup(func() { _ = starter.Close() })

	cfg := config.Config{HubGitDir: t.TempDir(), Organization: "acme"}
	_, hydrator, err := newDatabaseService(context.Background(), cfg, nil, nil, nil, store.NewLazy(starter, 1, nil), fixedTeam(1), nil, testLogger())
	if hydrator != nil {
		t.Cleanup(func() { _ = hydrator.Close() })
	}

	require.NoError(t, err)
	require.NotNil(t, hydrator)
	assert.NotNil(t, hubReadyChecker(hydrator))
}

func TestNewDatabaseServiceHubFollowsTheRequestTeam(t *testing.T) {
	cfg := config.Config{HubGitDir: t.TempDir(), Organization: "*"}
	svc, hydrator, err := newDatabaseService(context.Background(), cfg, nil, nil, nil, nil, nil, nil, testLogger())
	if hydrator != nil {
		t.Cleanup(func() { _ = hydrator.Close() })
	}
	require.NoError(t, err)
	require.NotNil(t, svc)

	acme, err := svc.Hub(auth.WithIdentity(t.Context(), identityIn(1)))
	require.NoError(t, err)
	globex, err := svc.Hub(auth.WithIdentity(t.Context(), identityIn(2)))
	require.NoError(t, err)
	require.NotNil(t, acme)
	require.NotNil(t, globex)
	assert.NotSame(t, acme, globex)
}

func TestNewActivityServiceIsNilWhenEveryOrganizationIsServed(t *testing.T) {
	assert.Nil(t, newActivityService(config.Config{Organization: "*"}, nil))
}

func TestNewActivityServiceIsBuiltForANamedOrganization(t *testing.T) {
	assert.NotNil(t, newActivityService(config.Config{Organization: "acme"}, nil))
}

func TestNewDatabaseServiceAgentWithoutEverestIsNil(t *testing.T) {
	svc, hydrator, err := newDatabaseService(context.Background(), config.Config{Role: config.RoleAgent}, nil, nil, nil, nil, nil, nil, testLogger())
	require.NoError(t, err)
	assert.Nil(t, svc)
	assert.Nil(t, hydrator)
}

func TestNewDatabaseServiceAgentBuildsTheRegistry(t *testing.T) {
	client, err := everest.New(everest.Options{BaseURL: "http://127.0.0.1:9"})
	require.NoError(t, err)

	svc, hydrator, err := newDatabaseService(context.Background(), config.Config{
		Role: config.RoleAgent, EverestNamespace: "pds",
	}, nil, nil, client, nil, nil, nil, testLogger())
	require.NoError(t, err)
	require.NotNil(t, svc)
	assert.Nil(t, hydrator)

	got, err := svc.Hub(t.Context())
	require.NoError(t, err)
	assert.Nil(t, got, "an agent has no Git backend")

	_, err = svc.List(t.Context())
	require.Error(t, err)
}

func TestNewPMMServicesFollowsTheRole(t *testing.T) {
	httpMetrics, httpAlerts, err := newPMMServices(config.Config{Role: config.RoleHTTP}, testLogger())
	require.NoError(t, err)
	require.NotNil(t, httpMetrics)

	agentMetrics, agentAlerts, err := newPMMServices(config.Config{Role: config.RoleAgent}, testLogger())
	require.NoError(t, err)
	require.NotNil(t, agentMetrics)

	withURL, urlAlerts, err := newPMMServices(config.Config{
		Role: config.RoleAgent, PMMURL: "http://127.0.0.1:9",
	}, testLogger())
	require.NoError(t, err)
	require.NotNil(t, withURL)
	assert.Nil(t, httpAlerts, "the HTTP role reads no PMM")
	assert.Nil(t, agentAlerts, "an agent with no PMM_URL has no alert reader")
	assert.NotNil(t, urlAlerts)

	monitor := agentMonitor(agentMetrics, testLogger())
	reading := monitor(t.Context(), engine.Instance{Name: "orders", Namespace: "pds"})
	require.NotNil(t, reading)

	absent := monitoringExists(nil, "pds", "monitoring")
	ok, err := absent(t.Context())
	require.NoError(t, err)
	assert.False(t, ok)

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = monitoringExists(clientOrNil(t), "pds", "monitoring")(ctx)
	require.Error(t, err)
}

func clientOrNil(t *testing.T) *everest.Client {
	t.Helper()

	client, err := everest.New(everest.Options{BaseURL: "http://127.0.0.1:9"})
	require.NoError(t, err)

	return client
}

func TestRepoPrefixOfIsBlankWithoutADatabase(t *testing.T) {
	prefix, err := repoPrefixOf(nil, 1)(t.Context())

	require.NoError(t, err)
	assert.Empty(t, prefix, "new environments are named after the environment alone")
}

func TestRepoPrefixOfFailsWhileTheDatabaseIsNotConnected(t *testing.T) {
	_, err := repoPrefixOf(store.NewLazy(nil, 1, nil), 1)(t.Context())

	require.ErrorIs(t, err, store.ErrNotConnected)
}

func TestNewOIDCClientIsAbsentWithoutAnIssuer(t *testing.T) {
	got := newOIDCClient(t.Context(), config.Config{}, testLogger())

	assert.Nil(t, got, "an empty OIDC_ISSUER leaves only the password login")
}

func TestNewOIDCClientDiscoversAReachableIssuer(t *testing.T) {
	issuer := testutil.NewFakeIssuer(t)

	got := newOIDCClient(t.Context(), config.Config{
		OIDCIssuer:       issuer.URL,
		HubPublicURL:     "https://pds.example",
		OIDCClientID:     testutil.IssuerClientID,
		OIDCClientSecret: testutil.IssuerClientSecret,
	}, testLogger())

	require.NotNil(t, got)
	assert.Equal(t, issuer.URL, got.Issuer())
}

func TestNewOIDCClientFallsBackWhenDiscoveryFails(t *testing.T) {
	var logged bytes.Buffer
	logger := log.New(&logged, log.FormatJSON, slog.LevelDebug)
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close()

	got := newOIDCClient(t.Context(), config.Config{
		OIDCIssuer: down.URL, HubPublicURL: "https://pds.example", OIDCClientID: "id", OIDCClientSecret: "client-secret-value",
	}, logger)

	assert.Nil(t, got, "a bad issuer must not lock customers out of the password login")
	assert.Contains(t, logged.String(), "password login")
	assert.NotContains(t, logged.String(), "client-secret-value")
}

func TestNewOIDCClientFallsBackWithoutAPublicURL(t *testing.T) {
	issuer := testutil.NewFakeIssuer(t)

	got := newOIDCClient(t.Context(), config.Config{
		OIDCIssuer: issuer.URL, OIDCClientID: testutil.IssuerClientID, OIDCClientSecret: testutil.IssuerClientSecret,
	}, testLogger())

	assert.Nil(t, got, "the redirect address must come from configuration, not from a request")
}

func TestEngineRegistryServesBothEngines(t *testing.T) {
	registry := engineRegistry(config.Config{}, nil)

	for _, id := range []string{"mysql", "postgresql"} {
		plugin, err := registry.Get(id)
		require.NoError(t, err, id)
		assert.Equal(t, id, plugin.Engine())
	}
}

type recordingQuerier struct{ queries []string }

func (r *recordingQuerier) QueryRange(_ context.Context, q pmm.RangeQuery) ([]pmm.Series, error) {
	r.queries = append(r.queries, q.Query)

	return nil, nil
}

// The agent's sample loop is the one place a reading is taken, so it has to
// hand each database's engine to the queries.
func TestAgentMonitorAsksAboutTheDatabaseEngine(t *testing.T) {
	querier := &recordingQuerier{}
	monitor := agentMonitor(service.NewMetrics(querier), testLogger())

	monitor(t.Context(), engine.Instance{Name: "reports", Namespace: "db", Engine: postgresql.EngineID})

	require.NotEmpty(t, querier.queries)
	assert.Contains(t, querier.queries[0], `pg_up{cluster="reports"}`)
}

func TestNoSupportNetworkIsANilInterfaceSoTheHubMakesNoCall(t *testing.T) {
	got, err := newSupportTailnet(config.Config{})

	require.NoError(t, err)
	assert.True(t, got == nil)
}

func TestConfiguredSupportNetworkBuildsAClient(t *testing.T) {
	got, err := newSupportTailnet(config.Config{SupportTailnet: config.SupportTailnet{
		Name: "example.com", Domain: "tail1234.ts.net", KeyClientID: "a", KeyClientSecret: "b", ServiceClientID: "c", ServiceClientSecret: "d",
		EnvironmentTag: "tag:env", ServiceTag: "tag:svc",
	}})

	require.NoError(t, err)
	assert.NotNil(t, got)
}

func TestAnExpiredWindowIsRecordedAsDoneBySystem(t *testing.T) {
	activity := service.NewActivity(nil, service.ActivityOptions{})

	supportExpired(activity, nil, 1)(t.Context(), "eks-prod", hub.SupportWindow{})

	page, err := activity.List(t.Context(), service.ActivityQuery{})
	require.NoError(t, err)
	require.Len(t, page.Entries, 1)
	assert.Equal(t, service.ActionSupportAccessExpire, page.Entries[0].Action)
	assert.Equal(t, "system", page.Entries[0].Actor)
}

func TestSurfaceFlagsTurnSupportAccessOffWithoutTheNetwork(t *testing.T) {
	for _, tc := range []struct {
		name     string
		operator map[string]bool
	}{
		{"unset", nil},
		{"operator turned it on", map[string]bool{features.SupportAccess: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := surfaceFlags(config.Config{Features: tc.operator})

			assert.Equal(t, false, got[features.SupportAccess])
		})
	}
}

func TestSurfaceFlagsLeaveSupportAccessToTheOperatorWithTheNetwork(t *testing.T) {
	network := config.SupportTailnet{Name: "example.com"}
	for _, tc := range []struct {
		name     string
		operator map[string]bool
		want     map[string]bool
	}{
		{"unset", nil, map[string]bool{}},
		{"operator turned it off", map[string]bool{features.SupportAccess: false}, map[string]bool{features.SupportAccess: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := surfaceFlags(config.Config{Features: tc.operator, SupportTailnet: network})

			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSurfaceFlagsLeaveTheOperatorsSetAsItWas(t *testing.T) {
	operator := map[string]bool{features.Chat: false}

	got := surfaceFlags(config.Config{Features: operator})

	assert.Equal(t, map[string]bool{features.Chat: false}, operator)
	assert.Equal(t, false, got[features.Chat])
}
