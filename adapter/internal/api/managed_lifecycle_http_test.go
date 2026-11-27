package api_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/api"
	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/aws"
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

func (h *managedHTTP) ready(t *testing.T, name string) {
	t.Helper()

	h.create(t, name)
	h.managed.runs[len(h.managed.runs)-1].Status = store.RunSucceeded
}

func TestARunOfEachKindAPersonStartsGoesThroughTheLifecycle(t *testing.T) {
	for _, tc := range []struct{ body, call, kind string }{
		{`{"kind":"upgrade_pds"}`, "upgrade", managed.KindUpgradePDS},
		{`{"kind":"upgrade_kubernetes","kubernetesVersion":"1.34","confirmNodeGroupReplace":true}`, "update", managed.KindKubernetes},
		{`{"kind":"rebuild"}`, "rebuild", managed.KindRebuild},
	} {
		h := newManagedHTTP(t)
		h.ready(t, "orders")

		rec := h.do(t, 0, http.MethodPost, "/api/environments/orders/runs", tc.body)

		require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
		got := jsonOf(t, rec)
		run, _ := got["run"].(map[string]any)
		assert.Equal(t, tc.kind, run["kind"])
		assert.Equal(t, []string{tc.call}, h.life.calls)
	}
}

func TestAKubernetesRunPassesTheVersionAndTheNodeGroupConfirmOn(t *testing.T) {
	h := newManagedHTTP(t)
	h.ready(t, "orders")

	rec := h.do(t, 0, http.MethodPost, "/api/environments/orders/runs", `{"kind":"upgrade_kubernetes","kubernetesVersion":"1.34","confirmNodeGroupReplace":true}`)

	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
	assert.Equal(t, managed.InfraUpdate{KubernetesVersion: "1.34", ConfirmNodeGroupReplace: true}, h.life.update)
}

func TestAnotherRunWhileOneIsActiveIsAConflictWithOneAction(t *testing.T) {
	h := newManagedHTTP(t)
	h.create(t, "orders")

	rec := h.do(t, 0, http.MethodPost, "/api/environments/orders/runs", `{"kind":"upgrade_pds"}`)

	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Equal(t, api.CodeConflict, jsonOf(t, rec)["code"])
	assert.Equal(t, "This environment already has a run in progress. Wait for it to finish, or retry it if it failed.", jsonOf(t, rec)["message"])
}

func TestWhatTheLifecycleRefusesBecomesOneSafeSentenceWithOneAction(t *testing.T) {
	for _, tc := range []struct {
		refusal error
		status  int
		message string
	}{
		{managed.ErrNothingToUpgrade, http.StatusConflict, "This environment is up to date, so there is nothing to upgrade."},
		{managed.ErrNotReady, http.StatusConflict, "This environment has not finished connecting. Wait for its first run to finish, then try again."},
		{managed.ErrLost, http.StatusConflict, "This environment's infrastructure is missing. Rebuild it to bring it back."},
		{managed.ErrNotLost, http.StatusConflict, "Only an environment whose infrastructure is missing can be rebuilt. Check its state, then try again."},
		{managed.ErrWouldReplace, http.StatusConflict, "This update would replace the network or the Kubernetes cluster, so PDS will not run it. Contact Percona support."},
		{managed.ErrConfirmRequired, http.StatusConflict, "This update replaces the node group and restarts every database. Confirm it to go on."},
		{managed.ErrConnectionNotReady, http.StatusConflict, "This AWS account is not ready. Open it, check the connection, then try again."},
		{managed.ErrInvalid, http.StatusBadRequest, "Check the run kind and the Kubernetes version, then try again."},
		{managed.ErrUnavailable, http.StatusNotImplemented, "This control plane cannot build or change managed environments. Contact Percona support."},
	} {
		h := newManagedHTTP(t)
		h.ready(t, "orders")
		h.life.errs["update"] = tc.refusal

		rec := h.do(t, 0, http.MethodPost, "/api/environments/orders/runs", `{"kind":"upgrade_kubernetes"}`)

		assert.Equal(t, tc.status, rec.Code, tc.refusal.Error())
		got := jsonOf(t, rec)
		assert.Equal(t, tc.message, got["message"], tc.refusal.Error())
		assert.NotContains(t, rec.Body.String(), tc.refusal.Error(), "the cause stays in the log")
	}
}

func TestAnUnknownRunKindIsRefused(t *testing.T) {
	h := newManagedHTTP(t)
	h.ready(t, "orders")

	for _, kind := range []string{"create", "delete", "upgrade_everything", ""} {
		rec := h.do(t, 0, http.MethodPost, "/api/environments/orders/runs", `{"kind":"`+kind+`"}`)

		assert.Equal(t, http.StatusBadRequest, rec.Code, kind)
	}
	assert.Empty(t, h.life.calls)
}

func TestAnotherTeamsEnvironmentAnswersNotFoundOnEveryLifecycleRoute(t *testing.T) {
	h := newManagedHTTP(t)
	h.ready(t, "orders")
	h.life.errs["upgrade"] = store.ErrManagedEnvironmentUnknown
	h.life.errs["defer"] = store.ErrManagedEnvironmentUnknown
	h.life.errs["window"] = store.ErrManagedEnvironmentUnknown
	h.life.errs["preview"] = store.ErrManagedEnvironmentUnknown

	for _, call := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/environments/orders/runs", `{"kind":"upgrade_pds"}`},
		{http.MethodPost, "/api/environments/orders/upgrades/defer", `{"days":3}`},
		{http.MethodPut, "/api/environments/orders/maintenance-window", `{"day":5,"hour":22,"timeZone":"UTC"}`},
		{http.MethodPost, "/api/environments/orders/updates/preview", `{}`},
	} {
		rec := h.do(t, 8, call.method, call.path, call.body)

		assert.Equal(t, http.StatusNotFound, rec.Code, call.path)
	}
}

