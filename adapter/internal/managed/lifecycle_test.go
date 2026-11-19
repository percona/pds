package managed_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/store"
)

type startedRun struct {
	team                  uint
	environment, kind, in string
}

type fakeLifecycleRuns struct {
	mu      sync.Mutex
	started []startedRun
	err     error
	busy    map[string]bool
}

func (f *fakeLifecycleRuns) StartWith(_ context.Context, team uint, environment, kind, params string) (store.ProvisioningRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return store.ProvisioningRun{}, f.err
	}
	f.started = append(f.started, startedRun{team: team, environment: environment, kind: kind, in: params})

	return store.ProvisioningRun{ID: uint(len(f.started)), TeamID: team, Environment: environment, Kind: kind, Params: params, Status: store.RunQueued}, nil
}

func (f *fakeLifecycleRuns) Busy(_ context.Context, _ uint, environment string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.busy[environment], nil
}

var paths = managed.UpgradePaths{"0.1.0": {"0.2.0", "0.3.0"}, "0.3.0": {"0.4.0"}}

type lifecycleLab struct {
	*lab
	runs *fakeLifecycleRuns
	life *managed.Lifecycle
	who  auth.Scope
	now  time.Time
}

// sundayNight is inside the default maintenance window: Sunday 02:30 UTC.
var sundayNight = time.Date(2026, time.November, 22, 2, 30, 0, 0, time.UTC)

func newLifecycleLab(t *testing.T) *lifecycleLab {
	t.Helper()

	l := newLab(t)
	runs := &fakeLifecycleRuns{busy: map[string]bool{}}
	ll := &lifecycleLab{lab: l, runs: runs, who: auth.Scope{UserID: 3, Team: auth.Team{ID: l.team}}, now: sundayNight}
	ll.life = managed.NewLifecycle(l.deps, ownRecords{l.db.ManagedEnvironments(), l.team}, runs, paths, managed.LifecycleOptions{Now: func() time.Time { return ll.now }})

	return ll
}

type ownRecords struct {
	*store.ManagedEnvironments
	team uint
}

// All lists only this lab's environments, since the database is shared with
// every other test.
func (o ownRecords) All(ctx context.Context) ([]store.ManagedEnvironment, error) {
	all, err := o.ManagedEnvironments.All(ctx)
	var own []store.ManagedEnvironment
	for _, e := range all {
		if e.TeamID == o.team {
			own = append(own, e)
		}
	}

	return own, err
}

func (l *lifecycleLab) started() []startedRun {
	l.runs.mu.Lock()
	defer l.runs.mu.Unlock()

	return append([]startedRun(nil), l.runs.started...)
}

func (l *lifecycleLab) only(t *testing.T) startedRun {
	t.Helper()

	require.Len(t, l.started(), 1)

	return l.started()[0]
}

func TestTheMaintenanceWindowStartsOnSundayAtTwoUTC(t *testing.T) {
	l := newLifecycleLab(t)

	status, err := l.life.Status(t.Context(), l.who, l.env)

	require.NoError(t, err)
	assert.Equal(t, store.MaintenanceWindow{Day: time.Sunday, Hour: 2, Zone: "UTC"}, status.Window)
}

func TestAUserSetsTheWeeklyMaintenanceWindowInATimeZone(t *testing.T) {
	l := newLifecycleLab(t)
	w := store.MaintenanceWindow{Day: time.Friday, Hour: 22, Zone: "Europe/Berlin"}

	require.NoError(t, l.life.SetWindow(t.Context(), l.who, l.env, w))

	assert.Equal(t, w, l.record().Window())
}

func TestAMaintenanceWindowThatMakesNoSenseIsRefused(t *testing.T) {
	l := newLifecycleLab(t)

	for _, w := range []store.MaintenanceWindow{
		{Day: 7, Hour: 2, Zone: "UTC"},
		{Day: time.Sunday, Hour: 24, Zone: "UTC"},
		{Day: time.Sunday, Hour: -1, Zone: "UTC"},
		{Day: time.Sunday, Hour: 2, Zone: "Mars/Olympus"},
		{Day: time.Sunday, Hour: 2, Zone: ""},
	} {
		err := l.life.SetWindow(t.Context(), l.who, l.env, w)
		require.ErrorIs(t, err, managed.ErrInvalid, "%+v", w)
	}
	assert.Equal(t, store.MaintenanceWindow{Day: time.Sunday, Hour: 2, Zone: "UTC"}, l.record().Window())
}

