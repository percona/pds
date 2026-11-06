package hub_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/percona/pds/adapter/internal/engine"
	"github.com/percona/pds/adapter/internal/hub"
)

func TestInventoryUnreachableUntilHeartbeat(t *testing.T) {
	inv := hub.NewInventory(90 * time.Second)
	inv.Register("spoke-a", "pds")

	environments := inv.List(time.Now())
	require.Len(t, environments, 1)
	assert.Equal(t, "spoke-a", environments[0].Name)
	assert.Equal(t, hub.StatusUnreachable, environments[0].Status)
}

func TestInventoryConnectedAfterHeartbeat(t *testing.T) {
	inv := hub.NewInventory(90 * time.Second)
	inv.Register("spoke-a", "pds")
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		At:          now,
		Instances: []engine.Instance{{
			ID:          "spoke-a/pds.orders-dev",
			Name:        "orders-dev",
			Namespace:   "pds",
			Environment: "spoke-a",
			Engine:      "mysql",
			Status:      engine.Status{Phase: engine.PhaseReady},
		}},
	})

	environments := inv.List(now)
	require.Len(t, environments, 1)
	assert.Equal(t, hub.StatusConnected, environments[0].Status)
	assert.Equal(t, now, environments[0].LastSeen)

	stale := inv.List(now.Add(91 * time.Second))
	assert.Equal(t, hub.StatusUnreachable, stale[0].Status)
}

func TestInventoryUnknownEnvironmentIsUnreachable(t *testing.T) {
	inv := hub.NewInventory(90 * time.Second)
	_, ok := inv.Get("missing", time.Now())
	assert.False(t, ok)
}

// A spoke token is scoped to the path environment. Instance IDs and Environment
// fields in the body used to be stored as sent, so one spoke could plant a
// row that later operator actions would run against another environment.
func TestHeartbeatRewritesInjectedInstanceIDs(t *testing.T) {
	inv := hub.NewInventory(90 * time.Second)
	inv.Register("spoke-a", "pds")
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		At:          now,
		Instances: []engine.Instance{{
			ID:          "spoke-b/other-ns.orders-dev",
			Name:        "orders-dev",
			Namespace:   "other-ns",
			Environment: "spoke-b",
			Engine:      "mysql",
			Status:      engine.Status{Phase: engine.PhaseReady},
		}},
	})

	snap, ok := inv.Snapshot("spoke-a")
	require.True(t, ok)
	require.Len(t, snap.Instances, 1)
	assert.Equal(t, "spoke-a", snap.Instances[0].Environment)
	assert.Equal(t, "spoke-a/other-ns.orders-dev", snap.Instances[0].ID)
	assert.Equal(t, "orders-dev", snap.Instances[0].Name)
	assert.Equal(t, "other-ns", snap.Instances[0].Namespace)
}

func TestHeartbeatOmitInstancesKeepsThePreviousSnapshot(t *testing.T) {
	inv := hub.NewInventory(90 * time.Second)
	inv.Register("spoke-a", "pds")
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		At:          now,
		Instances: []engine.Instance{{
			Name:      "orders-dev",
			Namespace: "pds",
			Engine:    "mysql",
			Status:    engine.Status{Phase: engine.PhaseReady},
		}},
		Backups: []engine.Backup{{ID: "orders-dev-1", Status: engine.BackupSucceeded, Instance: "orders-dev"}},
	})

	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment:   "spoke-a",
		Namespace:     "pds",
		At:            now.Add(time.Minute),
		OmitInstances: true,
	})

	snap, ok := inv.Snapshot("spoke-a")
	require.True(t, ok)
	require.Len(t, snap.Instances, 1)
	assert.Equal(t, "orders-dev", snap.Instances[0].Name)
	require.Len(t, snap.Backups, 1)
	assert.Equal(t, "orders-dev-1", snap.Backups[0].ID)
	assert.False(t, snap.OmitInstances)
	assert.False(t, snap.OmitBackups)
	assert.Equal(t, now.Add(time.Minute), snap.At)
}

func TestHeartbeatOmitBackupsKeepsThePreviousBackups(t *testing.T) {
	inv := hub.NewInventory(90 * time.Second)
	inv.Register("spoke-a", "pds")
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		At:          now,
		Instances: []engine.Instance{{
			Name:      "orders-dev",
			Namespace: "pds",
			Engine:    "mysql",
			Status:    engine.Status{Phase: engine.PhaseReady},
		}},
		Backups: []engine.Backup{{ID: "orders-dev-1", Status: engine.BackupSucceeded, Instance: "orders-dev"}},
	})

	inv.ApplyHeartbeat(hub.Heartbeat{
		Environment: "spoke-a",
		Namespace:   "pds",
		At:          now.Add(time.Minute),
		Instances:   []engine.Instance{{Name: "orders-dev", Namespace: "pds"}},
		OmitBackups: true,
	})

	snap, ok := inv.Snapshot("spoke-a")
	require.True(t, ok)
	require.Len(t, snap.Backups, 1)
	assert.Equal(t, "orders-dev-1", snap.Backups[0].ID)
	assert.False(t, snap.OmitBackups)
	assert.Equal(t, "orders-dev", snap.Instances[0].Name)
}

func TestReconcilePendingBackupsKeepsAFailedRecord(t *testing.T) {
	inv := hub.NewInventory(90 * time.Second)
	inv.Register("spoke-a", "pds")
	inv.AddPendingBackup("spoke-a", "pds.orders", engine.Backup{
		ID:       "orders-missing",
		Status:   engine.BackupPending,
		Instance: "orders",
	})

	gaveUp := false
	for range 20 {
		missing := inv.ReconcilePendingBackups("spoke-a", nil)
		pending := inv.PendingBackups("spoke-a")
		require.Len(t, pending, 1)
		assert.Equal(t, "orders-missing", pending[0].Backup.ID)
		if pending[0].Backup.Status == engine.BackupFailed {
			assert.Empty(t, missing, "a given-up backup must not be asked for again")
			gaveUp = true
			break
		}
		require.Len(t, missing, 1)
		assert.Equal(t, engine.BackupPending, pending[0].Backup.Status)
	}
	require.True(t, gaveUp, "the hub must mark the backup failed instead of dropping it")

	missing := inv.ReconcilePendingBackups("spoke-a", nil)
	assert.Empty(t, missing)
	pending := inv.PendingBackups("spoke-a")
	require.Len(t, pending, 1)
	assert.Equal(t, engine.BackupFailed, pending[0].Backup.Status)
}
