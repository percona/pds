package aws

import (
	"context"
	"fmt"
	"slices"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	volumeARN = "arn:aws:ec2:eu-west-1:111122223333:volume/vol-0a1"
	eniARN    = "arn:aws:ec2:eu-west-1:111122223333:network-interface/eni-0b2"
	sgARN     = "arn:aws:ec2:eu-west-1:111122223333:security-group/sg-0c3"
	snapARN   = "arn:aws:ec2:eu-west-1:111122223333:snapshot/snap-0d4"
	lbARN     = "arn:aws:elasticloadbalancing:eu-west-1:111122223333:loadbalancer/net/orders-lb/50dc6c495c0c9188"
	tgARN     = "arn:aws:elasticloadbalancing:eu-west-1:111122223333:targetgroup/orders-tg/73e2d6bc24d8a067"
	vpcARN    = "arn:aws:ec2:eu-west-1:111122223333:vpc/vpc-0e5"
	bucketARN = "arn:aws:s3:::pds-111122223333-0123456789ab"
)

// fakeTagging answers each GetResources call from the tag filter's key, and
// pages the answer two at a time so pagination is exercised.
type fakeTagging struct {
	byKey map[string][]string
	calls []string
}

func (f *fakeTagging) GetResources(_ context.Context, in *resourcegroupstaggingapi.GetResourcesInput, _ ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error) {
	key := sdkaws.ToString(in.TagFilters[0].Key)
	f.calls = append(f.calls, key)
	all := f.byKey[key]
	start := 0
	if in.PaginationToken != nil && *in.PaginationToken != "" {
		start = 2
	}
	end := min(start+2, len(all))
	out := &resourcegroupstaggingapi.GetResourcesOutput{}
	for _, arn := range all[start:end] {
		out.ResourceTagMappingList = append(out.ResourceTagMappingList, rgtypes.ResourceTagMapping{ResourceARN: sdkaws.String(arn)})
	}
	if end < len(all) {
		out.PaginationToken = sdkaws.String("more")
	}

	return out, nil
}

type fakeEC2Sweep struct {
	deleted []string
	errs    map[string]error
}

func (f *fakeEC2Sweep) record(id string) error {
	f.deleted = append(f.deleted, id)

	return f.errs[id]
}

func (f *fakeEC2Sweep) DeleteVolume(_ context.Context, in *ec2.DeleteVolumeInput, _ ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error) {
	return &ec2.DeleteVolumeOutput{}, f.record(sdkaws.ToString(in.VolumeId))
}

func (f *fakeEC2Sweep) DeleteSnapshot(_ context.Context, in *ec2.DeleteSnapshotInput, _ ...func(*ec2.Options)) (*ec2.DeleteSnapshotOutput, error) {
	return &ec2.DeleteSnapshotOutput{}, f.record(sdkaws.ToString(in.SnapshotId))
}

func (f *fakeEC2Sweep) DeleteNetworkInterface(_ context.Context, in *ec2.DeleteNetworkInterfaceInput, _ ...func(*ec2.Options)) (*ec2.DeleteNetworkInterfaceOutput, error) {
	return &ec2.DeleteNetworkInterfaceOutput{}, f.record(sdkaws.ToString(in.NetworkInterfaceId))
}

func (f *fakeEC2Sweep) DeleteSecurityGroup(_ context.Context, in *ec2.DeleteSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error) {
	return &ec2.DeleteSecurityGroupOutput{}, f.record(sdkaws.ToString(in.GroupId))
}

type fakeELB struct {
	deleted []string
	errs    map[string]error
}

func (f *fakeELB) DeleteLoadBalancer(_ context.Context, in *elasticloadbalancingv2.DeleteLoadBalancerInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DeleteLoadBalancerOutput, error) {
	f.deleted = append(f.deleted, sdkaws.ToString(in.LoadBalancerArn))

	return &elasticloadbalancingv2.DeleteLoadBalancerOutput{}, f.errs[sdkaws.ToString(in.LoadBalancerArn)]
}

func (f *fakeELB) DeleteTargetGroup(_ context.Context, in *elasticloadbalancingv2.DeleteTargetGroupInput, _ ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DeleteTargetGroupOutput, error) {
	f.deleted = append(f.deleted, sdkaws.ToString(in.TargetGroupArn))

	return &elasticloadbalancingv2.DeleteTargetGroupOutput{}, f.errs[sdkaws.ToString(in.TargetGroupArn)]
}

