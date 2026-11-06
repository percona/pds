package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

// staleHub is an environment the hub has met and has not heard from since. Its last
// heartbeat is old enough to read unreachable, which is the state a network
// blip or a spoke restart produces.
func staleHub(t *testing.T) (*hub.Service, *hub.MemoryStore) {
	t.Helper()

	store := hub.NewMemoryStore()
	inv := hub.NewInventory(time.Minute)
	inv.Register("spoke-a", "pds")

	joined := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{Environment: "spoke-a", Namespace: "pds", At: joined})

	// An hour later, with a one minute staleness window.
	now := joined.Add(time.Hour)
	svc := hub.NewService(store, inv, hub.Options{
		Namespace:     "pds",
		BackupStorage: "s3",
		Now:           func() time.Time { return now },
	})

	got, ok := inv.Get("spoke-a", now)
	require.True(t, ok)
	require.Equal(t, hub.StatusUnreachable, got.Status, "fixture must be unreachable")

	return svc, store
}

// A create is a Git write. Flux applies it when the environment comes back, so
// refusing it throws away the main thing putting Git in the loop bought.
func TestCreateIsAcceptedWhileUnreachable(t *testing.T) {
	svc, store := staleHub(t)
	ctx := context.Background()

	got, err := svc.Create(ctx, engine.CreateInput{
		Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})
	require.NoError(t, err)
	assert.Equal(t, engine.PhasePending, got.Status.Phase)

	_, err = store.Get(ctx, "clusters/spoke-a/instances/orders-dev.yaml")
	require.NoError(t, err, "the manifest must be waiting in Git")
}

func TestScaleIsAcceptedWhileUnreachable(t *testing.T) {
	svc, _ := staleHub(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, engine.CreateInput{
		Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})
	require.NoError(t, err)

	cpu := 2
	err = svc.Scale(ctx, "spoke-a/pds.orders-dev", engine.ScaleInput{CPUCores: &cpu})
	require.NoError(t, err)
}

func TestSetBackupScheduleIsAcceptedWhileUnreachable(t *testing.T) {
	svc, _ := staleHub(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, engine.CreateInput{
		Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})
	require.NoError(t, err)

	_, err = svc.SetBackupSchedule(ctx, "spoke-a/pds.orders-dev", nil)
	require.NoError(t, err)
}

// Removing the file is the delete. Flux prunes when the environment returns.
func TestDeleteIsAcceptedWhileUnreachable(t *testing.T) {
	svc, store := staleHub(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, engine.CreateInput{
		Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})
	require.NoError(t, err)

	require.NoError(t, svc.Delete(ctx, "spoke-a/pds.orders-dev"))

	_, err = store.Get(ctx, "clusters/spoke-a/instances/orders-dev.yaml")
	require.Error(t, err, "the manifest must be gone from Git")
}

// Backup is an event carried on the beat. There is no beat, so there is no
// honest answer other than refusing.
func TestBackupIsRefusedWhileUnreachable(t *testing.T) {
	svc, _ := staleHub(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, engine.CreateInput{
		Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "spoke-a",
	})
	require.NoError(t, err)

	_, err = svc.Backup(ctx, "spoke-a/pds.orders-dev")
	require.ErrorIs(t, err, hub.ErrUnreachable)
}

func TestRestoreIsRefusedWhileUnreachable(t *testing.T) {
	svc, _ := staleHub(t)
	ctx := context.Background()

	_, err := svc.RestoreToNewDatabase(ctx, "spoke-a/pds.orders-dev", "orders-dev-1", "orders-restore-1")
	require.ErrorIs(t, err, hub.ErrUnreachable)
}

// Accepting a write for an environment the hub has never met would put a manifest in
// a tree nothing pulls, with no row in the UI to explain it.
func TestCreateIsRefusedForAnUnknownEnvironment(t *testing.T) {
	svc, _ := staleHub(t)

	_, err := svc.Create(context.Background(), engine.CreateInput{
		Name: "orders-dev", Engine: "mysql", Profile: "dev", Environment: "never-seen",
	})
	require.ErrorIs(t, err, hub.ErrUnreachable)
}
