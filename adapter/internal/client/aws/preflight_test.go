package aws

import (
	"context"
	"errors"
	"testing"

	sdkaws "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	sqtypes "github.com/aws/aws-sdk-go-v2/service/servicequotas/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeQuotas struct {
	limits map[string]float64
	failed map[string]bool
	asked  []string
}

func (f *fakeQuotas) GetServiceQuota(_ context.Context, in *servicequotas.GetServiceQuotaInput, _ ...func(*servicequotas.Options)) (*servicequotas.GetServiceQuotaOutput, error) {
	code := sdkaws.ToString(in.QuotaCode)
	f.asked = append(f.asked, code)
	if f.failed[code] {
		return nil, apiErr("AccessDeniedException")
	}
	limit, ok := f.limits[code]
	if !ok {
		return nil, apiErr("NoSuchResourceException")
	}

	return &servicequotas.GetServiceQuotaOutput{Quota: &sqtypes.ServiceQuota{Value: sdkaws.Float64(limit)}}, nil
}

type fakeRegions struct {
	enabled []string
	err     error
}

func (f *fakeRegions) DescribeRegions(_ context.Context, _ *ec2.DescribeRegionsInput, _ ...func(*ec2.Options)) (*ec2.DescribeRegionsOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := &ec2.DescribeRegionsOutput{}
	for _, name := range f.enabled {
		out.Regions = append(out.Regions, ec2types.Region{RegionName: sdkaws.String(name)})
	}

	return out, nil
}

var roomyQuotas = map[string]float64{
	"L-0263D0A3": 5, "L-F678F1CE": 5, "L-A4707A72": 5, "L-FE5A380F": 5, "L-1216C47A": 256,
}

func preflightInfra(q *fakeQuotas, r *fakeRegions) *Infra {
	infra := newInfra(&fakeCFN{}, &fakeEKS{})
	infra.quotas = func(sdkaws.Config) quotasAPI { return q }
	infra.regions = func(sdkaws.Config) regionsAPI { return r }

	return infra
}

func TestPreflightPassesWhenTheAccountHasRoom(t *testing.T) {
	infra := preflightInfra(&fakeQuotas{limits: roomyQuotas}, &fakeRegions{enabled: []string{"eu-west-1"}})

	_, refused := infra.Preflight(context.Background(), target, Needs{VCPUs: 12})

	assert.False(t, refused)
}

func TestPreflightRefusesAQuotaBelowWhatTheEnvironmentNeeds(t *testing.T) {
	limits := map[string]float64{}
	for code, value := range roomyQuotas {
		limits[code] = value
	}
	limits["L-1216C47A"] = 8
	infra := preflightInfra(&fakeQuotas{limits: limits}, &fakeRegions{enabled: []string{"eu-west-1"}})

	refusal, refused := infra.Preflight(context.Background(), target, Needs{VCPUs: 12})

	require.True(t, refused)
	assert.Equal(t, ReasonQuota, refusal.Reason)
	assert.Equal(t, "Amazon EC2", refusal.Service)
	assert.Equal(t, "running On-Demand Standard instance vCPUs", refusal.Item)
	assert.Equal(t, "eu-west-1", refusal.Region)
}

func TestPreflightRefusesARegionTheAccountHasNotEnabled(t *testing.T) {
	infra := preflightInfra(&fakeQuotas{limits: roomyQuotas}, &fakeRegions{})

	refusal, refused := infra.Preflight(context.Background(), target, Needs{VCPUs: 12})

	require.True(t, refused)
	assert.Equal(t, ReasonRegionNotEnabled, refusal.Reason)
	assert.Equal(t, "eu-west-1", refusal.Region)
}

func TestPreflightNeverBlocksOnARead(t *testing.T) {
	failed := map[string]bool{}
	for code := range roomyQuotas {
		failed[code] = true
	}
	infra := preflightInfra(&fakeQuotas{failed: failed}, &fakeRegions{err: errors.New("endpoint unreachable")})

	_, refused := infra.Preflight(context.Background(), target, Needs{VCPUs: 12})

	assert.False(t, refused, "a failed read says nothing about the account")
}

func TestPreflightNeverBlocksWhenTheRoleCannotBeAssumed(t *testing.T) {
	infra := preflightInfra(&fakeQuotas{limits: roomyQuotas}, &fakeRegions{enabled: []string{"eu-west-1"}})
	infra.assume = func(context.Context, Target) (sdkaws.Config, error) { return sdkaws.Config{}, ErrAssumeDenied }

	_, refused := infra.Preflight(context.Background(), target, Needs{VCPUs: 12})

	assert.False(t, refused)
}
