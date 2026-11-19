package aws

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// ErrNotRemovable means the resource is of a kind the sweep never deletes by
// itself. It stays, and the person is told it is there.
var ErrNotRemovable = errors.New("this kind of resource is not removed by the sweep")

// ownershipTag is on everything the control plane creates, directly or through
// a controller. Its value is the environment's ID.
const ownershipTag = "percona.com/pds"

// ClusterMissing reports whether err says the EKS cluster does not exist, as
// it does for a create that stopped before the cluster or a delete that ran
// before.
func ClusterMissing(err error) bool {
	return err != nil && errorCode(err) == "ResourceNotFoundException"
}

// Resource is something an environment owns in the customer's account.
type Resource struct {
	// Type is a short name a person reads, such as "EBS volume".
	Type string
	ID   string
	ARN  string
}

// String is how a person reads the resource.
func (r Resource) String() string { return r.Type + " " + r.ID }

type taggingAPI interface {
	GetResources(context.Context, *resourcegroupstaggingapi.GetResourcesInput, ...func(*resourcegroupstaggingapi.Options)) (*resourcegroupstaggingapi.GetResourcesOutput, error)
}

type ec2SweepAPI interface {
	DeleteVolume(context.Context, *ec2.DeleteVolumeInput, ...func(*ec2.Options)) (*ec2.DeleteVolumeOutput, error)
	DeleteSnapshot(context.Context, *ec2.DeleteSnapshotInput, ...func(*ec2.Options)) (*ec2.DeleteSnapshotOutput, error)
	DeleteNetworkInterface(context.Context, *ec2.DeleteNetworkInterfaceInput, ...func(*ec2.Options)) (*ec2.DeleteNetworkInterfaceOutput, error)
	DeleteSecurityGroup(context.Context, *ec2.DeleteSecurityGroupInput, ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error)
}

type elbAPI interface {
	DeleteLoadBalancer(context.Context, *elasticloadbalancingv2.DeleteLoadBalancerInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DeleteLoadBalancerOutput, error)
	DeleteTargetGroup(context.Context, *elasticloadbalancingv2.DeleteTargetGroupInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DeleteTargetGroupOutput, error)
}

type bucketEmptyAPI interface {
	GetBucketTagging(context.Context, *s3.GetBucketTaggingInput, ...func(*s3.Options)) (*s3.GetBucketTaggingOutput, error)
	ListObjectVersions(context.Context, *s3.ListObjectVersionsInput, ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
	DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
	DeleteBucket(context.Context, *s3.DeleteBucketInput, ...func(*s3.Options)) (*s3.DeleteBucketOutput, error)
}

// Owned finds what the environment owns by tag, every time it is asked: nothing
// is remembered, so a restart or a half-finished delete resumes from where AWS
// is. A resource counts when it carries the ownership tag, or when a cluster
// the control plane created owns it through the tag the cluster's controllers
// leave. The backup bucket is left to DeleteBucket. The result is in the order
// the resources can be removed: load balancers before the network interfaces
// they hold, and the security groups last.
func (i *Infra) Owned(ctx context.Context, t Target, environmentID string) ([]Resource, error) {
	session, err := i.assume(ctx, t)
	if err != nil {
		return nil, err
	}
	client := i.tagging(session)

	filters := []rgtypes.TagFilter{
		{Key: sdkaws.String(ownershipTag), Values: []string{environmentID}},
		{Key: sdkaws.String("kubernetes.io/cluster/" + t.EKSName)},
		{Key: sdkaws.String("elbv2.k8s.aws/cluster"), Values: []string{t.EKSName}},
		{Key: sdkaws.String("aws:eks:cluster-name"), Values: []string{t.EKSName}},
	}
	seen := map[string]bool{}
	var found []Resource
	for _, filter := range filters {
		token := ""
		for {
			out, err := client.GetResources(ctx, &resourcegroupstaggingapi.GetResourcesInput{
				TagFilters: []rgtypes.TagFilter{filter}, PaginationToken: sdkaws.String(token),
			})
			if err != nil {
				return nil, fmt.Errorf("find the environment's resources: %w", err)
			}
			for _, m := range out.ResourceTagMappingList {
				arn := sdkaws.ToString(m.ResourceARN)
				if seen[arn] {
					continue
				}
				seen[arn] = true
				if r, ok := parseResource(arn); ok {
					found = append(found, r)
				}
			}
			if token = sdkaws.ToString(out.PaginationToken); token == "" {
				break
			}
		}
	}
	slices.SortStableFunc(found, func(a, b Resource) int { return removalRank(a.Type) - removalRank(b.Type) })

	return found, nil
}

// removalRank orders kinds so what holds another resource goes first.
func removalRank(kind string) int {
	order := []string{"load balancer", "target group", "network interface", "EBS volume", "EBS snapshot", "security group"}
	if i := slices.Index(order, kind); i >= 0 {
		return i
	}

	return len(order)
}

// parseResource reads an ARN. The backup bucket is not a resource of the sweep.
func parseResource(arn string) (Resource, bool) {
	parts := strings.SplitN(arn, ":", 6)
	if len(parts) < 6 || parts[2] == "s3" {
		return Resource{}, false
	}
	service, resource := parts[2], parts[5]
	kind, rest, _ := strings.Cut(resource, "/")

	switch {
	case service == "ec2" && kind == "volume":
		return Resource{Type: "EBS volume", ID: rest, ARN: arn}, true
	case service == "ec2" && kind == "snapshot":
		return Resource{Type: "EBS snapshot", ID: rest, ARN: arn}, true
	case service == "ec2" && kind == "network-interface":
		return Resource{Type: "network interface", ID: rest, ARN: arn}, true
	case service == "ec2" && kind == "security-group":
		return Resource{Type: "security group", ID: rest, ARN: arn}, true
	case service == "elasticloadbalancing" && kind == "loadbalancer":
		// An ALB or NLB reads app/<name>/<id> or net/<name>/<id>; a classic one reads <name>.
		flavor, name, ok := strings.Cut(rest, "/")
		if flavor != "app" && flavor != "net" || !ok {
			return Resource{Type: "classic load balancer", ID: rest, ARN: arn}, true
		}
		name, _, _ = strings.Cut(name, "/")

		return Resource{Type: "load balancer", ID: name, ARN: arn}, true
	case service == "elasticloadbalancing" && kind == "targetgroup":
		name, _, _ := strings.Cut(rest, "/")

		return Resource{Type: "target group", ID: name, ARN: arn}, true
	}

	return Resource{Type: service + " " + kind, ID: rest, ARN: arn}, true
}

// Release deletes one resource that Owned found. One that is already gone is
// released. A kind the sweep does not delete returns ErrNotRemovable, and one
// AWS refuses, because it is still in use, returns what AWS said.
func (i *Infra) Release(ctx context.Context, t Target, r Resource) error {
	session, err := i.assume(ctx, t)
	if err != nil {
		return err
	}

	var gone string
	switch r.Type {
	case "EBS volume":
		_, err = i.ec2(session).DeleteVolume(ctx, &ec2.DeleteVolumeInput{VolumeId: &r.ID})
		gone = "InvalidVolume.NotFound"
	case "EBS snapshot":
		_, err = i.ec2(session).DeleteSnapshot(ctx, &ec2.DeleteSnapshotInput{SnapshotId: &r.ID})
		gone = "InvalidSnapshot.NotFound"
	case "network interface":
		_, err = i.ec2(session).DeleteNetworkInterface(ctx, &ec2.DeleteNetworkInterfaceInput{NetworkInterfaceId: &r.ID})
		gone = "InvalidNetworkInterfaceID.NotFound"
	case "security group":
		_, err = i.ec2(session).DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{GroupId: &r.ID})
		gone = "InvalidGroup.NotFound"
	case "load balancer":
		_, err = i.elb(session).DeleteLoadBalancer(ctx, &elasticloadbalancingv2.DeleteLoadBalancerInput{LoadBalancerArn: &r.ARN})
		gone = "LoadBalancerNotFound"
	case "target group":
		_, err = i.elb(session).DeleteTargetGroup(ctx, &elasticloadbalancingv2.DeleteTargetGroupInput{TargetGroupArn: &r.ARN})
		gone = "TargetGroupNotFound"
	default:
		return fmt.Errorf("%w: %s", ErrNotRemovable, r)
	}
	if err != nil && errorCode(err) != gone {
		return fmt.Errorf("delete %s: %w", r, err)
	}

	return nil
}

