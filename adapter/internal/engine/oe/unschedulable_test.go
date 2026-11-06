package oe

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/percona/pds/adapter/internal/client/everest"
)

const (
	capacitySentence  = "Pods cannot be scheduled. Check that the Kubernetes cluster has enough CPU and memory."
	placementSentence = "Pods cannot be scheduled. No node in the Kubernetes cluster can run the members. Check node taints and labels, or add nodes."
	storageSentence   = "Pods cannot be scheduled. Storage cannot be provisioned. Check that the Kubernetes cluster has a working storage class."
	unknownSentence   = "Pods cannot be scheduled. Check the nodes and storage of the Kubernetes cluster."
)

// The scheduler's reason is always Unschedulable, so the condition message is
// the only thing that tells a full node from a missing volume. Each cause maps
// to a fixed sentence, because the scheduler's own text names internals.
func TestUnschedulablePodsGetAdviceForTheirCause(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		message string
		want    string
	}{
		{
			name:    "not enough CPU",
			message: "0/3 nodes are available: 3 Insufficient cpu. preemption: 0/3 nodes are available: 3 No preemption victims found for incoming pod.",
			want:    capacitySentence,
		},
		{
			name:    "not enough memory",
			message: "0/3 nodes are available: 1 Insufficient memory, 2 node(s) had untolerated taint {dedicated: gpu}.",
			want:    capacitySentence,
		},
		{
			name:    "taint no member tolerates",
			message: "0/3 nodes are available: 3 node(s) had untolerated taint {dedicated: gpu}.",
			want:    placementSentence,
		},
		{
			name:    "node selector or affinity no node matches",
			message: "0/3 nodes are available: 3 node(s) didn't match Pod's node affinity/selector.",
			want:    placementSentence,
		},
		{
			name:    "anti-affinity leaves no node free",
			message: "0/3 nodes are available: 3 node(s) didn't match pod anti-affinity rules.",
			want:    placementSentence,
		},
		{
			name:    "claim cannot bind",
			message: "0/3 nodes are available: pod has unbound immediate PersistentVolumeClaims. preemption: 0/3 nodes are available: 3 Preemption is not helpful for scheduling.",
			want:    storageSentence,
		},
		{
			name:    "claim waits for a volume",
			message: "0/3 nodes are available: 3 node(s) didn't find available persistent volumes to bind.",
			want:    storageSentence,
		},
		{
			name:    "claim is missing",
			message: `persistentvolumeclaim "data-orders-0" not found`,
			want:    storageSentence,
		},
		{
			name:    "storage outranks a placement note",
			message: "0/3 nodes are available: 1 node(s) had untolerated taint {a: b}, 2 node(s) didn't find available persistent volumes to bind.",
			want:    storageSentence,
		},
		{
			name:    "volume pinned to another zone",
			message: "0/3 nodes are available: 3 node(s) had volume node affinity conflict.",
			want:    storageSentence,
		},
		{
			name:    "storage outranks capacity",
			message: "0/3 nodes are available: 1 Insufficient cpu, 2 node(s) didn't find available persistent volumes to bind.",
			want:    storageSentence,
		},
		{
			name:    "unknown cause gets the generic sentence",
			message: "0/3 nodes are available: 3 node(s) are unhappy.",
			want:    unknownSentence,
		},
		{
			name:    "no message gets the generic sentence",
			message: "",
			want:    unknownSentence,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := toStatus(everest.InstanceStatus{
				Phase: everest.PhaseInitializing,
				Conditions: []everest.Condition{
					{Type: everest.ConditionPodsScheduled, Status: "False", Reason: "Unschedulable", Message: tt.message, LastTransitionTime: transitionedAgo(10 * time.Minute)},
				},
			})

			assert.Equal(t, tt.want, got.Message)
			assert.NotContains(t, got.Message, "0/3")
		})
	}
}
