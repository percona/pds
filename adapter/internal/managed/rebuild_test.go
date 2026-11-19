package managed_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/store"
)

func (l *lab) lost() {
	l.t.Helper()

	require.NoError(l.t, l.db.ManagedEnvironments().SetLost(l.t.Context(), l.team, l.env, true, time.Now()))
}

func stackIn(status string) aws.StackState { return aws.StackState{Exists: true, Status: status} }

func TestRebuildBuildsTheStacksAgainAndInstallsTheVersionTheEnvironmentRan(t *testing.T) {
	l := newLab(t)
	l.ready("0.0.9")
	l.lost()
	l.cloud.stacks = []aws.StackState{stackIn("CREATE_COMPLETE"), stackIn("DELETE_IN_PROGRESS"), {Exists: false}, stackIn("CREATE_COMPLETE")}
	l.hub.connected.Store(true)
	e := l.kindEngine(managed.RebuildKind(l.deps))
	run := l.startWith(e, "")

	l.work(e)

	got := l.run(run.ID)
	require.Equal(t, store.RunSucceeded, got.Status, got.Message)
	assert.Equal(t, 1, l.cloud.count("delete-stack"), "what is left of the old stack is removed first")
	assert.Equal(t, 1, l.cloud.count("ensure-stack"))
	assert.Equal(t, 1, l.cloud.count("ensure-bucket"), "the bucket is kept, and made only if it is gone")
	assert.Contains(t, l.releaseInGit(), "version: 0.0.9", "a rebuild installs the version the environment ran, not the newest")
	assert.Equal(t, 1, l.hub.joins, "the new cluster gets its own join token")
	rec := l.record()
	assert.Nil(t, rec.LostAt, "a rebuilt environment is no longer lost")
	assert.NotNil(t, rec.ReadyAt)
}

func TestRebuildOfAnAccountWhoseStackIsGoneDeletesNothing(t *testing.T) {
	l := newLab(t)
	l.ready("0.0.9")
	l.lost()
	l.cloud.stacks = []aws.StackState{{Exists: false}, stackIn("CREATE_COMPLETE")}
	l.hub.connected.Store(true)
	e := l.kindEngine(managed.RebuildKind(l.deps))
	run := l.startWith(e, "")

	l.work(e)

	require.Equal(t, store.RunSucceeded, l.run(run.ID).Status)
	assert.Zero(t, l.cloud.count("delete-stack"))
}

func TestRebuildReportsAStackAWSCouldNotDeleteAndRetryDeletesItAgain(t *testing.T) {
	l := newLab(t)
	l.ready("0.0.9")
	l.lost()
	l.cloud.stacks = []aws.StackState{
		stackIn("CREATE_COMPLETE"),
		{Exists: true, Status: "DELETE_FAILED", FailureReason: "The vpc 'vpc-1' has dependencies and cannot be deleted. (Service: AmazonEC2; Status Code: 400; Error Code: DependencyViolation; Request ID: x)"},
	}
	e := l.kindEngine(managed.RebuildKind(l.deps))
	run := l.startWith(e, "")

	l.work(e)

	step := l.steps(run.ID)["clear-stack"]
	require.Equal(t, store.StepFailed, step.Status)
	assert.Contains(t, step.Failure, "Contact Percona support")
	assert.Equal(t, 1, l.cloud.count("delete-stack"))

	l.cloud.mu.Lock()
	l.cloud.stacks = []aws.StackState{{Exists: false}, stackIn("CREATE_COMPLETE")}
	l.cloud.mu.Unlock()
	l.hub.connected.Store(true)
	require.NoError(t, e.Retry(t.Context(), l.team, l.env))
	l.work(e)

	assert.Equal(t, store.RunSucceeded, l.run(run.ID).Status, l.run(run.ID).Message)
}

func TestRebuildShowsItsStepsInThePhasesOfACreate(t *testing.T) {
	assert.Equal(t, "Creating network and Kubernetes cluster", managed.Phase("clear-stack"))
	for _, name := range managed.RebuildStepNames {
		assert.NotEmpty(t, managed.Phase(name), name)
	}
}
