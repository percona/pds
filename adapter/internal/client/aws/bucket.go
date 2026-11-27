package aws

import (
	"context"
	"fmt"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type bucketAPI interface {
	CreateBucket(context.Context, *s3.CreateBucketInput, ...func(*s3.Options)) (*s3.CreateBucketOutput, error)
	PutPublicAccessBlock(context.Context, *s3.PutPublicAccessBlockInput, ...func(*s3.Options)) (*s3.PutPublicAccessBlockOutput, error)
	PutBucketTagging(context.Context, *s3.PutBucketTaggingInput, ...func(*s3.Options)) (*s3.PutBucketTaggingOutput, error)
}

// EnsureBucket creates the environment's backup bucket in the Region, blocks
// public access and tags it. A bucket the account already owns is kept, and
// the settings are applied again. The bucket is outside the stack because a
// stack cannot delete a bucket that holds objects.
func (i *Infra) EnsureBucket(ctx context.Context, t Target, bucket string, tags map[string]string) error {
	session, err := i.assume(ctx, t)
	if err != nil {
		return err
	}
	client := i.buckets(session)

	in := &s3.CreateBucketInput{Bucket: &bucket}
	if t.Region != "us-east-1" {
		in.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{LocationConstraint: s3types.BucketLocationConstraint(t.Region)}
	}
	if _, err := client.CreateBucket(ctx, in); err != nil && errorCode(err) != "BucketAlreadyOwnedByYou" {
		return fmt.Errorf("create bucket: %w", err)
	}

	if _, err := client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: &bucket,
		PublicAccessBlockConfiguration: &s3types.PublicAccessBlockConfiguration{
			BlockPublicAcls: sdkaws.Bool(true), IgnorePublicAcls: sdkaws.Bool(true),
			BlockPublicPolicy: sdkaws.Bool(true), RestrictPublicBuckets: sdkaws.Bool(true),
		},
	}); err != nil {
		return fmt.Errorf("block public access to the bucket: %w", err)
	}

	set := make([]s3types.Tag, 0, len(tags))
	for _, key := range sortedKeys(tags) {
		set = append(set, s3types.Tag{Key: sdkaws.String(key), Value: sdkaws.String(tags[key])})
	}
	if len(set) == 0 {
		return nil
	}
	if _, err := client.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{Bucket: &bucket, Tagging: &s3types.Tagging{TagSet: set}}); err != nil {
		return fmt.Errorf("tag the bucket: %w", err)
	}

	return nil
}
