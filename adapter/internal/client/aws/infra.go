package aws

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go/middleware"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// adminPolicy is the access policy the control plane holds for as long
// as an Access window is open.
const adminPolicy = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy" // allow-legacy-name: the AWS SDK names it so

// tokenPrefix and tokenLifetime are those of an EKS bearer token: a presigned
// GetCallerIdentity request that EKS replays to learn who is calling.
const (
	tokenPrefix   = "k8s-aws-v1."
	tokenLifetime = "60"
	tokenHeader   = "x-k8s-aws-id"
)

// Target names the account, role and stack one step works on. The control plane
// assumes the role afresh for every call.
type Target struct {
	RoleARN        string
	ExternalID     string
	SourceIdentity string
	Region         string
	StackName      string
	EKSName        string
}

// StackSpec is what a stack is created from.
type StackSpec struct {
	Template       string
	ServiceRoleARN string
	Parameters     map[string]string
	Tags           map[string]string
}

// StackState is what CloudFormation says of a stack.
type StackState struct {
	Exists  bool
	Status  string
	Outputs map[string]string
	// FailureReason is the first failed resource's raw text, kept so it can be
	// classified. It is never shown to a person.
	FailureReason string
}

// Complete reports whether the stack finished creating or updating.
func (s StackState) Complete() bool {
	return s.Exists && (s.Status == "CREATE_COMPLETE" || s.Status == "UPDATE_COMPLETE")
}

// InProgress reports whether CloudFormation is still working on the stack.
func (s StackState) InProgress() bool {
	return s.Exists && strings.HasSuffix(s.Status, "_IN_PROGRESS") && !strings.HasPrefix(s.Status, "ROLLBACK") &&
		!strings.HasPrefix(s.Status, "DELETE")
}

// Failed reports whether the stack stopped in a state only a retry or a delete leaves.
func (s StackState) Failed() bool {
	return s.Exists && (strings.Contains(s.Status, "FAILED") || strings.HasPrefix(s.Status, "ROLLBACK") ||
		s.Status == "DELETE_COMPLETE" || strings.HasPrefix(s.Status, "DELETE"))
}

// ClusterAccess is what the control plane needs to call an environment's
// Kubernetes API while the Access window is open.
type ClusterAccess struct {
	Endpoint string
	CA       []byte
}

type cfnAPI interface {
	CreateStack(context.Context, *cloudformation.CreateStackInput, ...func(*cloudformation.Options)) (*cloudformation.CreateStackOutput, error)
	DeleteStack(context.Context, *cloudformation.DeleteStackInput, ...func(*cloudformation.Options)) (*cloudformation.DeleteStackOutput, error)
	DescribeStacks(context.Context, *cloudformation.DescribeStacksInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error)
	DescribeStackEvents(context.Context, *cloudformation.DescribeStackEventsInput, ...func(*cloudformation.Options)) (*cloudformation.DescribeStackEventsOutput, error)
}

type eksAPI interface {
	DescribeCluster(context.Context, *eks.DescribeClusterInput, ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) // allow-legacy-name: the AWS SDK names it so
	UpdateClusterConfig(context.Context, *eks.UpdateClusterConfigInput, ...func(*eks.Options)) (*eks.UpdateClusterConfigOutput, error)
	DescribeUpdate(context.Context, *eks.DescribeUpdateInput, ...func(*eks.Options)) (*eks.DescribeUpdateOutput, error)
	CreateAccessEntry(context.Context, *eks.CreateAccessEntryInput, ...func(*eks.Options)) (*eks.CreateAccessEntryOutput, error)
	AssociateAccessPolicy(context.Context, *eks.AssociateAccessPolicyInput, ...func(*eks.Options)) (*eks.AssociateAccessPolicyOutput, error)
	DeleteAccessEntry(context.Context, *eks.DeleteAccessEntryInput, ...func(*eks.Options)) (*eks.DeleteAccessEntryOutput, error)
}

// Infra builds and reads a managed environment's infrastructure in a customer's
// account. It keeps no credential: every call assumes the role again.
type Infra struct {
	assume func(context.Context, Target) (sdkaws.Config, error)
	cfn    func(sdkaws.Config) cfnAPI
	eks    func(sdkaws.Config) eksAPI
	// poll is how often a wait looks again.
	poll time.Duration
}