func sweepInfra(tagging *fakeTagging, e *fakeEC2Sweep, l *fakeELB) *Infra {
	infra := newInfra(&fakeCFN{}, &fakeEKS{})
	infra.tagging = func(sdkaws.Config) taggingAPI { return tagging }
	infra.ec2 = func(sdkaws.Config) ec2SweepAPI { return e }
	infra.elb = func(sdkaws.Config) elbAPI { return l }

	return infra
}

func TestOwnedFindsWhatIsTaggedOrOwnedByTheEKS(t *testing.T) {
	tagging := &fakeTagging{byKey: map[string][]string{
		"percona.com/pds":                        {volumeARN, snapARN, vpcARN, bucketARN},
		"kubernetes.io/cluster/pds-0123456789ab": {volumeARN, eniARN, sgARN},
		"elbv2.k8s.aws/cluster":                  {lbARN, tgARN},
	}}

	got, err := sweepInfra(tagging, &fakeEC2Sweep{}, &fakeELB{}).Owned(context.Background(), target, "0123456789ab")
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{"percona.com/pds", "kubernetes.io/cluster/pds-0123456789ab", "elbv2.k8s.aws/cluster", "aws:eks:cluster-name"}, slices.Compact(slices.Sorted(slices.Values(tagging.calls))),
		"every ownership tag is searched, so a resource without our tag is still found")
	assert.Equal(t, []Resource{
		{Type: "load balancer", ID: "orders-lb", ARN: lbARN},
		{Type: "target group", ID: "orders-tg", ARN: tgARN},
		{Type: "network interface", ID: "eni-0b2", ARN: eniARN},
		{Type: "EBS volume", ID: "vol-0a1", ARN: volumeARN},
		{Type: "EBS snapshot", ID: "snap-0d4", ARN: snapARN},
		{Type: "security group", ID: "sg-0c3", ARN: sgARN},
		{Type: "ec2 vpc", ID: "vpc-0e5", ARN: vpcARN},
	}, got, "each once, in the order they can be removed, and the backup bucket left to its own step")
}

func TestOwnedReadsEveryPage(t *testing.T) {
	tagging := &fakeTagging{byKey: map[string][]string{"percona.com/pds": {volumeARN, eniARN, sgARN}}}

	got, err := sweepInfra(tagging, &fakeEC2Sweep{}, &fakeELB{}).Owned(context.Background(), target, "0123456789ab")
	require.NoError(t, err)

	assert.Len(t, got, 3)
}

func TestReleaseDeletesEachKindThroughItsOwnCall(t *testing.T) {
	ec2s, elbs := &fakeEC2Sweep{}, &fakeELB{}
	infra := sweepInfra(&fakeTagging{}, ec2s, elbs)

	for _, r := range []Resource{
		{Type: "EBS volume", ID: "vol-0a1", ARN: volumeARN},
		{Type: "EBS snapshot", ID: "snap-0d4", ARN: snapARN},
		{Type: "network interface", ID: "eni-0b2", ARN: eniARN},
		{Type: "security group", ID: "sg-0c3", ARN: sgARN},
		{Type: "load balancer", ID: "orders-lb", ARN: lbARN},
		{Type: "target group", ID: "orders-tg", ARN: tgARN},
	} {
		require.NoError(t, infra.Release(context.Background(), target, r), r.Type)
	}

	assert.Equal(t, []string{"vol-0a1", "snap-0d4", "eni-0b2", "sg-0c3"}, ec2s.deleted)
	assert.Equal(t, []string{lbARN, tgARN}, elbs.deleted)
}

func TestReleaseTreatsAResourceThatIsAlreadyGoneAsDone(t *testing.T) {
	ec2s := &fakeEC2Sweep{errs: map[string]error{"vol-0a1": apiErr("InvalidVolume.NotFound")}}
	elbs := &fakeELB{errs: map[string]error{lbARN: apiErr("LoadBalancerNotFound")}}
	infra := sweepInfra(&fakeTagging{}, ec2s, elbs)

	require.NoError(t, infra.Release(context.Background(), target, Resource{Type: "EBS volume", ID: "vol-0a1", ARN: volumeARN}))
	require.NoError(t, infra.Release(context.Background(), target, Resource{Type: "load balancer", ID: "orders-lb", ARN: lbARN}))
}

