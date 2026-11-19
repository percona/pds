package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeCFN struct {
	deleted    []*cloudformation.DeleteStackInput
	created    []*cloudformation.CreateStackInput
	createErr  error
	stacks     []cfntypes.Stack
	describeEr error
	events     []cfntypes.StackEvent
}

func (f *fakeCFN) CreateStack(_ context.Context, in *cloudformation.CreateStackInput, _ ...func(*cloudformation.Options)) (*cloudformation.CreateStackOutput, error) {
	f.created = append(f.created, in)

	return &cloudformation.CreateStackOutput{}, f.createErr
}

func (f *fakeCFN) DeleteStack(_ context.Context, in *cloudformation.DeleteStackInput, _ ...func(*cloudformation.Options)) (*cloudformation.DeleteStackOutput, error) {
	f.deleted = append(f.deleted, in)

	return &cloudformation.DeleteStackOutput{}, nil
}

func (f *fakeCFN) DescribeStacks(context.Context, *cloudformation.DescribeStacksInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	return &cloudformation.DescribeStacksOutput{Stacks: f.stacks}, f.describeEr
}

func (f *fakeCFN) DescribeStackEvents(context.Context, *cloudformation.DescribeStackEventsInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeStackEventsOutput, error) {
	return &cloudformation.DescribeStackEventsOutput{StackEvents: f.events}, nil
}

type fakeEKS struct {
	cluster       ekstypes.Cluster
	describeErr   error
	updates       []*eks.UpdateClusterConfigInput
	entries       []*eks.CreateAccessEntryInput
	entryErr      error
	associated    []*eks.AssociateAccessPolicyInput
	deleted       []*eks.DeleteAccessEntryInput
	deleteErr     error
	updateStatus  ekstypes.UpdateStatus
	describeCalls int
}

func (f *fakeEKS) DescribeCluster(context.Context, *eks.DescribeClusterInput, ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
	f.describeCalls++
	if f.describeErr != nil {
		return nil, f.describeErr
	}
	cluster := f.cluster

	return &eks.DescribeClusterOutput{Cluster: &cluster}, nil
}

func (f *fakeEKS) UpdateClusterConfig(_ context.Context, in *eks.UpdateClusterConfigInput, _ ...func(*eks.Options)) (*eks.UpdateClusterConfigOutput, error) {
	f.updates = append(f.updates, in)
	f.cluster.ResourcesVpcConfig.EndpointPublicAccess = in.ResourcesVpcConfig.EndpointPublicAccess != nil && *in.ResourcesVpcConfig.EndpointPublicAccess
	f.cluster.ResourcesVpcConfig.PublicAccessCidrs = in.ResourcesVpcConfig.PublicAccessCidrs

	return &eks.UpdateClusterConfigOutput{Update: &ekstypes.Update{Id: sdkaws.String("u1")}}, nil
}

func (f *fakeEKS) DescribeUpdate(context.Context, *eks.DescribeUpdateInput, ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error) {
	return &eks.DescribeUpdateOutput{Update: &ekstypes.Update{Status: ekstypes.UpdateStatusSuccessful}}, nil
}

func (f *fakeEKS) CreateAccessEntry(_ context.Context, in *eks.CreateAccessEntryInput, _ ...func(*eks.Options)) (*eks.CreateAccessEntryOutput, error) {
	f.entries = append(f.entries, in)

	return &eks.CreateAccessEntryOutput{}, f.entryErr
}

func (f *fakeEKS) AssociateAccessPolicy(_ context.Context, in *eks.AssociateAccessPolicyInput, _ ...func(*eks.Options)) (*eks.AssociateAccessPolicyOutput, error) {
	f.associated = append(f.associated, in)

	return &eks.AssociateAccessPolicyOutput{}, nil
}