func TestADeferralPutsTheVersionOffAndAnswersWithTheLifecycle(t *testing.T) {
	h := newManagedHTTP(t)
	h.ready(t, "orders")
	h.life.status = managed.Status{ChartVersion: "0.1.0", NextChart: "0.2.0", Deferred: true}

	rec := h.do(t, 0, http.MethodPost, "/api/environments/orders/upgrades/defer", `{"days":7}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, 7, h.life.days)
	assert.Equal(t, true, jsonOf(t, rec)["deferred"])
}

func TestADeferralThatCannotBeMadeSaysWhy(t *testing.T) {
	for _, tc := range []struct {
		refusal error
		status  int
		message string
	}{
		{managed.ErrAlreadyDeferred, http.StatusConflict, "This version was already deferred once. It upgrades in the next maintenance window, or upgrade it now."},
		{managed.ErrNothingToUpgrade, http.StatusConflict, "This environment is up to date, so there is nothing to upgrade."},
		{managed.ErrInvalid, http.StatusBadRequest, "Choose from 1 to 30 days."},
	} {
		h := newManagedHTTP(t)
		h.ready(t, "orders")
		h.life.errs["defer"] = tc.refusal

		rec := h.do(t, 0, http.MethodPost, "/api/environments/orders/upgrades/defer", `{"days":3}`)

		assert.Equal(t, tc.status, rec.Code)
		assert.Equal(t, tc.message, jsonOf(t, rec)["message"])
	}
}

func TestADeferralNeedsTheDays(t *testing.T) {
	h := newManagedHTTP(t)
	h.ready(t, "orders")

	assert.Equal(t, http.StatusBadRequest, h.do(t, 0, http.MethodPost, "/api/environments/orders/upgrades/defer", `{}`).Code)
	assert.Empty(t, h.life.calls)
}

func TestTheMaintenanceWindowIsSetByDayHourAndTimeZone(t *testing.T) {
	h := newManagedHTTP(t)
	h.ready(t, "orders")

	rec := h.do(t, 0, http.MethodPut, "/api/environments/orders/maintenance-window", `{"day":0,"hour":0,"timeZone":"Europe/Berlin"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, store.MaintenanceWindow{Day: time.Sunday, Hour: 0, Zone: "Europe/Berlin"}, h.life.window, "Sunday midnight is a window, not a missing field")
	assert.Contains(t, jsonOf(t, rec), "maintenanceWindow")
}

func TestAMaintenanceWindowThatMakesNoSenseIsRefusedWithOneSentence(t *testing.T) {
	h := newManagedHTTP(t)
	h.ready(t, "orders")

	for _, body := range []string{`{"hour":2,"timeZone":"UTC"}`, `{"day":1,"timeZone":"UTC"}`, `{"day":1,"hour":2}`} {
		assert.Equal(t, http.StatusBadRequest, h.do(t, 0, http.MethodPut, "/api/environments/orders/maintenance-window", body).Code, body)
	}
	h.life.errs["window"] = managed.ErrInvalid
	rec := h.do(t, 0, http.MethodPut, "/api/environments/orders/maintenance-window", `{"day":9,"hour":2,"timeZone":"UTC"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "Choose a day, an hour from 0 to 23 and a time zone such as Europe/Berlin.", jsonOf(t, rec)["message"])
}

func TestThePreviewShowsWhatAnUpdateWouldChangeWithoutStartingARun(t *testing.T) {
	h := newManagedHTTP(t)
	h.ready(t, "orders")
	h.life.verdict = managed.Verdict{ReplacesNodeGroup: true}
	h.life.verdict.Plan.Changes = []aws.Change{{Resource: "NodeGroup", Type: "AWS::EKS::Nodegroup", Action: "Modify", Replacement: "True"}}

	rec := h.do(t, 0, http.MethodPost, "/api/environments/orders/updates/preview", `{"kubernetesVersion":"1.34"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := jsonOf(t, rec)
	assert.Equal(t, false, got["replacesNetwork"])
	assert.Equal(t, true, got["replacesNodeGroup"])
	assert.Equal(t, []any{map[string]any{"resource": "NodeGroup", "type": "AWS::EKS::Nodegroup", "action": "Modify", "replacement": "True"}}, got["changes"])
	assert.Equal(t, "1.34", h.life.update.KubernetesVersion)
	assert.Empty(t, h.managed.runs[1:], "only the create run exists")
}

func TestAPreviewOfAnUpToDateEnvironmentSaysThereIsNothingToUpgrade(t *testing.T) {
	h := newManagedHTTP(t)
	h.ready(t, "orders")
	h.life.errs["preview"] = managed.ErrNothingToUpgrade

	rec := h.do(t, 0, http.MethodPost, "/api/environments/orders/updates/preview", `{}`)

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "This environment is up to date, so there is nothing to upgrade.", jsonOf(t, rec)["message"])
}