func TestReleaseReportsWhatAWSRefusedAndNeverTouchesAKindItDoesNotKnow(t *testing.T) {
	ec2s := &fakeEC2Sweep{errs: map[string]error{"vol-0a1": apiErr("VolumeInUse")}}
	infra := sweepInfra(&fakeTagging{}, ec2s, &fakeELB{})

	err := infra.Release(context.Background(), target, Resource{Type: "EBS volume", ID: "vol-0a1", ARN: volumeARN})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VolumeInUse")

	err = infra.Release(context.Background(), target, Resource{Type: "ec2 vpc", ID: "vpc-0e5", ARN: vpcARN})
	require.ErrorIs(t, err, ErrNotRemovable)
	assert.Equal(t, []string{"vol-0a1"}, ec2s.deleted)
}

// fakeEmptying is a bucket holding two object versions and a delete marker.
type fakeEmptying struct {
	tags     []s3types.Tag
	tagsErr  error
	versions []s3types.ObjectVersion
	markers  []s3types.DeleteMarkerEntry
	removed  []string
	deleted  []string
}

func (f *fakeEmptying) GetBucketTagging(context.Context, *s3.GetBucketTaggingInput, ...func(*s3.Options)) (*s3.GetBucketTaggingOutput, error) {
	return &s3.GetBucketTaggingOutput{TagSet: f.tags}, f.tagsErr
}

func (f *fakeEmptying) ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	out := &s3.ListObjectVersionsOutput{Versions: f.versions, DeleteMarkers: f.markers}
	f.versions, f.markers = nil, nil

	return out, nil
}

func (f *fakeEmptying) DeleteObjects(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	for _, o := range in.Delete.Objects {
		f.removed = append(f.removed, sdkaws.ToString(o.Key)+"@"+sdkaws.ToString(o.VersionId))
	}

	return &s3.DeleteObjectsOutput{}, nil
}

func (f *fakeEmptying) DeleteBucket(_ context.Context, in *s3.DeleteBucketInput, _ ...func(*s3.Options)) (*s3.DeleteBucketOutput, error) {
	f.deleted = append(f.deleted, sdkaws.ToString(in.Bucket))

	return &s3.DeleteBucketOutput{}, nil
}

func emptyingInfra(b *fakeEmptying) *Infra {
	infra := newInfra(&fakeCFN{}, &fakeEKS{})
	infra.emptyBuckets = func(sdkaws.Config) bucketEmptyAPI { return b }

	return infra
}

func TestDeleteBucketEmptiesEveryVersionAndMarkerThenDeletesIt(t *testing.T) {
	b := &fakeEmptying{
		tags:     []s3types.Tag{{Key: sdkaws.String("percona.com/pds"), Value: sdkaws.String("0123456789ab")}},
		versions: []s3types.ObjectVersion{{Key: sdkaws.String("a"), VersionId: sdkaws.String("v1")}, {Key: sdkaws.String("a"), VersionId: sdkaws.String("v2")}},
		markers:  []s3types.DeleteMarkerEntry{{Key: sdkaws.String("b"), VersionId: sdkaws.String("m1")}},
	}

	removed, err := emptyingInfra(b).DeleteBucket(context.Background(), target, "pds-111122223333-0123456789ab", "0123456789ab")
	require.NoError(t, err)

	assert.True(t, removed)
	assert.ElementsMatch(t, []string{"a@v1", "a@v2", "b@m1"}, b.removed)
	assert.Equal(t, []string{"pds-111122223333-0123456789ab"}, b.deleted)
}

func TestDeleteBucketLeavesABucketThatIsNotTheEnvironments(t *testing.T) {
	b := &fakeEmptying{tags: []s3types.Tag{{Key: sdkaws.String("percona.com/pds"), Value: sdkaws.String("ffffffffffff")}}}

	removed, err := emptyingInfra(b).DeleteBucket(context.Background(), target, "pds-111122223333-0123456789ab", "0123456789ab")
	require.NoError(t, err)

	assert.False(t, removed)
	assert.Empty(t, b.deleted)
	assert.Empty(t, b.removed)
}

func TestDeleteBucketIsDoneWhenThereIsNoBucket(t *testing.T) {
	b := &fakeEmptying{tagsErr: apiErr("NoSuchBucket")}

	removed, err := emptyingInfra(b).DeleteBucket(context.Background(), target, "pds-111122223333-0123456789ab", "0123456789ab")
	require.NoError(t, err)

	assert.False(t, removed)
}

func TestEKSMissingRecognisesAnAbsentEKS(t *testing.T) {
	assert.True(t, EKSMissing(fmt.Errorf("create access entry: %w", apiErr("ResourceNotFoundException"))))
	assert.False(t, EKSMissing(fmt.Errorf("create access entry: %w", apiErr("AccessDeniedException"))))
	assert.False(t, EKSMissing(nil))
}