func (f *fakeEKS) DeleteAccessEntry(_ context.Context, in *eks.DeleteAccessEntryInput, _ ...func(*eks.Options)) (*eks.DeleteAccessEntryOutput, error) {
	f.deleted = append(f.deleted, in)

	return &eks.DeleteAccessEntryOutput{}, f.deleteErr
}

func newInfra(c *fakeCFN, e *fakeEKS) *Infra {
	return &Infra{
		assume: func(context.Context, Target) (sdkaws.Config, error) {
			return sdkaws.Config{Region: "eu-west-1", Credentials: credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "token")}, nil
		},
		cfn:  func(sdkaws.Config) cfnAPI { return c },
		eks:  func(sdkaws.Config) eksAPI { return e },
		poll: time.Millisecond,
	}
}

var target = Target{
	RoleARN: "arn:aws:iam::111122223333:role/pds/abc/pds-abc-control-plane", Region: "eu-west-1",
	StackName: "pds-env-0123456789ab", ClusterName: "pds-0123456789ab",
}

func apiErr(code string) error { return &smithy.GenericAPIError{Code: code, Message: code} }

func TestEnsureStackCreatesItThroughTheServiceRoleWithTheOwnershipTag(t *testing.T) {
	cfn := &fakeCFN{}
	infra := newInfra(cfn, &fakeEKS{})

	err := infra.EnsureStack(context.Background(), target, StackSpec{
		Template: "tpl", ServiceRoleARN: "arn:aws:iam::111122223333:role/pds/abc/pds-abc-cloudformation",
		Parameters: map[string]string{"EnvironmentId": "0123456789ab"},
		Tags:       map[string]string{"percona.com/pds": "0123456789ab"},
	})
	require.NoError(t, err)

	require.Len(t, cfn.created, 1)
	in := cfn.created[0]
	assert.Equal(t, "pds-env-0123456789ab", sdkaws.ToString(in.StackName))
	assert.Equal(t, "arn:aws:iam::111122223333:role/pds/abc/pds-abc-cloudformation", sdkaws.ToString(in.RoleARN))
	assert.Equal(t, "tpl", sdkaws.ToString(in.TemplateBody))
	assert.Equal(t, []cfntypes.Parameter{{ParameterKey: sdkaws.String("EnvironmentId"), ParameterValue: sdkaws.String("0123456789ab")}}, in.Parameters)
	assert.Equal(t, []cfntypes.Tag{{Key: sdkaws.String("percona.com/pds"), Value: sdkaws.String("0123456789ab")}}, in.Tags)
	assert.False(t, sdkaws.ToBool(in.EnableTerminationProtection), "an environment's stack is deleted with it")
	assert.Equal(t, cfntypes.OnFailureDoNothing, in.OnFailure, "a failed create is kept for the person to read and retry")
}

func TestEnsureStackIsDoneWhenTheStackExists(t *testing.T) {
	cfn := &fakeCFN{createErr: apiErr("AlreadyExistsException")}

	require.NoError(t, newInfra(cfn, &fakeEKS{}).EnsureStack(context.Background(), target, StackSpec{}))
}

func TestEnsureStackReturnsWhatAWSRefused(t *testing.T) {
	cfn := &fakeCFN{createErr: apiErr("AccessDenied")}

	err := newInfra(cfn, &fakeEKS{}).EnsureStack(context.Background(), target, StackSpec{})
	require.Error(t, err)
	_, classified := Classify(err, Where{})
	assert.True(t, classified)
}

