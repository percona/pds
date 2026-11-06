package mysql_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/client/everest"
	"github.com/percona/pds/adapter/internal/engine"
)

func exposedInstance(ranges ...string) *everest.Instance {
	in := pxcInstance("orders", 1, "1", "2Gi", "20Gi")
	in.Metadata.Namespace = "pds"
	in.Status.Phase = everest.PhaseReady
	in.Status.ConnectionSecretRef = &everest.ObjectRef{Name: "orders-conn"}

	svc := &everest.ServiceSpec{ServiceType: everest.ServiceTypeLoadBalancer}
	if len(ranges) > 0 {
		svc.LoadBalancerService = &everest.LoadBalancerService{SourceRanges: ranges}
	}
	in.SetProxyComponent(everest.ComponentSpec{Type: everest.ComponentTypeHAProxy, Service: svc})

	return in
}

func TestListCarriesExposureForTheHeartbeat(t *testing.T) {
	p, fake := newPlugin(t)
	fake.instances[key("pds", "orders")] = exposedInstance("10.0.0.0/8")
	fake.connections[key("pds", "orders")] = &everest.Connection{Host: "198.51.100.20", Port: 3306, Username: "root"}

	got, err := p.List(context.Background())
	require.NoError(t, err)
	require.Len(t, got, 1)

	assert.Equal(t, engine.Exposure{
		External:     true,
		SourceRanges: []string{"10.0.0.0/8"},
		Address:      "198.51.100.20",
	}, got[0].Exposure)
}

func TestScaleKeepsTheExposure(t *testing.T) {
	p, fake := newPlugin(t)
	fake.instances[key("pds", "orders")] = exposedInstance("10.0.0.0/8")

	require.NoError(t, p.Scale(context.Background(), "pds.orders", engine.ScaleInput{CPUCores: intPtr(2)}))

	svc := fake.stored("pds", "orders").ProxyComponent().Service
	require.NotNil(t, svc)
	assert.Equal(t, everest.ServiceTypeLoadBalancer, svc.ServiceType)
	assert.Equal(t, []string{"10.0.0.0/8"}, svc.LoadBalancerService.SourceRanges)
}
