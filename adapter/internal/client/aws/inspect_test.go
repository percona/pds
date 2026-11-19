package aws_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/aws"
)

// fakeAWS answers the four calls an inspection makes, in the query protocol
// STS, CloudFormation and IAM share, and keeps what it was asked.
type fakeAWS struct {
	mu       sync.Mutex
	calls    []recorded
	assume   func(w http.ResponseWriter)
	stack    func(w http.ResponseWriter)
	missing  map[string]bool
	identity string
}

type recorded struct {
	action string
	form   map[string]string
	auth   string
}

func (f *fakeAWS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}
	call := recorded{action: r.Form.Get("Action"), form: map[string]string{}, auth: r.Header.Get("Authorization")}
	for key := range r.Form {
		call.form[key] = r.Form.Get(key)
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()

	w.Header().Set("Content-Type", "text/xml")
	switch call.action {
	case "AssumeRole":
		f.assume(w)
	case "GetCallerIdentity":
		fmt.Fprintf(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Arn>arn:aws:sts::%[1]s:assumed-role/r/s</Arn><UserId>U</UserId><Account>%[1]s</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`, f.identity)
	case "DescribeStacks":
		f.stack(w)
	case "GetRole":
		name := call.form["RoleName"]
		if f.missing[name] {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `<ErrorResponse><Error><Type>Sender</Type><Code>NoSuchEntity</Code><Message>no</Message></Error><RequestId>1</RequestId></ErrorResponse>`)

			return
		}
		fmt.Fprintf(w, `<GetRoleResponse xmlns="https://iam.amazonaws.com/doc/2010-05-08/"><GetRoleResult><Role><Path>/</Path><RoleName>%s</RoleName><RoleId>R</RoleId><Arn>arn:aws:iam::1:role/%s</Arn><CreateDate>2026-11-19T12:00:00Z</CreateDate></Role></GetRoleResult></GetRoleResponse>`, name, name)
	default:
		http.Error(w, "unexpected "+call.action, http.StatusBadRequest)
	}
}

func assumeOK(w http.ResponseWriter) {
	fmt.Fprint(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials><AccessKeyId>ASIATEMP</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>token</SessionToken><Expiration>2030-01-01T00:00:00Z</Expiration></Credentials></AssumeRoleResult></AssumeRoleResponse>`)
}

func stackOK(status, version string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		fmt.Fprintf(w, `<DescribeStacksResponse xmlns="http://cloudformation.amazonaws.com/doc/2010-05-15/"><DescribeStacksResult><Stacks><member><StackName>pds-abc</StackName><StackStatus>%s</StackStatus><CreationTime>2026-11-19T12:00:00Z</CreationTime><Outputs><member><OutputKey>TemplateVersion</OutputKey><OutputValue>%s</OutputValue></member></Outputs></member></Stacks></DescribeStacksResult></DescribeStacksResponse>`, status, version)
	}
}

func newInspector(t *testing.T, fake *fakeAWS) *aws.Inspector {
	t.Helper()

	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	return aws.NewInspector(sdkaws.Config{
		Region:       "eu-west-1",
		Credentials:  credentials.NewStaticCredentialsProvider("ASIAHUB", "hub-secret", ""),
		BaseEndpoint: sdkaws.String(server.URL),
	})
}

func request() aws.InspectRequest {
	return aws.InspectRequest{
		RoleARN: "arn:aws:iam::123456789012:role/pds/abc/pds-abc-control-plane", ExternalID: "ext", SourceIdentity: "pds-user-42",
		Region: "eu-west-1", StackName: "pds-abc", RoleNames: []string{"pds-abc-node", "pds-abc-ebs-csi"},
	}
}

func TestInspectAssumesTheRoleWithTheSecretAndWhoActedThenReadsTheAccountAsIt(t *testing.T) {
	fake := &fakeAWS{assume: assumeOK, stack: stackOK("CREATE_COMPLETE", "3"), identity: "123456789012"}

	got, err := newInspector(t, fake).Inspect(t.Context(), request())
	require.NoError(t, err)

	assert.Equal(t, aws.Inspection{AccountID: "123456789012", StackStatus: "CREATE_COMPLETE", TemplateVersion: 3}, got)
	assume := fake.calls[0]
	assert.Equal(t, "AssumeRole", assume.action)
	assert.Equal(t, "arn:aws:iam::123456789012:role/pds/abc/pds-abc-control-plane", assume.form["RoleArn"])
	assert.Equal(t, "ext", assume.form["ExternalId"])
	assert.Equal(t, "pds-user-42", assume.form["SourceIdentity"])
	assert.Contains(t, assume.auth, "ASIAHUB", "the hub's own identity asks to assume")
	for _, call := range fake.calls[1:] {
		assert.Contains(t, call.auth, "ASIATEMP", "%s runs as the assumed role", call.action)
	}
}

func TestInspectReportsRolesThatAreMissingAndNotOnesThatAreThere(t *testing.T) {
	fake := &fakeAWS{assume: assumeOK, stack: stackOK("UPDATE_COMPLETE", "1"), identity: "123456789012", missing: map[string]bool{"pds-abc-ebs-csi": true}}

	got, err := newInspector(t, fake).Inspect(t.Context(), request())
	require.NoError(t, err)

	assert.Equal(t, []string{"pds-abc-ebs-csi"}, got.MissingRoles)
}

func TestInspectSaysTheRoleWasRefusedOnlyWhenAWSDeniedIt(t *testing.T) {
	denied := &fakeAWS{assume: func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>not authorized</Message></Error><RequestId>1</RequestId></ErrorResponse>`)
	}}
	_, err := newInspector(t, denied).Inspect(t.Context(), request())
	require.ErrorIs(t, err, aws.ErrAssumeDenied)

	broken := &fakeAWS{assume: func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `<ErrorResponse><Error><Type>Sender</Type><Code>ValidationError</Code><Message>bad</Message></Error><RequestId>1</RequestId></ErrorResponse>`)
	}}
	_, err = newInspector(t, broken).Inspect(t.Context(), request())
	require.Error(t, err)
	assert.NotErrorIs(t, err, aws.ErrAssumeDenied)
}

func TestInspectReadsAMissingStackAsAStatusNotAFailure(t *testing.T) {
	fake := &fakeAWS{assume: assumeOK, identity: "123456789012", stack: func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `<ErrorResponse><Error><Type>Sender</Type><Code>ValidationError</Code><Message>Stack with id pds-abc does not exist</Message></Error><RequestId>1</RequestId></ErrorResponse>`)
	}}

	got, err := newInspector(t, fake).Inspect(t.Context(), request())
	require.NoError(t, err)

	assert.Equal(t, "DOES_NOT_EXIST", got.StackStatus)
}
