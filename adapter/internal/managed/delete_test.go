package managed_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/provision"
	"github.com/percona/pds/adapter/internal/store"
)

type fakeSweeper struct {
	mu         sync.Mutex
	events     []string
	owned      [][]aws.Resource
	releaseErr map[string]error
	bucket     bool
	bucketErr  error
}

func (f *fakeSweeper) Owned(_ context.Context, _ aws.Target, id string) ([]aws.Resource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "owned "+id)
	if len(f.owned) == 0 {
		return nil, nil
	}
	found := f.owned[0]
	if len(f.owned) > 1 {
		f.owned = f.owned[1:]
	}

	return found, nil
}

func (f *fakeSweeper) Release(_ context.Context, _ aws.Target, r aws.Resource) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "release "+r.ID)

	return f.releaseErr[r.ID]
}

func (f *fakeSweeper) DeleteBucket(_ context.Context, _ aws.Target, bucket, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "bucket "+bucket+" "+id)

	return f.bucket, f.bucketErr
}

func (f *fakeSweeper) count(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, e := range f.events {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}

	return n
}

type fakeDrainer struct {
	mu        sync.Mutex
	databases []int
	volumes   []int
	err       error
	calls     []string
}

func (f *fakeDrainer) next(queue *[]int) int {
	if len(*queue) == 0 {
		return 0
	}
	n := (*queue)[0]
	if len(*queue) > 1 {
		*queue = (*queue)[1:]
	}

	return n
}

func (f *fakeDrainer) RemoveDatabases(context.Context, managed.KubeAccess) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "databases")
	if f.err != nil {
		return 0, f.err
	}

	return f.next(&f.databases), nil
}

func (f *fakeDrainer) RemoveVolumeUsers(context.Context, managed.KubeAccess) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "volume users")
	if f.err != nil {
		return 0, f.err
	}

	return f.next(&f.volumes), nil
}

// deleteLab is a lab whose runs are deletes. Forgetting the environment removes
// its record, as production does.
type deleteLab struct {
	*lab
	sweeper   *fakeSweeper
	drainer   *fakeDrainer
	forgotten []string
	forgetErr error
}

func newDeleteLab(t *testing.T) *deleteLab {
	t.Helper()

	d := &deleteLab{lab: newLab(t), sweeper: &fakeSweeper{releaseErr: map[string]error{}}, drainer: &fakeDrainer{}}
	d.deps.Sweeper = d.sweeper
	d.deps.Drainer = d.drainer
	d.deps.Forget = func(ctx context.Context, team uint, name string) error {
		d.forgotten = append(d.forgotten, name)
		if err := d.db.ManagedEnvironments().Remove(ctx, team, name); err != nil {
			return err
		}

		return d.forgetErr
	}
	d.deps.Settings.DeleteWait = 100 * time.Millisecond
	d.deps.Settings.DrainWait = 100 * time.Millisecond
	d.deps.Settings.APIWait = 20 * time.Millisecond
	// The stack is there until the delete asks for it to go.
	d.cloud.stacks = []aws.StackState{{Exists: true, Status: "CREATE_COMPLETE"}, {Exists: false}}

	return d
}

func (d *deleteLab) engine() *provision.Engine {
	d.t.Helper()

	kind := managed.DeleteKind(d.deps)
	kind.Name = d.kind
	e, err := provision.New(d.prov, []provision.Kind{kind}, provision.Options{
		Owner: "worker-1", LeaseTTL: time.Minute, RenewEvery: 10 * time.Millisecond,
		MaxAttempts: 2, Backoff: func(int) time.Duration { return 0 },
	})
	require.NoError(d.t, err)

	return e
}

func (d *deleteLab) finish(e *provision.Engine, run store.ProvisioningRun) store.ProvisioningRun {
	d.t.Helper()

	for range 10 {
		ran, err := e.RunOnce(d.t.Context())
		require.NoError(d.t, err)
		if !ran {
			break
		}
		if got := d.run(run.ID); got.Status != store.RunQueued {
			return got
		}
	}

	return d.run(run.ID)
}

func volume(id string) aws.Resource {
	return aws.Resource{Type: "EBS volume", ID: id, ARN: "arn:aws:ec2:eu-west-1:111122223333:volume/" + id}
}

