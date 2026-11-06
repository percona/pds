package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/config"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
	"github.com/percona/pds/adapter/internal/log"
	"github.com/percona/pds/adapter/internal/service"
)

// teamHeader lets a test pick the signed-in team per request.
type teamHeader struct{}

func (teamHeader) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		team, _ := strconv.ParseUint(r.Header.Get("X-Team"), 10, 64)
		ctx := auth.WithIdentity(r.Context(), identityIn(uint(team)))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type alertsLab struct {
	now time.Time
	hub *hub.Service
	srv *api.Server
}

func newAlertsLab(t *testing.T, environments ...string) *alertsLab {
	t.Helper()

	lab := &alertsLab{now: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
	inv := hub.NewInventory(time.Minute)
	for _, name := range environments {
		inv.Register(name, "pds")
	}
	lab.hub = hub.NewService(hub.NewMemoryStore(), inv, hub.Options{
		Namespace:     testEverestNamespace,
		BackupStorage: testBackupStorage,
		Now:           func() time.Time { return lab.now },
		Tokens:        hub.NewTokenStore(nil, 0),
	})
	lab.srv = newAlertsServer(t, map[uint]*hub.Service{1: lab.hub})

	return lab
}

func newAlertsServer(t *testing.T, hubs map[uint]*hub.Service) *api.Server {
	t.Helper()

	databases := service.NewDatabase(engine.NewRegistry(), service.WithHubFor(func(ctx context.Context) (*hub.Service, error) {
		team, _ := auth.TeamFromContext(ctx)
		svc, ok := hubs[team]
		if !ok {
			return nil, context.Canceled
		}

		return svc, nil
	}))

	return api.NewServer(api.Deps{
		Config:    config.Config{Addr: ":8080", Env: config.EnvDev, EverestNamespace: testEverestNamespace},
		Logger:    log.New(io.Discard, log.FormatJSON, slog.LevelDebug),
		Auth:      teamHeader{},
		Databases: databases,
	})
}

func (l *alertsLab) beat(environment string, dbs []string, backups []engine.Backup, alerts *hub.HeartbeatAlerts) {
	beatTo(l.hub, environment, dbs, backups, alerts)
}

func beatTo(svc *hub.Service, environment string, dbs []string, backups []engine.Backup, alerts *hub.HeartbeatAlerts) {
	var instances []engine.Instance
	for _, name := range dbs {
		instances = append(instances, engine.Instance{Name: name, Namespace: "pds"})
	}
	svc.ApplyHeartbeat(hub.Heartbeat{
		Environment: environment, Namespace: "pds", Instances: instances, Backups: backups, Alerts: alerts,
	})
}

func liveAlerts(at time.Time, items ...hub.HeartbeatAlert) *hub.HeartbeatAlerts {
	if items == nil {
		items = []hub.HeartbeatAlert{}
	}

	return &hub.HeartbeatAlerts{At: at, Total: len(items), Items: items}
}

func firing(fingerprint, rule, severity string, since time.Time, labels map[string]string) hub.HeartbeatAlert {
	return hub.HeartbeatAlert{
		Fingerprint: fingerprint, Rule: rule, Severity: severity,
		Summary: rule + " is firing", StartsAt: since, Labels: labels,
	}
}

type alertAction struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

type alertRow struct {
	ID          string            `json:"id"`
	Environment string            `json:"environment"`
	DatabaseID  string            `json:"databaseId"`
	Rule        string            `json:"rule"`
	Severity    string            `json:"severity"`
	Summary     string            `json:"summary"`
	StartsAt    time.Time         `json:"startsAt"`
	Labels      map[string]string `json:"labels"`
	Action      alertAction       `json:"action"`
}

type alertEnvironment struct {
	Name    string       `json:"name"`
	State   string       `json:"state"`
	Pending string       `json:"pending"`
	Reason  string       `json:"reason"`
	Action  *alertAction `json:"action"`
	ReadAt  *time.Time   `json:"readAt"`
	Total   int          `json:"total"`
	Shown   int          `json:"shown"`
	Note    string       `json:"note"`
}

type alertsBody struct {
	Environments []alertEnvironment `json:"environments"`
	Alerts       []alertRow         `json:"alerts"`
	Criticals    int                `json:"criticals"`
}

type databaseAlertsBody struct {
	DatabaseID  string           `json:"databaseId"`
	Environment alertEnvironment `json:"environment"`
	Alerts      []alertRow       `json:"alerts"`
}

func getJSON[T any](t *testing.T, srv http.Handler, team uint, path string, want int) T {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-Team", strconv.FormatUint(uint64(team), 10))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	require.Equal(t, want, rec.Code, rec.Body.String())

	var out T
	if want == http.StatusOK {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	}

	return out
}

func (l *alertsLab) all(t *testing.T) alertsBody {
	t.Helper()

	return getJSON[alertsBody](t, l.srv, 1, "/api/alerts", http.StatusOK)
}

func environmentNamed(t *testing.T, body alertsBody, name string) alertEnvironment {
	t.Helper()

	for _, env := range body.Environments {
		if env.Name == name {
			return env
		}
	}
	require.Failf(t, "environment missing", "no %s in %+v", name, body.Environments)

	return alertEnvironment{}
}

func TestTwoEnvironmentsWithTheSameDatabaseNameGetTheirOwnAlerts(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu", "prod-us")
	since := lab.now.Add(-time.Hour)
	labels := map[string]string{"cluster": "orders", "namespace": "pds"}
	lab.beat("prod-eu", []string{"orders"}, nil, liveAlerts(lab.now,
		firing("eu-1", "service_down", "critical", since, labels)))
	lab.beat("prod-us", []string{"orders"}, nil, liveAlerts(lab.now))

	body := lab.all(t)

	require.Len(t, body.Alerts, 1)
	assert.Equal(t, "prod-eu", body.Alerts[0].Environment)
	assert.Equal(t, "prod-eu/pds.orders", body.Alerts[0].DatabaseID)
	assert.Equal(t, 1, body.Criticals)

	us := getJSON[databaseAlertsBody](t, lab.srv, 1, "/api/databases/prod-us/pds.orders/alerts", http.StatusOK)
	assert.Empty(t, us.Alerts)
	assert.Equal(t, "live", us.Environment.State)
	eu := getJSON[databaseAlertsBody](t, lab.srv, 1, "/api/databases/prod-eu/pds.orders/alerts", http.StatusOK)
	require.Len(t, eu.Alerts, 1)
	assert.Equal(t, "service_down", eu.Alerts[0].Rule)
}

func TestAnAlertWithoutAnEnvironmentLabelComesBackAgainstItsEnvironment(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu")
	lab.beat("prod-eu", []string{"orders"}, nil, liveAlerts(lab.now,
		firing("own-1", "my_own_rule", "info", lab.now.Add(-time.Minute), map[string]string{"namespace": "pds"}),
		firing("own-2", "my_other_rule", "info", lab.now.Add(-time.Minute), map[string]string{"cluster": "ghost", "namespace": "pds"}),
	))

	body := lab.all(t)

	require.Len(t, body.Alerts, 2)
	for _, alert := range body.Alerts {
		assert.Equal(t, "prod-eu", alert.Environment)
		assert.Empty(t, alert.DatabaseID, "an alert that maps to no database has no database link")
		assert.Equal(t, alertAction{Kind: "view_environment", Label: "View environment"}, alert.Action)
	}
	db := getJSON[databaseAlertsBody](t, lab.srv, 1, "/api/databases/prod-eu/pds.orders/alerts", http.StatusOK)
	assert.Empty(t, db.Alerts)
}

func TestAStaleHeartbeatReportsTheEnvironmentStale(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu")
	lab.beat("prod-eu", []string{"orders"}, nil, liveAlerts(lab.now,
		firing("eu-1", "service_down", "critical", lab.now, map[string]string{"cluster": "orders", "namespace": "pds"})))
	lastBeat := lab.now
	lab.now = lab.now.Add(10 * time.Minute)

	body := lab.all(t)

	env := environmentNamed(t, body, "prod-eu")
	assert.Equal(t, "stale", env.State)
	require.NotNil(t, env.ReadAt)
	assert.True(t, env.ReadAt.Equal(lastBeat))
	assert.Equal(t, "Alerts are not current in prod-eu because it has not reported in 10 minutes. Check that the Kubernetes cluster is running.", env.Reason)
	require.NotNil(t, env.Action)
	assert.Equal(t, "view_environment", env.Action.Kind)
	assert.Empty(t, body.Alerts, "alerts older than the stale window are not shown as current")
	assert.Zero(t, body.Criticals)
}

func TestAnEnvironmentThatNeverReportedIsNotReadAsHealthy(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu")

	env := environmentNamed(t, lab.all(t), "prod-eu")

	assert.Equal(t, "stale", env.State)
	assert.Nil(t, env.ReadAt)
	assert.NotEmpty(t, env.Reason)
}

func TestMonitoringThatCannotBeReadIsOffWithAReasonAndOneAction(t *testing.T) {
	cases := map[string]struct {
		alerts *hub.HeartbeatAlerts
		reason string
		action string
	}{
		"not set up": {
			&hub.HeartbeatAlerts{Pending: "not-configured", Items: []hub.HeartbeatAlert{}},
			"Alerts are off in prod-eu because monitoring is not set up. Set up monitoring for this environment.",
			"set_up_monitoring",
		},
		"not ready": {
			&hub.HeartbeatAlerts{Pending: "unreachable", Items: []hub.HeartbeatAlert{}},
			"Alerts are off in prod-eu because monitoring is not ready yet. Check again in a minute.",
			"open_monitoring",
		},
		"not serving": {
			&hub.HeartbeatAlerts{Pending: "unavailable", Items: []hub.HeartbeatAlert{}},
			"Alerts are off in prod-eu because monitoring is not serving alerts. Open monitoring to see why.",
			"open_monitoring",
		},
		"agent has no reading": {
			nil,
			"Alerts are off in prod-eu because it has not sent alerts yet. Update the connection to this environment.",
			"update_connection",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			lab := newAlertsLab(t, "prod-eu")
			lab.beat("prod-eu", []string{"orders"}, nil, tc.alerts)

			env := environmentNamed(t, lab.all(t), "prod-eu")

			assert.Equal(t, "off", env.State)
			assert.Equal(t, tc.reason, env.Reason)
			require.NotNil(t, env.Action)
			assert.Equal(t, tc.action, env.Action.Kind)
		})
	}
}

