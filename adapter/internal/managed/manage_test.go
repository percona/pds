package managed_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/auth"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/provision"
	"github.com/percona/pds/adapter/internal/store"
)

type roster map[string]store.ManagedEnvironment

func (r roster) List(_ context.Context, team uint) ([]store.ManagedEnvironment, error) {
	var out []store.ManagedEnvironment
	for _, row := range r {
		if row.TeamID == team {
			out = append(out, row)
		}
	}

	return out, nil
}

func (r roster) Get(_ context.Context, team uint, name string) (store.ManagedEnvironment, error) {
	row, ok := r[name]
	if !ok || row.TeamID != team {
		return store.ManagedEnvironment{}, store.ErrManagedEnvironmentUnknown
	}

	return row, nil
}

type fakeBook struct {
	runs    map[string]store.ProvisioningRun
	steps   map[uint][]store.ProvisioningStep
	started []string
	retried []string
	startFn func(kind string) error
}

func (f *fakeBook) Start(_ context.Context, _ uint, environment, kind string) (store.ProvisioningRun, error) {
	if f.startFn != nil {
		if err := f.startFn(kind); err != nil {
			return store.ProvisioningRun{}, err
		}
	}
	f.started = append(f.started, kind+"/"+environment)

	return store.ProvisioningRun{ID: 99, Environment: environment, Kind: kind, Status: store.RunQueued}, nil
}

func (f *fakeBook) Retry(_ context.Context, _ uint, environment string) error {
	f.retried = append(f.retried, environment)

	return nil
}

func (f *fakeBook) Latest(_ context.Context, _ uint, environment string) (store.ProvisioningRun, error) {
	run, ok := f.runs[environment]
	if !ok {
		return store.ProvisioningRun{}, store.ErrRunNotFound
	}

	return run, nil
}

func (f *fakeBook) Steps(_ context.Context, runID uint) ([]store.ProvisioningStep, error) {
	return f.steps[runID], nil
}

type fakeRemover struct {
	deleted []string
	removed []string
	err     error
}

func (f *fakeRemover) Delete(_ context.Context, _ auth.Scope, name string) (store.ProvisioningRun, error) {
	f.deleted = append(f.deleted, name)

	return store.ProvisioningRun{ID: 98, Environment: name, Kind: store.KindDelete, Status: store.RunQueued}, f.err
}

func (f *fakeRemover) RemoveFromPDS(_ context.Context, _ auth.Scope, name string) error {
	if f.err == nil {
		f.removed = append(f.removed, name)
	}

	return f.err
}

var managerScope = auth.Scope{UserID: 3, Team: auth.Team{ID: 7}}

func newManager(book *fakeBook, remover managed.Removal) *managed.Manager {
	return managed.NewManager(
		roster{
			"orders": {ID: "aaaaaaaaaaaa", TeamID: 7, Name: "orders", ConnectionID: "conn1", Region: "eu-west-1"},
			"other":  {ID: "bbbbbbbbbbbb", TeamID: 8, Name: "other", ConnectionID: "conn2", Region: "eu-west-1"},
		},
		book,
		remover,
	)
}

func createSteps(statuses ...string) []store.ProvisioningStep {
	var rows []store.ProvisioningStep
	for i, name := range managed.CreateStepNames {
		status := store.StepPending
		if i < len(statuses) {
			status = statuses[i]
		}
		rows = append(rows, store.ProvisioningStep{RunID: 5, Position: i, Name: name, Status: status})
	}

	return rows
}

func TestAnEnvironmentThatNeverHadARunHasNoProvisioning(t *testing.T) {
	m := newManager(&fakeBook{}, nil)

	got, err := m.Provisioning(t.Context(), managerScope.Team.ID, "orders")

	require.NoError(t, err)
	assert.Equal(t, managed.ProgressReady, got.Progress)
	assert.Zero(t, got.Run.ID)
}