func TestStackReadsStatusOutputsAndTheFirstFailure(t *testing.T) {
	cfn := &fakeCFN{
		stacks: []cfntypes.Stack{{
			StackStatus: cfntypes.StackStatusCreateFailed,
			Outputs:     []cfntypes.Output{{OutputKey: sdkaws.String("TemplateRevision"), OutputValue: sdkaws.String("1")}},
		}},
		events: []cfntypes.StackEvent{
			{ResourceStatus: cfntypes.ResourceStatusCreateFailed, LogicalResourceId: sdkaws.String("Cluster"),
				ResourceStatusReason: sdkaws.String("Resource creation cancelled")},
			{ResourceStatus: cfntypes.ResourceStatusCreateFailed, LogicalResourceId: sdkaws.String("Vpc"),
				ResourceStatusReason: sdkaws.String("The maximum number of VPCs has been reached. (Service: AmazonEC2; Status Code: 400; Error Code: VpcLimitExceeded; Request ID: x)")},
		},
	}

	got, err := newInfra(cfn, &fakeEKS{}).Stack(context.Background(), target)
	require.NoError(t, err)

	assert.True(t, got.Exists)
	assert.Equal(t, "CREATE_FAILED", got.Status)
	assert.True(t, got.Failed())
	assert.Equal(t, "1", got.Outputs["TemplateRevision"])
	_, classified := Classify(errors.New(got.FailureReason), Where{})
	assert.True(t, classified, "the oldest failed resource's text names the quota")
}

func TestStackReportsAMissingStack(t *testing.T) {
	cfn := &fakeCFN{describeEr: &smithy.GenericAPIError{Code: "ValidationError", Message: "Stack with id pds-env-0123456789ab does not exist"}}

	got, err := newInfra(cfn, &fakeEKS{}).Stack(context.Background(), target)
	require.NoError(t, err)
	assert.False(t, got.Exists)
}

func TestStackStatusClasses(t *testing.T) {
	assert.True(t, StackState{Exists: true, Status: "CREATE_COMPLETE"}.Complete())
	assert.True(t, StackState{Exists: true, Status: "CREATE_IN_PROGRESS"}.InProgress())
	for _, status := range []string{"CREATE_FAILED", "ROLLBACK_COMPLETE", "ROLLBACK_FAILED", "ROLLBACK_IN_PROGRESS", "DELETE_COMPLETE"} {
		assert.False(t, StackState{Exists: true, Status: status}.Complete(), status)
	}
}

func newCluster() ekstypes.Cluster {
	return ekstypes.Cluster{
		Name: sdkaws.String("pds-0123456789ab"), Status: ekstypes.ClusterStatusActive,
		Endpoint:             sdkaws.String("https://ABC.gr7.eu-west-1.eks.amazonaws.com"),
		CertificateAuthority: &ekstypes.Certificate{Data: sdkaws.String(base64.StdEncoding.EncodeToString([]byte("ca")))},
		ResourcesVpcConfig:   &ekstypes.VpcConfigResponse{EndpointPrivateAccess: true},
	}
}

func TestOpenAccessAddsTheEntryAndOpensTheEndpointToTheDeclaredAddresses(t *testing.T) {
	kube := &fakeEKS{cluster: newCluster()}

	got, err := newInfra(&fakeCFN{}, kube).OpenAccess(context.Background(), target, []string{"203.0.113.7/32"})
	require.NoError(t, err)

	require.Len(t, kube.entries, 1)
	assert.Equal(t, target.RoleARN, sdkaws.ToString(kube.entries[0].PrincipalArn))
	assert.Equal(t, "pds-0123456789ab", sdkaws.ToString(kube.entries[0].ClusterName))
	require.Len(t, kube.associated, 1)
	assert.Contains(t, sdkaws.ToString(kube.associated[0].PolicyArn), "AmazonEKSClusterAdminPolicy")
	assert.Equal(t, ekstypes.AccessScopeTypeCluster, kube.associated[0].AccessScope.Type)

	require.Len(t, kube.updates, 1)
	vpc := kube.updates[0].ResourcesVpcConfig
	assert.True(t, sdkaws.ToBool(vpc.EndpointPublicAccess))
	assert.Equal(t, []string{"203.0.113.7/32"}, vpc.PublicAccessCidrs)

	assert.Equal(t, "https://ABC.gr7.eu-west-1.eks.amazonaws.com", got.Endpoint)
	assert.Equal(t, []byte("ca"), got.CA)
}

