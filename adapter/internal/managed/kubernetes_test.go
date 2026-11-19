package managed_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/aws"
	"github.com/percona/pds/adapter/internal/managed"
	"github.com/percona/pds/adapter/internal/store"
)

func updated(version string) aws.StackState {
	return aws.StackState{
		Exists: true, Status: "UPDATE_COMPLETE",
		Outputs:    map[string]string{"TemplateRevision": fmt.Sprint(managed.TemplateRevision)},
		Parameters: map[string]string{"KubernetesVersion": version},
	}
}

func before(version string) aws.StackState {
	state := updated(version)
	state.Status = "CREATE_COMPLETE"
	state.Outputs["TemplateRevision"] = "0"

	return state
}

func inProgress() aws.StackState { return aws.StackState{Exists: true, Status: "UPDATE_IN_PROGRESS"} }

func replacing(resource, typ, replacement string) aws.ChangePlan {
	return aws.ChangePlan{Changes: []aws.Change{{Resource: resource, Type: typ, Action: "Modify", Replacement: replacement}}}
}

const toNextMinor = `{"kubernetesVersion":"1.34"}`

func TestKubernetesUpgradeUpdatesTheStackThroughAChangeSetAndWaitsForIt(t *testing.T) {
	l := newLab(t)
	l.ready("0.1.0")
	l.cloud.stacks = []aws.StackState{before("1.33"), before("1.33"), inProgress(), updated("1.34")}
	l.cloud.clusters = []aws.KubernetesState{{Exists: true, Status: "ACTIVE", Version: "1.34"}}
	e := l.kindEngine(managed.KubernetesKind(l.deps))
	run := l.startWith(e, toNextMinor)

	l.work(e)

	got := l.run(run.ID)
	require.Equal(t, store.RunSucceeded, got.Status, got.Message)
	require.Len(t, l.cloud.updates, 1)
	spec := l.cloud.updates[0]
	assert.Equal(t, managed.NetworkTemplate, spec.Template, "an update always carries the template this release embeds")
	assert.Equal(t, map[string]string{"KubernetesVersion": "1.34"}, spec.Parameters, "every other parameter keeps its value")
	assert.Equal(t, "arn:aws:iam::111122223333:role/pds/"+l.connID+"/pds-"+l.connID+"-cloudformation", spec.ServiceRoleARN)
	assert.Equal(t, "1", spec.Tags["percona.com/pds-template-revision"])
	assert.Equal(t, l.envID, spec.Tags["percona.com/pds"])
	assert.Equal(t, []string{spec.Name}, l.cloud.applied)
	assert.Equal(t, 1, l.cloud.count("check-role"))
	assert.Zero(t, l.cloud.count("preflight"), "an upgrade builds nothing new, so the quota check is for a create")
	assert.Zero(t, l.cloud.count("open-access"), "the Kubernetes API is not needed")
}

func TestAnUpdateThatWouldReplaceTheNetworkOrTheKubernetesStopsBeforeChangingAnything(t *testing.T) {
	for _, tc := range []struct{ resource, typ string }{
		{"Vpc", "AWS::EC2::VPC"},
		{"Cluster", "AWS::EKS::Cluster"},
	} {
		t.Run(tc.resource, func(t *testing.T) {
			l := newLab(t)
			l.ready("0.1.0")
			l.cloud.stacks = []aws.StackState{before("1.33")}
			l.cloud.plans = []aws.ChangePlan{replacing(tc.resource, tc.typ, "True")}
			e := l.kindEngine(managed.KubernetesKind(l.deps))
			run := l.startWith(e, `{"kubernetesVersion":"1.34","confirmNodeGroupReplace":true}`)

			l.work(e)

			step := l.steps(run.ID)["update-stack"]
			assert.Equal(t, store.StepFailed, step.Status)
			assert.Contains(t, step.Failure, "replace the network or the Kubernetes cluster")
			assert.Empty(t, l.cloud.applied, "even a confirmed update does not replace the network or the cluster")
			assert.Len(t, l.cloud.discarded, 1, "the change set is removed")
		})
	}
}

func TestReplacingTheNodeGroupNeedsAnExplicitConfirm(t *testing.T) {
	l := newLab(t)
	l.ready("0.1.0")
	l.cloud.stacks = []aws.StackState{before("1.33"), updated("1.34")}
	l.cloud.plans = []aws.ChangePlan{replacing("NodeGroup", "AWS::EKS::Nodegroup", "True")}
	l.cloud.clusters = []aws.KubernetesState{{Exists: true, Status: "ACTIVE", Version: "1.34"}}
	e := l.kindEngine(managed.KubernetesKind(l.deps))
	run := l.startWith(e, toNextMinor)

	l.work(e)

	step := l.steps(run.ID)["update-stack"]
	assert.Equal(t, store.StepFailed, step.Status)
	assert.Contains(t, step.Failure, "replaces the node group")
	assert.Empty(t, l.cloud.applied)
	assert.Len(t, l.cloud.discarded, 1)

	require.NoError(t, l.db.Gorm().WithContext(t.Context()).Model(&store.ProvisioningRun{}).Where("id = ?", run.ID).Update("status", store.RunCancelled).Error)
	l.cloud.stacks = []aws.StackState{before("1.33"), updated("1.34")}
	confirmed := l.startWith(e, `{"kubernetesVersion":"1.34","confirmNodeGroupReplace":true}`)
	l.work(e)

	assert.Equal(t, store.RunSucceeded, l.run(confirmed.ID).Status)
	assert.Len(t, l.cloud.applied, 1)
}