func TestAnotherTeamsEnvironmentIsNotFoundForEveryLifecycleCall(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	other := auth.Scope{UserID: 4, Team: auth.Team{ID: l.team + 1000}}
	ctx := t.Context()

	_, err := l.life.Status(ctx, other, l.env)
	assert.ErrorIs(t, err, store.ErrManagedEnvironmentUnknown)
	assert.ErrorIs(t, l.life.SetWindow(ctx, other, l.env, store.MaintenanceWindow{Hour: 3, Zone: "UTC"}), store.ErrManagedEnvironmentUnknown)
	assert.ErrorIs(t, l.life.Defer(ctx, other, l.env, 3), store.ErrManagedEnvironmentUnknown)
	_, err = l.life.UpgradeNow(ctx, other, l.env)
	assert.ErrorIs(t, err, store.ErrManagedEnvironmentUnknown)
	_, err = l.life.UpdateInfrastructure(ctx, other, l.env, managed.InfraUpdate{})
	assert.ErrorIs(t, err, store.ErrManagedEnvironmentUnknown)
	_, err = l.life.Rebuild(ctx, other, l.env)
	assert.ErrorIs(t, err, store.ErrManagedEnvironmentUnknown)
	assert.Empty(t, l.started())
}

func TestStatusNamesTheNextVersionTheDeferralAndWhatDrifted(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	until := l.now.Add(72 * time.Hour)
	require.NoError(t, l.db.ManagedEnvironments().SetDeferral(t.Context(), l.team, l.env, "0.3.0", until))
	drift := []store.DriftItem{{Resource: "PrivateRoute (AWS::EC2::Route)", Detail: "Changed outside PDS"}}
	require.NoError(t, l.db.ManagedEnvironments().SetDrift(t.Context(), l.team, l.env, drift, l.now))

	status, err := l.life.Status(t.Context(), l.who, l.env)

	require.NoError(t, err)
	assert.Equal(t, "0.1.0", status.ChartVersion)
	assert.Equal(t, "0.3.0", status.NextChart, "the newest version the pin lists for 0.1.0")
	assert.True(t, status.Deferred)
	assert.WithinDuration(t, until, *status.DeferredUntil, time.Second)
	assert.Equal(t, drift, status.Drift)
	assert.False(t, status.Lost)
	assert.True(t, time.Date(2026, time.November, 22, 2, 0, 0, 0, time.UTC).Equal(status.WindowStart), "the window now open began at two")
	assert.True(t, time.Date(2026, time.November, 22, 4, 0, 0, 0, time.UTC).Equal(status.WindowEnd))
}

func TestStatusTellsWhenTheNextWindowOpens(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.now = time.Date(2026, time.November, 23, 9, 0, 0, 0, time.UTC)

	status, err := l.life.Status(t.Context(), l.who, l.env)

	require.NoError(t, err)
	assert.True(t, time.Date(2026, time.November, 29, 2, 0, 0, 0, time.UTC).Equal(status.WindowStart))
}

func TestDeferringAVersionPutsItOffForUpToThirtyDaysOnce(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")

	require.NoError(t, l.life.Defer(t.Context(), l.who, l.env, 30))

	rec := l.record()
	assert.Equal(t, "0.3.0", rec.DeferredVersion)
	assert.WithinDuration(t, l.now.Add(30*24*time.Hour), *rec.DeferredUntil, time.Second)

	err := l.life.Defer(t.Context(), l.who, l.env, 1)
	require.ErrorIs(t, err, managed.ErrAlreadyDeferred, "a version is deferred once")
}

func TestADeferralOfMoreThanThirtyDaysOrLessThanOneIsRefused(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")

	require.ErrorIs(t, l.life.Defer(t.Context(), l.who, l.env, 31), managed.ErrInvalid)
	require.ErrorIs(t, l.life.Defer(t.Context(), l.who, l.env, 0), managed.ErrInvalid)
	assert.Empty(t, l.record().DeferredVersion)
}