// NewInfra builds Infra on the control plane's own identity.
func NewInfra(base sdkaws.Config) *Infra {
	return &Infra{
		assume: func(ctx context.Context, t Target) (sdkaws.Config, error) { return assumeRole(ctx, base, t) },
		cfn:    func(cfg sdkaws.Config) cfnAPI { return cloudformation.NewFromConfig(cfg) },
		eks:    func(cfg sdkaws.Config) eksAPI { return eks.NewFromConfig(cfg) },
		poll:   15 * time.Second,
	}
}

// NewDefaultInfra builds Infra from the identity the process runs as.
func NewDefaultInfra(ctx context.Context) (*Infra, error) {
	base, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(defaultRegion("")))
	if err != nil {
		return nil, fmt.Errorf("load the control plane's AWS identity: %w", err)
	}

	return NewInfra(base), nil
}

// assumeRole returns a config that signs as the role, for one step.
func assumeRole(ctx context.Context, base sdkaws.Config, t Target) (sdkaws.Config, error) {
	assumed, err := sts.NewFromConfig(base).AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn: &t.RoleARN, RoleSessionName: sdkaws.String("pds-provisioning"),
		ExternalId: &t.ExternalID, SourceIdentity: &t.SourceIdentity,
		DurationSeconds: sdkaws.Int32(3600),
	})
	if err != nil {
		if errorCode(err) == "AccessDenied" {
			return sdkaws.Config{}, fmt.Errorf("%w: %v", ErrAssumeDenied, err)
		}

		return sdkaws.Config{}, fmt.Errorf("assume role: %w", err)
	}
	session := base.Copy()
	session.Region = t.Region
	creds := assumed.Credentials
	session.Credentials = credentials.NewStaticCredentialsProvider(
		sdkaws.ToString(creds.AccessKeyId), sdkaws.ToString(creds.SecretAccessKey), sdkaws.ToString(creds.SessionToken))

	return session, nil
}

// CheckRole assumes the role and reports ErrAssumeDenied when AWS refuses.
func (i *Infra) CheckRole(ctx context.Context, t Target) error {
	_, err := i.assume(ctx, t)

	return err
}

// EnsureStack creates the stack through the service role. A stack that exists
// is left as it is: the step then reads its state.
func (i *Infra) EnsureStack(ctx context.Context, t Target, spec StackSpec) error {
	session, err := i.assume(ctx, t)
	if err != nil {
		return err
	}
	in := &cloudformation.CreateStackInput{
		StackName: &t.StackName, TemplateBody: &spec.Template, RoleARN: &spec.ServiceRoleARN,
		OnFailure:                   cfntypes.OnFailureDoNothing,
		EnableTerminationProtection: sdkaws.Bool(false),
	}
	for _, key := range sortedKeys(spec.Parameters) {
		in.Parameters = append(in.Parameters, cfntypes.Parameter{ParameterKey: sdkaws.String(key), ParameterValue: sdkaws.String(spec.Parameters[key])})
	}
	for _, key := range sortedKeys(spec.Tags) {
		in.Tags = append(in.Tags, cfntypes.Tag{Key: sdkaws.String(key), Value: sdkaws.String(spec.Tags[key])})
	}
	if _, err := i.cfn(session).CreateStack(ctx, in); err != nil && errorCode(err) != "AlreadyExistsException" {
		return fmt.Errorf("create stack: %w", err)
	}

	return nil
}

// DeleteStack asks CloudFormation to delete the stack. It does so through the
// role the stack was made with, and the caller reads the stack until it is gone.
func (i *Infra) DeleteStack(ctx context.Context, t Target) error {
	session, err := i.assume(ctx, t)
	if err != nil {
		return err
	}
	if _, err := i.cfn(session).DeleteStack(ctx, &cloudformation.DeleteStackInput{StackName: &t.StackName}); err != nil {
		return fmt.Errorf("delete stack: %w", err)
	}

	return nil
}

