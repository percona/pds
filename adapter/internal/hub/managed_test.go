package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/hub"
)

func managedHub(t *testing.T) (*hub.Service, *hub.MemoryStore) {
	t.Helper()

	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	tokens := hub.NewTokenStore(hub.NewSecretTokenPersist(&memKV{}, "everest-system", "pds-join-tokens"), 0)

	return hub.NewService(store, inv, hub.Options{Namespace: "pds", Tokens: tokens}), store
}

func TestEnrollManagedHandsBackAnAgentTokenTheHubAccepts(t *testing.T) {
	ctx := context.Background()
	svc, _ := managedHub(t)

	got, err := svc.EnrollManaged(ctx, "orders")
	require.NoError(t, err)

	name, ok := svc.JoinEnvironment(ctx, got.AgentToken)
	assert.True(t, ok)
	assert.Equal(t, "orders", name)
	assert.Equal(t, "pds", got.Namespace)
	assert.Equal(t, "orders", got.Repo)
	listed := svc.Environments()
	require.Len(t, listed, 1, "the environment is registered the moment it is enrolled")
	assert.Equal(t, hub.StatusUnreachable, listed[0].Status)
}

func TestEnrollManagedAgainRevokesTheEarlierToken(t *testing.T) {
	ctx := context.Background()
	svc, _ := managedHub(t)
	first, err := svc.EnrollManaged(ctx, "orders")
	require.NoError(t, err)

	second, err := svc.EnrollManaged(ctx, "orders")
	require.NoError(t, err)

	_, oldWorks := svc.JoinEnvironment(ctx, first.AgentToken)
	_, newWorks := svc.JoinEnvironment(ctx, second.AgentToken)
	assert.False(t, oldWorks, "a rerun must not leave two live credentials")
	assert.True(t, newWorks)
	assert.NotEqual(t, first.AgentToken, second.AgentToken)
}

func TestEnrollManagedKeepsAnotherEnvironmentsToken(t *testing.T) {
	ctx := context.Background()
	svc, _ := managedHub(t)
	other, err := svc.EnrollManaged(ctx, "billing")
	require.NoError(t, err)

	_, err = svc.EnrollManaged(ctx, "orders")
	require.NoError(t, err)
	_, err = svc.EnrollManaged(ctx, "orders")
	require.NoError(t, err)

	_, ok := svc.JoinEnvironment(ctx, other.AgentToken)
	assert.True(t, ok)
}

func TestEnrollManagedRefusesAnInvalidName(t *testing.T) {
	svc, _ := managedHub(t)

	_, err := svc.EnrollManaged(context.Background(), "Not A Name")

	assert.ErrorIs(t, err, hub.ErrInvalidEnvironment)
}

func TestCommitFilesWritesIntoTheEnvironmentsTree(t *testing.T) {
	ctx := context.Background()
	svc, store := managedHub(t)
	_, err := svc.EnrollManaged(ctx, "orders")
	require.NoError(t, err)

	require.NoError(t, svc.CommitFiles(ctx, "orders", map[string][]byte{"clusters/orders/platform/pds/helmrelease.yaml": []byte("kind: HelmRelease\n")}, "release"))

	body, err := store.Get(ctx, "clusters/orders/platform/pds/helmrelease.yaml")
	require.NoError(t, err)
	assert.Equal(t, "kind: HelmRelease\n", string(body))
}

func TestCommitFilesRefusesAPathOutsideTheEnvironment(t *testing.T) {
	svc, store := managedHub(t)

	err := svc.CommitFiles(context.Background(), "orders", map[string][]byte{"clusters/billing/instances/x.yaml": []byte("x")}, "release")

	require.Error(t, err)
	_, getErr := store.Get(context.Background(), "clusters/billing/instances/x.yaml")
	assert.Error(t, getErr)
}

func TestEnvironmentConnectedFollowsTheFirstHeartbeat(t *testing.T) {
	ctx := context.Background()
	svc, _ := managedHub(t)
	_, err := svc.EnrollManaged(ctx, "orders")
	require.NoError(t, err)
	assert.False(t, svc.EnvironmentConnected("orders"))

	svc.ApplyHeartbeat(hub.Heartbeat{Environment: "orders", Namespace: "pds", At: time.Now()})

	assert.True(t, svc.EnvironmentConnected("orders"))
	assert.False(t, svc.EnvironmentConnected("billing"))
}
