package backupkey

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var spec = Spec{
	Name:           "pds-k3j9x2m4p1q7-backup",
	Path:           "/pds/aaaaaaaaaaaa/",
	BoundaryARN:    "arn:aws:iam::123456789012:policy/pds/aaaaaaaaaaaa/pds-aaaaaaaaaaaa-backup-boundary",
	Bucket:         "pds-123456789012-k3j9x2m4p1q7",
	EnvironmentTag: "k3j9x2m4p1q7",
}

// fakeIAM is one account's users, keys and inline policies.
type fakeIAM struct {
	users    map[string]*iam.CreateUserInput
	tags     map[string]map[string]string
	policies map[string]map[string]string
	keys     map[string][]string
	minted   int
	calls    []string
}

func newFakeIAM() *fakeIAM {
	return &fakeIAM{
		users: map[string]*iam.CreateUserInput{}, tags: map[string]map[string]string{},
		policies: map[string]map[string]string{}, keys: map[string][]string{},
	}
}

func notFound() error { return &iamtypes.NoSuchEntityException{} }

func (f *fakeIAM) CreateUser(_ context.Context, in *iam.CreateUserInput, _ ...func(*iam.Options)) (*iam.CreateUserOutput, error) {
	f.calls = append(f.calls, "CreateUser")
	name := sdkaws.ToString(in.UserName)
	if _, ok := f.users[name]; ok {
		return nil, &iamtypes.EntityAlreadyExistsException{}
	}
	f.users[name] = in
	f.tags[name] = map[string]string{}
	for _, tag := range in.Tags {
		f.tags[name][sdkaws.ToString(tag.Key)] = sdkaws.ToString(tag.Value)
	}

	return &iam.CreateUserOutput{}, nil
}

func (f *fakeIAM) TagUser(_ context.Context, in *iam.TagUserInput, _ ...func(*iam.Options)) (*iam.TagUserOutput, error) {
	f.calls = append(f.calls, "TagUser")
	name := sdkaws.ToString(in.UserName)
	if _, ok := f.users[name]; !ok {
		return nil, notFound()
	}
	for _, tag := range in.Tags {
		f.tags[name][sdkaws.ToString(tag.Key)] = sdkaws.ToString(tag.Value)
	}

	return &iam.TagUserOutput{}, nil
}

func (f *fakeIAM) PutUserPolicy(_ context.Context, in *iam.PutUserPolicyInput, _ ...func(*iam.Options)) (*iam.PutUserPolicyOutput, error) {
	f.calls = append(f.calls, "PutUserPolicy")
	name := sdkaws.ToString(in.UserName)
	if _, ok := f.users[name]; !ok {
		return nil, notFound()
	}
	if f.policies[name] == nil {
		f.policies[name] = map[string]string{}
	}
	f.policies[name][sdkaws.ToString(in.PolicyName)] = sdkaws.ToString(in.PolicyDocument)

	return &iam.PutUserPolicyOutput{}, nil
}

func (f *fakeIAM) ListAccessKeys(_ context.Context, in *iam.ListAccessKeysInput, _ ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error) {
	f.calls = append(f.calls, "ListAccessKeys")
	name := sdkaws.ToString(in.UserName)
	if _, ok := f.users[name]; !ok {
		return nil, notFound()
	}
	out := &iam.ListAccessKeysOutput{}
	for _, id := range f.keys[name] {
		out.AccessKeyMetadata = append(out.AccessKeyMetadata, iamtypes.AccessKeyMetadata{AccessKeyId: sdkaws.String(id)})
	}

	return out, nil
}

func (f *fakeIAM) CreateAccessKey(_ context.Context, in *iam.CreateAccessKeyInput, _ ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error) {
	f.calls = append(f.calls, "CreateAccessKey")
	name := sdkaws.ToString(in.UserName)
	if _, ok := f.users[name]; !ok {
		return nil, notFound()
	}
	if len(f.keys[name]) >= 2 {
		return nil, &iamtypes.LimitExceededException{}
	}
	f.minted++
	id := "AKIAFAKE" + string(rune('A'+f.minted))
	f.keys[name] = append(f.keys[name], id)

	return &iam.CreateAccessKeyOutput{AccessKey: &iamtypes.AccessKey{
		AccessKeyId: sdkaws.String(id), SecretAccessKey: sdkaws.String("secret-of-" + id),
	}}, nil
}

func (f *fakeIAM) DeleteAccessKey(_ context.Context, in *iam.DeleteAccessKeyInput, _ ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error) {
	f.calls = append(f.calls, "DeleteAccessKey")
	name := sdkaws.ToString(in.UserName)
	kept := f.keys[name][:0:0]
	for _, id := range f.keys[name] {
		if id != sdkaws.ToString(in.AccessKeyId) {
			kept = append(kept, id)
		}
	}
	f.keys[name] = kept

	return &iam.DeleteAccessKeyOutput{}, nil
}

func (f *fakeIAM) ListUserPolicies(_ context.Context, in *iam.ListUserPoliciesInput, _ ...func(*iam.Options)) (*iam.ListUserPoliciesOutput, error) {
	f.calls = append(f.calls, "ListUserPolicies")
	name := sdkaws.ToString(in.UserName)
	if _, ok := f.users[name]; !ok {
		return nil, notFound()
	}
	out := &iam.ListUserPoliciesOutput{}
	for policy := range f.policies[name] {
		out.PolicyNames = append(out.PolicyNames, policy)
	}

	return out, nil
}

func (f *fakeIAM) DeleteUserPolicy(_ context.Context, in *iam.DeleteUserPolicyInput, _ ...func(*iam.Options)) (*iam.DeleteUserPolicyOutput, error) {
	f.calls = append(f.calls, "DeleteUserPolicy")
	delete(f.policies[sdkaws.ToString(in.UserName)], sdkaws.ToString(in.PolicyName))

	return &iam.DeleteUserPolicyOutput{}, nil
}

