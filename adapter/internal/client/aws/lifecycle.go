package aws

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

// ErrStackMissing means an update was asked of a stack that does not exist.
var ErrStackMissing = errors.New("the stack does not exist")

type changeAPI interface {
	CreateChangeSet(context.Context, *cloudformation.CreateChangeSetInput, ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error)
	DescribeChangeSet(context.Context, *cloudformation.DescribeChangeSetInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeChangeSetOutput, error)
	ExecuteChangeSet(context.Context, *cloudformation.ExecuteChangeSetInput, ...func(*cloudformation.Options)) (*cloudformation.ExecuteChangeSetOutput, error)
	DeleteChangeSet(context.Context, *cloudformation.DeleteChangeSetInput, ...func(*cloudformation.Options)) (*cloudformation.DeleteChangeSetOutput, error)
	DetectStackDrift(context.Context, *cloudformation.DetectStackDriftInput, ...func(*cloudformation.Options)) (*cloudformation.DetectStackDriftOutput, error)
	DescribeStackDriftDetectionStatus(context.Context, *cloudformation.DescribeStackDriftDetectionStatusInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeStackDriftDetectionStatusOutput, error)
	DescribeStackResourceDrifts(context.Context, *cloudformation.DescribeStackResourceDriftsInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeStackResourceDriftsOutput, error)
}

type nodegroupAPI interface {
	ListNodegroups(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error)
	DescribeNodegroup(context.Context, *eks.DescribeNodegroupInput, ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error)
}

// UpdateSpec is a change to an existing stack, made through a change set.
type UpdateSpec struct {
	// Name is the change set's name. A run uses the same name on every attempt,
	// so an attempt after a restart finds the change set it made.
	Name           string
	Template       string
	ServiceRoleARN string
	// Parameters are the values to change. Every other parameter the stack has
	// keeps its value, and one the template adds takes its default.
	Parameters map[string]string
	Tags       map[string]string
}

// Change is one resource a change set touches.
type Change struct {
	Resource string
	Type     string
	// Action is Add, Modify or Remove, and Replacement is True, False or
	// Conditional.
	Action      string
	Replacement string
}

// ChangePlan is what a change set would do.
type ChangePlan struct {
	Name    string
	Changes []Change
	// Empty means the update changes nothing.
	Empty bool
}

// PlanUpdate creates a change set for the stack and reads what it would do. It
// changes nothing in the stack.
func (i *Infra) PlanUpdate(ctx context.Context, t Target, spec UpdateSpec) (ChangePlan, error) {
	current, err := i.Stack(ctx, t)
	if err != nil {
		return ChangePlan{}, err
	}
	if !current.Exists {
		return ChangePlan{}, ErrStackMissing
	}
	session, err := i.assume(ctx, t)
	if err != nil {
		return ChangePlan{}, err
	}
	client := i.changes(session)

	in := &cloudformation.CreateChangeSetInput{
		StackName: &t.StackName, ChangeSetName: &spec.Name, ChangeSetType: cfntypes.ChangeSetTypeUpdate,
		TemplateBody: &spec.Template, RoleARN: &spec.ServiceRoleARN,
	}
	for _, key := range sortedKeys(current.Parameters) {
		if value, ok := spec.Parameters[key]; ok {
			in.Parameters = append(in.Parameters, cfntypes.Parameter{ParameterKey: sdkaws.String(key), ParameterValue: sdkaws.String(value)})

			continue
		}
		in.Parameters = append(in.Parameters, cfntypes.Parameter{ParameterKey: sdkaws.String(key), UsePreviousValue: sdkaws.Bool(true)})
	}
	// A revision may add a parameter, which a stack made before it has no value for.
	for _, key := range sortedKeys(spec.Parameters) {
		if _, has := current.Parameters[key]; !has {
			in.Parameters = append(in.Parameters, cfntypes.Parameter{ParameterKey: sdkaws.String(key), ParameterValue: sdkaws.String(spec.Parameters[key])})
		}
	}
	for _, key := range sortedKeys(spec.Tags) {
		in.Tags = append(in.Tags, cfntypes.Tag{Key: sdkaws.String(key), Value: sdkaws.String(spec.Tags[key])})
	}
	if _, err := client.CreateChangeSet(ctx, in); err != nil && errorCode(err) != "AlreadyExistsException" {
		return ChangePlan{}, fmt.Errorf("create change set: %w", err)
	}

	for {
		out, err := client.DescribeChangeSet(ctx, &cloudformation.DescribeChangeSetInput{StackName: &t.StackName, ChangeSetName: &spec.Name})
		if err != nil {
			return ChangePlan{}, fmt.Errorf("describe change set: %w", err)
		}
		switch out.Status {
		case cfntypes.ChangeSetStatusCreateComplete:
			return ChangePlan{Name: spec.Name, Changes: changesOf(out.Changes)}, nil
		case cfntypes.ChangeSetStatusFailed:
			reason := sdkaws.ToString(out.StatusReason)
			_ = i.DiscardPlan(ctx, t, spec.Name)
			if strings.Contains(reason, "didn't contain changes") || strings.Contains(reason, "No updates are to be performed") {
				return ChangePlan{Name: spec.Name, Empty: true}, nil
			}

			return ChangePlan{}, fmt.Errorf("the change set failed: %s", reason)
		}
		select {
		case <-ctx.Done():
			return ChangePlan{}, ctx.Err()
		case <-time.After(i.poll):
		}
	}
}