// DeleteBucket empties the bucket, every version and delete marker included,
// and deletes it, but only when it carries the environment's tag: a bucket that
// does not is someone else's and is left. It reports whether it deleted one. A
// bucket that does not exist is done.
func (i *Infra) DeleteBucket(ctx context.Context, t Target, bucket, environmentID string) (bool, error) {
	session, err := i.assume(ctx, t)
	if err != nil {
		return false, err
	}
	client := i.emptyBuckets(session)

	tagged, err := client.GetBucketTagging(ctx, &s3.GetBucketTaggingInput{Bucket: &bucket})
	if err != nil {
		if code := errorCode(err); code == "NoSuchBucket" || code == "NoSuchTagSet" {
			return false, nil
		}

		return false, fmt.Errorf("read the bucket's tags: %w", err)
	}
	if !slices.ContainsFunc(tagged.TagSet, func(tag s3types.Tag) bool {
		return sdkaws.ToString(tag.Key) == ownershipTag && sdkaws.ToString(tag.Value) == environmentID
	}) {
		return false, nil
	}

	for {
		listed, err := client.ListObjectVersions(ctx, &s3.ListObjectVersionsInput{Bucket: &bucket})
		if err != nil {
			return false, fmt.Errorf("list the bucket's objects: %w", err)
		}
		var batch []s3types.ObjectIdentifier
		for _, v := range listed.Versions {
			batch = append(batch, s3types.ObjectIdentifier{Key: v.Key, VersionId: v.VersionId})
		}
		for _, m := range listed.DeleteMarkers {
			batch = append(batch, s3types.ObjectIdentifier{Key: m.Key, VersionId: m.VersionId})
		}
		if len(batch) == 0 {
			break
		}
		if _, err := client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: &bucket, Delete: &s3types.Delete{Objects: batch, Quiet: sdkaws.Bool(true)},
		}); err != nil {
			return false, fmt.Errorf("empty the bucket: %w", err)
		}
	}

	if _, err := client.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: &bucket}); err != nil {
		if errorCode(err) == "NoSuchBucket" {
			return false, nil
		}

		return false, fmt.Errorf("delete the bucket: %w", err)
	}

	return true, nil
}