func TestANewerVersionCanBeDeferredAfterAnOlderOneWas(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	require.NoError(t, l.life.Defer(t.Context(), l.who, l.env, 7))
	require.NoError(t, l.db.ManagedEnvironments().SetChartVersion(t.Context(), l.team, l.env, "0.3.0"))

	require.NoError(t, l.life.Defer(t.Context(), l.who, l.env, 7), "0.4.0 is a version nobody deferred")

	assert.Equal(t, "0.4.0", l.record().DeferredVersion)
}

func TestThereIsNothingToDeferOrUpgradeWithoutANewerVersion(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.4.0")

	require.ErrorIs(t, l.life.Defer(t.Context(), l.who, l.env, 3), managed.ErrNothingToUpgrade)
	_, err := l.life.UpgradeNow(t.Context(), l.who, l.env)
	require.ErrorIs(t, err, managed.ErrNothingToUpgrade)
}

func TestAnEarlyUpgradeStartsARunNowEvenWhenTheVersionWasDeferredOrSkipped(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.now = time.Date(2026, time.November, 24, 12, 0, 0, 0, time.UTC)
	require.NoError(t, l.db.ManagedEnvironments().SetDeferral(t.Context(), l.team, l.env, "0.3.0", l.now.Add(24*time.Hour)))
	require.NoError(t, l.db.ManagedEnvironments().SetSkippedVersion(t.Context(), l.team, l.env, "0.3.0"))

	run, err := l.life.UpgradeNow(t.Context(), l.who, l.env)

	require.NoError(t, err)
	assert.Equal(t, managed.KindUpgradePDS, run.Kind)
	started := l.only(t)
	assert.Equal(t, l.team, started.team)
	assert.JSONEq(t, `{"from":"0.1.0","version":"0.3.0"}`, started.in, "a User's retry tries the skipped version again")
}

func TestAnEarlyUpgradeOfAnEnvironmentThatNeverReportedIsRefused(t *testing.T) {
	l := newLifecycleLab(t)

	_, err := l.life.UpgradeNow(t.Context(), l.who, l.env)

	require.ErrorIs(t, err, managed.ErrNotReady)
	assert.Empty(t, l.started())
}

func TestAnEarlyUpgradeOfALostEnvironmentIsRefused(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.lost()

	_, err := l.life.UpgradeNow(t.Context(), l.who, l.env)

	require.ErrorIs(t, err, managed.ErrLost)
}

func TestAnEarlyUpgradeWhileARunIsActiveIsRefusedByTheRunStore(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.runs.err = store.ErrRunActive

	_, err := l.life.UpgradeNow(t.Context(), l.who, l.env)

	require.ErrorIs(t, err, store.ErrRunActive)
}

func TestTheWindowUpgradesAnEnvironmentThatIsDue(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")

	require.NoError(t, l.life.UpgradeDue(t.Context()))

	started := l.only(t)
	assert.Equal(t, managed.KindUpgradePDS, started.kind)
	assert.Equal(t, l.env, started.environment)
	assert.JSONEq(t, `{"from":"0.1.0","version":"0.3.0"}`, started.in)
}

func TestNothingIsUpgradedOutsideTheWindow(t *testing.T) {
	for name, at := range map[string]time.Time{
		"the hour before":    time.Date(2026, time.November, 22, 1, 59, 0, 0, time.UTC),
		"when it has closed": time.Date(2026, time.November, 22, 4, 0, 0, 0, time.UTC),
		"another day":        time.Date(2026, time.November, 23, 2, 30, 0, 0, time.UTC),
	} {
		t.Run(name, func(t *testing.T) {
			l := newLifecycleLab(t)
			l.ready("0.1.0")
			l.now = at

			require.NoError(t, l.life.UpgradeDue(t.Context()))

			assert.Empty(t, l.started())
		})
	}
}

func TestTheWindowIsReadInTheEnvironmentsTimeZone(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	require.NoError(t, l.life.SetWindow(t.Context(), l.who, l.env, store.MaintenanceWindow{Day: time.Saturday, Hour: 23, Zone: "America/New_York"}))
	// Sunday 04:30 UTC is Saturday 23:30 in New York, in November.
	l.now = time.Date(2026, time.November, 22, 4, 30, 0, 0, time.UTC)

	require.NoError(t, l.life.UpgradeDue(t.Context()))

	assert.Len(t, l.started(), 1)
}