func changesOf(in []cfntypes.Change) []Change {
	var out []Change
	for _, c := range in {
		rc := c.ResourceChange
		if rc == nil {
			continue
		}
		out = append(out, Change{
			Resource: sdkaws.ToString(rc.LogicalResourceId), Type: sdkaws.ToString(rc.ResourceType),
			Action: string(rc.Action), Replacement: string(rc.Replacement),
		})
	}

	return out
}

// ApplyPlan executes the change set. One that was executed already is not an
// error, so a step that ran before a restart can run again.
func (i *Infra) ApplyPlan(ctx context.Context, t Target, name string) error {
	session, err := i.assume(ctx, t)
	if err != nil {
		return err
	}
	_, err = i.changes(session).ExecuteChangeSet(ctx, &cloudformation.ExecuteChangeSetInput{StackName: &t.StackName, ChangeSetName: &name})
	if err != nil && errorCode(err) != "InvalidChangeSetStatus" {
		return fmt.Errorf("execute change set: %w", err)
	}

	return nil
}

// DiscardPlan deletes a change set that will not be executed.
func (i *Infra) DiscardPlan(ctx context.Context, t Target, name string) error {
	session, err := i.assume(ctx, t)
	if err != nil {
		return err
	}
	if _, err := i.changes(session).DeleteChangeSet(ctx, &cloudformation.DeleteChangeSetInput{StackName: &t.StackName, ChangeSetName: &name}); err != nil {
		return fmt.Errorf("delete change set: %w", err)
	}

	return nil
}

// Drift is a resource of the stack that was changed or deleted outside PDS.
type Drift struct {
	Resource string
	Type     string
	// Status is MODIFIED or DELETED.
	Status string
}

