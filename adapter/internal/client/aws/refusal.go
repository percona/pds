package aws

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/aws/smithy-go"
)

// Reason says why AWS refused a step. Each one is something the customer
// fixes in their own account, so a refusal fails the step and waits for Retry.
type Reason string

// The reasons a refusal can have.
const (
	ReasonQuota            Reason = "quota"
	ReasonPermission       Reason = "permission"
	ReasonRegionNotEnabled Reason = "region_not_enabled"
)

// Where names what a refusal is about. Granted says whether the stack's
// template grants an action to the PDS role; nil means it does not.
type Where struct {
	Account string
	Region  string
	Cluster string
	Granted func(action string) bool
}

// Refusal is one AWS refusal in the shape the run, the environment row, the API
// and the tools share. It holds no raw AWS text, ARN or request ID.
type Refusal struct {
	Reason Reason
	// Service is the AWS service the refusal came from, and Item the quota or
	// the action that was refused.
	Service string
	Item    string
	Region  string
}

// quotas maps the error codes AWS uses for a reached limit to the service and
// the quota a person finds in Service Quotas.
var quotas = map[string]struct{ service, item string }{
	"VpcLimitExceeded":              {"Amazon VPC", "VPCs per Region"},
	"InternetGatewayLimitExceeded":  {"Amazon VPC", "internet gateways per Region"},
	"NatGatewayLimitExceeded":       {"Amazon VPC", "NAT gateways per Availability Zone"},
	"AddressLimitExceeded":          {"Amazon EC2", "Elastic IP addresses per Region"},
	"SecurityGroupLimitExceeded":    {"Amazon VPC", "security groups per network interface"},
	"MaxSpotInstanceCountExceeded":  {"Amazon EC2", "running instances"},
	"InstanceLimitExceeded":         {"Amazon EC2", "running instances"},
	"VcpuLimitExceeded":             {"Amazon EC2", "running vCPUs"},
	"ServiceQuotaExceededException": {"", ""},
	"ResourceLimitExceeded":         {"", ""},
	"LimitExceededException":        {"", ""},
}

var (
	// permissionCodes are the codes AWS uses when an action is refused for
	// lack of permission.
	permissionCodes = map[string]bool{
		"AccessDenied": true, "AccessDeniedException": true, "UnauthorizedOperation": true,
		"UnauthorizedAccess": true, "AuthorizationError": true,
	}
	// regionCodes are the codes AWS uses for a Region the account has not
	// opted in to.
	regionCodes = map[string]bool{"OptInRequired": true, "RegionDisabledException": true}
	// throttleCodes are the codes that ask the caller to slow down.
	throttleCodes = map[string]bool{
		"Throttling": true, "ThrottlingException": true, "RequestLimitExceeded": true,
		"TooManyRequestsException": true, "RequestThrottled": true, "SlowDown": true,
	}

	// embeddedCode finds the error code CloudFormation puts in a stack event's
	// text, which is all it keeps of the call that failed.
	embeddedCode = regexp.MustCompile(`Error Code: ([A-Za-z]+)`)
	action       = regexp.MustCompile(`perform: ([a-z0-9-]+:[A-Za-z0-9*]+)`)
)

func codeAndMessage(err error) (string, string) {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode(), apiErr.ErrorMessage()
	}
	message := err.Error()
	if m := embeddedCode.FindStringSubmatch(message); m != nil {
		return m[1], message
	}

	return "", message
}

// Classify types an AWS error as a refusal the customer fixes. It reports false
// for anything else, which the caller shows as a generic failure.
func Classify(err error, where Where) (Refusal, bool) {
	if err == nil {
		return Refusal{}, false
	}
	code, message := codeAndMessage(err)
	out := Refusal{Region: where.Region}

	switch {
	case regionCodes[code]:
		out.Reason = ReasonRegionNotEnabled
	case permissionCodes[code]:
		out.Reason = ReasonPermission
		if m := action.FindStringSubmatch(message); m != nil {
			out.Item = m[1]
			out.Service, _, _ = strings.Cut(m[1], ":")
		}
	default:
		quota, ok := quotas[code]
		if !ok {
			return Refusal{}, false
		}
		out.Reason, out.Service, out.Item = ReasonQuota, quota.service, quota.item
	}

	return out, true
}

// Transient reports whether AWS asked the caller to slow down, or has not yet
// caught up with a role that was just created. Both pass by themselves.
func Transient(err error) bool {
	if err == nil {
		return false
	}
	code, message := codeAndMessage(err)
	if throttleCodes[code] {
		return true
	}

	return code == "InvalidParameterException" && strings.Contains(message, "could not be assumed")
}

// Sentence says what AWS refused and what to do, naming the AWS account and
// the Kubernetes cluster and never the raw AWS text.
func (r Refusal) Sentence(where Where) string {
	account := "AWS account " + where.Account
	cluster := "the Kubernetes cluster " + where.Cluster
	switch r.Reason {
	case ReasonQuota:
		limit := "a service quota"
		if r.Item != "" {
			limit = "the quota for " + r.Item
		}

		return fmt.Sprintf("%s has reached %s in %s, so %s cannot be built. Raise the quota in AWS, then retry.", account, limit, r.Region, cluster)
	case ReasonRegionNotEnabled:
		return fmt.Sprintf("The region %s is not enabled in %s. Enable it in AWS, then retry.", r.Region, account)
	default:
		what := "an action"
		if r.Item != "" {
			what = r.Item
		}
		if where.Granted != nil && r.Item != "" && where.Granted(r.Item) {
			return fmt.Sprintf("AWS refused %s in %s for %s. The PDS role may have been edited. Check Changed outside PDS, then retry.", what, account, cluster)
		}

		return fmt.Sprintf("AWS refused %s in %s for %s. A policy on the account, such as a service control policy, may block it. Allow the action in AWS, then retry.", what, account, cluster)
	}
}