func TestALiveReadingWithNoAlertsIsLive(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu")
	lab.beat("prod-eu", []string{"orders"}, nil, liveAlerts(lab.now))

	env := environmentNamed(t, lab.all(t), "prod-eu")

	assert.Equal(t, "live", env.State)
	assert.Empty(t, env.Reason)
	assert.Nil(t, env.Action)
	assert.Zero(t, env.Total)
}

func TestAFailedNewestBackupYieldsBackupFailed(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu")
	t0 := lab.now.Add(-3 * time.Hour)
	backups := []engine.Backup{
		{ID: "b1", Instance: "orders", Status: engine.BackupSucceeded, CreatedAt: t0, FinishedAt: t0.Add(time.Minute)},
		{ID: "b2", Instance: "orders", Status: engine.BackupFailed, CreatedAt: t0.Add(time.Hour), FinishedAt: t0.Add(time.Hour + time.Minute)},
		{ID: "b3", Instance: "orders", Status: "running", CreatedAt: t0.Add(2 * time.Hour)},
		{ID: "c1", Instance: "billing", Status: engine.BackupFailed, CreatedAt: t0, FinishedAt: t0.Add(time.Minute)},
		{ID: "c2", Instance: "billing", Status: engine.BackupSucceeded, CreatedAt: t0.Add(time.Hour), FinishedAt: t0.Add(time.Hour + time.Minute)},
	}
	lab.beat("prod-eu", []string{"orders", "billing"}, backups, liveAlerts(lab.now))

	body := lab.all(t)

	require.Len(t, body.Alerts, 1, "only orders has a failed newest backup")
	alert := body.Alerts[0]
	assert.Equal(t, "backup_failed", alert.Rule)
	assert.Equal(t, "warning", alert.Severity)
	assert.Equal(t, "prod-eu/pds.orders", alert.DatabaseID)
	assert.Equal(t, alertAction{Kind: "retry_backup", Label: "Retry backup"}, alert.Action)
	assert.True(t, alert.StartsAt.Equal(t0.Add(time.Hour+time.Minute)))
	assert.Equal(t, "The newest backup of orders failed.", alert.Summary)
	assert.Zero(t, body.Criticals)
	assert.Equal(t, 1, environmentNamed(t, body, "prod-eu").Total)
}

