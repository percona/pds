package postgresql_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine/postgresql"
)

func transitionedAgo(d time.Duration) *time.Time {
	at := time.Now().Add(-d)

	return &at
}

func (f *fakeEverest) seedConditions(namespace, name, phase string, conditions ...everest.Condition) {
	f.mu.Lock()
	defer f.mu.Unlock()

	stored := f.instances[key(namespace, name)]
	stored.Status.Phase = phase
	stored.Status.Conditions = conditions
}

func (f *fakeEverest) seedMessage(namespace, name, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.instances[key(namespace, name)].Status.Message = message
}

// The provider reports no Failed phase for a database that cannot start, so the
// scheduling condition is the only place the reason is. PodsReady is False for
// every start and never a reason on its own.
func TestListShowsWhyAStuckDatabaseCannotStart(t *testing.T) {
	tests := []struct {
		name       string
		phase      string
		message    string
		conditions []everest.Condition
		want       string
	}{
		{
			name:  "pods not scheduled",
			phase: everest.PhaseInitializing,
			conditions: []everest.Condition{
				{Type: everest.ConditionPodsScheduled, Status: "False", Reason: "Unschedulable", Message: "0/3 nodes are available: insufficient memory", LastTransitionTime: transitionedAgo(10 * time.Minute)},
				{Type: "PodsReady", Status: "False", Reason: "PodsNotReady"},
			},
			want: "Pods cannot be scheduled. Check that the Kubernetes cluster has enough CPU and memory.",
		},
		{
			name:    "the scheduling sentence wins over the provider's own initializing text",
			phase:   everest.PhaseInitializing,
			message: "database is initializing",
			conditions: []everest.Condition{
				{Type: everest.ConditionPodsScheduled, Status: "False", Reason: "Unschedulable", Message: "0/3 nodes are available: insufficient cpu", LastTransitionTime: transitionedAgo(10 * time.Minute)},
			},
			want: "Pods cannot be scheduled. Check that the Kubernetes cluster has enough CPU and memory.",
		},
		{
			name:    "the provider text stays while the scheduling failure is brief",
			phase:   everest.PhaseInitializing,
			message: "waiting for postgres replicas (1/3 ready)",
			conditions: []everest.Condition{
				{Type: everest.ConditionPodsScheduled, Status: "False", Reason: "Unschedulable", LastTransitionTime: transitionedAgo(5 * time.Second)},
			},
			want: "waiting for postgres replicas (1/3 ready)",
		},
		{
			name:  "pods scheduled but not ready is a normal start",
			phase: everest.PhaseInitializing,
			conditions: []everest.Condition{
				{Type: everest.ConditionPodsScheduled, Status: "True"},
				{Type: "PodsReady", Status: "False", Reason: "ContainersNotReady", LastTransitionTime: transitionedAgo(time.Hour)},
			},
			want: "Starting database",
		},
		{
			name:  "a brief scheduling failure keeps the stage message",
			phase: everest.PhaseProvisioning,
			conditions: []everest.Condition{
				{Type: everest.ConditionPodsScheduled, Status: "False", Reason: "Unschedulable", LastTransitionTime: transitionedAgo(5 * time.Second)},
			},
			want: "Creating volumes",
		},
		{
			name:  "an unknown reason gets a generic sentence and never the raw text",
			phase: everest.PhaseProvisioning,
			conditions: []everest.Condition{
				{Type: everest.ConditionPodsScheduled, Status: "False", Reason: "FluxReconcileFailed", Message: "the percona-postgresql-operator could not place pg-0", LastTransitionTime: transitionedAgo(10 * time.Minute)},
			},
			want: "Pods are waiting for a node. Check that the Kubernetes cluster has capacity.",
		},
		{
			name:  "healthy conditions keep the stage message",
			phase: everest.PhaseInitializing,
			conditions: []everest.Condition{
				{Type: everest.ConditionPodsScheduled, Status: "True"},
				{Type: "PodsReady", Status: "True"},
			},
			want: "Starting database",
		},
		{
			name:  "a ready database ignores a stale condition",
			phase: everest.PhaseReady,
			conditions: []everest.Condition{
				{Type: everest.ConditionPodsScheduled, Status: "False", Reason: "Unschedulable", LastTransitionTime: transitionedAgo(time.Hour)},
			},
			want: "Publishing connection",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, fake := newPlugin(t)
			created := seedProfile(t, fake, "orders", "dev")
			fake.seedConditions(postgresql.DefaultNamespace, "orders", tt.phase, tt.conditions...)
			fake.seedMessage(postgresql.DefaultNamespace, "orders", tt.message)

			got := listNamed(t, p, created)

			assert.Equal(t, tt.want, got.Status.Message)
		})
	}
}
