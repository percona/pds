package hub_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/hub"
)

const (
	seedNamespace = "everest-system"
	joinSecret    = "pds-join-tokens"
	seedInventory = "pds-hub-inventory"
	workConfigMap = "pds-hub-work"
)

// seededHub is a hub with one enrolled environment that has a heartbeat, a live
// join token, queued work and a clone identity, all kept in the persists it is
// given. It returns the token the environment holds.
func seededHub(t *testing.T, persists hub.Persists) string {
	t.Helper()
	ctx := t.Context()

	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(persists.Inventory)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment:   "eks-prod",
		Namespace:     "pds",
		BackupStorage: "s3-backups",
		At:            time.Now(),
	})

	tokens := hub.NewTokenStore(persists.Tokens, time.Hour)
	token, err := tokens.Issue(ctx, "eks-prod")
	require.NoError(t, err)

	require.NoError(t, persists.Work.Save(ctx, "eks-prod", []hub.Work{
		{ID: "w1", Kind: hub.WorkDelete, Database: "orders"},
	}))
	require.NoError(t, persists.CloneKeys.Save(ctx, "eks-prod", hub.CloneKey{
		Identity: "-----BEGIN OPENSSH PRIVATE KEY-----",
		Pub:      "ssh-ed25519 AAAA",
	}))

	return token
}

func objectPersists() hub.Persists {
	kv := &memKV{}

	return hub.Persists{
		Inventory: hub.NewConfigMapInventoryPersist(kv, seedNamespace, seedInventory),
		Tokens:    hub.NewSecretTokenPersist(kv, seedNamespace, joinSecret),
		Work:      hub.NewConfigMapWorkPersist(kv, seedNamespace, workConfigMap),
		CloneKeys: hub.NewSecretCloneKeyPersist(kv, seedNamespace, joinSecret),
	}
}

func databasePersists(state *fakeState) hub.Persists {
	return hub.Persists{
		Inventory: hub.NewDBInventoryPersist(state),
		Tokens:    hub.NewDBTokenPersist(state),
		Work:      hub.NewDBWorkPersist(state),
		CloneKeys: hub.NewDBCloneKeyPersist(state),
	}
}

func TestHydrationBringsAHubUpFromTheDatabase(t *testing.T) {
	state := newFakeState()
	persists := databasePersists(state)
	token := seededHub(t, persists)
	ctx := t.Context()

	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(persists.Inventory)
	tokens := hub.NewTokenStore(persists.Tokens, time.Hour)
	svc := hub.NewService(hub.NewMemoryStore(), inv, hub.Options{
		Namespace: "pds",
		Tokens:    tokens,
		CloneKeys: persists.CloneKeys,
	})

	require.NoError(t, hub.Hydration{
		Inventory: inv,
		Tokens:    tokens,
		Service:   svc,
		Work:      persists.Work,
	}.Run(ctx))

	environments := svc.Environments()
	require.Len(t, environments, 1)
	assert.Equal(t, "eks-prod", environments[0].Name)

	environment, ok := tokens.Environment(ctx, token)
	require.True(t, ok)
	assert.Equal(t, "eks-prod", environment)
}

func TestHydrationWithoutADatabaseLoadsTheObjectsItAlwaysDid(t *testing.T) {
	persists := objectPersists()
	token := seededHub(t, persists)
	ctx := t.Context()

	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(persists.Inventory)
	tokens := hub.NewTokenStore(persists.Tokens, time.Hour)

	require.NoError(t, hub.Hydration{Inventory: inv, Tokens: tokens}.Run(ctx))

	_, ok := inv.Get("eks-prod", time.Now())
	assert.True(t, ok)

	environment, ok := tokens.Environment(ctx, token)
	require.True(t, ok)
	assert.Equal(t, "eks-prod", environment)
}

func TestHydratorReportsNotReadyUntilItLands(t *testing.T) {
	state := newFakeState()

	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewDBInventoryPersist(state))

	release := make(chan struct{})
	hydrator := hub.StartHydration(t.Context(), func(ctx context.Context) error {
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, hub.Hydration{Inventory: inv}, nil)
	t.Cleanup(func() { _ = hydrator.Close() })

	require.Error(t, hydrator.Ready(t.Context()), "a pod that has not loaded the fleet must not take traffic")

	close(release)
	require.Eventually(t, func() bool {
		return hydrator.Ready(context.Background()) == nil
	}, 5*time.Second, 10*time.Millisecond)
}

func TestHydratorRetriesAFailedAttempt(t *testing.T) {
	state := newFakeState()

	inv := hub.NewInventory(time.Minute)
	inv.SetPersist(hub.NewDBInventoryPersist(state))

	attempts := make(chan struct{}, 4)
	hydrator := hub.StartHydration(t.Context(), func(context.Context) error {
		select {
		case attempts <- struct{}{}:
		default:
		}
		if len(attempts) < 2 {
			return errors.New("the database is not reachable yet")
		}

		return nil
	}, hub.Hydration{Inventory: inv}, nil)
	t.Cleanup(func() { _ = hydrator.Close() })

	require.Eventually(t, func() bool {
		return hydrator.Ready(context.Background()) == nil
	}, 10*time.Second, 50*time.Millisecond, "a database that comes back has to be picked up without a restart")
}

func TestHydratorReadyOnNothingIsNotAnError(t *testing.T) {
	var hydrator *hub.Hydrator

	require.NoError(t, hydrator.Ready(context.Background()))
	require.NoError(t, hydrator.Close())
}
