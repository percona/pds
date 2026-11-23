package mongodb_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/mongodb"
	"github.com/percona/pds/adapter/internal/engine/oe"
)

const scaleID = "pds.orders"

// scaleFake holds one Instance the way the Everest API does: a read returns a
// copy, and an update replaces what is held.
type scaleFake struct {
	mu      sync.Mutex
	held    everest.Instance
	backups []everest.Backup
}

var _ mongodb.EverestClient = (*scaleFake)(nil)

func copyOf(in everest.Instance) everest.Instance {
	body, err := json.Marshal(in)
	if err != nil {
		panic(err)
	}
	var out everest.Instance
	if err := json.Unmarshal(body, &out); err != nil {
		panic(err)
	}

	return out
}

func (f *scaleFake) ListInstances(context.Context, string) ([]everest.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return []everest.Instance{copyOf(f.held)}, nil
}

func (f *scaleFake) GetInstance(context.Context, string, string) (*everest.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := copyOf(f.held)

	return &out, nil
}

func (f *scaleFake) UpdateInstance(_ context.Context, _, _ string, in *everest.Instance) (*everest.Instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held = copyOf(*in)
	out := copyOf(f.held)

	return &out, nil
}

func (f *scaleFake) DeleteInstance(context.Context, string, string) error { return nil }

func (f *scaleFake) GetInstanceConnection(context.Context, string, string) (*everest.Connection, error) {
	return nil, everest.ErrNotFound
}

func (f *scaleFake) ListInstanceBackups(context.Context, string, string) ([]everest.Backup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.backups, nil
}

func (f *scaleFake) CreateBackup(context.Context, string, *everest.Backup) (*everest.Backup, error) {
	return nil, everest.ErrNotFound
}

func (f *scaleFake) setStatus(status everest.InstanceStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.held.Status = status
}

func (f *scaleFake) stored() *everest.Instance {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := copyOf(f.held)

	return &out
}

func newScaleFake(members int32, status everest.InstanceStatus) *scaleFake {
	return &scaleFake{held: mongoInstance("orders", members, status)}
}

func ints(v int) *int { return &v }

func timeAgo(minutes int) time.Time { return time.Now().Add(-time.Duration(minutes) * time.Minute) }

func ready(members int32) everest.InstanceStatus {
	return everest.InstanceStatus{
		Phase: everest.PhaseReady, Components: replicas(members, members),
		ConnectionSecretRef: &everest.ObjectRef{Name: "orders-conn"},
	}
}

// The provider reports Provisioning for every change, so the same words mean a
// first start or a change depending on whether the database was Ready before.
func TestScaleOfAReadyMongoDBDatabaseReadsUpdatingNotProvisioning(t *testing.T) {
	fake := newScaleFake(1, ready(1))
	p := mongodb.New(fake)

	first, err := p.List(context.Background())
	require.NoError(t, err)
	require.Equal(t, engine.PhaseReady, first[0].Status.Phase)

	// Members come up one at a time, so a ready member is already counted.
	fake.setStatus(everest.InstanceStatus{
		Phase: everest.PhaseProvisioning, Message: "Cluster is being created", Components: replicas(1, 3),
	})
	changing, err := p.List(context.Background())
	require.NoError(t, err)
	assert.Equal(t, engine.PhaseUpdating, changing[0].Status.Phase)
	assert.Equal(t, "Applying changes", changing[0].Status.Message)

	fake.setStatus(ready(3))
	done, err := p.List(context.Background())
	require.NoError(t, err)
	assert.Equal(t, engine.PhaseReady, done[0].Status.Phase)
}

func TestANewMongoDBDatabaseStaysProvisioningUntilItsFirstReady(t *testing.T) {
	fake := newScaleFake(3, everest.InstanceStatus{Phase: everest.PhaseProvisioning, Components: replicas(0, 3)})
	p := mongodb.New(fake)

	got, err := p.List(context.Background())
	require.NoError(t, err)
	assert.Equal(t, engine.PhaseProvisioning, got[0].Status.Phase)

	fake.setStatus(everest.InstanceStatus{Phase: everest.PhaseProvisioning, Components: replicas(2, 3)})
	got, err = p.List(context.Background())
	require.NoError(t, err)
	assert.Equal(t, engine.PhaseInitializing, got[0].Status.Phase, "a first start keeps the create stages")
	assert.NotEqual(t, engine.PhaseUpdating, got[0].Status.Phase)
}

