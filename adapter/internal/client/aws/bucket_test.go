package aws

import (
	"context"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeBuckets struct {
	created   []*s3.CreateBucketInput
	createErr error
	blocked   []*s3.PutPublicAccessBlockInput
	tagged    []*s3.PutBucketTaggingInput
}

func (f *fakeBuckets) CreateBucket(_ context.Context, in *s3.CreateBucketInput, _ ...func(*s3.Options)) (*s3.CreateBucketOutput, error) {
	f.created = append(f.created, in)

	return &s3.CreateBucketOutput{}, f.createErr
}

func (f *fakeBuckets) PutPublicAccessBlock(_ context.Context, in *s3.PutPublicAccessBlockInput, _ ...func(*s3.Options)) (*s3.PutPublicAccessBlockOutput, error) {
	f.blocked = append(f.blocked, in)

	return &s3.PutPublicAccessBlockOutput{}, nil
}

func (f *fakeBuckets) PutBucketTagging(_ context.Context, in *s3.PutBucketTaggingInput, _ ...func(*s3.Options)) (*s3.PutBucketTaggingOutput, error) {
	f.tagged = append(f.tagged, in)

	return &s3.PutBucketTaggingOutput{}, nil
}

func bucketInfra(b *fakeBuckets) *Infra {
	infra := newInfra(&fakeCFN{}, &fakeEKS{})
	infra.buckets = func(sdkaws.Config) bucketAPI { return b }

	return infra
}

func TestEnsureBucketCreatesAPrivateTaggedBucketInTheRegion(t *testing.T) {
	buckets := &fakeBuckets{}

	err := bucketInfra(buckets).EnsureBucket(context.Background(), target, "pds-111122223333-0123456789ab", map[string]string{"percona.com/pds": "0123456789ab"})
	require.NoError(t, err)

	require.Len(t, buckets.created, 1)
	assert.Equal(t, "pds-111122223333-0123456789ab", sdkaws.ToString(buckets.created[0].Bucket))
	assert.Equal(t, s3types.BucketLocationConstraint("eu-west-1"), buckets.created[0].CreateBucketConfiguration.LocationConstraint)
	require.Len(t, buckets.blocked, 1)
	assert.True(t, sdkaws.ToBool(buckets.blocked[0].PublicAccessBlockConfiguration.BlockPublicAcls))
	assert.True(t, sdkaws.ToBool(buckets.blocked[0].PublicAccessBlockConfiguration.RestrictPublicBuckets))
	require.Len(t, buckets.tagged, 1)
	assert.Equal(t, []s3types.Tag{{Key: sdkaws.String("percona.com/pds"), Value: sdkaws.String("0123456789ab")}}, buckets.tagged[0].Tagging.TagSet)
}

func TestEnsureBucketInUSEast1SendsNoLocationConstraint(t *testing.T) {
	buckets := &fakeBuckets{}
	in := target
	in.Region = "us-east-1"

	require.NoError(t, bucketInfra(buckets).EnsureBucket(context.Background(), in, "pds-b", nil))

	assert.Nil(t, buckets.created[0].CreateBucketConfiguration)
}

func TestEnsureBucketIsDoneWhenTheAccountOwnsIt(t *testing.T) {
	buckets := &fakeBuckets{createErr: apiErr("BucketAlreadyOwnedByYou")}

	require.NoError(t, bucketInfra(buckets).EnsureBucket(context.Background(), target, "pds-b", nil))

	assert.Len(t, buckets.blocked, 1, "the settings are applied again, in case a first attempt stopped early")
}

func TestEnsureBucketReturnsWhatAWSRefused(t *testing.T) {
	buckets := &fakeBuckets{createErr: apiErr("AccessDenied")}

	err := bucketInfra(buckets).EnsureBucket(context.Background(), target, "pds-b", nil)

	require.Error(t, err)
	_, classified := Classify(err, Where{})
	assert.True(t, classified)
}