func TestAWindowThatRunsPastMidnightStillCoversTheNextDay(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	require.NoError(t, l.life.SetWindow(t.Context(), l.who, l.env, store.MaintenanceWindow{Day: time.Saturday, Hour: 23, Zone: "UTC"}))
	l.now = time.Date(2026, time.November, 22, 0, 30, 0, 0, time.UTC)

	require.NoError(t, l.life.UpgradeDue(t.Context()))

	assert.Len(t, l.started(), 1)
}

func TestADeferredVersionWaitsUntilItsDeferralEnds(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	require.NoError(t, l.db.ManagedEnvironments().SetDeferral(t.Context(), l.team, l.env, "0.3.0", l.now.Add(time.Hour)))

	require.NoError(t, l.life.UpgradeDue(t.Context()))
	assert.Empty(t, l.started(), "deferred")

	l.now = time.Date(2026, time.November, 22, 3, 45, 0, 0, time.UTC)
	require.NoError(t, l.life.UpgradeDue(t.Context()))

	assert.Len(t, l.started(), 1, "after the deferral the version rolls out, so security fixes are not skipped forever")
}

func TestADeferralOfAnOlderVersionDoesNotHoldBackANewerOne(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.3.0")
	require.NoError(t, l.db.ManagedEnvironments().SetDeferral(t.Context(), l.team, l.env, "0.3.0", l.now.Add(24*time.Hour)))

	require.NoError(t, l.life.UpgradeDue(t.Context()))

	assert.Len(t, l.started(), 1)
}

func TestARolledBackVersionIsNotTriedAgainByTheWindow(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	require.NoError(t, l.db.ManagedEnvironments().SetSkippedVersion(t.Context(), l.team, l.env, "0.3.0"))

	require.NoError(t, l.life.UpgradeDue(t.Context()))
	assert.Empty(t, l.started(), "0.3.0 failed here and nothing newer exists")

	paths["0.1.0"] = append(paths["0.1.0"], "0.3.1")
	t.Cleanup(func() { paths["0.1.0"] = paths["0.1.0"][:2] })
	require.NoError(t, l.life.UpgradeDue(t.Context()))

	assert.Len(t, l.started(), 1, "a newer version arrived")
	assert.JSONEq(t, `{"from":"0.1.0","version":"0.3.1"}`, l.started()[0].in)
}

func TestTheWindowLeavesAnEnvironmentThatIsNotReadyOrIsLostOrIsBusy(t *testing.T) {
	l := newLifecycleLab(t)
	require.NoError(t, l.life.UpgradeDue(t.Context()))
	assert.Empty(t, l.started(), "an environment that never reported")

	l.ready("0.1.0")
	l.lost()
	require.NoError(t, l.life.UpgradeDue(t.Context()))
	assert.Empty(t, l.started(), "an environment whose infrastructure is gone")

	require.NoError(t, l.db.ManagedEnvironments().MarkReady(t.Context(), l.team, l.env, l.now))
	l.runs.err = store.ErrRunActive
	require.NoError(t, l.life.UpgradeDue(t.Context()), "a run in hand is not an error")
}

func TestTheWindowKeepsGoingWhenOneEnvironmentCannotBeStarted(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.runs.err = errors.New("the database is away")

	require.NoError(t, l.life.UpgradeDue(t.Context()))
}

func TestInfrastructureShowsTheTemplateRevisionAndTheNextKubernetesVersion(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.cloud.stackState = &aws.StackState{Exists: true, Status: "CREATE_COMPLETE", Outputs: map[string]string{"TemplateRevision": "0"}}
	l.cloud.clusters = []aws.ClusterState{{Exists: true, Status: "ACTIVE", Version: "1.33", NodeGroups: []aws.NodeGroupState{healthyNodes()}}}

	got, err := l.life.Infrastructure(t.Context(), l.who, l.env)

	require.NoError(t, err)
	assert.Equal(t, 0, got.TemplateRevision)
	assert.Equal(t, managed.TemplateRevision, got.LatestTemplateRevision)
	assert.Equal(t, "1.33", got.KubernetesVersion)
	assert.Equal(t, "1.34", got.NextKubernetesVersion)
	assert.Len(t, got.NodeGroups, 1)
	assert.True(t, got.TemplateOutOfDate())
}

