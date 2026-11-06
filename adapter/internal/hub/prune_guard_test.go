package hub_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

// failWriteStore refuses writes whose path ends with failSuffix.
type failWriteStore struct {
	hub.Store
	failSuffix string
}

func (f *failWriteStore) Write(ctx context.Context, files map[string][]byte, message string) error {
	for path := range files {
		if f.failSuffix != "" && strings.HasSuffix(path, f.failSuffix) {
			return fmt.Errorf("refused write of %s", path)
		}
	}

	return f.Store.Write(ctx, files, message)
}

// blindStore answers every listing as empty while still holding the files.
// That is what a hub whose Git volume failed to mount looks like from inside
// the process: no error, just nothing there.
type blindStore struct {
	hub.Store
	blind bool
}

func (b *blindStore) List(ctx context.Context, prefix string) ([]string, error) {
	if b.blind {
		return nil, nil
	}

	return b.Store.List(ctx, prefix)
}

func hubOverStore(t *testing.T, store hub.Store) *hub.Service {
	t.Helper()

	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")
	now := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: now})

	return hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
	})
}

// The whole point of prune is that a manifest missing from Git is deleted from
// the environment. So an index that goes from listing databases to listing none is
// an instruction to delete every database on that environment, and the hub must
// only ever issue it on purpose.
func TestARewriteWillNotEmptyAnIndexThatHadResources(t *testing.T) {
	ctx := context.Background()
	backing := hub.NewMemoryStore()
	store := &blindStore{Store: backing}
	svc := hubOverStore(t, store)

	_, err := svc.Create(ctx, engine.CreateInput{
		Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})
	require.NoError(t, err)
	releaseCredentials(t, svc)

	before, err := backing.Get(ctx, "clusters/spoke-a/instances/kustomization.yaml")
	require.NoError(t, err)
	require.Contains(t, string(before), "orders-dev.yaml")

	// The volume drops out. A second create now lists nothing.
	store.blind = true

	_, err = svc.Create(ctx, engine.CreateInput{
		Name: "checkout-prod", Engine: "mysql", Profile: "prod", Environment: "spoke-a",
	})
	require.Error(t, err, "a create that can see no files must not rewrite the index")

	after, err := backing.Get(ctx, "clusters/spoke-a/instances/kustomization.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(after), "orders-dev.yaml",
		"the index must still list the database that exists")
}

// Deleting the last database is the one time an empty index is correct.
func TestDeletingTheLastDatabaseMayEmptyTheIndex(t *testing.T) {
	ctx := context.Background()
	store := hub.NewMemoryStore()
	svc := hubOverStore(t, store)

	_, err := svc.Create(ctx, engine.CreateInput{
		Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})
	require.NoError(t, err)

	require.NoError(t, svc.Delete(ctx, "spoke-a/pds.orders-dev"))

	body, err := store.Get(ctx, "clusters/spoke-a/instances/kustomization.yaml")
	require.NoError(t, err)
	assert.NotContains(t, string(body), "orders-dev.yaml")
}

// An environment that has just joined has no databases, so its first index is empty
// and that is not the dangerous case.
func TestAFreshEnvironmentGetsAnEmptyIndex(t *testing.T) {
	ctx := context.Background()
	store := hub.NewMemoryStore()
	svc := hubOverStore(t, store)

	_, err := svc.IssueJoin(ctx, "spoke-new")
	require.NoError(t, err)

	body, err := store.Get(ctx, "clusters/spoke-new/instances/kustomization.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(body), "kind: Kustomization")
}

// A delete that removes the manifest and then fails the index rewrite used
// to leave Git pointing at a file that was gone. A retry treated the still
// observed database as heartbeat-only and queued a direct delete.
func TestDeleteRestoresManifestWhenIndexRewriteFails(t *testing.T) {
	ctx := context.Background()
	store := &failWriteStore{Store: hub.NewMemoryStore()}
	svc := hubOverStore(t, store)

	created, err := svc.Create(ctx, engine.CreateInput{
		Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})
	require.NoError(t, err)
	releaseCredentials(t, svc)

	store.failSuffix = "kustomization.yaml"
	require.Error(t, svc.Delete(ctx, created.ID))

	body, err := store.Get(ctx, "clusters/spoke-a/instances/orders-dev.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(body), "orders-dev")
	index, err := store.Get(ctx, "clusters/spoke-a/instances/kustomization.yaml")
	require.NoError(t, err)
	assert.Contains(t, string(index), "orders-dev.yaml")
}