func TestDeleteRemovesWhatIsInsideBeforeWhatHoldsIt(t *testing.T) {
	d := newDeleteLab(t)
	d.sweeper.owned = [][]aws.Resource{{volume("vol-1")}}
	d.sweeper.bucket = true
	e := d.engine()
	run := d.start(e)

	got := d.finish(e, run)

	require.Equal(t, store.RunSucceeded, got.Status, got.Message)
	assert.Equal(t, []string{"databases", "volume users"}, d.drainer.calls)
	assert.Equal(t, 1, d.cloud.count("delete-stack"))
	assert.Equal(t, 0, d.cloud.count("preflight"), "a delete never checks quotas")
	assert.Equal(t, []string{"owned " + d.envID, "release vol-1"}, d.sweeper.events[:2])
	assert.Equal(t, "bucket pds-111122223333-"+d.envID+" "+d.envID, d.sweeper.events[2])
	assert.Equal(t, []string{"orders"}, d.forgotten)

	steps := d.steps(run.ID)
	assert.Contains(t, steps["sweep"].Detail, "EBS volume vol-1", "the sweep records what it removed")
	assert.Contains(t, steps["delete-bucket"].Detail, "backup bucket")
}

func TestDeleteWaitsForDatabasesToLeaveBeforeGoingOn(t *testing.T) {
	d := newDeleteLab(t)
	d.drainer.databases = []int{2, 1, 0}
	e := d.engine()
	run := d.start(e)

	got := d.finish(e, run)

	require.Equal(t, store.RunSucceeded, got.Status, got.Message)
	assert.Equal(t, 3, strings.Count(strings.Join(d.drainer.calls, ","), "databases"))
}

func TestDeleteRecordsAnEnvironmentItCannotReachAndGoesOnToDeleteTheEKS(t *testing.T) {
	d := newDeleteLab(t)
	d.drainer.err = errors.New("dial tcp: i/o timeout")
	e := d.engine()
	run := d.start(e)

	got := d.finish(e, run)

	require.Equal(t, store.RunSucceeded, got.Status, got.Message)
	steps := d.steps(run.ID)
	assert.Contains(t, steps["remove-databases"].Detail, "Kubernetes API did not answer")
	assert.Contains(t, steps["remove-services"].Detail, "Kubernetes API did not answer")
	assert.Equal(t, 1, d.cloud.count("delete-stack"), "the cluster is deleted anyway and the sweep removes the rest")
}

func TestDeleteOfAnEnvironmentWhoseEKSWasNeverBuiltSkipsTheInside(t *testing.T) {
	d := newDeleteLab(t)
	d.cloud.stacks = []aws.StackState{{Exists: false}}
	d.cloud.failAt["open-access"] = []error{apiErr("ResourceNotFoundException", "No cluster found"), apiErr("ResourceNotFoundException", "No cluster found")}
	e := d.engine()
	run := d.start(e)

	got := d.finish(e, run)

	require.Equal(t, store.RunSucceeded, got.Status, got.Message)
	assert.Empty(t, d.drainer.calls)
	assert.Equal(t, 0, d.cloud.count("delete-stack"))
	assert.Contains(t, d.steps(run.ID)["remove-databases"].Detail, "already gone")
}

func TestDeleteSweepsAndTriesAgainWhenTheStackCannotDeleteItsNetwork(t *testing.T) {
	d := newDeleteLab(t)
	d.cloud.stacks = []aws.StackState{
		{Exists: true, Status: "CREATE_COMPLETE"},
		{Exists: true, Status: "DELETE_FAILED", FailureReason: "The vpc has dependencies and cannot be deleted"},
		{Exists: false},
	}
	d.sweeper.owned = [][]aws.Resource{{{Type: "network interface", ID: "eni-1", ARN: "arn:eni-1"}}}
	e := d.engine()
	run := d.start(e)

	got := d.finish(e, run)

	require.Equal(t, store.RunSucceeded, got.Status, got.Message)
	assert.Equal(t, 2, d.cloud.count("delete-stack"))
	assert.Positive(t, d.sweeper.count("release eni-1"))
}

