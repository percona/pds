package managed_test

import (
	"context"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/provision"
	"github.com/percona/pds/adapter/internal/store"
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

func (f *fakeCloud) Cluster(_ context.Context, t aws.Target) (aws.KubernetesState, error) {
	if err := f.record("cluster", t); err != nil {
		return aws.KubernetesState{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.clusters) == 0 {
		return aws.KubernetesState{Exists: true, Status: "ACTIVE", Version: "1.33", NodeGroups: []aws.NodeGroupState{healthyNodes()}}, nil
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

// kindEngine builds an engine that runs only the given kind under this lab's
// kind name, so a run another test left behind is never claimed.
func (l *lab) kindEngine(kind provision.Kind) *provision.Engine {
	l.t.Helper()

	kind.Name = l.kind
	e, err := provision.New(l.prov, []provision.Kind{kind}, provision.Options{
		Owner: "worker-1", LeaseTTL: time.Minute, RenewEvery: 10 * time.Millisecond,
		MaxAttempts: 3, Backoff: func(int) time.Duration { return time.Minute },
	})
	require.NoError(l.t, err)

	return e
}

func (l *lab) startWith(e *provision.Engine, params string) store.ProvisioningRun {
	l.t.Helper()

	run, err := e.StartWith(l.t.Context(), l.team, l.env, l.kind, params)
	require.NoError(l.t, err)

	return run
}

// ready makes the environment one that has reported and runs the given chart.
func (l *lab) ready(chart string) {
	l.t.Helper()

	require.NoError(l.t, l.db.ManagedEnvironments().SetChartVersion(l.t.Context(), l.team, l.env, chart))
	require.NoError(l.t, l.db.ManagedEnvironments().MarkReady(l.t.Context(), l.team, l.env, time.Now()))
}

func (l *lab) record() store.ManagedEnvironment {
	l.t.Helper()

	rec, err := l.db.ManagedEnvironments().Get(l.t.Context(), l.team, l.env)
	require.NoError(l.t, err)

	return rec
}

func (l *lab) work(e *provision.Engine) {
	l.t.Helper()

	ran, err := e.RunOnce(l.t.Context())
	require.NoError(l.t, err)
	require.True(l.t, ran)
}
