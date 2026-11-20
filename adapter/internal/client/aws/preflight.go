package aws

import (
	"context"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
)

type quotasAPI interface {
	GetServiceQuota(context.Context, *servicequotas.GetServiceQuotaInput, ...func(*servicequotas.Options)) (*servicequotas.GetServiceQuotaOutput, error)
}

type regionsAPI interface {
	DescribeRegions(context.Context, *ec2.DescribeRegionsInput, ...func(*ec2.Options)) (*ec2.DescribeRegionsOutput, error)
}

// Needs is what a create asks of the account beyond one of everything the
// network takes: the vCPUs of the node group.
type Needs struct {
	VCPUs int
}

// preflightQuota is one Service Quotas read. need is how many the network
// takes, and zero means the node group's vCPUs.
type preflightQuota struct {
	service, code string
	item          Refusal
	need          int
}

// preflightQuotas are the limits a new network and node group run into first.
// Each item matches the name Service Quotas shows, so the sentence points at
// the place the customer raises it.
var preflightQuotas = []preflightQuota{
	{"vpc", "L-F678F1CE", Refusal{Service: "Amazon VPC", Item: "VPCs per Region"}, 1},
	{"vpc", "L-A4707A72", Refusal{Service: "Amazon VPC", Item: "internet gateways per Region"}, 1},
	{"vpc", "L-FE5A380F", Refusal{Service: "Amazon VPC", Item: "NAT gateways per Availability Zone"}, 1},
	{"ec2", "L-0263D0A3", Refusal{Service: "Amazon EC2", Item: "Elastic IP addresses per Region"}, 1},
	{"ec2", "L-1216C47A", Refusal{Service: "Amazon EC2", Item: "running On-Demand Standard instance vCPUs"}, 0},
}

// Preflight looks, before anything is built, for a Region the account has not
// enabled and for a quota that is below what the environment takes. It is best
// effort: a read that fails, or a role that cannot be assumed, says nothing
// about the account, so it never reports a refusal for one. The limits are
// compared with what one environment needs and not with what the account
// already uses, so it catches an account that could never build the
// environment and leaves the rest to the stack.
func (i *Infra) Preflight(ctx context.Context, t Target, needs Needs) (Refusal, bool) {
	session, err := i.assume(ctx, t)
	if err != nil {
		return Refusal{}, false
	}

	if out, err := i.regions(session).DescribeRegions(ctx, &ec2.DescribeRegionsInput{RegionNames: []string{t.Region}}); err == nil && len(out.Regions) == 0 {
		return Refusal{Reason: ReasonRegionNotEnabled, Region: t.Region}, true
	}

	client := i.quotas(session)
	for _, quota := range preflightQuotas {
		need := quota.need
		if need == 0 {
			need = needs.VCPUs
		}
		out, err := client.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{
			ServiceCode: sdkaws.String(quota.service), QuotaCode: sdkaws.String(quota.code),
		})
		if err != nil || out.Quota == nil || out.Quota.Value == nil {
			continue
		}
		if int(*out.Quota.Value) < need {
			refusal := quota.item
			refusal.Reason, refusal.Region = ReasonQuota, t.Region

			return refusal, true
		}
	}

	return Refusal{}, false
}
