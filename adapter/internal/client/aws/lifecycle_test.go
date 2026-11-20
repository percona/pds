package aws

import (
	"context"
	"errors"
	"testing"
	"time"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeChanges struct {
	created   []*cloudformation.CreateChangeSetInput
	createErr error
	described []*cloudformation.DescribeChangeSetOutput
	executed  []*cloudformation.ExecuteChangeSetInput
	execErr   error
	deleted   []*cloudformation.DeleteChangeSetInput

	detectErr error
	statuses  []cfntypes.StackDriftDetectionStatus
	drifts    []cfntypes.StackResourceDrift
	detected  int
}

func (f *fakeChanges) CreateChangeSet(_ context.Context, in *cloudformation.CreateChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error) {
	f.created = append(f.created, in)

	return &cloudformation.CreateChangeSetOutput{}, f.createErr
}

func (f *fakeChanges) DescribeChangeSet(context.Context, *cloudformation.DescribeChangeSetInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeChangeSetOutput, error) {
	out := f.described[0]
	if len(f.described) > 1 {
		f.described = f.described[1:]
	}

	return out, nil
}

func (f *fakeChanges) ExecuteChangeSet(_ context.Context, in *cloudformation.ExecuteChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.ExecuteChangeSetOutput, error) {
	f.executed = append(f.executed, in)

	return &cloudformation.ExecuteChangeSetOutput{}, f.execErr
}

func (f *fakeChanges) DeleteChangeSet(_ context.Context, in *cloudformation.DeleteChangeSetInput, _ ...func(*cloudformation.Options)) (*cloudformation.DeleteChangeSetOutput, error) {
	f.deleted = append(f.deleted, in)

	return &cloudformation.DeleteChangeSetOutput{}, nil
}

func (f *fakeChanges) DetectStackDrift(context.Context, *cloudformation.DetectStackDriftInput, ...func(*cloudformation.Options)) (*cloudformation.DetectStackDriftOutput, error) {
	f.detected++

	return &cloudformation.DetectStackDriftOutput{StackDriftDetectionId: sdkaws.String("detect-1")}, f.detectErr
}

func (f *fakeChanges) DescribeStackDriftDetectionStatus(context.Context, *cloudformation.DescribeStackDriftDetectionStatusInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeStackDriftDetectionStatusOutput, error) {
	status := f.statuses[0]
	if len(f.statuses) > 1 {
		f.statuses = f.statuses[1:]
	}

	return &cloudformation.DescribeStackDriftDetectionStatusOutput{DetectionStatus: status}, nil
}

func (f *fakeChanges) DescribeStackResourceDrifts(context.Context, *cloudformation.DescribeStackResourceDriftsInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeStackResourceDriftsOutput, error) {
	return &cloudformation.DescribeStackResourceDriftsOutput{StackResourceDrifts: f.drifts}, nil
}

type fakeNodes struct {
	names   []string
	groups  map[string]ekstypes.Nodegroup
	listErr error
}

func (f *fakeNodes) ListNodegroups(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
	return &eks.ListNodegroupsOutput{Nodegroups: f.names}, f.listErr
}

func (f *fakeNodes) DescribeNodegroup(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
	g := f.groups[sdkaws.ToString(in.NodegroupName)]

	return &eks.DescribeNodegroupOutput{Nodegroup: &g}, nil
}

func updateInfra(c *fakeCFN, e *fakeEKS, ch *fakeChanges, n *fakeNodes) *Infra {
	i := newInfra(c, e)
	i.changes = func(sdkaws.Config) changeAPI { return ch }
	i.nodes = func(sdkaws.Config) nodegroupAPI { return n }

	return i
}

func existingStack() cfntypes.Stack {
	return cfntypes.Stack{
		StackStatus: cfntypes.StackStatusCreateComplete,
		Parameters: []cfntypes.Parameter{
			{ParameterKey: sdkaws.String("EnvironmentId"), ParameterValue: sdkaws.String("0123456789ab")},
			{ParameterKey: sdkaws.String("KubernetesVersion"), ParameterValue: sdkaws.String("1.33")},
			{ParameterKey: sdkaws.String("NodeCount"), ParameterValue: sdkaws.String("3")},
		},
	}
}

func changeSet(status cfntypes.ChangeSetStatus, reason string, changes ...cfntypes.Change) *cloudformation.DescribeChangeSetOutput {
	return &cloudformation.DescribeChangeSetOutput{Status: status, StatusReason: sdkaws.String(reason), Changes: changes}
}

func resourceChange(id, typ string, action cfntypes.ChangeAction, replacement cfntypes.Replacement) cfntypes.Change {
	return cfntypes.Change{ResourceChange: &cfntypes.ResourceChange{
		LogicalResourceId: sdkaws.String(id), ResourceType: sdkaws.String(typ), Action: action, Replacement: replacement,
	}}
}

func TestPlanUpdateKeepsEveryParameterItDoesNotChange(t *testing.T) {
	cfn := &fakeCFN{stacks: []cfntypes.Stack{existingStack()}}
	ch := &fakeChanges{described: []*cloudformation.DescribeChangeSetOutput{
		changeSet(cfntypes.ChangeSetStatusCreateComplete, "", resourceChange("Cluster", "AWS::EKS::Cluster", cfntypes.ChangeActionModify, cfntypes.ReplacementFalse)),
	}}
	infra := updateInfra(cfn, &fakeEKS{}, ch, &fakeNodes{})

	plan, err := infra.PlanUpdate(t.Context(), target, UpdateSpec{
		Name: "pds-run-7", Template: "Resources: {}", ServiceRoleARN: "arn:role",
		Parameters: map[string]string{"KubernetesVersion": "1.34"}, Tags: map[string]string{"percona.com/pds": "x"},
	})

	require.NoError(t, err)
	require.Len(t, ch.created, 1)
	in := ch.created[0]
	assert.Equal(t, cfntypes.ChangeSetTypeUpdate, in.ChangeSetType)
	assert.Equal(t, "pds-run-7", sdkaws.ToString(in.ChangeSetName))
	assert.Equal(t, "arn:role", sdkaws.ToString(in.RoleARN))
	got := map[string]cfntypes.Parameter{}
	for _, p := range in.Parameters {
		got[sdkaws.ToString(p.ParameterKey)] = p
	}
	assert.Equal(t, "1.34", sdkaws.ToString(got["KubernetesVersion"].ParameterValue))
	assert.False(t, sdkaws.ToBool(got["KubernetesVersion"].UsePreviousValue))
	assert.True(t, sdkaws.ToBool(got["EnvironmentId"].UsePreviousValue))
	assert.True(t, sdkaws.ToBool(got["NodeCount"].UsePreviousValue))
	assert.Equal(t, []Change{{Resource: "Cluster", Type: "AWS::EKS::Cluster", Action: "Modify", Replacement: "False"}}, plan.Changes)
	assert.False(t, plan.Empty)
}

func TestPlanUpdatePassesAParameterTheStackDidNotHaveYet(t *testing.T) {
	cfn := &fakeCFN{stacks: []cfntypes.Stack{existingStack()}}
	ch := &fakeChanges{described: []*cloudformation.DescribeChangeSetOutput{changeSet(cfntypes.ChangeSetStatusCreateComplete, "")}}
	infra := updateInfra(cfn, &fakeEKS{}, ch, &fakeNodes{})

	_, err := infra.PlanUpdate(t.Context(), target, UpdateSpec{
		Name: "pds-run-8", Template: "Resources: {}", ServiceRoleARN: "arn:role",
		Parameters: map[string]string{"AgentRoleArn": "arn:aws:iam::1:role/agent"},
	})

	require.NoError(t, err)
	got := map[string]cfntypes.Parameter{}
	for _, p := range ch.created[0].Parameters {
		got[sdkaws.ToString(p.ParameterKey)] = p
	}
	assert.Equal(t, "arn:aws:iam::1:role/agent", sdkaws.ToString(got["AgentRoleArn"].ParameterValue),
		"a revision adds a parameter, and the stack made before it has no value to keep")
	assert.True(t, sdkaws.ToBool(got["EnvironmentId"].UsePreviousValue))
}

func TestPlanUpdateReportsAnUpdateThatChangesNothing(t *testing.T) {
	cfn := &fakeCFN{stacks: []cfntypes.Stack{existingStack()}}
	ch := &fakeChanges{described: []*cloudformation.DescribeChangeSetOutput{
		changeSet(cfntypes.ChangeSetStatusFailed, "The submitted information didn't contain changes. Submit different information to create a change set."),
	}}
	infra := updateInfra(cfn, &fakeEKS{}, ch, &fakeNodes{})

	plan, err := infra.PlanUpdate(t.Context(), target, UpdateSpec{Name: "pds-run-7", Template: "x", ServiceRoleARN: "r"})

	require.NoError(t, err)
	assert.True(t, plan.Empty)
	assert.Len(t, ch.deleted, 1, "an empty change set is removed, since AWS keeps it")
}

func TestPlanUpdateWaitsForTheChangeSetToBeCreated(t *testing.T) {
	cfn := &fakeCFN{stacks: []cfntypes.Stack{existingStack()}}
	ch := &fakeChanges{described: []*cloudformation.DescribeChangeSetOutput{
		changeSet(cfntypes.ChangeSetStatusCreateInProgress, ""),
		changeSet(cfntypes.ChangeSetStatusCreateComplete, "", resourceChange("NodeGroup", "AWS::EKS::Nodegroup", cfntypes.ChangeActionModify, cfntypes.ReplacementTrue)),
	}}
	infra := updateInfra(cfn, &fakeEKS{}, ch, &fakeNodes{})

	plan, err := infra.PlanUpdate(t.Context(), target, UpdateSpec{Name: "n", Template: "x", ServiceRoleARN: "r"})

	require.NoError(t, err)
	assert.Equal(t, "True", plan.Changes[0].Replacement)
}

func TestPlanUpdateReusesAChangeSetAnEarlierAttemptCreated(t *testing.T) {
	cfn := &fakeCFN{stacks: []cfntypes.Stack{existingStack()}}
	ch := &fakeChanges{
		createErr: &smithy.GenericAPIError{Code: "AlreadyExistsException"},
		described: []*cloudformation.DescribeChangeSetOutput{changeSet(cfntypes.ChangeSetStatusCreateComplete, "")},
	}
	infra := updateInfra(cfn, &fakeEKS{}, ch, &fakeNodes{})

	_, err := infra.PlanUpdate(t.Context(), target, UpdateSpec{Name: "n", Template: "x", ServiceRoleARN: "r"})

	require.NoError(t, err)
}

func TestPlanUpdateKeepsTheCauseOfAFailedChangeSet(t *testing.T) {
	cfn := &fakeCFN{stacks: []cfntypes.Stack{existingStack()}}
	ch := &fakeChanges{described: []*cloudformation.DescribeChangeSetOutput{changeSet(cfntypes.ChangeSetStatusFailed, "Parameter validation failed")}}
	infra := updateInfra(cfn, &fakeEKS{}, ch, &fakeNodes{})

	_, err := infra.PlanUpdate(t.Context(), target, UpdateSpec{Name: "n", Template: "x", ServiceRoleARN: "r"})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Parameter validation failed")
	assert.Len(t, ch.deleted, 1)
}

func TestPlanUpdateOfAStackThatIsGoneFails(t *testing.T) {
	cfn := &fakeCFN{}
	infra := updateInfra(cfn, &fakeEKS{}, &fakeChanges{}, &fakeNodes{})

	_, err := infra.PlanUpdate(t.Context(), target, UpdateSpec{Name: "n", Template: "x", ServiceRoleARN: "r"})

	require.ErrorIs(t, err, ErrStackMissing)
}

func TestApplyPlanExecutesTheChangeSetAndToleratesAnExecutedOne(t *testing.T) {
	ch := &fakeChanges{}
	infra := updateInfra(&fakeCFN{}, &fakeEKS{}, ch, &fakeNodes{})

	require.NoError(t, infra.ApplyPlan(t.Context(), target, "pds-run-7"))
	assert.Equal(t, "pds-run-7", sdkaws.ToString(ch.executed[0].ChangeSetName))
	assert.Equal(t, target.StackName, sdkaws.ToString(ch.executed[0].StackName))

	ch.execErr = &smithy.GenericAPIError{Code: "InvalidChangeSetStatus", Message: "EXECUTE_COMPLETE"}
	require.NoError(t, infra.ApplyPlan(t.Context(), target, "pds-run-7"), "a rerun after a restart finds it executed")

	ch.execErr = errors.New("boom")
	require.Error(t, infra.ApplyPlan(t.Context(), target, "pds-run-7"))
}

func TestDiscardPlanDeletesTheChangeSet(t *testing.T) {
	ch := &fakeChanges{}
	infra := updateInfra(&fakeCFN{}, &fakeEKS{}, ch, &fakeNodes{})

	require.NoError(t, infra.DiscardPlan(t.Context(), target, "pds-run-7"))
	assert.Equal(t, "pds-run-7", sdkaws.ToString(ch.deleted[0].ChangeSetName))
}

func TestDetectDriftNamesWhatWasChangedOrDeleted(t *testing.T) {
	ch := &fakeChanges{
		statuses: []cfntypes.StackDriftDetectionStatus{cfntypes.StackDriftDetectionStatusDetectionInProgress, cfntypes.StackDriftDetectionStatusDetectionComplete},
		drifts: []cfntypes.StackResourceDrift{
			{LogicalResourceId: sdkaws.String("PrivateRoute"), ResourceType: sdkaws.String("AWS::EC2::Route"), StackResourceDriftStatus: cfntypes.StackResourceDriftStatusModified},
			{LogicalResourceId: sdkaws.String("NatGateway"), ResourceType: sdkaws.String("AWS::EC2::NatGateway"), StackResourceDriftStatus: cfntypes.StackResourceDriftStatusDeleted},
		},
	}
	infra := updateInfra(&fakeCFN{}, &fakeEKS{}, ch, &fakeNodes{})

	got, err := infra.DetectDrift(t.Context(), target)

	require.NoError(t, err)
	assert.Equal(t, []Drift{
		{Resource: "PrivateRoute", Type: "AWS::EC2::Route", Status: "MODIFIED"},
		{Resource: "NatGateway", Type: "AWS::EC2::NatGateway", Status: "DELETED"},
	}, got)
}

func TestDetectDriftReportsADetectionAWSCouldNotFinish(t *testing.T) {
	ch := &fakeChanges{statuses: []cfntypes.StackDriftDetectionStatus{cfntypes.StackDriftDetectionStatusDetectionFailed}}
	infra := updateInfra(&fakeCFN{}, &fakeEKS{}, ch, &fakeNodes{})

	_, err := infra.DetectDrift(t.Context(), target)

	require.Error(t, err)
}

func TestDetectDriftGivesUpWhenTheContextEnds(t *testing.T) {
	ch := &fakeChanges{statuses: []cfntypes.StackDriftDetectionStatus{cfntypes.StackDriftDetectionStatusDetectionInProgress}}
	infra := updateInfra(&fakeCFN{}, &fakeEKS{}, ch, &fakeNodes{})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	_, err := infra.DetectDrift(ctx, target)

	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestKubernetesReadsTheVersionTheEndpointAndTheNodeGroups(t *testing.T) {
	e := &fakeEKS{cluster: ekstypes.Cluster{
		Status: ekstypes.ClusterStatusActive, Version: sdkaws.String("1.33"),
		ResourcesVpcConfig: &ekstypes.VpcConfigResponse{EndpointPublicAccess: true},
	}}
	n := &fakeNodes{names: []string{"pds-nodes"}, groups: map[string]ekstypes.Nodegroup{
		"pds-nodes": {
			NodegroupName: sdkaws.String("pds-nodes"), Status: ekstypes.NodegroupStatusActive, Version: sdkaws.String("1.33"),
			InstanceTypes: []string{"m6i.xlarge"},
			ScalingConfig: &ekstypes.NodegroupScalingConfig{MinSize: sdkaws.Int32(3), DesiredSize: sdkaws.Int32(3), MaxSize: sdkaws.Int32(3)},
		},
	}}
	infra := updateInfra(&fakeCFN{}, e, &fakeChanges{}, n)

	got, err := infra.Cluster(t.Context(), target)

	require.NoError(t, err)
	assert.Equal(t, KubernetesState{
		Exists: true, Status: "ACTIVE", Version: "1.33", PublicEndpoint: true,
		NodeGroups: []NodeGroupState{{Name: "pds-nodes", Status: "ACTIVE", Version: "1.33", InstanceTypes: []string{"m6i.xlarge"}, Min: 3, Desired: 3, Max: 3}},
	}, got)
}

func TestKubernetesSaysItIsMissingWhenEKSDoesNotKnowIt(t *testing.T) {
	e := &fakeEKS{describeErr: &ekstypes.ResourceNotFoundException{}}
	infra := updateInfra(&fakeCFN{}, e, &fakeChanges{}, &fakeNodes{})

	got, err := infra.Cluster(t.Context(), target)

	require.NoError(t, err)
	assert.False(t, got.Exists)
}

func TestKubernetesKeepsAReadThatFailed(t *testing.T) {
	e := &fakeEKS{describeErr: errors.New("throttled")}
	infra := updateInfra(&fakeCFN{}, e, &fakeChanges{}, &fakeNodes{})

	_, err := infra.Cluster(t.Context(), target)

	require.Error(t, err)
}

func TestStackStateCarriesItsParameters(t *testing.T) {
	cfn := &fakeCFN{stacks: []cfntypes.Stack{existingStack()}}
	infra := updateInfra(cfn, &fakeEKS{}, &fakeChanges{}, &fakeNodes{})

	state, err := infra.Stack(t.Context(), target)

	require.NoError(t, err)
	assert.Equal(t, "1.33", state.Parameters["KubernetesVersion"])
}

func TestStackReadsTheFirstFailedResourceOfARolledBackUpdate(t *testing.T) {
	stack := existingStack()
	stack.StackStatus = cfntypes.StackStatusUpdateRollbackComplete
	cfn := &fakeCFN{stacks: []cfntypes.Stack{stack}, events: []cfntypes.StackEvent{
		{ResourceStatus: cfntypes.ResourceStatusUpdateFailed, ResourceStatusReason: sdkaws.String("Cancelled")},
		{ResourceStatus: cfntypes.ResourceStatusUpdateFailed, ResourceStatusReason: sdkaws.String("VcpuLimitExceeded")},
	}}
	infra := updateInfra(cfn, &fakeEKS{}, &fakeChanges{}, &fakeNodes{})

	state, err := infra.Stack(t.Context(), target)

	require.NoError(t, err)
	assert.Equal(t, "VcpuLimitExceeded", state.FailureReason, "the oldest failure is the cause, the rest is the rollback")
}