func TestACreateInProgressShowsFourPhasesAndTheOneRunning(t *testing.T) {
	book := &fakeBook{
		runs: map[string]store.ProvisioningRun{"orders": {ID: 5, Kind: managed.KindCreate, Status: store.RunRunning}},
		steps: map[uint][]store.ProvisioningStep{5: createSteps(
			store.StepSucceeded, store.StepSucceeded, store.StepSucceeded, store.StepSucceeded, store.StepRunning)},
	}

	got, err := newManager(book, nil).Provisioning(t.Context(), 7, "orders")

	require.NoError(t, err)
	assert.Equal(t, managed.ProgressInProgress, got.Progress)
	assert.Equal(t, []managed.PhaseView{
		{Name: "Creating network and Kubernetes cluster", Status: managed.PhaseSucceeded},
		{Name: "Connecting to the environment", Status: managed.PhaseRunning},
		{Name: "Installing PDS", Status: managed.PhasePending},
		{Name: "Finishing", Status: managed.PhasePending},
	}, got.Phases)
	assert.Nil(t, got.Failure)
}

func TestAFailedStepGivesItsSafeSentenceOneActionAndTheTypedRefusal(t *testing.T) {
	rows := createSteps(store.StepSucceeded, store.StepFailed)
	rows[1].Failure = "AWS account 123456789012 has reached the quota for Elastic IP addresses per Region in eu-west-1."
	rows[1].Detail = "raw AWS text with arn:aws:iam::123456789012:role/x"
	rows[1].Refusal = store.StepRefusal{Reason: "quota", Service: "Amazon EC2", Item: "Elastic IP addresses per Region", Region: "eu-west-1"}
	book := &fakeBook{
		runs:  map[string]store.ProvisioningRun{"orders": {ID: 5, Kind: managed.KindCreate, Status: store.RunFailed}},
		steps: map[uint][]store.ProvisioningStep{5: rows},
	}

	got, err := newManager(book, nil).Provisioning(t.Context(), 7, "orders")

	require.NoError(t, err)
	assert.Equal(t, managed.ProgressFailed, got.Progress)
	require.NotNil(t, got.Failure)
	assert.Equal(t, rows[1].Failure, got.Failure.Sentence)
	assert.Equal(t, managed.ActionRetry, got.Failure.Action)
	assert.Equal(t, &managed.Refusal{Reason: "quota", Service: "Amazon EC2", Item: "Elastic IP addresses per Region", Region: "eu-west-1"}, got.Failure.Refusal)
	assert.Equal(t, managed.PhaseFailed, got.Phases[0].Status)
	assert.NotContains(t, fmt.Sprintf("%+v", got), "arn:aws", "the raw detail never leaves the record")
}

func TestAFailureThatIsNotARefusalHasNoRefusalObject(t *testing.T) {
	rows := createSteps(store.StepFailed)
	rows[0].Failure = "A step did not finish. Retry it, or contact Percona support if it fails again."
	book := &fakeBook{
		runs:  map[string]store.ProvisioningRun{"orders": {ID: 5, Kind: managed.KindCreate, Status: store.RunFailed}},
		steps: map[uint][]store.ProvisioningStep{5: rows},
	}

	got, err := newManager(book, nil).Provisioning(t.Context(), 7, "orders")

	require.NoError(t, err)
	require.NotNil(t, got.Failure)
	assert.Nil(t, got.Failure.Refusal)
}

func TestARunPausedOnARevokedConnectionIsWaitingNotFailed(t *testing.T) {
	book := &fakeBook{
		runs:  map[string]store.ProvisioningRun{"orders": {ID: 5, Kind: managed.KindCreate, Status: store.RunPaused, Message: provision.WaitingForConnection}},
		steps: map[uint][]store.ProvisioningStep{5: createSteps(store.StepSucceeded)},
	}

	got, err := newManager(book, nil).Provisioning(t.Context(), 7, "orders")

	require.NoError(t, err)
	assert.Equal(t, managed.ProgressWaiting, got.Progress)
	assert.Equal(t, provision.WaitingForConnection, got.Message)
	assert.Nil(t, got.Failure)
	assert.Equal(t, managed.PhaseWaiting, got.Phases[0].Status, "the phase the run stopped in waits")
	assert.Equal(t, managed.PhasePending, got.Phases[1].Status)
}