func TestAnotherTeamsAlertsNeverAppear(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	newHub := func() *hub.Service {
		inv := hub.NewInventory(time.Minute)
		inv.Register("prod", "pds")

		return hub.NewService(hub.NewMemoryStore(), inv, hub.Options{
			Namespace: testEverestNamespace, BackupStorage: testBackupStorage,
			Now: func() time.Time { return now }, Tokens: hub.NewTokenStore(nil, 0),
		})
	}
	acme, globex := newHub(), newHub()
	beatTo(acme, "prod", []string{"orders"}, nil, liveAlerts(now,
		firing("a-1", "service_down", "critical", now, map[string]string{"cluster": "orders", "namespace": "pds"})))
	beatTo(globex, "prod", []string{"orders"}, nil, liveAlerts(now))
	srv := newAlertsServer(t, map[uint]*hub.Service{1: acme, 2: globex})

	theirs := getJSON[alertsBody](t, srv, 2, "/api/alerts", http.StatusOK)
	mine := getJSON[alertsBody](t, srv, 1, "/api/alerts", http.StatusOK)

	assert.Empty(t, theirs.Alerts)
	assert.Zero(t, theirs.Criticals)
	assert.Len(t, mine.Alerts, 1)
	other := getJSON[databaseAlertsBody](t, srv, 2, "/api/databases/prod/pds.orders/alerts", http.StatusOK)
	assert.Empty(t, other.Alerts)
}