func TestInfrastructureOfAnUnreachableAccountSaysSo(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.cloud.failAt["stack"] = []error{aws.ErrAssumeDenied}

	_, err := l.life.Infrastructure(t.Context(), l.who, l.env)

	require.ErrorIs(t, err, managed.ErrConnectionNotReady)
}

func (l *lifecycleLab) stack(revision, version string) {
	l.cloud.stackState = &aws.StackState{
		Exists: true, Status: "CREATE_COMPLETE",
		Outputs: map[string]string{"TemplateRevision": revision}, Parameters: map[string]string{"KubernetesVersion": version},
	}
	l.cloud.clusters = []aws.ClusterState{{Exists: true, Status: "ACTIVE", Version: version, NodeGroups: []aws.NodeGroupState{healthyNodes()}}}
}

func TestAnInfrastructureUpdateStartsAKubernetesRunAfterShowingTheChangeSet(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.stack("1", "1.33")

	run, err := l.life.UpdateInfrastructure(t.Context(), l.who, l.env, managed.InfraUpdate{KubernetesVersion: "1.34"})

	require.NoError(t, err)
	assert.Equal(t, managed.KindKubernetes, run.Kind)
	assert.JSONEq(t, `{"kubernetesVersion":"1.34"}`, l.only(t).in)
	require.Len(t, l.cloud.updates, 1, "the change set is made and read before the run starts")
	assert.Equal(t, map[string]string{"KubernetesVersion": "1.34"}, l.cloud.updates[0].Parameters)
	assert.Equal(t, []string{l.cloud.updates[0].Name}, l.cloud.discarded, "and removed, since the run makes its own")
	assert.Zero(t, l.cloud.count("apply-plan"), "starting a run changes nothing in AWS")
}

func TestKubernetesMovesOneMinorAtATimeAndNeverBack(t *testing.T) {
	for name, version := range map[string]string{
		"a skipped minor": "1.35", "the same version": "1.33", "a downgrade": "1.32", "not a version": "latest", "a patch": "1.34.1",
	} {
		t.Run(name, func(t *testing.T) {
			l := newLifecycleLab(t)
			l.ready("0.1.0")
			l.stack("1", "1.33")

			_, err := l.life.UpdateInfrastructure(t.Context(), l.who, l.env, managed.InfraUpdate{KubernetesVersion: version})

			require.ErrorIs(t, err, managed.ErrInvalid)
			assert.Empty(t, l.started())
			assert.Empty(t, l.cloud.updates)
		})
	}
}

func TestATemplateUpdateWithNothingToChangeIsRefused(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.stack("1", "1.33")

	_, err := l.life.UpdateInfrastructure(t.Context(), l.who, l.env, managed.InfraUpdate{})

	require.ErrorIs(t, err, managed.ErrNothingToUpgrade)
	assert.Empty(t, l.started())
}

func TestATemplateUpdateRunsWhenTheStacksRevisionIsOlder(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.stack("0", "1.33")

	run, err := l.life.UpdateInfrastructure(t.Context(), l.who, l.env, managed.InfraUpdate{})

	require.NoError(t, err)
	assert.Equal(t, managed.KindKubernetes, run.Kind)
	assert.JSONEq(t, `{}`, l.only(t).in)
}

func TestAnUpdateThatWouldReplaceTheNetworkOrTheClusterIsNotStarted(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.stack("0", "1.33")
	l.cloud.plans = []aws.ChangePlan{replacing("Vpc", "AWS::EC2::VPC", "True")}

	_, err := l.life.UpdateInfrastructure(t.Context(), l.who, l.env, managed.InfraUpdate{ConfirmNodeGroupReplace: true})

	require.ErrorIs(t, err, managed.ErrWouldReplace)
	assert.Empty(t, l.started())
	assert.Len(t, l.cloud.discarded, 1)
}

