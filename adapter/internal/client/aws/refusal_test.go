package aws_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/aws"
)

func apiError(code, message string) error {
	return &smithy.GenericAPIError{Code: code, Message: message}
}

func TestClassifyTypesTheRefusalsAPersonCanFix(t *testing.T) {
	where := aws.Where{Account: "111122223333", Region: "eu-west-1", Cluster: "orders"}

	tests := []struct {
		name    string
		err     error
		granted bool
		reason  aws.Reason
		service string
		item    string
	}{
		{"vpc quota from the SDK", apiError("VpcLimitExceeded", "The maximum number of VPCs has been reached."), false, aws.ReasonQuota, "Amazon VPC", "VPCs per Region"},
		{"quota carried in a stack event", errors.New("The maximum number of addresses has been reached. (Service: AmazonEC2; Status Code: 400; Error Code: AddressLimitExceeded; Request ID: abc)"), false, aws.ReasonQuota, "Amazon EC2", "Elastic IP addresses per Region"},
		{"service quotas error", apiError("ServiceQuotaExceededException", "quota"), false, aws.ReasonQuota, "", ""},
		{"permission denied for a granted action", apiError("UnauthorizedOperation", "You are not authorized to perform: ec2:CreateVpc on resource"), true, aws.ReasonPermission, "ec2", "ec2:CreateVpc"},
		{"permission denied by the account", apiError("AccessDenied", "User: arn:aws:sts::1:assumed-role/x is not authorized to perform: eks:CreateCluster with an explicit deny in a service control policy"), false, aws.ReasonPermission, "eks", "eks:CreateCluster"},
		{"region not enabled", apiError("OptInRequired", "You are not subscribed to this service."), false, aws.ReasonRegionNotEnabled, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			where := where
			where.Granted = func(string) bool { return tt.granted }
			got, ok := aws.Classify(tt.err, where)
			require.True(t, ok)
			assert.Equal(t, tt.reason, got.Reason)
			assert.Equal(t, tt.service, got.Service)
			assert.Equal(t, tt.item, got.Item)
			assert.Equal(t, "eu-west-1", got.Region)
			assert.Contains(t, got.Sentence(where), "111122223333")
			assert.NotContains(t, got.Sentence(where), "arn:", "a raw principal never reaches a person")
		})
	}
}

func TestSentenceSendsAGrantedPermissionToChangedOutsidePDS(t *testing.T) {
	where := aws.Where{Account: "111122223333", Region: "eu-west-1", Cluster: "orders", Granted: func(string) bool { return true }}
	got, ok := aws.Classify(apiError("AccessDenied", "not authorized to perform: ec2:CreateVpc"), where)
	require.True(t, ok)
	assert.Contains(t, got.Sentence(where), "Changed outside PDS")

	where.Granted = func(string) bool { return false }
	assert.NotContains(t, got.Sentence(where), "Changed outside PDS")
	assert.Contains(t, got.Sentence(where), "policy")
}

func TestClassifyLeavesWhatItDoesNotKnow(t *testing.T) {
	_, ok := aws.Classify(errors.New("connection reset by peer"), aws.Where{})
	assert.False(t, ok)

	_, ok = aws.Classify(apiError("InternalFailure", "oops"), aws.Where{})
	assert.False(t, ok)
}

func TestTransientCoversThrottlingAndRolePropagation(t *testing.T) {
	assert.True(t, aws.Transient(apiError("Throttling", "Rate exceeded")))
	assert.True(t, aws.Transient(apiError("RequestLimitExceeded", "slow down")))
	assert.True(t, aws.Transient(fmt.Errorf("wrapped: %w", apiError("ThrottlingException", "x"))))
	assert.True(t, aws.Transient(apiError("InvalidParameterException", "Role arn:aws:iam::1:role/pds could not be assumed because it does not exist or the trust policy does not allow it")))
	assert.False(t, aws.Transient(apiError("VpcLimitExceeded", "x")))
	assert.False(t, aws.Transient(errors.New("boom")))
}

func TestConnectionRolesHaveDeterministicARNs(t *testing.T) {
	assert.Equal(t, "pds-abc123abc123-node", aws.RoleName("abc123abc123", "node"))
	assert.Equal(t, "arn:aws:iam::111122223333:role/pds/abc123abc123/pds-abc123abc123-control-plane",
		aws.RoleARN("111122223333", "abc123abc123", "control-plane"))
}