func TestDeleteIsBlockedAndListsWhatAWSWillNotRelease(t *testing.T) {
	d := newDeleteLab(t)
	d.sweeper.owned = [][]aws.Resource{{volume("vol-1"), volume("vol-2")}}
	d.sweeper.releaseErr["vol-1"] = apiErr("VolumeInUse", "vol-1 is in use")
	e := d.engine()
	run := d.start(e)

	got := d.finish(e, run)

	require.Equal(t, store.RunFailed, got.Status)
	assert.True(t, strings.HasPrefix(got.Message, managed.DeleteBlocked), got.Message)
	assert.Contains(t, got.Message, "EBS volume vol-1")
	assert.NotContains(t, got.Message, "vol-2", "what was released is not a leftover")
	assert.Equal(t, store.StepFailed, d.steps(run.ID)["sweep"].Status)
	assert.Empty(t, d.forgotten, "a blocked delete keeps the environment")
	assert.LessOrEqual(t, len(got.Message), 512)
}

func TestRetryOfABlockedDeleteRediscoversAndFinishes(t *testing.T) {
	d := newDeleteLab(t)
	d.sweeper.owned = [][]aws.Resource{{volume("vol-1")}}
	d.sweeper.releaseErr["vol-1"] = apiErr("VolumeInUse", "vol-1 is in use")
	d.cloud.stacks = []aws.StackState{{Exists: false}}
	e := d.engine()
	run := d.start(e)
	require.Equal(t, store.RunFailed, d.finish(e, run).Status)
	before := d.sweeper.count("owned")

	delete(d.sweeper.releaseErr, "vol-1")
	require.NoError(t, e.Retry(t.Context(), d.team, d.env))
	got := d.finish(e, run)

	require.Equal(t, store.RunSucceeded, got.Status, got.Message)
	assert.Greater(t, d.sweeper.count("owned"), before, "every attempt rediscovers by tag")
	assert.Equal(t, 1, d.steps(run.ID)["remove-databases"].Attempts, "only the failed step runs again")
}

func TestDeleteLeavesABucketThatIsNotTheEnvironmentsAndSaysSo(t *testing.T) {
	d := newDeleteLab(t)
	d.sweeper.bucket = false
	e := d.engine()
	run := d.start(e)

	got := d.finish(e, run)

	require.Equal(t, store.RunSucceeded, got.Status, got.Message)
	assert.Contains(t, d.steps(run.ID)["delete-bucket"].Detail, "No backup bucket")
}

func TestDeletePausesWhileTheConnectionIsUnreachable(t *testing.T) {
	d := newDeleteLab(t)
	require.NoError(t, d.db.Gorm().WithContext(t.Context()).Model(&store.CloudConnection{}).Where("id = ?", d.connID).
		Update("state", store.CloudUnreachable).Error)
	e := d.engine()
	run := d.start(e)

	got := d.finish(e, run)

	assert.Equal(t, store.RunPaused, got.Status)
	assert.Equal(t, provision.WaitingForConnection, got.Message)
	assert.Equal(t, 0, d.cloud.count("delete-stack"))
}

func TestDeleteFinishesWhenARerunFindsTheRecordAlreadyForgotten(t *testing.T) {
	d := newDeleteLab(t)
	d.forgetErr = errors.New("the control plane stopped after removing the record")
	e := d.engine()
	run := d.start(e)

	got := d.finish(e, run)

	require.Equal(t, store.RunSucceeded, got.Status, got.Message)
	assert.Len(t, d.forgotten, 1, "the second attempt finds the record gone and has nothing left to do")
}

func TestDeleteStepNamesAreInTheOrderTheyRunAndEachHasAPhase(t *testing.T) {
	assert.Equal(t, []string{
		"assume-role", "remove-databases", "remove-services", "close-access", "delete-stack", "sweep", "delete-bucket", "forget",
	}, managed.DeleteStepNames)
	for _, name := range managed.DeleteStepNames {
		assert.NotEmpty(t, managed.DeletePhase(name), name)
	}
	assert.Empty(t, managed.DeletePhase("not-a-step"))
	assert.True(t, strings.Contains(fmt.Sprint(managed.DeletePhase("delete-stack")), "Kubernetes cluster"))
}