func TestAFailedDeleteIsBlockedAndAFinishedCreateIsReady(t *testing.T) {
	book := &fakeBook{
		runs: map[string]store.ProvisioningRun{
			"orders": {ID: 6, Kind: store.KindDelete, Status: store.RunFailed, Message: "Delete blocked"},
			"done":   {ID: 7, Kind: managed.KindCreate, Status: store.RunSucceeded},
		},
		steps: map[uint][]store.ProvisioningStep{6: {{RunID: 6, Name: "teardown", Status: store.StepFailed, Failure: "Two volumes remain."}}},
	}
	m := newManager(book, nil)

	blocked, err := m.Provisioning(t.Context(), 7, "orders")
	require.NoError(t, err)
	done, err := m.Provisioning(t.Context(), 7, "done")
	require.NoError(t, err)

	assert.Equal(t, managed.ProgressBlocked, blocked.Progress)
	assert.Equal(t, "Two volumes remain.", blocked.Failure.Sentence)
	assert.Equal(t, managed.ProgressReady, done.Progress)
}

func TestTheStateOfAManagedEnvironmentFollowsItsRunsThenItsHeartbeat(t *testing.T) {
	for _, tc := range []struct {
		name      string
		run       store.ProvisioningRun
		connected bool
		lost      bool
		want      string
	}{
		{"creating", store.ProvisioningRun{Kind: managed.KindCreate, Status: store.RunRunning}, false, false, managed.StateCreating},
		{"create failed", store.ProvisioningRun{Kind: managed.KindCreate, Status: store.RunFailed}, false, false, managed.StateFailed},
		{"deleting", store.ProvisioningRun{Kind: store.KindDelete, Status: store.RunQueued}, true, false, managed.StateDeleting},
		{"delete blocked is still deleting", store.ProvisioningRun{Kind: store.KindDelete, Status: store.RunFailed}, true, false, managed.StateDeleting},
		{"upgrade running keeps the environment ready", store.ProvisioningRun{Kind: managed.KindUpgradePDS, Status: store.RunRunning}, true, false, managed.StateReady},
		{"connected after a create", store.ProvisioningRun{Kind: managed.KindCreate, Status: store.RunSucceeded}, true, false, managed.StateReady},
		{"silent after a create", store.ProvisioningRun{Kind: managed.KindCreate, Status: store.RunSucceeded}, false, false, managed.StateDegraded},
		{"infrastructure missing", store.ProvisioningRun{Kind: managed.KindCreate, Status: store.RunSucceeded}, false, true, managed.StateLost},
		{"a rebuild in progress is creating", store.ProvisioningRun{Kind: managed.KindRebuild, Status: store.RunRunning}, false, true, managed.StateCreating},
		{"a delete of a lost environment is deleting", store.ProvisioningRun{Kind: store.KindDelete, Status: store.RunRunning}, false, true, managed.StateDeleting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := managed.Provisioning{Run: managed.RunView{Kind: tc.run.Kind}}
			p.Progress = managed.ProgressOf(tc.run)

			assert.Equal(t, tc.want, managed.StateOf(p, tc.connected, tc.lost))
		})
	}
}

func TestStartingARunNeedsAManagedEnvironmentOfTheTeamAndAKnownKind(t *testing.T) {
	book := &fakeBook{}
	m := newManager(book, nil)

	run, err := m.Start(t.Context(), managerScope, "orders", managed.KindUpgradePDS)
	require.NoError(t, err)
	assert.Equal(t, uint(99), run.ID)
	assert.Equal(t, []string{"upgrade_pds/orders"}, book.started)

	_, err = m.Start(t.Context(), managerScope, "other", managed.KindRebuild)
	require.ErrorIs(t, err, store.ErrManagedEnvironmentUnknown, "another Team's environment is not found")
	_, err = m.Start(t.Context(), managerScope, "orders", "create")
	require.ErrorIs(t, err, managed.ErrInvalid)
	_, err = m.Start(t.Context(), managerScope, "orders", "delete")
	require.ErrorIs(t, err, managed.ErrInvalid, "delete has its own route")
	assert.Len(t, book.started, 1)
}

