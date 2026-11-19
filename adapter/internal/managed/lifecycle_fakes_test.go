package managed_test

import (
	"context"

	"github.com/percona/pds/adapter/internal/client/aws"
)

func (f *fakeCloud) PlanUpdate(_ context.Context, t aws.Target, spec aws.UpdateSpec) (aws.ChangePlan, error) {
	f.mu.Lock()
	f.updates = append(f.updates, spec)
	f.mu.Unlock()
	if err := f.record("plan-update", t); err != nil {
		return aws.ChangePlan{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.plans) == 0 {
		return aws.ChangePlan{Name: spec.Name, Changes: []aws.Change{{Resource: "Cluster", Type: "AWS::EKS::Cluster", Action: "Modify", Replacement: "False"}}}, nil
	}
	plan := f.plans[0]
	if len(f.plans) > 1 {
		f.plans = f.plans[1:]
	}
	plan.Name = spec.Name

	return plan, nil
}

func (f *fakeCloud) ApplyPlan(_ context.Context, t aws.Target, name string) error {
	f.mu.Lock()
	f.applied = append(f.applied, name)
	f.mu.Unlock()

	return f.record("apply-plan", t)
}

func (f *fakeCloud) DiscardPlan(_ context.Context, t aws.Target, name string) error {
	f.mu.Lock()
	f.discarded = append(f.discarded, name)
	f.mu.Unlock()

	return f.record("discard-plan", t)
}

func (f *fakeCloud) DetectDrift(_ context.Context, t aws.Target) ([]aws.Drift, error) {
	if err := f.record("detect-drift", t); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.drifts, nil
}

func (f *fakeCloud) Cluster(_ context.Context, t aws.Target) (aws.ClusterState, error) {
	if err := f.record("cluster", t); err != nil {
		return aws.ClusterState{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.clusters) == 0 {
		return aws.ClusterState{Exists: true, Status: "ACTIVE", Version: "1.33", NodeGroups: []aws.NodeGroupState{healthyNodes()}}, nil
	}
	state := f.clusters[0]
	if len(f.clusters) > 1 {
		f.clusters = f.clusters[1:]
	}

	return state, nil
}

func healthyNodes() aws.NodeGroupState {
	return aws.NodeGroupState{
		Name: "pds-nodes", Status: "ACTIVE", Version: "1.33", InstanceTypes: []string{"m6i.xlarge"}, Min: 3, Desired: 3, Max: 3,
	}
}