func TestReplacingTheNodeGroupIsNotStartedWithoutTheConfirm(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.stack("0", "1.33")
	l.cloud.plans = []aws.ChangePlan{replacing("NodeGroup", "AWS::EKS::Nodegroup", "True")}

	_, err := l.life.UpdateInfrastructure(t.Context(), l.who, l.env, managed.InfraUpdate{})
	require.ErrorIs(t, err, managed.ErrConfirmRequired)
	assert.Empty(t, l.started())

	_, err = l.life.UpdateInfrastructure(t.Context(), l.who, l.env, managed.InfraUpdate{ConfirmNodeGroupReplace: true})
	require.NoError(t, err)
	assert.JSONEq(t, `{"confirmNodeGroupReplace":true}`, l.only(t).in)
}

func TestPreviewShowsTheChangeSetWithoutStartingARun(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.stack("0", "1.33")
	l.cloud.plans = []aws.ChangePlan{replacing("NodeGroup", "AWS::EKS::Nodegroup", "True")}

	verdict, err := l.life.PreviewUpdate(t.Context(), l.who, l.env, managed.InfraUpdate{KubernetesVersion: "1.34"})

	require.NoError(t, err)
	assert.True(t, verdict.ReplacesNodeGroup)
	assert.False(t, verdict.ReplacesNetwork)
	assert.Len(t, verdict.Plan.Changes, 1)
	assert.Empty(t, l.started())
	assert.Len(t, l.cloud.discarded, 1)
}

func TestInfrastructureUpdateOfALostEnvironmentIsRefused(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.lost()

	_, err := l.life.UpdateInfrastructure(t.Context(), l.who, l.env, managed.InfraUpdate{})

	require.ErrorIs(t, err, managed.ErrLost)
}

func TestRebuildStartsOnlyForALostEnvironment(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")

	_, err := l.life.Rebuild(t.Context(), l.who, l.env)
	require.ErrorIs(t, err, managed.ErrNotLost)
	assert.Empty(t, l.started())

	l.lost()
	run, err := l.life.Rebuild(t.Context(), l.who, l.env)

	require.NoError(t, err)
	assert.Equal(t, managed.KindRebuild, run.Kind)
	assert.Equal(t, managed.KindRebuild, l.only(t).kind)
}

func TestStatusSaysLostWhenTheClusterWasFoundMissing(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.lost()

	status, err := l.life.Status(t.Context(), l.who, l.env)

	require.NoError(t, err)
	assert.True(t, status.Lost)
	assert.Equal(t, "Infrastructure missing", managed.LostReason)
}


func (l *lifecycleLab) drift() []store.DriftItem { return l.record().Drift }

func TestTheHourlyCheckRecordsWhatCloudFormationFoundChanged(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.hub.connected.Store(true)
	l.cloud.drifts = []aws.Drift{{Resource: "PrivateRoute", Type: "AWS::EC2::Route", Status: "MODIFIED"}, {Resource: "NatGateway", Type: "AWS::EC2::NatGateway", Status: "DELETED"}}

	require.NoError(t, l.life.Check(t.Context()))

	assert.Equal(t, []store.DriftItem{
		{Resource: "PrivateRoute (AWS::EC2::Route)", Detail: "Changed outside PDS"},
		{Resource: "NatGateway (AWS::EC2::NatGateway)", Detail: "Deleted outside PDS"},
	}, l.drift())
	assert.NotNil(t, l.record().DriftCheckedAt)
	assert.Zero(t, l.cloud.count("apply-plan")+l.cloud.count("ensure-stack")+l.cloud.count("delete-stack"), "the check only reports")
}

func TestTheHourlyCheckComparesTheClusterAndNodeGroupWithTheStack(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.hub.connected.Store(true)
	l.cloud.stackState = &aws.StackState{Exists: true, Status: "CREATE_COMPLETE", Parameters: map[string]string{
		"KubernetesVersion": "1.33", "NodeCount": "3", "NodeInstanceType": "m6i.xlarge",
	}}
	group := healthyNodes()
	group.Desired, group.InstanceTypes = 5, []string{"m5.large"}
	l.cloud.clusters = []aws.ClusterState{{Exists: true, Status: "ACTIVE", Version: "1.34", PublicEndpoint: true, NodeGroups: []aws.NodeGroupState{group}}}

	require.NoError(t, l.life.Check(t.Context()))

	var names []string
	for _, d := range l.drift() {
		names = append(names, d.Resource)
		assert.Equal(t, "Changed outside PDS", d.Detail)
	}
	assert.ElementsMatch(t, []string{
		"EKS cluster Kubernetes version", "EKS cluster public endpoint", "EKS node group pds-nodes size", "EKS node group pds-nodes instance type",
	}, names)
}

