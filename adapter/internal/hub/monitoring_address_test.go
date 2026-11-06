package hub_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/hub"
)

func beatWithAddress(t *testing.T, svc *hub.Service, environment, address string) {
	t.Helper()

	_, ok, err := svc.AcceptHeartbeat(hub.Heartbeat{Environment: environment, Namespace: "pds", MonitoringAddress: address}, nil)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestHubKeepsTheMonitoringAddressEachEnvironmentReported(t *testing.T) {
	svc := newRelayHub(t)

	assert.Empty(t, svc.MonitoringAddress("spoke-a"), "nothing reported yet")

	beatWithAddress(t, svc, "spoke-a", "pmm.elb.example.com")
	beatWithAddress(t, svc, "spoke-b", "10.0.4.9")

	assert.Equal(t, "pmm.elb.example.com", svc.MonitoringAddress("spoke-a"))
	assert.Equal(t, "10.0.4.9", svc.MonitoringAddress("spoke-b"))
}

// The load balancer was removed, so the next beat has no address and the hub
// stops showing the old one.
func TestHubForgetsTheAddressWhenTheNextBeatHasNone(t *testing.T) {
	svc := newRelayHub(t)
	beatWithAddress(t, svc, "spoke-a", "pmm.elb.example.com")

	beatWithAddress(t, svc, "spoke-a", "")

	assert.Empty(t, svc.MonitoringAddress("spoke-a"))
}

func TestHubHasNoAddressForAnUnknownEnvironment(t *testing.T) {
	assert.Empty(t, newRelayHub(t).MonitoringAddress("nope"))
}