func TestTheDatabaseRouteReturnsOnlyThatDatabasesAlerts(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu")
	since := lab.now.Add(-time.Hour)
	lab.beat("prod-eu", []string{"orders", "billing"}, nil, liveAlerts(lab.now,
		firing("1", "service_down", "critical", since, map[string]string{"cluster": "orders", "namespace": "pds"}),
		firing("2", "disk_used", "warning", since, map[string]string{"cluster": "billing", "namespace": "pds"}),
		firing("3", "node_down", "critical", since, map[string]string{"namespace": "pds"}),
	))

	body := getJSON[databaseAlertsBody](t, lab.srv, 1, "/api/databases/prod-eu/pds.orders/alerts", http.StatusOK)

	assert.Equal(t, "prod-eu/pds.orders", body.DatabaseID)
	require.Len(t, body.Alerts, 1)
	assert.Equal(t, "service_down", body.Alerts[0].Rule)
}

func TestTheDatabaseAlertsRouteAnswersNotFoundForAnUnknownDatabase(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu")
	lab.beat("prod-eu", []string{"orders"}, nil, liveAlerts(lab.now))

	getJSON[map[string]any](t, lab.srv, 1, "/api/databases/prod-eu/pds.nothing/alerts", http.StatusNotFound)
}

func TestTheDatabaseAlertsRouteDoesNotResolveAnotherNamespaceByName(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu")
	lab.beat("prod-eu", []string{"orders"}, nil, liveAlerts(lab.now))

	getJSON[map[string]any](t, lab.srv, 1, "/api/databases/prod-eu/staging.orders/alerts", http.StatusNotFound)
}

func TestTheDatabaseAlertsRouteServesEachSameNamedDatabaseItsOwnAlerts(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu")
	since := lab.now.Add(-time.Hour)
	lab.hub.ApplyHeartbeat(hub.Heartbeat{
		Environment: "prod-eu", Namespace: "pds",
		Instances: []engine.Instance{{Name: "orders", Namespace: "pds"}, {Name: "orders", Namespace: "staging"}},
		Alerts: liveAlerts(lab.now,
			firing("1", "service_down", "critical", since, map[string]string{"cluster": "orders", "namespace": "pds"}),
			firing("2", "disk_used", "warning", since, map[string]string{"cluster": "orders", "namespace": "staging"}),
		),
	})

	body := getJSON[databaseAlertsBody](t, lab.srv, 1, "/api/databases/prod-eu/staging.orders/alerts", http.StatusOK)

	assert.Equal(t, "prod-eu/staging.orders", body.DatabaseID)
	require.Len(t, body.Alerts, 1)
	assert.Equal(t, "disk_used", body.Alerts[0].Rule)
}

func TestSameNamedDatabasesShareBackupsSoAFailureIsNeverMaskedAsHealthy(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu")
	t0 := lab.now.Add(-3 * time.Hour)
	lab.hub.ApplyHeartbeat(hub.Heartbeat{
		Environment: "prod-eu", Namespace: "pds",
		Instances: []engine.Instance{{Name: "orders", Namespace: "pds"}, {Name: "orders", Namespace: "staging"}},
		Backups: []engine.Backup{
			{ID: "a", Instance: "orders", Status: engine.BackupFailed, CreatedAt: t0, FinishedAt: t0.Add(time.Minute)},
			{ID: "b", Instance: "orders", Status: engine.BackupSucceeded, CreatedAt: t0.Add(time.Hour), FinishedAt: t0.Add(time.Hour + time.Minute)},
		},
		Alerts: liveAlerts(lab.now),
	})

	body := lab.all(t)

	require.Len(t, body.Alerts, 2, "a failure that cannot be attributed is reported on every database sharing the name")
}