// Stack reads the stack's state, and for a failed one the first resource that failed.
func (i *Infra) Stack(ctx context.Context, t Target) (StackState, error) {
	session, err := i.assume(ctx, t)
	if err != nil {
		return StackState{}, err
	}
	client := i.cfn(session)
	described, err := client.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: &t.StackName})
	if err != nil {
		if stackDoesNotExist(err) {
			return StackState{}, nil
		}

		return StackState{}, fmt.Errorf("describe stack: %w", err)
	}
	if len(described.Stacks) == 0 {
		return StackState{}, nil
	}
	stack := described.Stacks[0]
	out := StackState{Exists: true, Status: string(stack.StackStatus), Outputs: map[string]string{}}
	for _, o := range stack.Outputs {
		out.Outputs[sdkaws.ToString(o.OutputKey)] = sdkaws.ToString(o.OutputValue)
	}
	if out.Failed() {
		events, err := client.DescribeStackEvents(ctx, &cloudformation.DescribeStackEventsInput{StackName: &t.StackName})
		if err != nil {
			return StackState{}, fmt.Errorf("describe stack events: %w", err)
		}
		out.FailureReason = firstFailure(events.StackEvents)
	}

	return out, nil
}

// firstFailure returns the oldest failed resource's reason. CloudFormation lists
// events newest first, and later failures are only the rollback cancelling the rest.
func firstFailure(events []cfntypes.StackEvent) string {
	for _, e := range slices.Backward(events) {
		if strings.HasSuffix(string(e.ResourceStatus), "_FAILED") && e.ResourceStatusReason != nil {
			return *e.ResourceStatusReason
		}
	}

	return ""
}

// OpenAccess opens the Access window: the control plane's access entry with
// cluster-admin rights, and a public endpoint limited to the given addresses.
// Every part is idempotent.
func (i *Infra) OpenAccess(ctx context.Context, t Target, cidrs []string) (ClusterAccess, error) {
	session, err := i.assume(ctx, t)
	if err != nil {
		return ClusterAccess{}, err
	}
	client := i.eks(session)

	if _, err := client.CreateAccessEntry(ctx, &eks.CreateAccessEntryInput{
		ClusterName:  &t.EKSName, // allow-legacy-name: the AWS SDK names it so
		PrincipalArn: &t.RoleARN, Type: sdkaws.String("STANDARD"),
	}); err != nil && errorCode(err) != "ResourceInUseException" {
		return ClusterAccess{}, fmt.Errorf("create access entry: %w", err)
	}
	if _, err := client.AssociateAccessPolicy(ctx, &eks.AssociateAccessPolicyInput{
		ClusterName:  &t.EKSName, // allow-legacy-name: the AWS SDK names it so
		PrincipalArn: &t.RoleARN, PolicyArn: sdkaws.String(adminPolicy),
		AccessScope: &ekstypes.AccessScope{Type: ekstypes.AccessScopeTypeCluster}, // allow-legacy-name: the AWS SDK names it so
	}); err != nil {
		return ClusterAccess{}, fmt.Errorf("associate access policy: %w", err)
	}

	cluster, err := describe(ctx, client, t.EKSName)
	if err != nil {
		return ClusterAccess{}, err
	}
	if !openTo(cluster, cidrs) {
		if err := i.setPublic(ctx, client, t.EKSName, true, cidrs); err != nil {
			return ClusterAccess{}, err
		}
		if cluster, err = describe(ctx, client, t.EKSName); err != nil {
			return ClusterAccess{}, err
		}
	}

	ca, err := base64.StdEncoding.DecodeString(sdkaws.ToString(cluster.CertificateAuthority.Data))
	if err != nil {
		return ClusterAccess{}, fmt.Errorf("read the cluster certificate: %w", err)
	}

	return ClusterAccess{Endpoint: sdkaws.ToString(cluster.Endpoint), CA: ca}, nil
}

// CloseAccess closes the Access window. It reads the real endpoint first, so a
// window it did not know was open is closed too, and returns what it did.
func (i *Infra) CloseAccess(ctx context.Context, t Target) (string, error) {
	session, err := i.assume(ctx, t)
	if err != nil {
		return "", err
	}
	client := i.eks(session)

	cluster, err := describe(ctx, client, t.EKSName)
	if err != nil {
		if errorCode(err) == "ResourceNotFoundException" {
			return "The cluster no longer exists, so there was nothing to close.", nil
		}

		return "", err
	}
	var did []string
	if cluster.ResourcesVpcConfig != nil && cluster.ResourcesVpcConfig.EndpointPublicAccess {
		if err := i.setPublic(ctx, client, t.EKSName, false, nil); err != nil {
			return "", err
		}
		did = append(did, "closed the public endpoint")
	} else {
		did = append(did, "the endpoint was already private")
	}
	if _, err := client.DeleteAccessEntry(ctx, &eks.DeleteAccessEntryInput{
		ClusterName:  &t.EKSName, // allow-legacy-name: the AWS SDK names it so
		PrincipalArn: &t.RoleARN,
	}); err != nil {
		if errorCode(err) != "ResourceNotFoundException" {
			return "", fmt.Errorf("delete access entry: %w", err)
		}
	} else {
		did = append(did, "removed the access entry")
	}

	return strings.ToUpper(did[0][:1]) + strings.Join(did, " and ")[1:] + ".", nil
}

