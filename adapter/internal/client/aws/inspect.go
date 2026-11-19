// Package aws reads a customer's AWS account through the role a cloud
// connection created. It assumes the role afresh for every inspection and
// keeps no credential.
package aws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
)

// ErrAssumeDenied means AWS refused to let the control plane assume the role,
// as opposed to AWS being out of reach.
var ErrAssumeDenied = errors.New("the role cannot be assumed")

// stackMissing is the status reported for a stack AWS has no record of, so a
// deleted stack reads as a state rather than a failure to check.
const stackMissing = "DOES_NOT_EXIST"

// sessionSeconds is the shortest session AWS allows. An inspection takes
// seconds, and role chaining caps a session at an hour anyway.
const sessionSeconds = 900

// InspectRequest names what to look at in a customer's account.
type InspectRequest struct {
	RoleARN        string
	ExternalID     string
	SourceIdentity string
	Region         string
	StackName      string
	// RoleNames are the roles the stack makes besides the one assumed.
	RoleNames []string
}

// Inspection is what the account answered.
type Inspection struct {
	AccountID       string
	StackStatus     string
	TemplateVersion int
	// MissingRoles are the RoleNames that do not exist.
	MissingRoles []string
}

// Inspector inspects accounts as the control plane's own AWS identity.
type Inspector struct{ base sdkaws.Config }

// NewInspector builds an inspector. The config carries the control plane's own
// identity, the principal customer roles trust.
func NewInspector(base sdkaws.Config) *Inspector { return &Inspector{base: base} }

// NewDefaultInspector builds an inspector from the identity the process runs
// as: Pod Identity, an instance role or the usual environment variables. The
// region only picks the STS endpoint that asks to assume a role.
func NewDefaultInspector(ctx context.Context) (*Inspector, error) {
	base, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(defaultRegion(os.Getenv("AWS_REGION"))))
	if err != nil {
		return nil, fmt.Errorf("load the control plane's AWS identity: %w", err)
	}

	return NewInspector(base), nil
}

func defaultRegion(set string) string {
	if set != "" {
		return set
	}

	return "us-east-1"
}

// Inspect assumes the role with its ExternalId and SourceIdentity, then reads
// the account, the stack and the roles as that session.
func (i *Inspector) Inspect(ctx context.Context, in InspectRequest) (Inspection, error) {
	assumed, err := sts.NewFromConfig(i.base).AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn: &in.RoleARN, RoleSessionName: sdkaws.String("pds-connection-check"),
		ExternalId: &in.ExternalID, SourceIdentity: &in.SourceIdentity,
		DurationSeconds: sdkaws.Int32(sessionSeconds),
	})
	if err != nil {
		if code := errorCode(err); code == "AccessDenied" {
			return Inspection{}, fmt.Errorf("%w: %v", ErrAssumeDenied, err)
		}

		return Inspection{}, fmt.Errorf("assume role: %w", err)
	}
	creds := assumed.Credentials
	session := i.base.Copy()
	session.Region = in.Region
	session.Credentials = credentials.NewStaticCredentialsProvider(
		sdkaws.ToString(creds.AccessKeyId), sdkaws.ToString(creds.SecretAccessKey), sdkaws.ToString(creds.SessionToken))

	var out Inspection
	identity, err := sts.NewFromConfig(session).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return Inspection{}, fmt.Errorf("get caller identity: %w", err)
	}
	out.AccountID = sdkaws.ToString(identity.Account)

	if out.StackStatus, out.TemplateVersion, err = describeStack(ctx, session, in.StackName); err != nil {
		return Inspection{}, err
	}
	client := iam.NewFromConfig(session)
	for _, name := range in.RoleNames {
		if _, err := client.GetRole(ctx, &iam.GetRoleInput{RoleName: sdkaws.String(name)}); err != nil {
			if errorCode(err) == "NoSuchEntity" {
				out.MissingRoles = append(out.MissingRoles, name)

				continue
			}

			return Inspection{}, fmt.Errorf("get role: %w", err)
		}
	}

	return out, nil
}

func describeStack(ctx context.Context, cfg sdkaws.Config, name string) (string, int, error) {
	described, err := cloudformation.NewFromConfig(cfg).DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: &name})
	if err != nil {
		// CloudFormation answers a stack it has no record of with a plain
		// ValidationError, and only the text tells it from a bad request.
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && apiErr.ErrorCode() == "ValidationError" && strings.Contains(apiErr.ErrorMessage(), "does not exist") {
			return stackMissing, 0, nil
		}

		return "", 0, fmt.Errorf("describe stack: %w", err)
	}
	if len(described.Stacks) == 0 {
		return stackMissing, 0, nil
	}
	stack := described.Stacks[0]
	version := 0
	for _, output := range stack.Outputs {
		if sdkaws.ToString(output.OutputKey) == "TemplateVersion" {
			version, _ = strconv.Atoi(sdkaws.ToString(output.OutputValue))
		}
	}

	return string(stack.StackStatus), version, nil
}

func errorCode(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}

	return ""
}

// RoleName is the name of one of a cloud connection's roles: control-plane,
// cloudformation, eks-cluster, node or ebs-csi. The bootstrap template names
// them the same way.
func RoleName(connectionID, role string) string { return "pds-" + connectionID + "-" + role }

// RoleARN is the deterministic ARN of one of a connection's roles. They all
// sit under the path /pds/<connection id>/.
func RoleARN(account, connectionID, role string) string {
	return "arn:aws:iam::" + account + ":role/pds/" + connectionID + "/" + RoleName(connectionID, role)
}