// DetectDrift runs CloudFormation's drift detection on the stack and returns
// the resources it found changed or deleted. Detection skips the EKS cluster and
// node groups, which Cluster reads instead.
func (i *Infra) DetectDrift(ctx context.Context, t Target) ([]Drift, error) {
	session, err := i.assume(ctx, t)
	if err != nil {
		return nil, err
	}
	client := i.changes(session)
	started, err := client.DetectStackDrift(ctx, &cloudformation.DetectStackDriftInput{StackName: &t.StackName})
	if err != nil {
		return nil, fmt.Errorf("detect stack drift: %w", err)
	}
	for {
		status, err := client.DescribeStackDriftDetectionStatus(ctx, &cloudformation.DescribeStackDriftDetectionStatusInput{StackDriftDetectionId: started.StackDriftDetectionId})
		if err != nil {
			return nil, fmt.Errorf("read the drift detection: %w", err)
		}
		switch status.DetectionStatus {
		case cfntypes.StackDriftDetectionStatusDetectionComplete:
			return resourceDrifts(ctx, client, t)
		case cfntypes.StackDriftDetectionStatusDetectionFailed:
			return nil, fmt.Errorf("the drift detection failed: %s", sdkaws.ToString(status.DetectionStatusReason))
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(i.poll):
		}
	}
}

func resourceDrifts(ctx context.Context, client changeAPI, t Target) ([]Drift, error) {
	out, err := client.DescribeStackResourceDrifts(ctx, &cloudformation.DescribeStackResourceDriftsInput{
		StackName: &t.StackName,
		StackResourceDriftStatusFilters: []cfntypes.StackResourceDriftStatus{
			cfntypes.StackResourceDriftStatusModified, cfntypes.StackResourceDriftStatusDeleted,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("read the drifted resources: %w", err)
	}
	var drifts []Drift
	for _, d := range out.StackResourceDrifts {
		drifts = append(drifts, Drift{
			Resource: sdkaws.ToString(d.LogicalResourceId), Type: sdkaws.ToString(d.ResourceType),
			Status: string(d.StackResourceDriftStatus),
		})
	}

	return drifts, nil
}

// NodeGroupState is what EKS says of a node group.
type NodeGroupState struct {
	Name          string
	Status        string
	Version       string
	InstanceTypes []string
	Min           int32
	Desired       int32
	Max           int32
}

// KubernetesState is what EKS says of the environment's cluster. A cluster EKS does
// not know has Exists false.
type KubernetesState struct {
	Exists         bool
	Status         string
	Version        string
	PublicEndpoint bool
	NodeGroups     []NodeGroupState
}

// Cluster reads the EKS cluster and its node groups.
func (i *Infra) Cluster(ctx context.Context, t Target) (KubernetesState, error) {
	session, err := i.assume(ctx, t)
	if err != nil {
		return KubernetesState{}, err
	}
	cluster, err := describe(ctx, i.eks(session), t.EKSName)
	if err != nil {
		var missing *ekstypes.ResourceNotFoundException
		if errors.As(err, &missing) {
			return KubernetesState{}, nil
		}

		return KubernetesState{}, err
	}
	out := KubernetesState{Exists: true, Status: string(cluster.Status), Version: sdkaws.ToString(cluster.Version)}
	if vpc := cluster.ResourcesVpcConfig; vpc != nil {
		out.PublicEndpoint = vpc.EndpointPublicAccess
	}

	nodes := i.nodes(session)
	listed, err := nodes.ListNodegroups(ctx, &eks.ListNodegroupsInput{ClusterName: &t.EKSName})
	if err != nil {
		return KubernetesState{}, fmt.Errorf("list node groups: %w", err)
	}
	for _, name := range listed.Nodegroups {
		got, err := nodes.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{ClusterName: &t.EKSName, NodegroupName: sdkaws.String(name)})
		if err != nil {
			return KubernetesState{}, fmt.Errorf("describe node group: %w", err)
		}
		g := got.Nodegroup
		state := NodeGroupState{
			Name: sdkaws.ToString(g.NodegroupName), Status: string(g.Status), Version: sdkaws.ToString(g.Version),
			InstanceTypes: g.InstanceTypes,
		}
		if sc := g.ScalingConfig; sc != nil {
			state.Min, state.Desired, state.Max = sdkaws.ToInt32(sc.MinSize), sdkaws.ToInt32(sc.DesiredSize), sdkaws.ToInt32(sc.MaxSize)
		}
		out.NodeGroups = append(out.NodeGroups, state)
	}

	return out, nil
}