func TestAKindNoReleaseHasStepsForIsUnavailable(t *testing.T) {
	book := &fakeBook{startFn: func(kind string) error { return fmt.Errorf("%w %q", provision.ErrUnknownKind, kind) }}

	_, err := newManager(book, nil).Start(t.Context(), managerScope, "orders", managed.KindRebuild)

	require.ErrorIs(t, err, managed.ErrUnavailable)
}

func TestARunAlreadyActiveIsPassedThroughForTheRouteToAnswer(t *testing.T) {
	book := &fakeBook{startFn: func(string) error { return store.ErrRunActive }}

	_, err := newManager(book, nil).Start(t.Context(), managerScope, "orders", managed.KindRebuild)

	require.ErrorIs(t, err, store.ErrRunActive)
}

func TestRetryNeedsTheLatestRunOfAManagedEnvironment(t *testing.T) {
	book := &fakeBook{runs: map[string]store.ProvisioningRun{"orders": {ID: 5, Status: store.RunFailed}}}
	m := newManager(book, nil)

	require.ErrorIs(t, m.Retry(t.Context(), managerScope, "orders", 4), store.ErrRunNotFound, "an older run is not retried")
	require.ErrorIs(t, m.Retry(t.Context(), managerScope, "other", 5), store.ErrManagedEnvironmentUnknown)
	assert.Empty(t, book.retried)

	require.NoError(t, m.Retry(t.Context(), managerScope, "orders", 5))
	assert.Equal(t, []string{"orders"}, book.retried)
}

func TestDeleteNeedsTheTypedNameAndStartsTheDeleteRun(t *testing.T) {
	remover := &fakeRemover{}
	m := newManager(&fakeBook{}, remover)

	_, err := m.Delete(t.Context(), managerScope, "orders", "order")
	require.ErrorIs(t, err, managed.ErrConfirm)
	_, err = m.Delete(t.Context(), managerScope, "orders", "")
	require.ErrorIs(t, err, managed.ErrConfirm)
	_, err = m.Delete(t.Context(), managerScope, "other", "other")
	require.ErrorIs(t, err, store.ErrManagedEnvironmentUnknown, "another Team's environment is not found")
	assert.Empty(t, remover.deleted)

	run, err := m.Delete(t.Context(), managerScope, "orders", "orders")
	require.NoError(t, err)
	assert.Equal(t, store.KindDelete, run.Kind)
	assert.Equal(t, []string{"orders"}, remover.deleted)
}

func TestRemoveFromPDSOnlyNeedsTheTypedNameAndPassesTheReachabilityRefusalThrough(t *testing.T) {
	remover := &fakeRemover{err: managed.ErrConnectionReachable}
	m := newManager(&fakeBook{}, remover)

	require.ErrorIs(t, m.RemoveFromPDS(t.Context(), managerScope, "orders", "wrong"), managed.ErrConfirm)
	require.ErrorIs(t, m.RemoveFromPDS(t.Context(), managerScope, "orders", "orders"), managed.ErrConnectionReachable)

	remover.err = nil
	require.NoError(t, m.RemoveFromPDS(t.Context(), managerScope, "orders", "orders"))
	assert.Equal(t, []string{"orders"}, remover.removed)
}

func TestDeletingIsUnavailableWithoutARemoval(t *testing.T) {
	m := newManager(&fakeBook{}, nil)

	_, err := m.Delete(t.Context(), managerScope, "orders", "orders")
	require.ErrorIs(t, err, managed.ErrUnavailable)
	require.ErrorIs(t, m.RemoveFromPDS(t.Context(), managerScope, "orders", "orders"), managed.ErrUnavailable)
}

func TestTheTeamsManagedEnvironmentsAreListedWithTheirProvisioning(t *testing.T) {
	book := &fakeBook{runs: map[string]store.ProvisioningRun{"orders": {ID: 5, Kind: managed.KindCreate, Status: store.RunSucceeded}}}

	got, err := newManager(book, nil).Environments(t.Context(), 7)

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "orders", got[0].Record.Name)
	assert.Equal(t, managed.ProgressReady, got[0].Provisioning.Progress)
}
