package mongodb_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/engine/mongodb"
	"github.com/percona/pds/adapter/internal/engine/oe"
)

// fakeEverest answers the reads a list makes with the instances it holds.
type fakeEverest struct {
	instances []everest.Instance
}

var _ mongodb.EverestClient = (*fakeEverest)(nil)

func (f *fakeEverest) ListInstances(context.Context, string) ([]everest.Instance, error) {
	return f.instances, nil
}

func (f *fakeEverest) GetInstance(context.Context, string, string) (*everest.Instance, error) {
	return nil, everest.ErrNotFound
}

func (f *fakeEverest) UpdateInstance(context.Context, string, string, *everest.Instance) (*everest.Instance, error) {
	return nil, everest.ErrNotFound
}

func (f *fakeEverest) DeleteInstance(context.Context, string, string) error { return nil }

func (f *fakeEverest) GetInstanceConnection(context.Context, string, string) (*everest.Connection, error) {
	return nil, everest.ErrNotFound
}

func (f *fakeEverest) ListInstanceBackups(context.Context, string, string) ([]everest.Backup, error) {
	return nil, everest.ErrNotFound
}

func (f *fakeEverest) CreateBackup(context.Context, string, *everest.Backup) (*everest.Backup, error) {
	return nil, everest.ErrNotFound
}

func mongoInstance(name string, members int32, status everest.InstanceStatus) everest.Instance {
	created := time.Date(2026, time.November, 23, 12, 0, 0, 0, time.UTC)
	in := everest.Instance{
		Metadata: everest.ObjectMeta{
			Name:              name,
			Namespace:         mongodb.DefaultNamespace,
			CreationTimestamp: &created,
			Labels: map[string]string{
				oe.LabelManagedBy: oe.ManagedBy,
				oe.LabelProfile:   "staging",
				oe.LabelEngine:    mongodb.EngineID,
			},
		},
		Spec:   everest.InstanceSpec{ProviderRef: everest.ObjectRef{Name: everest.ProviderMongoDB}},
		Status: status,
	}
	in.SetEngineComponent(everest.ComponentSpec{
		Replicas:  everest.Ptr(members),
		Storage:   &everest.Storage{Size: "100Gi"},
		Resources: &everest.ResourceRequirements{Limits: &everest.ResourceList{CPU: "2", Memory: "8Gi"}},
	})

	return in
}

func listOne(t *testing.T, in everest.Instance) engine.Instance {
	t.Helper()

	got, err := mongodb.New(&fakeEverest{instances: []everest.Instance{in}}).List(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)

	return got[0]
}

func replicas(ready, total int32) []everest.ComponentStatus {
	return []everest.ComponentStatus{{Name: everest.ComponentEngine, Replicas: &total, ReadyReplicas: &ready}}
}

func TestListReadsMembersSizingAndStatusOfAMongoDBDatabase(t *testing.T) {
	got := listOne(t, mongoInstance("orders", 3, everest.InstanceStatus{
		Phase: everest.PhaseReady, Components: replicas(3, 3),
		ConnectionSecretRef: &everest.ObjectRef{Name: "orders-conn"},
	}))

	assert.Equal(t, "mongodb", got.Engine)
	assert.Equal(t, "staging", got.Profile)
	assert.Equal(t, engine.PhaseReady, got.Status.Phase)
	assert.Equal(t, engine.Sizing{Members: 3, CPUCores: 2, MemoryGB: 8, StorageGB: 100}, got.Sizing)
	assert.Equal(t, int32(3), got.Spec.Components.Engine.Replicas)
}

func TestListLeavesAnotherProviderOut(t *testing.T) {
	other := mongoInstance("orders", 1, everest.InstanceStatus{})
	other.Spec.ProviderRef.Name = everest.ProviderPXC

	got, err := mongodb.New(&fakeEverest{instances: []everest.Instance{other}}).List(context.Background())

	require.NoError(t, err)
	assert.Empty(t, got)
}

// A three member set needs three nodes, and on fewer the provider keeps the
// phase at Provisioning with PodsScheduled False.
func TestListShowsWhyAMongoDBDatabaseCannotBePlaced(t *testing.T) {
	held := time.Now().Add(-10 * time.Minute)
	got := listOne(t, mongoInstance("orders", 3, everest.InstanceStatus{
		Phase: everest.PhaseProvisioning, Components: replicas(1, 3),
		Conditions: []everest.Condition{{
			Type: everest.ConditionPodsScheduled, Status: "False", Reason: "Unschedulable",
			Message:            "engine: 1 of 2 pods cannot be scheduled: 0/1 nodes are available: 1 node(s) didn't match pod anti-affinity rules.",
			LastTransitionTime: &held,
		}},
	}))

	assert.Equal(t,
		"Pods cannot be scheduled. No node in the Kubernetes cluster can run the members. Check node taints and labels, or add nodes.",
		got.Status.Message)
}

// The provider leaves the message empty when it refuses an Instance, for
// example for a name over 22 characters, because the reason is in its own log.
func TestListGivesAFailedMongoDBDatabaseASentenceWhenTheProviderSaysNothing(t *testing.T) {
	got := listOne(t, mongoInstance("orders", 1, everest.InstanceStatus{Phase: everest.PhaseFailed}))

	assert.Equal(t, engine.PhaseFailed, got.Status.Phase)
	assert.Equal(t, "The database could not be started. Contact Percona support.", got.Status.Message)
}

func TestListKeepsTheProvidersMessageOnAFailedDatabase(t *testing.T) {
	got := listOne(t, mongoInstance("orders", 1, everest.InstanceStatus{
		Phase: everest.PhaseFailed, Message: "storage is full",
	}))

	assert.Equal(t, "storage is full", got.Status.Message)
}
