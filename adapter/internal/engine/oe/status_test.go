package oe

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/percona/pds/adapter/internal/client/everest"
)

func transitionedAgo(d time.Duration) *time.Time {
	at := time.Now().Add(-d)

	return &at
}

func engineStatus(ready, total int32) []everest.ComponentStatus {
	return []everest.ComponentStatus{{Name: everest.ComponentEngine, Replicas: &total, ReadyReplicas: &ready}}
}

// PodsReady is False for the whole of every start, and PodsScheduled is briefly
// False while a volume binds, so only a scheduling failure that has held past
// the grace period is a blocker.
func TestToStatusShowsAStartBlockerOnlyWhenTheStartIsStuck(t *testing.T) {
	unschedulable := func(held time.Duration) everest.Condition {
		return everest.Condition{
			Type: everest.ConditionPodsScheduled, Status: "False", Reason: "Unschedulable",
			Message: "0/3 nodes are available: insufficient cpu", LastTransitionTime: transitionedAgo(held),
		}
	}
	podsNotReady := everest.Condition{
		Type: "PodsReady", Status: "False", Reason: "PodsNotReady", LastTransitionTime: transitionedAgo(time.Hour),
	}

	tests := []struct {
		name   string
		status everest.InstanceStatus
		want   string
	}{
		{
			name: "a healthy start with PodsReady False keeps the member count",
			status: everest.InstanceStatus{
				Phase: everest.PhaseProvisioning, Components: engineStatus(1, 3),
				Conditions: []everest.Condition{{Type: everest.ConditionPodsScheduled, Status: "True"}, podsNotReady},
			},
			want: "Starting member 1 of 3",
		},
		{
			name: "PodsReady False for a long time is still a normal start",
			status: everest.InstanceStatus{
				Phase: everest.PhaseInitializing, Components: engineStatus(3, 3),
				Conditions: []everest.Condition{podsNotReady},
			},
			want: "Starting database",
		},
		{
			name: "a brief Unschedulable while a volume binds keeps the stage message",
			status: everest.InstanceStatus{
				Phase: everest.PhaseProvisioning, Conditions: []everest.Condition{unschedulable(5 * time.Second)},
			},
			want: "Creating volumes",
		},
		{
			name: "Unschedulable held past the grace period names the capacity problem",
			status: everest.InstanceStatus{
				Phase: everest.PhaseProvisioning, Conditions: []everest.Condition{unschedulable(10 * time.Minute), podsNotReady},
			},
			want: "Pods cannot be scheduled. Check that the Kubernetes cluster has enough CPU and memory.",
		},
		{
			name: "an unknown scheduling reason gets a generic sentence and never the raw text",
			status: everest.InstanceStatus{
				Phase: everest.PhaseInitializing,
				Conditions: []everest.Condition{{
					Type: everest.ConditionPodsScheduled, Status: "False", Reason: "FluxReconcileFailed",
					Message: "the percona-postgresql-operator could not place pg-0", LastTransitionTime: transitionedAgo(10 * time.Minute),
				}},
			},
			want: "Pods are waiting for a node. Check that the Kubernetes cluster has capacity.",
		},
		{
			name: "a condition with no transition time cannot be shown as held",
			status: everest.InstanceStatus{
				Phase: everest.PhaseProvisioning,
				Conditions: []everest.Condition{{
					Type: everest.ConditionPodsScheduled, Status: "False", Reason: "Unschedulable",
				}},
			},
			want: "Creating volumes",
		},
		{
			name: "a ready database ignores a stale condition",
			status: everest.InstanceStatus{
				Phase: everest.PhaseReady, Conditions: []everest.Condition{unschedulable(time.Hour)},
			},
			want: "Publishing connection",
		},
		{
			name: "a blocker wins over the provider's own message",
			status: everest.InstanceStatus{
				Phase: everest.PhaseProvisioning, Message: "Restoring", Conditions: []everest.Condition{unschedulable(time.Hour)},
			},
			want: "Pods cannot be scheduled. Check that the Kubernetes cluster has enough CPU and memory.",
		},
		{
			name: "the provider's own message stays without a blocker",
			status: everest.InstanceStatus{
				Phase: everest.PhaseProvisioning, Message: "Restoring",
			},
			want: "Restoring",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, toStatus(tt.status).Message)
		})
	}
}
