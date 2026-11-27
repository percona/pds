// Package backupkey makes and removes the IAM user whose access key an
// environment's backups use. It runs inside the environment, with the
// credentials of the agent's own role, so the key is created where it is kept
// and never passes through the control plane.
package backupkey

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"
)

// policyName is the inline policy that lets the user reach its bucket.
const policyName = "pds-backup-storage"

// Spec names the user an environment's backups write as.
type Spec struct {
	// Name and Path place the user: the role the agent runs as may only touch the
	// user named after its own cluster, under the connection's path.
	Name string
	Path string
	// BoundaryARN caps what the user can ever be granted, so the agent cannot
	// give it more than its own bucket.
	BoundaryARN string
	Bucket      string
	// EnvironmentTag is the value of the percona.com/pds tag.
	EnvironmentTag string
}

// Key is an access key. The secret is shown by IAM once, when it is made.
type Key struct {
	ID     string
	Secret string
}

type iamAPI interface {
	CreateUser(context.Context, *iam.CreateUserInput, ...func(*iam.Options)) (*iam.CreateUserOutput, error)
	TagUser(context.Context, *iam.TagUserInput, ...func(*iam.Options)) (*iam.TagUserOutput, error)
	PutUserPolicy(context.Context, *iam.PutUserPolicyInput, ...func(*iam.Options)) (*iam.PutUserPolicyOutput, error)
	ListAccessKeys(context.Context, *iam.ListAccessKeysInput, ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error)
	CreateAccessKey(context.Context, *iam.CreateAccessKeyInput, ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error)
	DeleteAccessKey(context.Context, *iam.DeleteAccessKeyInput, ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error)
	ListUserPolicies(context.Context, *iam.ListUserPoliciesInput, ...func(*iam.Options)) (*iam.ListUserPoliciesOutput, error)
	DeleteUserPolicy(context.Context, *iam.DeleteUserPolicyInput, ...func(*iam.Options)) (*iam.DeleteUserPolicyOutput, error)
	DeleteUser(context.Context, *iam.DeleteUserInput, ...func(*iam.Options)) (*iam.DeleteUserOutput, error)
}

// Users makes and removes backup users.
type Users struct{ client iamAPI }

// New builds Users over an IAM client.
func New(client iamAPI) *Users { return &Users{client: client} }

// NewDefault builds Users from the credentials the pod holds, which is how an
// EKS Pod Identity role reaches the process.
func NewDefault(ctx context.Context, region string) (*Users, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("load the AWS configuration: %w", err)
	}

	return New(iam.NewFromConfig(cfg)), nil
}

func tags(spec Spec) []iamtypes.Tag {
	return []iamtypes.Tag{
		{Key: sdkaws.String("percona.com/pds"), Value: sdkaws.String(spec.EnvironmentTag)},
		{Key: sdkaws.String("managed-by"), Value: sdkaws.String("pds")},
	}
}

// Ensure makes the user, tags it and lets it reach its bucket. A user that is
// already there is kept and its tags and policy are applied again.
func (u *Users) Ensure(ctx context.Context, spec Spec) error {
	_, err := u.client.CreateUser(ctx, &iam.CreateUserInput{
		UserName: &spec.Name, Path: &spec.Path, PermissionsBoundary: &spec.BoundaryARN, Tags: tags(spec),
	})
	switch {
	case err == nil:
	case code(err) == "EntityAlreadyExists":
		if _, err := u.client.TagUser(ctx, &iam.TagUserInput{UserName: &spec.Name, Tags: tags(spec)}); err != nil {
			return fmt.Errorf("tag the backup user: %w", err)
		}
	default:
		return fmt.Errorf("create the backup user: %w", err)
	}

	document, err := json.Marshal(map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{{
			"Sid":    "PDSBackupStorage",
			"Effect": "Allow",
			"Action": []string{
				"s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:ListBucket", "s3:GetBucketLocation",
				"s3:AbortMultipartUpload", "s3:ListBucketMultipartUploads", "s3:ListMultipartUploadParts",
			},
			"Resource": []string{"arn:aws:s3:::" + spec.Bucket, "arn:aws:s3:::" + spec.Bucket + "/*"},
		}},
	})
	if err != nil {
		return fmt.Errorf("write the backup policy: %w", err)
	}
	if _, err := u.client.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName: &spec.Name, PolicyName: sdkaws.String(policyName), PolicyDocument: sdkaws.String(string(document)),
	}); err != nil {
		return fmt.Errorf("attach the backup policy: %w", err)
	}

	return nil
}

// NewKey makes an access key for the user. The keys it had go first: IAM holds
// two at most, and the secret of one it made earlier cannot be read back.
func (u *Users) NewKey(ctx context.Context, spec Spec) (Key, error) {
	if err := u.deleteKeys(ctx, spec.Name); err != nil {
		return Key{}, err
	}
	made, err := u.client.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: &spec.Name})
	if err != nil {
		return Key{}, fmt.Errorf("create the backup access key: %w", err)
	}
	if made.AccessKey == nil {
		return Key{}, errors.New("create the backup access key: IAM returned no key")
	}

	return Key{ID: sdkaws.ToString(made.AccessKey.AccessKeyId), Secret: sdkaws.ToString(made.AccessKey.SecretAccessKey)}, nil
}

// Remove deletes the user's keys and policies, then the user. A user that is
// already gone is done.
func (u *Users) Remove(ctx context.Context, spec Spec) error {
	if err := u.deleteKeys(ctx, spec.Name); err != nil {
		if code(err) == "NoSuchEntity" {
			return nil
		}

		return err
	}
	listed, err := u.client.ListUserPolicies(ctx, &iam.ListUserPoliciesInput{UserName: &spec.Name})
	if err != nil {
		return fmt.Errorf("list the backup user's policies: %w", err)
	}
	for _, name := range listed.PolicyNames {
		if _, err := u.client.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{UserName: &spec.Name, PolicyName: sdkaws.String(name)}); err != nil {
			return fmt.Errorf("delete the backup user's policy: %w", err)
		}
	}
	if _, err := u.client.DeleteUser(ctx, &iam.DeleteUserInput{UserName: &spec.Name}); err != nil && code(err) != "NoSuchEntity" {
		return fmt.Errorf("delete the backup user: %w", err)
	}

	return nil
}

func (u *Users) deleteKeys(ctx context.Context, user string) error {
	listed, err := u.client.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: &user})
	if err != nil {
		return fmt.Errorf("list the backup user's keys: %w", err)
	}
	for _, key := range listed.AccessKeyMetadata {
		if _, err := u.client.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{UserName: &user, AccessKeyId: key.AccessKeyId}); err != nil {
			return fmt.Errorf("delete a backup access key: %w", err)
		}
	}

	return nil
}

func code(err error) string {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode()
	}

	return ""
}