func TestAlertsSortBySeverityThenLongestFiringWithPerRuleActions(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu")
	at := lab.now
	l := map[string]string{"cluster": "orders", "namespace": "pds"}
	lab.beat("prod-eu", []string{"orders"}, nil, liveAlerts(at,
		firing("w-new", "replication_lag", "warning", at.Add(-time.Minute), l),
		firing("i-1", "custom_rule", "info", at.Add(-9*time.Hour), l),
		firing("w-old", "disk_used", "warning", at.Add(-2*time.Hour), l),
		firing("c-1", "service_down", "critical", at.Add(-time.Hour), l),
		firing("w-conn", "connections_used", "warning", at.Add(-30*time.Second), l),
		firing("c-2", "node_down", "critical", at.Add(-3*time.Hour), l),
	))

	body := lab.all(t)

	var order []string
	actions := map[string]string{}
	for _, a := range body.Alerts {
		order = append(order, a.Rule)
		actions[a.Rule] = a.Action.Label
	}
	assert.Equal(t, []string{"node_down", "service_down", "disk_used", "replication_lag", "connections_used", "custom_rule"}, order)
	assert.Equal(t, 2, body.Criticals)
	assert.Equal(t, "Open database", actions["service_down"])
	assert.Equal(t, "View environment", actions["node_down"])
	assert.Equal(t, "Scale storage", actions["disk_used"])
	assert.Equal(t, "View metrics", actions["replication_lag"])
	assert.Equal(t, "Scale resources", actions["connections_used"])
	assert.Equal(t, "Open database", actions["custom_rule"])
}

func TestAnAgentThatCutTheListSaysSo(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu")
	reading := liveAlerts(lab.now, firing("1", "disk_used", "warning", lab.now, map[string]string{"cluster": "orders", "namespace": "pds"}))
	reading.Total = 340
	lab.beat("prod-eu", []string{"orders"}, nil, reading)

	env := environmentNamed(t, lab.all(t), "prod-eu")

	assert.Equal(t, 340, env.Total)
	assert.Equal(t, 1, env.Shown)
	assert.Equal(t, "Showing 1 of 340 alerts in prod-eu. Open monitoring for the rest.", env.Note)
}

func newPendingDatabase(t *testing.T, lab *alertsLab, name string) {
	t.Helper()

	_, err := lab.hub.Create(context.Background(), engine.CreateInput{
		Environment: "prod-eu", Name: name, Profile: "dev", Engine: "mysql",
	})
	require.NoError(t, err)
}

func TestADatabaseWithAManifestAndNoHeartbeatEntryHasNoAlertsAndItsEnvironment(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu")
	lab.beat("prod-eu", nil, nil, liveAlerts(lab.now))
	newPendingDatabase(t, lab, "orders")

	body := getJSON[databaseAlertsBody](t, lab.srv, 1, "/api/databases/prod-eu/pds.orders/alerts", http.StatusOK)

	assert.Equal(t, "prod-eu/pds.orders", body.DatabaseID)
	assert.Empty(t, body.Alerts)
	assert.Equal(t, "prod-eu", body.Environment.Name)
}

func TestAPendingDatabaseIsNotShadowedByASameNamedDatabaseInAnotherNamespace(t *testing.T) {
	lab := newAlertsLab(t, "prod-eu")
	lab.beat("prod-eu", nil, nil, liveAlerts(lab.now))
	newPendingDatabase(t, lab, "orders")
	lab.hub.ApplyHeartbeat(hub.Heartbeat{
		Environment: "prod-eu", Namespace: "pds",
		Instances: []engine.Instance{{Name: "orders", Namespace: "staging"}},
		Alerts:    liveAlerts(lab.now),
	})

	body := getJSON[databaseAlertsBody](t, lab.srv, 1, "/api/databases/prod-eu/pds.orders/alerts", http.StatusOK)

	assert.Equal(t, "prod-eu/pds.orders", body.DatabaseID)
	assert.Empty(t, body.Alerts)
}