func TestOpenAccessIsIdempotent(t *testing.T) {
	cluster := newCluster()
	cluster.ResourcesVpcConfig.EndpointPublicAccess = true
	cluster.ResourcesVpcConfig.PublicAccessCidrs = []string{"203.0.113.7/32"}
	kube := &fakeEKS{cluster: cluster, entryErr: apiErr("ResourceInUseException")}

	_, err := newInfra(&fakeCFN{}, kube).OpenAccess(context.Background(), target, []string{"203.0.113.7/32"})
	require.NoError(t, err)
	assert.Empty(t, kube.updates, "an endpoint already open to those addresses is left alone")
}

func TestCloseAccessChecksTheEndpointThenClosesItAndDropsTheEntry(t *testing.T) {
	cluster := newCluster()
	cluster.ResourcesVpcConfig.EndpointPublicAccess = true
	cluster.ResourcesVpcConfig.PublicAccessCidrs = []string{"203.0.113.7/32"}
	kube := &fakeEKS{cluster: cluster}

	note, err := newInfra(&fakeCFN{}, kube).CloseAccess(context.Background(), target)
	require.NoError(t, err)

	require.Len(t, kube.updates, 1)
	assert.False(t, sdkaws.ToBool(kube.updates[0].ResourcesVpcConfig.EndpointPublicAccess))
	require.Len(t, kube.deleted, 1)
	assert.Equal(t, target.RoleARN, sdkaws.ToString(kube.deleted[0].PrincipalArn))
	assert.Contains(t, note, "Closed the public endpoint")
	assert.Contains(t, note, "removed the access entry")
}

func TestCloseAccessLeavesAClosedEndpointAndAMissingEntry(t *testing.T) {
	kube := &fakeEKS{cluster: newCluster(), deleteErr: apiErr("ResourceNotFoundException")}

	note, err := newInfra(&fakeCFN{}, kube).CloseAccess(context.Background(), target)
	require.NoError(t, err)
	assert.Empty(t, kube.updates)
	assert.Contains(t, note, "already private")
}

func TestCloseAccessOnAClusterThatIsGoneRecordsIt(t *testing.T) {
	kube := &fakeEKS{describeErr: apiErr("ResourceNotFoundException")}

	note, err := newInfra(&fakeCFN{}, kube).CloseAccess(context.Background(), target)
	require.NoError(t, err)
	assert.Contains(t, note, "no longer exists")
}

func TestKubeTokenIsASignedCallerIdentityRequestForTheCluster(t *testing.T) {
	token, err := newInfra(&fakeCFN{}, &fakeEKS{}).KubeToken(context.Background(), target)
	require.NoError(t, err)

	encoded, ok := strings.CutPrefix(token, "k8s-aws-v1.")
	require.True(t, ok)
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	require.NoError(t, err)
	signed, err := url.Parse(string(raw))
	require.NoError(t, err)

	assert.Equal(t, "sts.eu-west-1.amazonaws.com", signed.Host)
	assert.Equal(t, "GetCallerIdentity", signed.Query().Get("Action"))
	assert.Equal(t, "60", signed.Query().Get("X-Amz-Expires"))
	assert.Contains(t, signed.Query().Get("X-Amz-SignedHeaders"), "x-k8s-aws-id")
}

func TestDeleteStackAsksCloudFormationToRemoveItThroughItsOwnRole(t *testing.T) {
	cfn := &fakeCFN{}

	require.NoError(t, newInfra(cfn, &fakeEKS{}).DeleteStack(context.Background(), target))

	require.Len(t, cfn.deleted, 1)
	assert.Equal(t, "pds-env-0123456789ab", sdkaws.ToString(cfn.deleted[0].StackName))
	assert.Nil(t, cfn.deleted[0].RoleARN, "CloudFormation uses the role the stack was made with")
}
