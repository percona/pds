package hub_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

const loadBalancerTags = "service.beta.kubernetes.io/aws-load-balancer-additional-resource-tags"

// ownedHub is a hub whose environment spoke-a is a managed environment with
// the ID k3j9x2m4p1q7, and spoke-b is a connected one with none.
func ownedHub(t *testing.T) (*hub.Service, *hub.MemoryStore) {
	t.Helper()

	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	for _, name := range []string{"spoke-a", "spoke-b"} {
		inv.Register(name, "pds")
		inv.ApplyHeartbeat(hub.Heartbeat{Environment: name, Namespace: "pds", At: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)})
	}
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		OwnershipID: func(_ context.Context, environment string) (string, error) {
			if environment == "spoke-a" {
				return "k3j9x2m4p1q7", nil
			}

			return "", nil
		},
	})

	return svc, store
}

func TestADatabaseLoadBalancerOnAManagedEnvironmentIsTaggedWithItsID(t *testing.T) {
	ctx := context.Background()
	svc, store := ownedHub(t)

	_, err := svc.Create(ctx, engine.CreateInput{Name: "reports", Engine: "postgresql", Profile: "dev", Environment: "spoke-a"})
	require.NoError(t, err)

	body, err := store.Get(ctx, "clusters/spoke-a/instances/reports.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(body), loadBalancerTags+": percona.com/pds=k3j9x2m4p1q7")
}

func TestADatabaseLoadBalancerOnAConnectedEnvironmentCarriesNoTag(t *testing.T) {
	ctx := context.Background()
	svc, store := ownedHub(t)

	_, err := svc.Create(ctx, engine.CreateInput{Name: "reports", Engine: "postgresql", Profile: "dev", Environment: "spoke-b"})
	require.NoError(t, err)

	body, err := store.Get(ctx, "clusters/spoke-b/instances/reports.yaml")
	require.NoError(t, err)
	assert.NotContains(t, string(body), loadBalancerTags)
}

func TestMonitoringLoadBalancerOnAManagedEnvironmentIsTaggedWithItsID(t *testing.T) {
	ctx := context.Background()
	svc, store := ownedHub(t)

	in := hub.DefaultPMMSettings()
	in.Access = hub.PMMAccessPublic
	in.SourceRanges = []string{"203.0.113.0/24"}
	require.NoError(t, svc.SetPMMSettings(ctx, "spoke-a", in))

	body, err := store.Get(ctx, "clusters/spoke-a/platform/pmm/helmrelease.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(body), "percona.com/pds=k3j9x2m4p1q7")
}

func TestACreateIsRefusedWhenTheEnvironmentsTagCannotBeRead(t *testing.T) {
	ctx := context.Background()
	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")
	store := hub.NewMemoryStore()
	svc := hub.NewService(store, inv, hub.Options{
		Namespace: "pds",
		OwnershipID: func(context.Context, string) (string, error) {
			return "", errors.New("the database is not connected")
		},
	})

	_, err := svc.Create(ctx, engine.CreateInput{Name: "reports", Engine: "postgresql", Profile: "dev", Environment: "spoke-a"})

	require.Error(t, err)
	_, readErr := store.Get(ctx, "clusters/spoke-a/instances/reports.yaml")
	assert.Error(t, readErr, "nothing was written")
}