func (f *fakeIAM) DeleteUser(_ context.Context, in *iam.DeleteUserInput, _ ...func(*iam.Options)) (*iam.DeleteUserOutput, error) {
	f.calls = append(f.calls, "DeleteUser")
	name := sdkaws.ToString(in.UserName)
	if _, ok := f.users[name]; !ok {
		return nil, notFound()
	}
	if len(f.keys[name]) > 0 || len(f.policies[name]) > 0 {
		return nil, &iamtypes.DeleteConflictException{}
	}
	delete(f.users, name)

	return &iam.DeleteUserOutput{}, nil
}

func TestEnsureMakesAUserUnderThePathWithTheBoundaryAndTheOwnershipTag(t *testing.T) {
	f := newFakeIAM()

	require.NoError(t, New(f).Ensure(t.Context(), spec))

	created := f.users[spec.Name]
	require.NotNil(t, created)
	assert.Equal(t, spec.Path, sdkaws.ToString(created.Path))
	assert.Equal(t, spec.BoundaryARN, sdkaws.ToString(created.PermissionsBoundary), "the boundary caps what the agent can grant the user")
	assert.Equal(t, spec.EnvironmentTag, f.tags[spec.Name]["percona.com/pds"])
}

func TestEnsureLetsTheUserReachItsOwnBucketAndNothingElse(t *testing.T) {
	f := newFakeIAM()
	require.NoError(t, New(f).Ensure(t.Context(), spec))

	var doc struct {
		Statement []struct {
			Effect   string
			Action   []string
			Resource []string
		}
	}
	require.NoError(t, json.Unmarshal([]byte(f.policies[spec.Name]["pds-backup-storage"]), &doc))
	require.Len(t, doc.Statement, 1)
	assert.Equal(t, "Allow", doc.Statement[0].Effect)
	assert.ElementsMatch(t, []string{"arn:aws:s3:::" + spec.Bucket, "arn:aws:s3:::" + spec.Bucket + "/*"}, doc.Statement[0].Resource)
	for _, action := range doc.Statement[0].Action {
		assert.Regexp(t, `^s3:`, action)
	}
}

func TestEnsureTwiceKeepsTheUserAndItsPolicy(t *testing.T) {
	f := newFakeIAM()
	b := New(f)

	require.NoError(t, b.Ensure(t.Context(), spec))
	require.NoError(t, b.Ensure(t.Context(), spec))

	assert.Len(t, f.users, 1)
	assert.Len(t, f.policies[spec.Name], 1)
}

func TestNewKeyReplacesTheKeysTheUserHad(t *testing.T) {
	f := newFakeIAM()
	b := New(f)
	require.NoError(t, b.Ensure(t.Context(), spec))
	f.keys[spec.Name] = []string{"AKIAOLD1", "AKIAOLD2"}

	key, err := b.NewKey(t.Context(), spec)

	require.NoError(t, err, "a user holds two keys at most, so the old ones go first")
	assert.Equal(t, []string{key.ID}, f.keys[spec.Name])
	assert.Equal(t, "secret-of-"+key.ID, key.Secret)
}

func TestRemoveDeletesTheKeysThePoliciesAndTheUser(t *testing.T) {
	f := newFakeIAM()
	b := New(f)
	require.NoError(t, b.Ensure(t.Context(), spec))
	_, err := b.NewKey(t.Context(), spec)
	require.NoError(t, err)

	require.NoError(t, b.Remove(t.Context(), spec))

	assert.Empty(t, f.users)
}

func TestRemovingAUserThatIsGoneIsDone(t *testing.T) {
	require.NoError(t, New(newFakeIAM()).Remove(t.Context(), spec))
}

func TestAnIAMRefusalIsReturnedWithItsCause(t *testing.T) {
	denied := errors.New("AccessDenied")

	err := New(refusing{denied}).Ensure(t.Context(), spec)

	require.ErrorIs(t, err, denied)
}

// refusing answers every call with the same error.
type refusing struct{ err error }

func (r refusing) CreateUser(context.Context, *iam.CreateUserInput, ...func(*iam.Options)) (*iam.CreateUserOutput, error) {
	return nil, r.err
}

func (r refusing) TagUser(context.Context, *iam.TagUserInput, ...func(*iam.Options)) (*iam.TagUserOutput, error) {
	return nil, r.err
}

func (r refusing) PutUserPolicy(context.Context, *iam.PutUserPolicyInput, ...func(*iam.Options)) (*iam.PutUserPolicyOutput, error) {
	return nil, r.err
}

func (r refusing) ListAccessKeys(context.Context, *iam.ListAccessKeysInput, ...func(*iam.Options)) (*iam.ListAccessKeysOutput, error) {
	return nil, r.err
}

func (r refusing) CreateAccessKey(context.Context, *iam.CreateAccessKeyInput, ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error) {
	return nil, r.err
}

func (r refusing) DeleteAccessKey(context.Context, *iam.DeleteAccessKeyInput, ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error) {
	return nil, r.err
}

func (r refusing) ListUserPolicies(context.Context, *iam.ListUserPoliciesInput, ...func(*iam.Options)) (*iam.ListUserPoliciesOutput, error) {
	return nil, r.err
}

func (r refusing) DeleteUserPolicy(context.Context, *iam.DeleteUserPolicyInput, ...func(*iam.Options)) (*iam.DeleteUserPolicyOutput, error) {
	return nil, r.err
}

func (r refusing) DeleteUser(context.Context, *iam.DeleteUserInput, ...func(*iam.Options)) (*iam.DeleteUserOutput, error) {
	return nil, r.err
}
