package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/store"
)

// memoryLifecycle answers for managed.Lifecycle the way the routes can tell: it
// reports a fixed status, records what it was asked and starts runs in the
// memory run book, so a second active run is refused as it is in the store.
type memoryLifecycle struct {
	runs   *memoryManaged
	status managed.Status
	errs   map[string]error
	calls  []string

	window   store.MaintenanceWindow
	days     int
	update   managed.InfraUpdate
	verdict  managed.Verdict
	unavail  bool
	describe int
}

func newMemoryLifecycle(runs *memoryManaged) *memoryLifecycle {
	return &memoryLifecycle{runs: runs, errs: map[string]error{}}
}

func (f *memoryLifecycle) called(name string) error {
	f.calls = append(f.calls, name)

	return f.errs[name]
}

func (f *memoryLifecycle) Describe(rec store.ManagedEnvironment) (managed.Status, bool) {
	f.describe++
	status := f.status
	status.Lost, status.LostAt = rec.LostAt != nil, rec.LostAt

	return status, !f.unavail
}

func (f *memoryLifecycle) SetWindow(_ context.Context, _ auth.Scope, _ string, w store.MaintenanceWindow) error {
	f.window = w

	return f.called("window")
}

func (f *memoryLifecycle) Defer(_ context.Context, _ auth.Scope, _ string, days int) error {
	f.days = days

	return f.called("defer")
}

func (f *memoryLifecycle) start(scope auth.Scope, name, kind, call string) (store.ProvisioningRun, error) {
	if err := f.called(call); err != nil {
		return store.ProvisioningRun{}, err
	}

	return f.runs.Start(context.Background(), scope.Team.ID, name, kind)
}

func (f *memoryLifecycle) UpgradeNow(_ context.Context, scope auth.Scope, name string) (store.ProvisioningRun, error) {
	return f.start(scope, name, managed.KindUpgradePDS, "upgrade")
}

func (f *memoryLifecycle) UpdateInfrastructure(_ context.Context, scope auth.Scope, name string, in managed.InfraUpdate) (store.ProvisioningRun, error) {
	f.update = in

	return f.start(scope, name, managed.KindKubernetes, "update")
}

func (f *memoryLifecycle) PreviewUpdate(_ context.Context, _ auth.Scope, _ string, in managed.InfraUpdate) (managed.Verdict, error) {
	f.update = in

	return f.verdict, f.called("preview")
}

func (f *memoryLifecycle) Rebuild(_ context.Context, scope auth.Scope, name string) (store.ProvisioningRun, error) {
	return f.start(scope, name, managed.KindRebuild, "rebuild")
}

func TestAManagedEnvironmentReadCarriesWhatThePlatformKnowsAboutItsUpgradesAndDrift(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")
	until := time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC)
	checked := time.Date(2026, time.November, 20, 9, 0, 0, 0, time.UTC)
	h.life.status = managed.Status{
		ChartVersion: "0.1.0", NextChart: "0.2.0", SkippedVersion: "0.1.5", Deferred: true, DeferredUntil: &until,
		Window:      store.MaintenanceWindow{Day: time.Friday, Hour: 22, Zone: "Europe/Berlin"},
		WindowStart: time.Date(2026, time.November, 20, 21, 0, 0, 0, time.UTC), WindowEnd: time.Date(2026, time.November, 20, 23, 0, 0, 0, time.UTC),
		TemplateRevision: 1, LatestTemplateRevision: 2, DriftCheckedAt: &checked,
		Drift:             []store.DriftItem{{Resource: "PrivateRoute (AWS::EC2::Route)", Detail: "Changed outside PDS"}},
		KubernetesVersion: "1.32", KubernetesUpgradeAvailable: true,
	}

	rec := h.do(t, 0, http.MethodGet, "/api/environments/orders", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	life, _ := jsonOf(t, rec)["lifecycle"].(map[string]any)
	require.NotNil(t, life)
	assert.Equal(t, "0.1.0", life["chartVersion"])
	assert.Equal(t, "0.2.0", life["pendingChartVersion"])
	assert.Equal(t, "0.1.5", life["skippedChartVersion"])
	assert.Equal(t, true, life["deferred"])
	assert.Equal(t, "2026-12-01T00:00:00Z", life["deferredUntil"])
	assert.Equal(t, map[string]any{
		"day": float64(time.Friday), "hour": float64(22), "timeZone": "Europe/Berlin",
		"nextStart": "2026-11-20T21:00:00Z", "nextEnd": "2026-11-20T23:00:00Z",
	}, life["maintenanceWindow"])
	assert.Equal(t, float64(1), life["templateRevision"])
	assert.Equal(t, float64(2), life["latestTemplateRevision"])
	assert.Equal(t, true, life["templateOutOfDate"])
	assert.Equal(t, "Template revision 1, latest 2", life["templateSummary"])
	assert.Equal(t, "1.32", life["kubernetesVersion"])
	assert.Equal(t, true, life["kubernetesUpgradeAvailable"])
	assert.Equal(t, []any{map[string]any{"resource": "PrivateRoute (AWS::EC2::Route)", "detail": "Changed outside PDS"}}, life["drift"])
	assert.Equal(t, "2026-11-20T09:00:00Z", life["driftCheckedAt"])
	assert.Equal(t, false, life["lost"])
}

func TestAnEnvironmentWithNoDriftAndNothingNewerSaysSoWithEmptyLists(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")

	life, _ := jsonOf(t, h.do(t, 0, http.MethodGet, "/api/environments/orders", ""))["lifecycle"].(map[string]any)

	require.NotNil(t, life)
	assert.Equal(t, []any{}, life["drift"])
	assert.NotContains(t, life, "pendingChartVersion")
	assert.NotContains(t, life, "templateSummary")
	assert.Equal(t, false, life["templateOutOfDate"])
}

func TestALostEnvironmentReadsAsLostWithTheReasonAPersonReads(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")
	lostAt := time.Date(2026, time.November, 19, 3, 0, 0, 0, time.UTC)
	row := h.managed.rows[key(7, "orders")]
	row.LostAt = &lostAt
	h.managed.rows[key(7, "orders")] = row
	h.managed.runs[0].Status = store.RunSucceeded

	got := jsonOf(t, h.do(t, 0, http.MethodGet, "/api/environments/orders", ""))

	assert.Equal(t, "lost", got["state"])
	life, _ := got["lifecycle"].(map[string]any)
	assert.Equal(t, true, life["lost"])
	assert.Equal(t, "Infrastructure missing", life["lostReason"])
	assert.Equal(t, "2026-11-19T03:00:00Z", life["lostAt"])
}

func TestTheListCarriesTheLifecycleOfEachManagedEnvironmentAndNoneOnAConnectedOne(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")

	rows, _ := jsonOf(t, h.do(t, 0, http.MethodGet, "/api/environments", ""))["environments"].([]any)

	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		if row["origin"] == "managed" {
			assert.Contains(t, row, "lifecycle")
		} else {
			assert.NotContains(t, row, "lifecycle")
		}
	}
	assert.Len(t, rows, 2)
}

func TestWithoutALifecycleTheReadStillWorksAndOmitsIt(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")
	h.life.unavail = true

	got := jsonOf(t, h.do(t, 0, http.MethodGet, "/api/environments/orders", ""))

	assert.NotContains(t, got, "lifecycle")
	assert.Equal(t, "orders", got["name"])
}