func TestAMongoDBDatabaseThatFailsAfterBeingReadyReadsFailed(t *testing.T) {
	fake := newScaleFake(1, ready(1))
	p := mongodb.New(fake)
	_, err := p.List(context.Background())
	require.NoError(t, err)

	fake.setStatus(everest.InstanceStatus{Phase: everest.PhaseFailed})
	got, err := p.List(context.Background())
	require.NoError(t, err)
	assert.Equal(t, engine.PhaseFailed, got[0].Status.Phase)
}

func TestAnUpdatingMongoDBDatabaseThatCannotBePlacedSaysSo(t *testing.T) {
	fake := newScaleFake(1, ready(1))
	p := mongodb.New(fake)
	_, err := p.List(context.Background())
	require.NoError(t, err)

	held := timeAgo(10)
	fake.setStatus(everest.InstanceStatus{
		Phase: everest.PhaseProvisioning, Components: replicas(1, 3),
		Conditions: []everest.Condition{{
			Type: everest.ConditionPodsScheduled, Status: "False", Reason: "Unschedulable",
			Message:            "0/1 nodes are available: 1 node(s) didn't match pod anti-affinity rules.",
			LastTransitionTime: &held,
		}},
	})
	got, err := p.List(context.Background())
	require.NoError(t, err)

	assert.Equal(t, engine.PhaseUpdating, got[0].Status.Phase)
	assert.Contains(t, got[0].Status.Message, "Pods cannot be scheduled.")
}

func TestScaleOfAMongoDBDatabaseWritesMembersAndResourcesAndNoProxy(t *testing.T) {
	fake := newScaleFake(1, ready(1))
	p := mongodb.New(fake)

	require.NoError(t, p.Scale(context.Background(), scaleID, engine.ScaleInput{
		Replicas: ints(3), CPUCores: ints(4), MemoryGB: ints(16), StorageGB: ints(200),
	}))

	got := fake.stored()
	eng := got.EngineComponent()
	assert.Equal(t, int32(3), eng.ReplicaCount())
	assert.Equal(t, "4", eng.CPU())
	assert.Equal(t, "16Gi", eng.Memory())
	assert.Equal(t, "200Gi", eng.StorageSize())
	assert.Len(t, got.Spec.Components, 1, "a MongoDB Instance has the engine component only")
	assert.NotContains(t, got.Spec.Components, everest.ComponentProxy)
}

// The shared scale path sets a proxy count for the engines that have one, and
// a MongoDB Instance must not pick one up from it.
func TestScaleOfAMongoDBDatabaseLeavesAProxyComponentAlone(t *testing.T) {
	fake := newScaleFake(1, ready(1))
	fake.held.SetProxyComponent(everest.ComponentSpec{Type: everest.ComponentTypeHAProxy, Replicas: everest.Ptr(int32(7))})
	p := mongodb.New(fake)

	require.NoError(t, p.Scale(context.Background(), scaleID, engine.ScaleInput{Replicas: ints(3)}))

	assert.Equal(t, int32(7), fake.stored().ProxyComponent().ReplicaCount())
}

func TestScaleOfAMongoDBDatabaseRefusesWhatTheOtherEnginesRefuse(t *testing.T) {
	fake := newScaleFake(3, ready(3))
	p := mongodb.New(fake)

	err := p.Scale(context.Background(), scaleID, engine.ScaleInput{Replicas: ints(2)})
	var choice *engine.ChoiceError
	require.ErrorAs(t, err, &choice)
	assert.Equal(t, oe.FieldMembers, choice.Field)
	assert.Equal(t, int32(3), fake.stored().EngineComponent().ReplicaCount())

	err = p.Scale(context.Background(), scaleID, engine.ScaleInput{StorageGB: ints(50)})
	require.ErrorIs(t, err, engine.ErrStorageShrink)

	for _, members := range []int{1, 3, 5} {
		require.NoError(t, p.Scale(context.Background(), scaleID, engine.ScaleInput{Replicas: ints(members)}))
		assert.Equal(t, int32(members), fake.stored().EngineComponent().ReplicaCount())
	}
}

func TestScaleOfAMongoDBDatabaseIsBusyWhileABackupRuns(t *testing.T) {
	fake := newScaleFake(1, ready(1))
	fake.backups = []everest.Backup{{
		Metadata: everest.ObjectMeta{Name: "orders-b1"},
		Status:   everest.BackupStatus{State: everest.BackupStateRunning},
	}}
	p := mongodb.New(fake)

	err := p.Scale(context.Background(), scaleID, engine.ScaleInput{Replicas: ints(3)})

	var busy *engine.BusyError
	require.ErrorAs(t, err, &busy)
	assert.Equal(t, "backup", busy.Operation)
	assert.Equal(t, int32(1), fake.stored().EngineComponent().ReplicaCount())
}
