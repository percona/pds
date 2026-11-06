package mysql_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine/mysql"
)

func (f *fakeEverest) seedConditions(namespace, name, phase string, conditions ...everest.Condition) {
	f.mu.Lock()
	defer f.mu.Unlock()

	stored := f.instances[key(namespace, name)]
	stored.Status.Phase = phase
	stored.Status.Conditions = conditions
}

// A provider that reports pod conditions must not turn the PodsReady False of a
// normal start into a blocker, and a scheduling failure shows only once it has
// held.
func TestListShowsAStartBlockerOnlyForAStuckDatabase(t *testing.T) {
	ago := func(d time.Duration) *time.Time {
		at := time.Now().Add(-d)

		return &at
	}
	unschedulable := func(held time.Duration) everest.Condition {
		return everest.Condition{Type: everest.ConditionPodsScheduled, Status: "False", Reason: "Unschedulable", Message: "0/3 nodes are available: 3 Insufficient cpu.", LastTransitionTime: ago(held)}
	}

	tests := []struct {
		name       string
		conditions []everest.Condition
		want       string
	}{
		{
			name:       "PodsReady False keeps the stage message",
			conditions: []everest.Condition{{Type: "PodsReady", Status: "False", Reason: "PodsNotReady", LastTransitionTime: ago(time.Hour)}},
			want:       "Creating volumes",
		},
		{
			name:       "a brief Unschedulable keeps the stage message",
			conditions: []everest.Condition{unschedulable(5 * time.Second)},
			want:       "Creating volumes",
		},
		{
			name:       "Unschedulable held past the grace period shows the blocker",
			conditions: []everest.Condition{unschedulable(10 * time.Minute)},
			want:       "Pods cannot be scheduled. Check that the Kubernetes cluster has enough CPU and memory.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, fake := newPlugin(t)
			created := seedProfile(t, fake, "orders", "dev")
			fake.seedConditions(mysql.DefaultNamespace, "orders", everest.PhaseProvisioning, tt.conditions...)

			got := listNamed(t, p, created)

			assert.Equal(t, tt.want, got.Status.Message)
		})
	}
}