func TestAMissingNodeGroupIsDrift(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.hub.connected.Store(true)
	l.cloud.clusters = []aws.ClusterState{{Exists: true, Status: "ACTIVE", Version: "1.33"}}

	require.NoError(t, l.life.Check(t.Context()))

	assert.Equal(t, []store.DriftItem{{Resource: "EKS node group pds-nodes", Detail: "Deleted outside PDS"}}, l.drift())
}

func TestAHealthyEnvironmentHasNoDrift(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.hub.connected.Store(true)
	require.NoError(t, l.db.ManagedEnvironments().SetDrift(t.Context(), l.team, l.env, []store.DriftItem{{Resource: "old", Detail: "x"}}, time.Now()))

	require.NoError(t, l.life.Check(t.Context()))

	assert.Empty(t, l.drift(), "a change that was put back is no longer reported")
}

func TestADriftDetectionThatFailsKeepsWhatWasKnown(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.hub.connected.Store(true)
	require.NoError(t, l.db.ManagedEnvironments().SetDrift(t.Context(), l.team, l.env, []store.DriftItem{{Resource: "old", Detail: "x"}}, time.Now()))
	l.cloud.failAt["detect-drift"] = []error{errors.New("throttled")}

	require.NoError(t, l.life.Check(t.Context()))

	assert.Equal(t, "old", l.drift()[0].Resource)
}

func TestAStaleHeartbeatAndAMissingClusterMarkTheEnvironmentLost(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.cloud.clusters = []aws.ClusterState{{Exists: false}}

	require.NoError(t, l.life.Check(t.Context()))

	assert.NotNil(t, l.record().LostAt)
	assert.Zero(t, l.cloud.count("detect-drift"), "there is nothing to compare")
	assert.Empty(t, l.started(), "the control plane never rebuilds by itself")
	assert.Zero(t, l.cloud.count("ensure-stack"))
}

func TestAMissingClusterWithALiveHeartbeatIsNotLost(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.hub.connected.Store(true)
	l.cloud.clusters = []aws.ClusterState{{Exists: false}}

	require.NoError(t, l.life.Check(t.Context()))

	assert.Nil(t, l.record().LostAt)
}

func TestAStaleHeartbeatWithTheClusterThereIsNotLostAndClearsAnEarlierLoss(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.lost()

	require.NoError(t, l.life.Check(t.Context()))

	assert.Nil(t, l.record().LostAt)
}

func TestTheHourlyCheckLeavesEnvironmentsItCannotReadAlone(t *testing.T) {
	l := newLifecycleLab(t)
	require.NoError(t, l.life.Check(t.Context()))
	assert.Zero(t, l.cloud.count("cluster"), "an environment that never reported")

	l.ready("0.1.0")
	l.runs.busy[l.env] = true
	require.NoError(t, l.life.Check(t.Context()))
	assert.Zero(t, l.cloud.count("cluster"), "a run is changing it, and a half-built one would look lost")

	l.runs.busy[l.env] = false
	require.NoError(t, l.db.Gorm().WithContext(t.Context()).Model(&store.CloudConnection{}).Where("id = ?", l.connID).Update("state", store.CloudUnreachable).Error)
	require.NoError(t, l.life.Check(t.Context()))
	assert.Zero(t, l.cloud.count("cluster"), "an unreachable connection says nothing about the cluster")
	assert.Nil(t, l.record().LostAt)
}

func TestARoleThatCannotBeAssumedDoesNotMarkTheEnvironmentLost(t *testing.T) {
	l := newLifecycleLab(t)
	l.ready("0.1.0")
	l.cloud.failAt["cluster"] = []error{aws.ErrAssumeDenied}

	require.NoError(t, l.life.Check(t.Context()))

	assert.Nil(t, l.record().LostAt)
}