func TestAnUpdateAWSRollsBackFailsTheStepAndRetryTriesAgainWithAFreshChangeSet(t *testing.T) {
	l := newLab(t)
	l.ready("0.1.0")
	l.cloud.stacks = []aws.StackState{
		before("1.33"), inProgress(),
		{Exists: true, Status: "UPDATE_ROLLBACK_COMPLETE", FailureReason: "You have requested more vCPU capacity than your current vCPU limit (Service: AmazonEC2; Status Code: 400; Error Code: VcpuLimitExceeded; Request ID: x)"},
		{Exists: true, Status: "UPDATE_ROLLBACK_COMPLETE"}, inProgress(), updated("1.34"),
	}
	l.cloud.clusters = []aws.KubernetesState{{Exists: true, Status: "ACTIVE", Version: "1.34"}}
	e := l.kindEngine(managed.KubernetesKind(l.deps))
	run := l.startWith(e, toNextMinor)

	l.work(e)

	step := l.steps(run.ID)["update-stack"]
	require.Equal(t, store.StepFailed, step.Status)
	assert.Contains(t, step.Failure, "vCPU", "a typed refusal names its cause")

	require.NoError(t, e.Retry(t.Context(), l.team, l.env))
	l.work(e)

	assert.Equal(t, store.RunSucceeded, l.run(run.ID).Status)
	require.Len(t, l.cloud.updates, 2)
	assert.NotEqual(t, l.cloud.updates[0].Name, l.cloud.updates[1].Name, "an executed change set cannot be executed again")
}

func TestAnUpdateThatIsAlreadyInTheStackIsNotAppliedAgain(t *testing.T) {
	l := newLab(t)
	l.ready("0.1.0")
	l.cloud.stacks = []aws.StackState{updated("1.34")}
	l.cloud.clusters = []aws.KubernetesState{{Exists: true, Status: "ACTIVE", Version: "1.34"}}
	e := l.kindEngine(managed.KubernetesKind(l.deps))
	run := l.startWith(e, toNextMinor)

	l.work(e)

	assert.Equal(t, store.RunSucceeded, l.run(run.ID).Status, "a restart after the update finished only confirms it")
	assert.Empty(t, l.cloud.updates)
	assert.Empty(t, l.cloud.applied)
}

func TestATemplateOnlyUpdateChangesNoParameter(t *testing.T) {
	l := newLab(t)
	l.ready("0.1.0")
	l.cloud.stacks = []aws.StackState{before("1.33"), updated("1.33")}
	e := l.kindEngine(managed.KubernetesKind(l.deps))
	run := l.startWith(e, `{}`)

	l.work(e)

	require.Equal(t, store.RunSucceeded, l.run(run.ID).Status, l.run(run.ID).Message)
	assert.Empty(t, l.cloud.updates[0].Parameters)
}

func TestAnUpdateThatNeverFinishesFailsTheStepWithASentence(t *testing.T) {
	l := newLab(t)
	l.ready("0.1.0")
	l.cloud.stacks = []aws.StackState{before("1.33"), inProgress()}
	e := l.kindEngine(managed.KubernetesKind(l.deps))
	run := l.startWith(e, toNextMinor)

	l.work(e)

	step := l.steps(run.ID)["update-stack"]
	assert.Equal(t, store.StepFailed, step.Status)
	assert.Contains(t, step.Failure, "has not finished")
}

func TestAnUpdateThatLeavesTheKubernetesOnTheOldVersionFails(t *testing.T) {
	l := newLab(t)
	l.ready("0.1.0")
	l.cloud.stacks = []aws.StackState{before("1.33"), updated("1.34")}
	l.cloud.clusters = []aws.KubernetesState{{Exists: true, Status: "ACTIVE", Version: "1.33"}}
	e := l.kindEngine(managed.KubernetesKind(l.deps))
	run := l.startWith(e, toNextMinor)

	l.work(e)

	assert.Equal(t, store.StepFailed, l.steps(run.ID)["update-stack"].Status)
}

func TestKubernetesUpgradeRefusesRunParametersItCannotUse(t *testing.T) {
	l := newLab(t)
	l.ready("0.1.0")
	e := l.kindEngine(managed.KubernetesKind(l.deps))
	run := l.startWith(e, `{"kubernetesVersion":"1.34; drop"}`)

	l.work(e)

	assert.Equal(t, store.StepFailed, l.steps(run.ID)["update-stack"].Status)
	assert.Empty(t, l.cloud.updates)
}

func TestARevokedRoleParksAnUpgradeToo(t *testing.T) {
	l := newLab(t)
	l.ready("0.1.0")
	l.cloud.failAt["check-role"] = []error{fmt.Errorf("%w: denied", aws.ErrAssumeDenied)}
	e := l.kindEngine(managed.KubernetesKind(l.deps))
	run := l.startWith(e, toNextMinor)

	l.work(e)

	assert.Equal(t, store.RunPaused, l.run(run.ID).Status)
}