func describe(ctx context.Context, client eksAPI, name string) (*ekstypes.Cluster, error) {
	out, err := client.DescribeCluster(ctx, &eks.DescribeClusterInput{Name: &name}) // allow-legacy-name: the AWS SDK names it so
	if err != nil {
		return nil, fmt.Errorf("describe cluster: %w", err)
	}

	return out.Cluster, nil
}

func openTo(cluster *ekstypes.Cluster, cidrs []string) bool {
	vpc := cluster.ResourcesVpcConfig
	if vpc == nil || !vpc.EndpointPublicAccess {
		return false
	}
	have, want := slices.Clone(vpc.PublicAccessCidrs), slices.Clone(cidrs)
	slices.Sort(have)
	slices.Sort(want)

	return slices.Equal(have, want)
}

// setPublic changes the endpoint and waits for EKS to finish the update.
func (i *Infra) setPublic(ctx context.Context, client eksAPI, cluster string, public bool, cidrs []string) error {
	vpc := &ekstypes.VpcConfigRequest{EndpointPublicAccess: sdkaws.Bool(public), EndpointPrivateAccess: sdkaws.Bool(true)}
	if public {
		vpc.PublicAccessCidrs = cidrs
	}
	out, err := client.UpdateClusterConfig(ctx, &eks.UpdateClusterConfigInput{Name: &cluster, ResourcesVpcConfig: vpc})
	if err != nil {
		return fmt.Errorf("update cluster endpoint: %w", err)
	}

	for {
		status, err := client.DescribeUpdate(ctx, &eks.DescribeUpdateInput{Name: &cluster, UpdateId: out.Update.Id})
		if err != nil {
			return fmt.Errorf("describe cluster update: %w", err)
		}
		switch status.Update.Status {
		case ekstypes.UpdateStatusSuccessful:
			return nil
		case ekstypes.UpdateStatusFailed, ekstypes.UpdateStatusCancelled:
			return errors.New("the cluster endpoint update did not finish")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(i.poll):
		}
	}
}

// KubeToken returns a bearer token for the cluster's Kubernetes API, signed as
// the assumed role. It lasts a minute, so every call asks for one.
func (i *Infra) KubeToken(ctx context.Context, t Target) (string, error) {
	session, err := i.assume(ctx, t)
	if err != nil {
		return "", err
	}
	presigned, err := sts.NewPresignClient(sts.NewFromConfig(session)).PresignGetCallerIdentity(ctx, &sts.GetCallerIdentityInput{},
		func(o *sts.PresignOptions) {
			o.ClientOptions = append(o.ClientOptions, func(c *sts.Options) {
				c.APIOptions = append(c.APIOptions,
					smithyhttp.SetHeaderValue(tokenHeader, t.EKSName),
					func(stack *middleware.Stack) error {
						return stack.Build.Add(middleware.BuildMiddlewareFunc("pdsTokenLifetime",
							func(ctx context.Context, in middleware.BuildInput, next middleware.BuildHandler) (middleware.BuildOutput, middleware.Metadata, error) {
								if req, ok := in.Request.(*smithyhttp.Request); ok {
									query := url.Values{}
									query.Set("X-Amz-Expires", tokenLifetime)
									req.URL.RawQuery = query.Encode() + "&" + req.URL.RawQuery
								}

								return next.HandleBuild(ctx, in)
							}), middleware.After)
					})
			})
		})
	if err != nil {
		return "", fmt.Errorf("sign the cluster token: %w", err)
	}

	return tokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(presigned.URL)), nil
}

func stackDoesNotExist(err error) bool {
	var coded interface{ ErrorCode() string }
	if errors.As(err, &coded) && coded.ErrorCode() == "ValidationError" {
		return strings.Contains(err.Error(), "does not exist")
	}

	return false
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)

	return keys
}
